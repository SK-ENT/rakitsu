package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/config"
)

// ─── A2A JSON-RPC types (A2A v1.0.1, github.com/a2aproject/A2A) ─────────────
//
// Mirrors the shapes in cmd/rakitsu/a2a_serve.go (this package can't import
// package main, hence the duplication). Modeled from specification/a2a.proto
// + docs/specification.md at tag v1.0.1: PascalCase JSON-RPC method names
// matching gRPC 1:1, camelCase JSON fields, SCREAMING_SNAKE_CASE enums, no
// "kind" discriminator (oneofs serialize as whichever field is present).

type a2aRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      string      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type a2aResponse struct {
	JSONRPC string `json:"jsonrpc"`
	// ID is json.RawMessage, not string: JSON-RPC 2.0 permits a String,
	// Number, or Null id, and this client's whole purpose is talking to
	// real (non-rakitsu) A2A servers, not just its own — a peer that
	// echoes a numeric id would otherwise fail to decode entirely. Mirrors
	// srvA2AResponse.ID in cmd/rakitsu/a2a_serve.go. Never inspected here
	// (this client doesn't correlate responses to requests by id), so the
	// wider type has no effect beyond accepting what the spec allows.
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *a2aRPCError    `json:"error,omitempty"`
}

type a2aRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	taskStateSubmitted     = "TASK_STATE_SUBMITTED"
	taskStateWorking       = "TASK_STATE_WORKING"
	taskStateCompleted     = "TASK_STATE_COMPLETED"
	taskStateFailed        = "TASK_STATE_FAILED"
	taskStateCanceled      = "TASK_STATE_CANCELED"
	taskStateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	taskStateRejected      = "TASK_STATE_REJECTED"
	taskStateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"

	roleUser = "ROLE_USER"
)

type a2aMessage struct {
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
}

type a2aPart struct {
	Text string `json:"text,omitempty"`
}

// a2aSendMessageParams. Tenant carries the target agent name — see the
// matching comment on the server side (cmd/rakitsu/a2a_serve.go) for why.
type a2aSendMessageParams struct {
	Tenant  string     `json:"tenant,omitempty"`
	Message a2aMessage `json:"message"`
}

type a2aGetTaskParams struct {
	Tenant string `json:"tenant,omitempty"`
	ID     string `json:"id"`
}

type a2aSendMessageResult struct {
	Task    *a2aTask    `json:"task,omitempty"`
	Message *a2aMessage `json:"message,omitempty"`
}

