package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// paramRejectingServer replays a fixed sequence of chat-completion responses:
// the first rejectAttempts requests get the real 400 body LiteLLM/OpenAI send
// when a reasoning-tier model rejects an optional param (e.g. `openai/gpt-5.6-luna`
// rejecting a non-default `temperature`), then a normal completion. Each
// decoded request body is recorded so a test can assert exactly which
// optional params were (not) sent on each attempt.
func paramRejectingServer(t *testing.T, param string, rejectAttempts int) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	requests := make([]map[string]any, 0, rejectAttempts+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		requests = append(requests, body)

		w.Header().Set("Content-Type", "application/json")
		if len(requests) <= rejectAttempts {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"Unsupported parameter: '%s' is not supported with this model.","type":"invalid_request_error","param":"%s","code":null}}`, param, param)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"1","object":"chat.completion","created":1,"model":"openai/gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	return srv, &requests
}

func TestGenerate_OmitsRejectedParamAndRetries(t *testing.T) {
	// Regression for the live failure: `openai/gpt-5.6-luna` (a reasoning-tier
	// model behind LiteLLM's vendor-prefixed alias) rejects a non-default
	// temperature/top_p with a 400 naming the exact param. isReasoningModel's
	// prefix check misses "openai/gpt-5.6-luna" (it doesn't start with
	// "gpt-5"), so rakitsu used to send the param anyway and hard-fail.
	for _, param := range []string{"temperature", "top_p"} {
		t.Run(param, func(t *testing.T) {
			srv, requests := paramRejectingServer(t, param, 1)
			defer srv.Close()

			cfg := &llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna"}
			if param == "temperature" {
				cfg.Temperature = 0.3
			} else {
				cfg.TopP = 0.9
			}
			p := NewProvider(cfg)

			result, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if result == nil {
				t.Fatal("expected a result, got nil")
			}
			if len(*requests) != 2 {
				t.Fatalf("expected 2 requests (initial + 1 retry), got %d", len(*requests))
			}
			if _, present := (*requests)[0][param]; !present {
				t.Errorf("first request should have sent %q", param)
			}
			if _, present := (*requests)[1][param]; present {
				t.Errorf("retried request should have omitted %q", param)
			}
		})
	}
}

func TestGenerate_OmitsRejectedParamFromDoubleEncodedProxyMessage(t *testing.T) {
	// Regression for the real DGX LiteLLM proxy shape (not just the tidy
	// single-level body other tests use): the proxy's own top-level `param`
	// is null, and the useful `"param": "temperature"` is buried as literal
	// text inside its `message` string, which itself double-encodes the
	// upstream provider's full JSON error body plus trailing fallback prose.
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if _, present := body["temperature"]; !present {
				t.Error("first request should have sent temperature")
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"litellm.BadRequestError: OpenAIException - {\n  \"error\": {\n    \"message\": \"Unsupported parameter: 'temperature' is not supported with this model.\",\n    \"type\": \"invalid_request_error\",\n    \"param\": \"temperature\",\n    \"code\": null\n  }\n}No fallback model group found for original model_group=openai/gpt-5.6-luna.","type":"invalid_request_error","param":null,"code":null}}`)
			return
		}
		if _, present := body["temperature"]; present {
			t.Error("retried request should have omitted temperature")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"1","object":"chat.completion","created":1,"model":"openai/gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna", Temperature: 0.3})

	result, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result, got nil")
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests (initial + 1 retry), got %d", requests)
	}
}

func TestGenerate_UnrelatedBadRequestIsNotRetried(t *testing.T) {
	// A 400 that doesn't name temperature/top_p (e.g. an unknown model, a bad
	// key) is a different, real problem — it must still fail immediately,
	// not get silently retried.
	srv, requests := paramRejectingServer(t, "model", 5) // always rejects
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna", Temperature: 0.3})

	_, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if len(*requests) != 1 {
		t.Fatalf("expected exactly 1 request (no retry for an unrelated param), got %d", len(*requests))
	}
}

// TestGenerate_ParamRejectionWarningUsesWarnFn is a regression for the chat
// TUI screen-corruption bug: omitRejectedParam used to write its warning
// straight to os.Stderr, which races bubbletea's own concurrent renderer
// and corrupts the alt-screen (see internal/chat's interactive command).
// The warning must go through the package's swappable warnFn sink instead,
// so an alt-screen caller can redirect it away from the terminal.
func TestGenerate_ParamRejectionWarningUsesWarnFn(t *testing.T) {
	srv, _ := paramRejectingServer(t, "temperature", 1)
	defer srv.Close()

	var got []string
	orig := warnFn
	warnFn = func(s string) { got = append(got, s) }
	t.Cleanup(func() { warnFn = orig })

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna", Temperature: 0.3})
	if _, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 warning through warnFn, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], `model "openai/gpt-5.6-luna" rejected temperature`) {
		t.Errorf("warning text = %q, want it to name the model and rejected param", got[0])
	}
}

// TestGenerate_RetriesWithReasoningEffortNone is a regression,
// reproduced live: `--model gpt-5.6-luna` with no `reasoning_effort`
// configured hard-failed on the first tool-using step with a 400 telling
// the user to set `reasoning_effort: none` — this must now self-heal via
// applyReasoningEffortFallback instead of requiring a manual config edit.
func TestGenerate_RetriesWithReasoningEffortNone(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if _, present := body["reasoning_effort"]; present {
				t.Error("first request should not have sent reasoning_effort")
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-5.6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":null,"code":null}}`)
			return
		}
		if got, _ := body["reasoning_effort"].(string); got != "none" {
			t.Errorf("retried request reasoning_effort = %q, want %q", got, "none")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"1","object":"chat.completion","created":1,"model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "gpt-5.6-luna"})

	result, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, []llm.ToolDefinition{{Name: "noop"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result, got nil")
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests (initial + 1 retry), got %d", requests)
	}
}

func TestGenerateStream_OmitsRejectedParamAndRetries(t *testing.T) {
	// GenerateStream carries its own independent copy of the same gating
	// logic — verify the retry applies there too.
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if requests == 1 {
			if _, present := body["temperature"]; !present {
				t.Error("first request should have sent temperature")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unsupported parameter: 'temperature' is not supported with this model.","type":"invalid_request_error","param":"temperature","code":null}}`)
			return
		}
		if _, present := body["temperature"]; present {
			t.Error("retried request should have omitted temperature")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: "+`{"id":"1","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5.6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "openai/gpt-5.6-luna", Temperature: 0.3})

	result, err := p.GenerateStream(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range result.Chunks {
		// drain
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests (initial + 1 retry), got %d", requests)
	}
}
