package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// fakeEmbeddingResponse returns an OpenAI-compatible embeddings response.
func fakeEmbeddingServer(t *testing.T, statusCode int, embedding []float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"error": map[string]interface{}{
					"message": "unauthorized",
					"type":    "invalid_request_error",
					"code":    statusCode,
				},
			})
			return
		}
		resp := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"object":    "embedding",
					"index":     0,
					"embedding": embedding,
				},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]int{"prompt_tokens": 5, "total_tokens": 5},
		}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

// ─── TestEmbeddingClient_Embed ────────────────────────────────────────────────

func TestEmbeddingClient_Embed(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	srv := fakeEmbeddingServer(t, http.StatusOK, want)
	defer srv.Close()

	client := NewEmbeddingClient(&llm.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "text-embedding-3-small",
	})

	got, err := client.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d dims, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dim %d: want %f, got %f", i, want[i], got[i])
		}
	}
}

// ─── TestEmbeddingClient_EmptyResponse ───────────────────────────────────────

func TestEmbeddingClient_EmptyResponse(t *testing.T) {
	// Return an empty data array (no embedding objects at all)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"object": "list",
			"data":   []interface{}{},
			"model":  "text-embedding-3-small",
		})
	}))
	defer srv.Close()

	client := NewEmbeddingClient(&llm.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})

	_, err := client.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embedding data, got nil")
	}
}

// ─── TestEmbeddingClient_APIError ────────────────────────────────────────────

func TestEmbeddingClient_APIError(t *testing.T) {
	srv := fakeEmbeddingServer(t, http.StatusUnauthorized, nil)
	defer srv.Close()

	client := NewEmbeddingClient(&llm.ProviderConfig{
		APIKey:  "bad-key",
		BaseURL: srv.URL,
	})

	_, err := client.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

// ─── TestEmbeddingClient_GetName ─────────────────────────────────────────────

func TestEmbeddingClient_GetName(t *testing.T) {
	c1 := NewEmbeddingClient(&llm.ProviderConfig{APIKey: "k"})
	if c1.GetName() != "openai" {
		t.Errorf("want openai, got %q", c1.GetName())
	}

	c2 := NewEmbeddingClient(&llm.ProviderConfig{APIKey: "k", BaseURL: "http://localhost:4000"})
	if c2.GetName() != "openai-compatible" {
		t.Errorf("want openai-compatible, got %q", c2.GetName())
	}
}

// ─── TestEmbeddingClient_DefaultModel ────────────────────────────────────────

func TestEmbeddingClient_DefaultModel(t *testing.T) {
	c := NewEmbeddingClient(&llm.ProviderConfig{APIKey: "k"})
	if c.model != "text-embedding-3-small" {
		t.Errorf("want text-embedding-3-small, got %q", c.model)
	}
}
