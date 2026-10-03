package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// testA2AResponse mirrors srvA2AResponse but keeps Result as raw JSON so
// tests can decode it into whatever shape the method under test returns.
type testA2AResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *srvA2ARPCErr   `json:"error"`
}

func testConfig() *config.Config {
	return &config.Config{
		Name:        "Test Config",
		Description: "A config used for A2A handler tests.",
		Agents: []config.AgentDefinition{
			{Name: "Researcher"},
			{Name: "Writer"},
		},
	}
}

// stubRun returns a canned a2aRunFunc: text on success, or an error when
// query == triggerErr.
func stubRun(text string, triggerErr string, err error) a2aRunFunc {
	return func(ctx context.Context, cfg *config.Config, query string) (string, error) {
		if triggerErr != "" && query == triggerErr {
			return "", err
		}
		return text, nil
	}
}

func postA2A(t *testing.T, handler http.HandlerFunc, method string, id string, params interface{}) testA2AResponse {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var out testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// pollUntilTerminal polls GetTask against the same handler/store until the
// task reaches a terminal state, mirroring the backoff loop the real A2A
// client (internal/tools/a2a/tool.go's awaitTerminal) already does against a
// SendMessage that now returns before the agent finishes.
func pollUntilTerminal(t *testing.T, handler http.HandlerFunc, taskID string) srvA2ATask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp := postA2A(t, handler, "GetTask", "poll", srvGetTaskParams{ID: taskID})
		if resp.Error != nil {
			t.Fatalf("GetTask error: %+v", resp.Error)
		}
		var task srvA2ATask
		if err := json.Unmarshal(resp.Result, &task); err != nil {
			t.Fatalf("unmarshal task: %v", err)
		}
		if isTerminalTaskState(task.Status.State) {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not reach a terminal state in time (last state %q)", taskID, task.Status.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ─── SendMessage ──────────────────────────────────────────────────────────────

func TestA2AHandler_SendMessage_HappyPath(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("42", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "what is the answer"}}},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result srvSendMessageResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Task == nil {
		t.Fatal("expected a task in the result")
	}
	// SendMessage returns as soon as the task is recorded, not once the
	// agent finishes — it must not already be terminal here.
	if result.Task.Status.State != taskStateWorking {
		t.Errorf("state = %q, want %q immediately after SendMessage", result.Task.Status.State, taskStateWorking)
	}
	if result.Task.ID == "" || result.Task.ContextID == "" {
		t.Error("expected non-empty task id and context id")
	}

	final := pollUntilTerminal(t, handler, result.Task.ID)
	if final.Status.State != taskStateCompleted {
		t.Errorf("state = %q, want %q", final.Status.State, taskStateCompleted)
	}
	if len(final.Artifacts) != 1 || final.Artifacts[0].Parts[0].Text != "42" {
		t.Errorf("artifacts = %+v, want a single artifact with text \"42\"", final.Artifacts)
	}
}

func TestA2AHandler_SendMessage_AgentFailed(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("", "boom", errors.New("boom")))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "boom"}}},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected top-level RPC error (agent failures are task-level): %+v", resp.Error)
	}

	var result srvSendMessageResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	final := pollUntilTerminal(t, handler, result.Task.ID)
	if final.Status.State != taskStateFailed {
		t.Errorf("state = %q, want %q", final.Status.State, taskStateFailed)
	}
	if final.Status.Message == nil || !strings.Contains(final.Status.Message.Parts[0].Text, "boom") {
		t.Errorf("status.message = %+v, want it to mention the error", final.Status.Message)
	}
}

func TestA2AHandler_SendMessage_UnknownTenant(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "NoSuchAgent",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want code -32602", resp.Error)
	}
}

func TestA2AHandler_SendMessage_MissingTenant(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want code -32602", resp.Error)
	}
}

func TestA2AHandler_SendMessage_MissingText(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser},
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want code -32602", resp.Error)
	}
}

func TestA2AHandler_SendMessage_TaskContinuationUnsupported(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", TaskID: "existing-task", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	if resp.Error == nil || resp.Error.Code != -32004 {
		t.Fatalf("error = %+v, want code -32004 (UnsupportedOperationError)", resp.Error)
	}
}

