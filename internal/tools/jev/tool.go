// Package jev provides a tool for TypeSafe AI's Jev model.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

const defaultEndpoint = "https://api.typesafe.ai/v1/systemone"

// maxResponseBytes caps how much of Jev's response body Execute will read,
// matching the a2a tool's convention — a misbehaving or misconfigured
// custom endpoint (the tool supports a url override) shouldn't be able to
// stream an unbounded response into memory.
const maxResponseBytes = 1 << 20 // 1MB

// apiKeyEnv is the environment variable Jev's key falls back to when the
// tool definition sets none, matching the ${PROVIDER_API_KEY} convention
// other Rakitsu providers use.
const apiKeyEnv = "TYPESAFE_API_KEY"

// Tool invokes Jev's System One API.
type Tool struct {
	name        string
	description string
	endpoint    string
	apiKeyVal   string // from def.APIKey (config-expanded, e.g. ${TYPESAFE_API_KEY}); falls back to apiKeyEnv when empty
	client      *http.Client
}

// NewTool creates a Jev tool from a config.ToolDefinition (type: jev).
// The endpoint is overridable via the definition's url field. The API key
// follows the same convention as the a2a tool type: def.APIKey (already
// ${VAR}-expanded by config.Load) if set, else the TYPESAFE_API_KEY env var.
func NewTool(def *config.ToolDefinition) *Tool {
	endpoint := def.URL
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	description := def.Description
	if description == "" {
		description = "Ask TypeSafe AI Jev typed noul, choice, or score questions about a state."
	}
	// 0 (unset) falls back to the 30s default; a negative value disables the
	// HTTP client's own timeout entirely, leaving the call bound only by
	// whatever context deadline the caller supplies — same convention as
	// the a2a tool type's def.Timeout handling.
	var timeout time.Duration
	switch {
	case def.Timeout == 0:
		timeout = 30 * time.Second
	case def.Timeout > 0:
		// def.Timeout * time.Second overflows time.Duration's int64 for an
		// absurdly large configured value, wrapping to negative — which
		// http.Client treats as "no timeout", silently defeating the whole
		// point of this branch. Clamp instead of multiplying unchecked.
		if maxTimeoutSeconds := int(math.MaxInt64 / int64(time.Second)); def.Timeout > maxTimeoutSeconds {
			timeout = time.Duration(math.MaxInt64)
		} else {
			timeout = time.Duration(def.Timeout) * time.Second
		}
	}
	return &Tool{
		name: def.Name, description: description, endpoint: endpoint, apiKeyVal: def.APIKey,
		client: &http.Client{Timeout: timeout},
	}
}

func (t *Tool) GetName() string { return t.name }

func (t *Tool) GetDescription() string { return t.description }

func (t *Tool) GetParametersSchema() map[string]interface{} {
	question := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":         map[string]interface{}{"type": "string", "enum": []string{"noul", "choice", "score"}},
			"instructions": map[string]interface{}{"type": "string"},
			"criteria":     map[string]interface{}{},
		},
		"required": []string{"type", "instructions"},
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"state":     map[string]interface{}{"description": "State to evaluate. Optional: omit this entirely to evaluate the current turn's own query text instead of retyping it — do this whenever the state you'd pass is the same content you were already given this turn.", "anyOf": []interface{}{map[string]interface{}{"type": "string"}, map[string]interface{}{"type": "object"}, map[string]interface{}{"type": "array"}}},
			"questions": map[string]interface{}{"type": "object", "minProperties": 1, "additionalProperties": question},
		},
		"required": []string{"questions"},
	}
}

func (t *Tool) apiKey() string {
	if t.apiKeyVal != "" {
		return t.apiKeyVal
	}
	return os.Getenv(apiKeyEnv)
}

// Execute sends state and typed questions to Jev and returns its JSON response.
func (t *Tool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	state, ok := args["state"]
	if !ok || state == nil {
		// Fall back to this turn's own query text, carried in ctx (see
		// tools.WithTurnQuery, set once per Run in internal/agent/agent.go)
		// rather than requiring the model to retype content it was already
		// given — the retyping was the root cause of a real
		// truncation/fabrication bug on large diffs. Reading it from
		// ctx rather than a field on Tool keeps this race-free when the
		// same registered Tool instance serves concurrent runs.
		//
		// This fallback only ever carries plain text: attachments (e.g.
		// --attach images) are folded into conversation history, not into
		// the ctx-carried query. When the turn had attachments, silently
		// falling back to the text-only query would let this tool evaluate
		// the wrong thing — e.g. "review the attached diff" would evaluate
		// that short phrase, not the diff — which is exactly the failure
		// mode a Jev-based security/risk gate exists to prevent. Refuse
		// instead of guessing: Rakitsu's attachments today are images
		// (internal/llm/attach.go), and Jev's API isn't documented as
		// vision-capable, so there's no correct way to fold them into
		// state even if this tool tried to.
		q, hasAttachments, ok := tools.TurnQueryFromContext(ctx)
		switch {
		case ok && hasAttachments:
			return "", fmt.Errorf("state argument required: this turn has attachments, and the turn-query fallback only carries the text query, not attachment content")
		case ok && q != "":
			state = q
		default:
			return "", fmt.Errorf("state argument required (and no turn query available to fall back to)")
		}
	}
	questions, ok := args["questions"].(map[string]interface{})
	if !ok || len(questions) == 0 {
		return "", fmt.Errorf("questions argument required")
	}
	apiKey := t.apiKey()
	if apiKey == "" {
		return "", fmt.Errorf("Jev API key is missing (set %s)", apiKeyEnv)
	}
	body, err := json.Marshal(map[string]interface{}{"state": state, "model": "jev-latest", "questions": questions})
	if err != nil {
		return "", fmt.Errorf("failed to encode Jev request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create Jev request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Jev request failed: %w", err)
	}
	defer resp.Body.Close()
	// Read one byte past the cap so an oversized response can be told apart
	// from one that happens to end exactly at the limit — io.LimitReader
	// alone truncates silently, and returning truncated (near-certainly
	// invalid JSON) bytes as a successful result would let a caller treat
	// malformed output as a legitimate Jev answer instead of a clear failure.
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read Jev response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return "", fmt.Errorf("Jev response exceeded %d byte limit", maxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Jev API error: %s: %s", resp.Status, string(responseBody))
	}
	return string(responseBody), nil
}
