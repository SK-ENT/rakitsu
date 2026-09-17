package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// fakeStreamingServer replays a fixed SSE stream shaped like a real OpenAI
// response with stream_options.include_usage=true: content deltas, then a
// final chunk with an EMPTY choices array and only Usage populated.
func fakeStreamingServer(t *testing.T) *httptest.Server {
	t.Helper()
	chunks := []string{
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func TestGenerateStream_CapturesUsageFromFinalEmptyChoicesChunk(t *testing.T) {
	// Regression: the final SSE chunk (empty choices, populated Usage) was
	// skipped by the empty-choices `continue` before usage was ever read,
	// so TokenUsage was always nil for streaming despite requesting it.
	srv := fakeStreamingServer(t)
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL, Model: "gpt-4o-mini"})

	result, err := p.GenerateStream(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "hi")}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range result.Chunks {
		// drain
	}
	final := <-result.Final
	if final == nil {
		t.Fatal("expected a final result, got nil")
	}
	if final.TokenUsage == nil {
		t.Fatal("TokenUsage is nil; usage from the final empty-choices chunk was not captured")
	}
	if final.TokenUsage.TotalTokens != 12 {
		t.Errorf("TotalTokens = %d, want 12", final.TokenUsage.TotalTokens)
	}
}