// TestA2AHandler_SendMessage_ExtraPartsRejected covers a message with more
// than one part: only Parts[0].Text was ever read to build the agent
// query, so parts[1:] were silently discarded while the task still reported
// TASK_STATE_COMPLETED — a successful-looking result computed from a
// fraction of what the caller sent. Rejecting explicitly (mirroring TaskID
// continuation right above) gives the caller a signal instead of silent
// data loss.
func TestA2AHandler_SendMessage_ExtraPartsRejected(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant: "Researcher",
		Message: srvA2AMessage{
			MessageID: "m1", Role: roleUser,
			Parts: []srvA2APart{{Text: "part one"}, {Text: "part two"}},
		},
	})
	if resp.Error == nil || resp.Error.Code != -32004 {
		t.Fatalf("error = %+v, want code -32004 (UnsupportedOperationError)", resp.Error)
	}
}

// TestA2AHandler_SendMessage_ClientContextIDRejected covers a client
// supplying its own contextId: it was silently overwritten with a fresh
// uuid and the task reported success, with nothing telling the caller its
// context grouping didn't take — unlike TaskID continuation, which gets a
// clean rejection. Mirrors that same treatment.
func TestA2AHandler_SendMessage_ClientContextIDRejected(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant: "Researcher",
		Message: srvA2AMessage{
			MessageID: "m1", ContextID: "client-chosen-context", Role: roleUser,
			Parts: []srvA2APart{{Text: "hi"}},
		},
	})
	if resp.Error == nil || resp.Error.Code != -32004 {
		t.Fatalf("error = %+v, want code -32004 (UnsupportedOperationError)", resp.Error)
	}
}

// ─── GetTask / CancelTask ─────────────────────────────────────────────────────

func TestA2AHandler_GetTask_Found(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("result text", "", nil))

	sendResp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	var sendResult srvSendMessageResult
	if err := json.Unmarshal(sendResp.Result, &sendResult); err != nil {
		t.Fatalf("unmarshal SendMessage result: %v", err)
	}

	final := pollUntilTerminal(t, handler, sendResult.Task.ID)
	if final.ID != sendResult.Task.ID || final.Status.State != taskStateCompleted {
		t.Errorf("task = %+v, want id %q completed", final, sendResult.Task.ID)
	}
}

func TestA2AHandler_GetTask_NotFound(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "GetTask", "1", srvGetTaskParams{ID: "does-not-exist"})
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("error = %+v, want code -32001 (TaskNotFoundError)", resp.Error)
	}
}

func TestA2AHandler_GetTask_MissingID(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "GetTask", "1", srvGetTaskParams{})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want code -32602", resp.Error)
	}
}

func TestA2AHandler_CancelTask_TerminalRejected(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("result text", "", nil))

	sendResp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	var sendResult srvSendMessageResult
	if err := json.Unmarshal(sendResp.Result, &sendResult); err != nil {
		t.Fatalf("unmarshal SendMessage result: %v", err)
	}

	// Wait for the (fast, non-blocking) stub run to actually finish and
	// reach a terminal state before trying to cancel it.
	pollUntilTerminal(t, handler, sendResult.Task.ID)

	cancelResp := postA2A(t, handler, "CancelTask", "2", srvCancelTaskParams{ID: sendResult.Task.ID})
	if cancelResp.Error == nil || cancelResp.Error.Code != -32002 {
		t.Fatalf("error = %+v, want code -32002 (TaskNotCancelableError)", cancelResp.Error)
	}
}

