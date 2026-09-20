package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestToolExecuteQuestionTypes(t *testing.T) {
	tests := []struct {
		name     string
		question map[string]interface{}
		answer   map[string]interface{}
	}{
		{name: "noul", question: map[string]interface{}{"type": "noul", "instructions": "Is this safe?"}, answer: map[string]interface{}{"type": "noul", "noul": 1}},
		{name: "choice", question: map[string]interface{}{"type": "choice", "instructions": "Pick one", "criteria": map[string]interface{}{"a": "First", "b": nil}}, answer: map[string]interface{}{"type": "choice", "choice": "a", "probabilities": map[string]interface{}{"a": 0.9}, "confidence": 0.9}},
		{name: "score", question: map[string]interface{}{"type": "score", "instructions": "Rate it", "criteria": []interface{}{"low", "high"}}, answer: map[string]interface{}{"type": "score", "score": 1, "legend": "high", "probabilities": []interface{}{0.1, 0.9}, "confidence": 0.9}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("authorization = %q", got)
				}
				body, _ := io.ReadAll(r.Body)
				var request map[string]interface{}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Errorf("request JSON: %v", err)
				}
				if request["model"] != "jev-latest" || request["state"] != "test state" {
					t.Errorf("request = %#v", request)
				}
				questions := request["questions"].(map[string]interface{})
				if questions["q"].(map[string]interface{})["type"] != tt.question["type"] {
					t.Errorf("question = %#v", questions["q"])
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"model": "jev-latest", "answers": map[string]interface{}{"q": tt.answer}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 2}})
			}))
			defer server.Close()

			t.Setenv("TYPESAFE_API_KEY", "test-key")
			tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL + "/v1/systemone"})
			result, err := tool.Execute(context.Background(), map[string]interface{}{"state": "test state", "questions": map[string]interface{}{"q": tt.question}})
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]interface{}
			if err := json.Unmarshal([]byte(result), &response); err != nil || response["model"] != "jev-latest" {
				t.Fatalf("result = %q, error = %v", result, err)
			}
		})
	}
}

func TestToolExecuteHTTPStatusErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusTooManyRequests} {
		t.Run(strings.TrimSpace(http.StatusText(status)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("jev failure"))
			}))
			defer server.Close()
			t.Setenv("TYPESAFE_API_KEY", "test-key")
			tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
			_, err := tool.Execute(context.Background(), map[string]interface{}{"state": "state", "questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "check"}}})
			if err == nil || !strings.Contains(err.Error(), http.StatusText(status)) || !strings.Contains(err.Error(), "jev failure") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestNewToolClampsOversizedTimeoutInsteadOfOverflowing is a regression
// test for a real overflow bug: def.Timeout * time.Second on an absurdly
// large configured value wrapped time.Duration's int64 to negative, which
// http.Client treats as "no timeout" — silently defeating a supposedly
// bounded request instead of erroring or clamping.
func TestNewToolClampsOversizedTimeoutInsteadOfOverflowing(t *testing.T) {
	tool := NewTool(&config.ToolDefinition{Name: "jev", Timeout: 10_000_000_000}) // seconds; overflows int64 ns * time.Second unless clamped
	if tool.client.Timeout <= 0 {
		t.Fatalf("client.Timeout = %v, want a large positive duration, not overflowed to non-positive", tool.client.Timeout)
	}
}

// TestToolExecuteRejectsOversizedResponse is a regression test for a
// resource-exhaustion finding and its own follow-up finding: Execute must
// not buffer an unbounded response body from a misbehaving or
// misconfigured endpoint (the tool supports a url override) into memory,
// AND must not silently return a truncated (near-certainly invalid JSON)
// body as if it were a legitimate successful result — that would let a
// caller treat malformed output as a real Jev answer instead of a clear
// failure.
func TestToolExecuteRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		chunk := bytes.Repeat([]byte("x"), 1<<16)
		for i := 0; i < 20; i++ { // 20 * 64KB = 1.25MB, over the 1MB cap
			w.Write(chunk)
		}
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
	_, err := tool.Execute(context.Background(), map[string]interface{}{"state": "state", "questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "check"}}})
	if err == nil {
		t.Fatal("expected an error for an oversized response, got none")
	}
}

// TestToolExecuteAllowsResponseAtExactCap is a boundary check: a response
// of exactly maxResponseBytes must still succeed — only strictly over the
// cap should error.
func TestToolExecuteAllowsResponseAtExactCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// A real Jev response is JSON, but Execute doesn't parse it — it
		// just relays bytes — so this only needs to exercise the size
		// boundary, not be valid JSON.
		w.Write(bytes.Repeat([]byte("x"), maxResponseBytes))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"state": "state", "questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "check"}}})
	if err != nil {
		t.Fatalf("unexpected error at exact cap: %v", err)
	}
	if len(result) != maxResponseBytes {
		t.Fatalf("result length = %d, want exactly %d", len(result), maxResponseBytes)
	}
}

func TestToolUsesAPIKeyEnvironment(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev"})
	if tool.apiKey() != "env-key" {
		t.Fatalf("api key = %q", tool.apiKey())
	}
}

func TestToolPrefersConfigAPIKeyOverEnvironment(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", APIKey: "config-key"})
	if tool.apiKey() != "config-key" {
		t.Fatalf("api key = %q, want config-key to take precedence", tool.apiKey())
	}
}

func TestToolExecuteMissingState(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev"})
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("error = %v, want a state-related error", err)
	}
}

// TestToolExecuteFallsBackToTurnQuery is a regression test for the retyping/fabrication bug: state
// used to be a required literal argument, forcing the calling LLM to
// retype potentially large content it already had — which truncated on a
// real ~35KB diff and led to a fabricated fallback answer. state is now
// optional; when omitted, Execute falls back to the turn query carried in
// ctx (tools.WithTurnQuery — set once per Run in internal/agent/agent.go,
// not stored as mutable state on the Tool, since the same registered Tool
// instance can serve concurrent runs).
func TestToolExecuteFallsBackToTurnQuery(t *testing.T) {
	var gotState interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]interface{}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("request JSON: %v", err)
		}
		gotState = request["state"]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"model": "jev-latest", "answers": map[string]interface{}{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer server.Close()

	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
	ctx := tools.WithTurnQuery(context.Background(), "this turn's file+diff content", false)

	_, err := tool.Execute(ctx, map[string]interface{}{
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err != nil {
		t.Fatalf("Execute with omitted state (turn query in ctx) = %v, want success", err)
	}
	if gotState != "this turn's file+diff content" {
		t.Fatalf("request state = %#v, want the turn query", gotState)
	}
}

// TestToolExecuteRefusesTurnQueryFallbackWithAttachments is a regression
// test for a real finding: when the turn had attachments (e.g. --attach
// images), the ctx-carried query is text-only, so silently falling back to
// it would evaluate only the text and miss the attachment content — the
// exact failure mode a Jev-based security/risk gate exists to prevent.
// Execute must refuse this fallback with a clear error, not guess.
func TestToolExecuteRefusesTurnQueryFallbackWithAttachments(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
	ctx := tools.WithTurnQuery(context.Background(), "review the attached diff", true)

	_, err := tool.Execute(ctx, map[string]interface{}{
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err == nil {
		t.Fatal("expected an error when the turn has attachments and state is omitted, got none")
	}
	if called {
		t.Fatal("Jev API was called despite the missing-state guardrail — attachment content would have been silently dropped")
	}
}

// TestToolExecuteExplicitStateOverridesTurnQuery confirms a caller-supplied
// state still wins over the ctx-carried turn query — the fallback only
// applies when state is genuinely omitted, not as a silent override.
func TestToolExecuteExplicitStateOverridesTurnQuery(t *testing.T) {
	var gotState interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]interface{}
		json.Unmarshal(body, &request)
		gotState = request["state"]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"model": "jev-latest", "answers": map[string]interface{}{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer server.Close()

	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})
	ctx := tools.WithTurnQuery(context.Background(), "turn query — should NOT be used", false)

	_, err := tool.Execute(ctx, map[string]interface{}{
		"state":     "explicit state",
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotState != "explicit state" {
		t.Fatalf("request state = %#v, want explicit state to win over turn query", gotState)
	}
}

// TestToolExecuteConcurrentRunsDoNotRaceOnTurnQuery is a regression test
// for a race-condition review finding: a single shared Tool instance (as
// happens when it's referenced by name in `tools:` and registered once in
// the global registry) must not let one run's turn query leak into
// another concurrent run's omitted-state call. Runs N goroutines against
// the SAME *Tool, each with its own ctx carrying a distinct turn query,
// and asserts each one's request used its own query, never another's.
func TestToolExecuteConcurrentRunsDoNotRaceOnTurnQuery(t *testing.T) {
	var mu sync.Mutex
	seenStates := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]interface{}
		json.Unmarshal(body, &request)
		mu.Lock()
		seenStates[fmt.Sprintf("%v", request["state"])] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"model": "jev-latest", "answers": map[string]interface{}{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer server.Close()

	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL}) // one shared instance, like the global registry

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := tools.WithTurnQuery(context.Background(), fmt.Sprintf("run-%d content", i), false)
			_, err := tool.Execute(ctx, map[string]interface{}{
				"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Execute failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenStates) != n {
		t.Fatalf("saw %d distinct states across %d concurrent runs, want %d — a run's query leaked into another's", len(seenStates), n, n)
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("run-%d content", i)
		if !seenStates[want] {
			t.Errorf("missing expected state %q — got %v", want, seenStates)
		}
	}
}

func TestToolExecuteMissingQuestions(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev"})
	_, err := tool.Execute(context.Background(), map[string]interface{}{"state": "x"})
	if err == nil || !strings.Contains(err.Error(), "questions") {
		t.Fatalf("error = %v, want a questions-related error", err)
	}
}

func TestToolExecuteEmptyQuestions(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev"})
	_, err := tool.Execute(context.Background(), map[string]interface{}{"state": "x", "questions": map[string]interface{}{}})
	if err == nil || !strings.Contains(err.Error(), "questions") {
		t.Fatalf("error = %v, want a questions-related error", err)
	}
}

func TestToolExecuteContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, map[string]interface{}{
		"state":     "x",
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error = %v, want a context-canceled error", err)
	}
}

func TestToolExecuteNonJSONErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>upstream is down</html>"))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	tool := NewTool(&config.ToolDefinition{Name: "jev", URL: server.URL})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"state":     "x",
		"questions": map[string]interface{}{"q": map[string]interface{}{"type": "noul", "instructions": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "upstream is down") {
		t.Fatalf("error = %v, want the raw (non-JSON) body surfaced", err)
	}
}

func TestGetParametersSchemaShape(t *testing.T) {
	tool := NewTool(&config.ToolDefinition{Name: "jev"})
	schema := tool.GetParametersSchema()

	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}
	// state is intentionally NOT required: Execute falls back to the
	// current turn's query (via SetTurnQuery/TurnContextSetter) when it's
	// omitted, so the model isn't forced to retype content it already has.
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "questions" {
		t.Fatalf("schema required = %v, want [questions]", schema["required"])
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema properties missing or wrong type")
	}
	if _, ok := props["state"]; !ok {
		t.Error("schema missing state property")
	}
	if _, ok := props["questions"]; !ok {
		t.Error("schema missing questions property")
	}
}