type a2aTask struct {
	ID        string        `json:"id"`
	Status    a2aStatus     `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
}

type a2aStatus struct {
	State   string      `json:"state"`
	Message *a2aMessage `json:"message,omitempty"`
}

type a2aArtifact struct {
	Parts []a2aPart `json:"parts"`
}

// Polling backoff used while awaiting a task that a remote agent left in a
// non-terminal state (TASK_STATE_SUBMITTED/WORKING) instead of completing it
// synchronously, as rakitsu's own /a2a handler always does today.
const (
	a2aPollInitialDelay = 250 * time.Millisecond
	a2aPollMaxDelay     = 3 * time.Second
)

// ─── A2ATool ──────────────────────────────────────────────────────────────────

// A2ATool delegates a task to a named agent running in a remote rakitsu
// instance (or any real A2A server) via the A2A protocol's JSON-RPC binding
// (SendMessage, polling GetTask if the task isn't completed synchronously).
type A2ATool struct {
	name       string
	desc       string
	endpoint   string // base URL of the remote rakitsu, e.g. "http://host:9100"
	agentName  string
	apiKey     string // sent as "Authorization: Bearer <apiKey>" when non-empty
	httpClient *http.Client
	nextID     atomic.Int64
}

func (t *A2ATool) GetName() string        { return t.name }
func (t *A2ATool) GetDescription() string { return t.desc }

func (t *A2ATool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The task or question to delegate to the remote agent",
			},
		},
		"required": []string{"query"},
	}
}

func (t *A2ATool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("a2a tool %q: missing required argument \"query\"", t.name)
	}

	msg := a2aMessage{
		MessageID: uuid.New().String(),
		Role:      roleUser,
		Parts:     []a2aPart{{Text: query}},
	}

	var result a2aSendMessageResult
	if err := t.call(ctx, "SendMessage", a2aSendMessageParams{Tenant: t.agentName, Message: msg}, &result); err != nil {
		return "", err
	}

	if result.Task == nil {
		if result.Message != nil && len(result.Message.Parts) > 0 {
			return result.Message.Parts[0].Text, nil
		}
		return "", fmt.Errorf("a2a %q: empty result", t.name)
	}

	return t.awaitTerminal(ctx, result.Task)
}

// awaitTerminal resolves a task's final text, polling GetTask if the remote
// agent left it in a non-terminal state instead of completing it inline.
func (t *A2ATool) awaitTerminal(ctx context.Context, task *a2aTask) (string, error) {
	delay := a2aPollInitialDelay
	for {
		switch task.Status.State {
		case taskStateCompleted:
			if len(task.Artifacts) > 0 && len(task.Artifacts[0].Parts) > 0 {
				return task.Artifacts[0].Parts[0].Text, nil
			}
			return "", nil
		case taskStateFailed, taskStateRejected, taskStateCanceled:
			msg := "remote agent failed"
			if task.Status.Message != nil && len(task.Status.Message.Parts) > 0 && task.Status.Message.Parts[0].Text != "" {
				msg = task.Status.Message.Parts[0].Text
			}
			return "", fmt.Errorf("a2a %q: task %s: %s", t.name, task.Status.State, msg)
		case taskStateAuthRequired:
			return "", fmt.Errorf("a2a %q: remote agent requires authentication mid-task, which this client does not support", t.name)
		case taskStateInputRequired:
			return "", fmt.Errorf("a2a %q: remote agent requires additional input mid-task, which this client does not support", t.name)
		case taskStateSubmitted, taskStateWorking:
			// Not terminal — fall through to the poll below.
		default:
			// A state this client doesn't model (different enum casing from
			// a non-rakitsu peer, a future value, malformed/empty status) is
			// not "keep polling" — without this arm every unrecognized state
			// fell through to the poll loop below and never terminated.
			return "", fmt.Errorf("a2a %q: unexpected task state %q", t.name, task.Status.State)
		}

		// TASK_STATE_SUBMITTED / TASK_STATE_WORKING: wait and poll.
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("a2a %q: context ended while waiting for task %s (last state %s): %w", t.name, task.ID, task.Status.State, ctx.Err())
		case <-time.After(delay):
		}

		var next a2aTask
		if err := t.call(ctx, "GetTask", a2aGetTaskParams{Tenant: t.agentName, ID: task.ID}, &next); err != nil {
			return "", err
		}
		task = &next

		if delay < a2aPollMaxDelay {
			delay *= 2
			if delay > a2aPollMaxDelay {
				delay = a2aPollMaxDelay
			}
		}
	}
}

// call performs one JSON-RPC round trip against the endpoint's /a2a route
// and decodes the result into out.
func (t *A2ATool) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	req := a2aRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", t.nextID.Add(1)),
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("a2a %q: marshal request: %w", t.name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.endpoint, "/")+"/a2a", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("a2a %q: build request: %w", t.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("a2a %q: POST: %w", t.name, err)
	}
	defer resp.Body.Close()

	// A spec-compliant JSON-RPC peer answers RPC-level errors with HTTP 200
	// and a JSON-RPC error envelope (rakitsu's own server does exactly
	// that — see writeError in cmd/rakitsu/a2a_serve.go). A non-2xx status
	// means something failed before the JSON-RPC layer even ran — auth
	// (jsonErrorResponse's {"error": "<string>"} shape doesn't match this
	// struct's *a2aRPCError field and fails Decode below with a confusing
	// type-mismatch error), a gateway, a 500 — so check status first and
	// name the actual problem instead of a decode failure that isn't one.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("a2a %q: HTTP %s", t.name, resp.Status)
	}

	var a2aResp a2aResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a2aResp); err != nil {
		return fmt.Errorf("a2a %q: decode response: %w", t.name, err)
	}

	if a2aResp.Error != nil {
		return fmt.Errorf("a2a %q: RPC error %d: %s", t.name, a2aResp.Error.Code, a2aResp.Error.Message)
	}
	if len(a2aResp.Result) == 0 {
		return fmt.Errorf("a2a %q: empty result", t.name)
	}
	if err := json.Unmarshal(a2aResp.Result, out); err != nil {
		return fmt.Errorf("a2a %q: decode result: %w", t.name, err)
	}
	return nil
}

// ─── Factory ──────────────────────────────────────────────────────────────────

// NewA2ATool creates an A2ATool from a YAML ToolDefinition.
// Required fields: def.URL (endpoint), def.AgentName (agent name on remote).
func NewA2ATool(def *config.ToolDefinition) (*A2ATool, error) {
	if def.URL == "" {
		return nil, fmt.Errorf("a2a tool %q: endpoint (url) is required", def.Name)
	}
	if def.AgentName == "" {
		return nil, fmt.Errorf("a2a tool %q: agent name is required", def.Name)
	}

	desc := def.Description
	if desc == "" {
		desc = fmt.Sprintf("Delegate to agent %q at %s", def.AgentName, def.URL)
	}

	// 0 (unset) falls back to the 30s default; a negative value disables the
	// HTTP client's own timeout entirely, leaving the call bound only by
	// whatever context deadline the caller supplies — matching the <=0
	// convention used elsewhere (e.g. cmd/rakitsu/run.go's --timeout).
	var timeout time.Duration
	switch {
	case def.Timeout == 0:
		timeout = 30 * time.Second
	case def.Timeout > 0:
		timeout = time.Duration(def.Timeout) * time.Second
	}

	return &A2ATool{
		name:      def.Name,
		desc:      desc,
		endpoint:  def.URL,
		agentName: def.AgentName,
		apiKey:    def.APIKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}