// TestA2AHandler_CancelTask_CancelsRunningTask covers the actual point of
// async SendMessage: a task still in TASK_STATE_WORKING can be canceled —
// and canceling it must genuinely stop the in-flight run (observe ctx.Done()),
// not just flip the stored state while the run keeps going underneath.
func TestA2AHandler_CancelTask_CancelsRunningTask(t *testing.T) {
	started := make(chan struct{})
	var sawCancel int32
	run := func(ctx context.Context, cfg *config.Config, query string) (string, error) {
		close(started)
		<-ctx.Done()
		atomic.StoreInt32(&sawCancel, 1)
		return "", ctx.Err()
	}
	handler := a2aHandlerFunc(testConfig(), run)

	sendResp := postA2A(t, handler, "SendMessage", "1", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m1", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
	})
	var sendResult srvSendMessageResult
	if err := json.Unmarshal(sendResp.Result, &sendResult); err != nil {
		t.Fatalf("unmarshal SendMessage result: %v", err)
	}
	if sendResult.Task.Status.State != taskStateWorking {
		t.Fatalf("state = %q, want %q immediately after SendMessage", sendResult.Task.Status.State, taskStateWorking)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("run was never invoked")
	}

	cancelResp := postA2A(t, handler, "CancelTask", "2", srvCancelTaskParams{ID: sendResult.Task.ID})
	if cancelResp.Error != nil {
		t.Fatalf("unexpected cancel error: %+v", cancelResp.Error)
	}
	var canceled srvA2ATask
	if err := json.Unmarshal(cancelResp.Result, &canceled); err != nil {
		t.Fatalf("unmarshal cancel result: %v", err)
	}
	if canceled.Status.State != taskStateCanceled {
		t.Errorf("state = %q, want %q", canceled.Status.State, taskStateCanceled)
	}

	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&sawCancel) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("run's context was never actually canceled")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The goroutine's own completion path (ctx.Err() returned as execErr)
	// must not overwrite the canceled state back to failed once it runs.
	time.Sleep(20 * time.Millisecond)
	getResp := postA2A(t, handler, "GetTask", "3", srvGetTaskParams{ID: sendResult.Task.ID})
	var final srvA2ATask
	if err := json.Unmarshal(getResp.Result, &final); err != nil {
		t.Fatalf("unmarshal final task: %v", err)
	}
	if final.Status.State != taskStateCanceled {
		t.Errorf("final state = %q, want %q (must not be overwritten after cancellation)", final.Status.State, taskStateCanceled)
	}
}

func TestA2AHandler_CancelTask_NotFound(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "CancelTask", "1", srvCancelTaskParams{ID: "does-not-exist"})
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("error = %+v, want code -32001 (TaskNotFoundError)", resp.Error)
	}
}

// ─── Protocol-level errors ────────────────────────────────────────────────────

func TestA2AHandler_UnknownMethod(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("x", "", nil))
	resp := postA2A(t, handler, "tasks/send", "1", map[string]string{}) // pre-1.0 method name — must not resolve
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("error = %+v, want code -32601 (MethodNotFoundError)", resp.Error)
	}
}

func TestA2AHandler_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(a2aHandlerFunc(testConfig(), stubRun("x", "", nil)))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var out testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32700 {
		t.Fatalf("error = %+v, want code -32700 (JSONParseError)", out.Error)
	}
}

func TestA2AHandler_MethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(a2aHandlerFunc(testConfig(), stubRun("x", "", nil)))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// ─── Agent Card discovery ─────────────────────────────────────────────────────

func TestAgentCardHandler_Shape(t *testing.T) {
	// Fallback deliberately wrong: proves the asserted URL below came from
	// the request's Host, not this constructor argument.
	handler := agentCardHandlerFunc(testConfig(), "http://wrong-fallback.invalid/a2a")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "example.com"
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var card srvAgentCard
	if err := json.NewDecoder(rr.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	if card.Name != "Test Config" {
		t.Errorf("name = %q, want %q", card.Name, "Test Config")
	}
	if card.Version != "1.0.0" {
		t.Errorf("version = %q, want fallback %q (testConfig sets no Version)", card.Version, "1.0.0")
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != "http://example.com/a2a" {
		t.Errorf("supportedInterfaces = %+v", card.SupportedInterfaces)
	}
	if card.SupportedInterfaces[0].ProtocolBinding != "JSONRPC" {
		t.Errorf("protocolBinding = %q, want %q", card.SupportedInterfaces[0].ProtocolBinding, "JSONRPC")
	}
	// The wire schema this server implements (a2aproject/A2A), not this
	// agent's own config version above — the two are unrelated axes.
	if card.SupportedInterfaces[0].ProtocolVersion != "1.0.1" {
		t.Errorf("protocolVersion = %q, want %q", card.SupportedInterfaces[0].ProtocolVersion, "1.0.1")
	}
	if len(card.Skills) != 2 {
		t.Fatalf("skills = %+v, want 2 (one per configured agent)", card.Skills)
	}
	names := map[string]bool{card.Skills[0].ID: true, card.Skills[1].ID: true}
	if !names["Researcher"] || !names["Writer"] {
		t.Errorf("skill ids = %v, want Researcher and Writer", names)
	}
}

// TestAgentCardHandler_VersionFromConfig covers a configured Version being
// reported instead of the hardcoded fallback — a client caching capabilities
// by agent-card version should see it change when the config does.
func TestAgentCardHandler_VersionFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Version = "2.3.1"
	handler := agentCardHandlerFunc(cfg, "http://example.com/a2a")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "example.com"
	rr := httptest.NewRecorder()
	handler(rr, req)

	var card srvAgentCard
	if err := json.NewDecoder(rr.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	if card.Version != "2.3.1" {
		t.Errorf("version = %q, want %q (from cfg.Version)", card.Version, "2.3.1")
	}
}

// TestAgentCardHandler_URLFallsBackWhenHostEmpty covers the case an incoming
// request somehow has no Host (malformed or synthetic) — the handler should
// use the precomputed fallback URL rather than advertise a broken "://a2a".
func TestAgentCardHandler_URLFallsBackWhenHostEmpty(t *testing.T) {
	handler := agentCardHandlerFunc(testConfig(), "http://configured-fallback.example/a2a")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = ""
	rr := httptest.NewRecorder()
	handler(rr, req)

	var card srvAgentCard
	if err := json.NewDecoder(rr.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != "http://configured-fallback.example/a2a" {
		t.Errorf("supportedInterfaces = %+v, want fallback URL", card.SupportedInterfaces)
	}
}

// TestAgentCardHandler_SecuritySchemesWhenTokenConfigured covers the agent-card security schemes
// item 3: docs/SECURITY.md justifies leaving the agent-card route
// unauthenticated on the grounds a client can learn what auth is required
// before authenticating — but the card conveyed nothing, so that claim
// wasn't actually true. When RAKITSU_API_TOKEN is set, the card must
// advertise the bearer requirement.
func TestAgentCardHandler_SecuritySchemesWhenTokenConfigured(t *testing.T) {
	t.Setenv("RAKITSU_API_TOKEN", "s3cret")
	handler := agentCardHandlerFunc(testConfig(), "http://example.com/a2a")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "example.com"
	rr := httptest.NewRecorder()
	handler(rr, req)

	var card srvAgentCard
	if err := json.NewDecoder(rr.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	scheme, ok := card.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Fatalf("securitySchemes missing bearerAuth entry, got %+v", card.SecuritySchemes)
	}
	if scheme.Type != "http" || scheme.Scheme != "bearer" {
		t.Errorf("bearerAuth scheme = %+v, want type=http scheme=bearer", scheme)
	}
	if len(card.Security) != 1 {
		t.Fatalf("security = %+v, want one requirement entry", card.Security)
	}
	if _, ok := card.Security[0]["bearerAuth"]; !ok {
		t.Errorf("security[0] = %+v, want it to reference bearerAuth", card.Security[0])
	}
}

// TestAgentCardHandler_NoSecuritySchemesWhenTokenNotConfigured covers the
// common loopback-no-token case: the card must not claim an auth
// requirement that doesn't actually exist.
func TestAgentCardHandler_NoSecuritySchemesWhenTokenNotConfigured(t *testing.T) {
	t.Setenv("RAKITSU_API_TOKEN", "")
	handler := agentCardHandlerFunc(testConfig(), "http://example.com/a2a")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "example.com"
	rr := httptest.NewRecorder()
	handler(rr, req)

	var card srvAgentCard
	if err := json.NewDecoder(rr.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	if len(card.SecuritySchemes) != 0 {
		t.Errorf("securitySchemes = %+v, want none when no token is configured", card.SecuritySchemes)
	}
	if len(card.Security) != 0 {
		t.Errorf("security = %+v, want none when no token is configured", card.Security)
	}
}

// ─── a2aTaskStore ───────────────────────────────────────────────────────────

// TestA2ATaskStore_Get_ReturnsIndependentCopy covers put/get's copy being
// shallow: cp := *t copies the outer struct only, so Artifacts, History,
// Status.Message, and each artifact's Parts stayed shared with the stored
// task despite the doc comments' stronger claim ("never the internal
// pointer," "a concurrent write can never race with a caller reading...
// what get returned"). Not exploitable today — nothing currently mutates a
// task after put — but the store exists for when async execution lands,
// and a shallow copy is exactly the part of it that wouldn't hold up then.
// Asserts a caller mutating a get() result's nested slice can't reach the
// stored task.
func TestA2ATaskStore_Get_ReturnsIndependentCopy(t *testing.T) {
	store := newA2ATaskStore()
	store.put(&srvA2ATask{
		ID:     "t1",
		Status: srvA2AStatus{State: taskStateCompleted},
		Artifacts: []srvA2AArtifact{
			{ArtifactID: "a1", Parts: []srvA2APart{{Text: "original"}}},
		},
	})

	got, ok := store.get("t1")
	if !ok {
		t.Fatal("expected to find t1")
	}
	got.Artifacts[0].Parts[0].Text = "mutated by caller"

	got2, ok := store.get("t1")
	if !ok {
		t.Fatal("expected to find t1 again")
	}
	if got2.Artifacts[0].Parts[0].Text != "original" {
		t.Errorf("stored task mutated through a get() copy's shared slice: got %q, want %q",
			got2.Artifacts[0].Parts[0].Text, "original")
	}
}

// ─── Concurrency ──────────────────────────────────────────────────────────────

// TestA2AHandler_ConcurrentTraffic exercises SendMessage/GetTask/CancelTask
// against the same handler and overlapping task IDs from many goroutines at
// once, under -race. It's the direct regression test for the task-store race
// found in review (get-then-put outside a single lock let a concurrent
// CancelTask race a mutation) — cancelIfPossible's single-lock design and
// get/put's copy-on-access should make this race-free. Now that SendMessage
// itself runs its agent call in a background goroutine, this also exercises
// genuinely concurrent writers racing each task's completion/cancellation.
func TestA2AHandler_ConcurrentTraffic(t *testing.T) {
	handler := a2aHandlerFunc(testConfig(), stubRun("ok", "", nil))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	const workers = 20
	var wg sync.WaitGroup
	ids := make(chan string, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(map[string]interface{}{ //nolint:errcheck
				"jsonrpc": "2.0", "id": "1", "method": "SendMessage",
				"params": srvSendMessageParams{
					Tenant:  "Researcher",
					Message: srvA2AMessage{MessageID: "m", Role: roleUser, Parts: []srvA2APart{{Text: "hi"}}},
				},
			})
			resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("SendMessage POST: %v", err)
				return
			}
			defer resp.Body.Close()
			var out testA2AResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			var result srvSendMessageResult
			if err := json.Unmarshal(out.Result, &result); err != nil {
				t.Errorf("unmarshal result: %v", err)
				return
			}
			ids <- result.Task.ID
		}()
	}
	wg.Wait()
	close(ids)

	var idList []string
	for id := range ids {
		idList = append(idList, id)
	}
	if len(idList) == 0 {
		t.Fatal("no task IDs collected from SendMessage phase")
	}

	for i := 0; i < workers*2; i++ {
		wg.Add(1)
		id := idList[i%len(idList)]
		getRequest := i%2 == 0
		go func(id string, getRequest bool) {
			defer wg.Done()
			var body []byte
			if getRequest {
				body, _ = json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": "g", "method": "GetTask", "params": srvGetTaskParams{ID: id}}) //nolint:errcheck
			} else {
				body, _ = json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": "c", "method": "CancelTask", "params": srvCancelTaskParams{ID: id}}) //nolint:errcheck
			}
			resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("POST: %v", err)
				return
			}
			resp.Body.Close()
		}(id, getRequest)
	}
	wg.Wait()
}

func TestAgentCardHandler_MethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(agentCardHandlerFunc(testConfig(), "http://example.com/a2a"))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// Completion may have computed its result before CancelTask wins the store
// lock. Exercise that ordering deterministically, without timing dependencies.
func TestA2ATaskStore_FinishAfterCancel(t *testing.T) {
	for _, state := range []string{taskStateCompleted, taskStateFailed} {
		t.Run(state, func(t *testing.T) {
			store := newA2ATaskStore()
			store.put(&srvA2ATask{ID: "task", Status: srvA2AStatus{State: taskStateWorking}})
			status := srvA2AStatus{State: state}
			artifacts := []srvA2AArtifact{{ArtifactID: "result", Parts: []srvA2APart{{Text: "done"}}}}
			canceled, found, terminal := store.cancelIfPossible("task")
			if !found || terminal {
				t.Fatal("expected cancellation to succeed")
			}
			finished, ok := store.finishIfNotCanceled("task", status, artifacts)
			if ok || finished.Status != canceled.Status || len(finished.Artifacts) != 0 {
				t.Fatalf("completion overwrote cancellation: %+v, ok=%v", finished, ok)
			}
			stored, _ := store.get("task")
			if stored.Status != canceled.Status || len(stored.Artifacts) != 0 {
				t.Fatalf("stored cancellation changed: %+v", stored)
			}
		})
	}
}

// ─── Red-team / abuse tests ──────────────────────────────────────────────────

// Test_A2ASendMessage_OversizedBodyReturnsError verifies that a POST with a
// 2 MB JSON body is rejected cleanly without panic.
func Test_A2ASendMessage_OversizedBodyReturnsError(t *testing.T) {
	var calls int32
	handler := a2aHandlerFunc(testConfig(), func(ctx context.Context, cfg *config.Config, query string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "unexpected", nil
	})
	var panics int32
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				atomic.AddInt32(&panics, 1)
				http.Error(w, "handler panicked", http.StatusInternalServerError)
			}
		}()
		handler(w, r)
	})

	// Make the entire valid JSON request exactly 2 MiB.
	prefix := `{"jsonrpc":"2.0","id":"oversized","method":"SendMessage","params":{"tenant":"Researcher","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"`
	suffix := `"}]}}}`
	body := prefix + strings.Repeat("x", (2<<20)-len(prefix)-len(suffix)) + suffix
	if len(body) != 2<<20 || !json.Valid([]byte(body)) {
		t.Fatal("expected a valid, exactly 2 MiB JSON request")
	}

	srv := httptest.NewServer(wrappedHandler)
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var out testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode rejection: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32700 {
		t.Fatalf("error = %+v, want JSON parse error (-32700)", out.Error)
	}
	if len(out.Result) != 0 {
		t.Fatalf("rejected request returned a result: %s", out.Result)
	}
	if got := atomic.LoadInt32(&panics); got != 0 {
		t.Fatalf("handler panicked %d times", got)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("rejected request invoked run %d times", got)
	}
}

// Test_A2A_TaskStoreDoesNotGrowUnbounded verifies that flooding the store
// with 600 SendMessage requests keeps the store size bounded at maxStoredA2ATasks.
func Test_A2A_TaskStoreDoesNotGrowUnbounded(t *testing.T) {
	store := newA2ATaskStore()
	cfg := testConfig()
	run := func(context.Context, *config.Config, string) (string, error) {
		return "done", nil
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req srvA2ARequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		writeResult := func(id json.RawMessage, result interface{}) {
			if err := json.NewEncoder(w).Encode(srvA2AResponse{
				JSONRPC: "2.0", ID: id, Result: result,
			}); err != nil {
				t.Errorf("encode result: %v", err)
			}
		}
		writeErr := func(id json.RawMessage, code int, message string) {
			if err := json.NewEncoder(w).Encode(srvA2AResponse{
				JSONRPC: "2.0", ID: id,
				Error: &srvA2ARPCErr{Code: code, Message: message},
			}); err != nil {
				t.Errorf("encode error: %v", err)
			}
		}
		handleA2ASendMessage(r.Context(), cfg, run, store, req, writeResult, writeErr)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 5 * time.Second

	body := `{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"Researcher","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`
	ids := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		resp, err := client.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		var out testA2AResponse
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("decode request %d: %v", i, err)
		}
		if out.Error != nil {
			t.Fatalf("request %d RPC error: %+v", i, out.Error)
		}
		var result srvSendMessageResult
		if err := json.Unmarshal(out.Result, &result); err != nil {
			t.Fatalf("decode task %d: %v", i, err)
		}
		if result.Task == nil || result.Task.ID == "" {
			t.Fatalf("request %d returned no task ID", i)
		}
		ids = append(ids, result.Task.ID)

		store.mu.Lock()
		n, orderLen := len(store.tasks), len(store.order)
		store.mu.Unlock()
		if n > maxStoredA2ATasks || orderLen > maxStoredA2ATasks {
			t.Fatalf("request %d exceeded capacity: tasks=%d, order=%d", i, n, orderLen)
		}
	}

	// Wait for background completion and cancel-function cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		settled := len(store.cancels) == 0
		for _, task := range store.tasks {
			settled = settled && task.Status.State == taskStateCompleted
		}
		store.mu.Unlock()
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background tasks did not finish")
		}
		time.Sleep(time.Millisecond)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.tasks) != maxStoredA2ATasks || len(store.order) != maxStoredA2ATasks {
		t.Fatalf("store size: tasks=%d, order=%d; want %d",
			len(store.tasks), len(store.order), maxStoredA2ATasks)
	}
	for i, id := range ids {
		_, found := store.tasks[id]
		if want := i >= len(ids)-maxStoredA2ATasks; found != want {
			t.Fatalf("task %d retained=%v, want %v", i, found, want)
		}
	}
}

// Test_A2A_RaceConditionCancelAndCompletion verifies that concurrent
// CancelTask and task completion don't corrupt state.
func Test_A2A_RaceConditionCancelAndCompletion(t *testing.T) {
	store := newA2ATaskStore()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req srvA2ARequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		writeResult := func(id json.RawMessage, result interface{}) {
			if err := json.NewEncoder(w).Encode(srvA2AResponse{
				JSONRPC: "2.0", ID: id, Result: result,
			}); err != nil {
				t.Errorf("encode result: %v", err)
			}
		}
		writeErr := func(id json.RawMessage, code int, message string) {
			if err := json.NewEncoder(w).Encode(srvA2AResponse{
				JSONRPC: "2.0", ID: id,
				Error: &srvA2ARPCErr{Code: code, Message: message},
			}); err != nil {
				t.Errorf("encode error: %v", err)
			}
		}
		handleA2ACancelTask(store, req, writeResult, writeErr)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 5 * time.Second

	// Exercise both contenders repeatedly. Completion uses the same atomic
	// finalization path as the background SendMessage goroutine.
	for i := 0; i < 200; i++ {
		store.put(&srvA2ATask{
			ID: "task", ContextID: "context",
			Status: srvA2AStatus{State: taskStateWorking},
		})
		start := make(chan struct{})
		finished := make(chan bool, 1)
		go func() {
			<-start
			_, ok := store.finishIfNotCanceled(
				"task",
				srvA2AStatus{State: taskStateCompleted},
				[]srvA2AArtifact{{ArtifactID: "result", Parts: []srvA2APart{{Text: "done"}}}},
			)
			finished <- ok
		}()

		var out testA2AResponse
		var requestErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := client.Post(srv.URL, "application/json", strings.NewReader(
				`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"id":"task"}}`,
			))
			if err != nil {
				requestErr = err
				return
			}
			defer resp.Body.Close()
			requestErr = json.NewDecoder(resp.Body).Decode(&out)
		}()

		close(start)
		completionWon := <-finished
		wg.Wait()
		if requestErr != nil {
			t.Fatalf("iteration %d: CancelTask: %v", i, requestErr)
		}

		final, found := store.get("task")
		if !found || final.ID != "task" || final.ContextID != "context" {
			t.Fatalf("iteration %d: task identity corrupted: %+v", i, final)
		}
		if completionWon {
			if out.Error == nil || out.Error.Code != -32002 {
				t.Fatalf("iteration %d: completion won, cancel error=%+v, want code -32002", i, out.Error)
			}
			if final.Status.State != taskStateCompleted ||
				len(final.Artifacts) != 1 ||
				len(final.Artifacts[0].Parts) != 1 ||
				final.Artifacts[0].Parts[0].Text != "done" {
				t.Fatalf("iteration %d: corrupted completed task: %+v", i, final)
			}
		} else {
			if out.Error != nil {
				t.Fatalf("iteration %d: cancellation won, error=%+v", i, out.Error)
			}
			var canceled srvA2ATask
			if err := json.Unmarshal(out.Result, &canceled); err != nil {
				t.Fatalf("iteration %d: decode canceled task: %v", i, err)
			}
			if canceled.Status.State != taskStateCanceled ||
				final.Status.State != taskStateCanceled || len(final.Artifacts) != 0 {
				t.Fatalf("iteration %d: cancellation overwritten: %+v", i, final)
			}
		}
	}
}
