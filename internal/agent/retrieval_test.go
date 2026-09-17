package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestBM25_EmptyQuery(t *testing.T) {
	r := &BM25Retriever{errorBias: 2.0}
	r.Index("a", "hello world foo bar", SegmentMeta{Iteration: 0, Type: "step"})
	got := r.Query("", 5)
	if len(got) != 0 {
		t.Errorf("expected no results for empty query, got %d", len(got))
	}
}

func TestBM25_EmptyIndex(t *testing.T) {
	r := &BM25Retriever{errorBias: 2.0}
	got := r.Query("something", 5)
	if len(got) != 0 {
		t.Errorf("expected no results from empty index, got %d", len(got))
	}
}

func TestBM25_IndexAndQuery(t *testing.T) {
	r := &BM25Retriever{errorBias: 1.0} // no error bias
	r.Index("doc1", "write_file succeeded path resolved correctly", SegmentMeta{Iteration: 0, Type: "step"})
	r.Index("doc2", "unrelated content about weather forecast today", SegmentMeta{Iteration: 1, Type: "step"})
	r.Index("doc3", "absolute path not allowed use relative path instead", SegmentMeta{Iteration: 2, Type: "step"})

	results := r.Query("relative path file", 3)
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	// doc3 should rank first — matches both "relative" and "path"
	if results[0].ID != "doc3" {
		t.Errorf("expected doc3 to rank first, got %s (score %.4f)", results[0].ID, results[0].Score)
	}
}

func TestBM25_ErrorBias(t *testing.T) {
	r := &BM25Retriever{errorBias: 3.0}
	// Two docs with same term, one is an error
	r.Index("ok", "write_file call completed", SegmentMeta{Iteration: 0, Type: "step", IsError: false})
	r.Index("err", "write_file call completed", SegmentMeta{Iteration: 1, Type: "step", IsError: true})

	results := r.Query("write_file", 2)
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "err" {
		t.Errorf("expected error doc to rank first due to errorBias, got %s", results[0].ID)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("error doc score (%f) should exceed non-error (%f)", results[0].Score, results[1].Score)
	}
}

func TestBM25_TopKLimit(t *testing.T) {
	r := &BM25Retriever{errorBias: 1.0}
	for i := range 10 {
		r.Index(string(rune('a'+i)), "common term present here", SegmentMeta{Iteration: i, Type: "step"})
	}
	results := r.Query("common term", 3)
	if len(results) != 3 {
		t.Errorf("expected 3 results (topK=3), got %d", len(results))
	}
}

func TestBM25_Tokenize(t *testing.T) {
	tokens := bm25Tokenize("Hello, World! foo-bar baz_qux 123")
	want := []string{"hello", "world", "foo", "bar", "baz", "qux", "123"}
	if len(tokens) != len(want) {
		t.Fatalf("bm25Tokenize got %v, want %v", tokens, want)
	}
	for i, w := range want {
		if tokens[i] != w {
			t.Errorf("token[%d] = %q, want %q", i, tokens[i], w)
		}
	}
}

// failingProvider is an EmbeddingProvider that always returns an error.
type failingProvider struct{}

func (f *failingProvider) GetName() string { return "failing" }
func (f *failingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("provider unreachable")
}

func TestEmbeddingRetriever_FallsBackToBM25(t *testing.T) {
	cfg := config.RetrievalConfig{
		Enabled:   true,
		TopK:      3,
		ErrorBias: 2.0,
	}
	retriever := NewRetriever(cfg, &failingProvider{})

	// Index — provider always fails, falls back to BM25 internally
	retriever.Index("step0", "use relative path for write_file", SegmentMeta{Iteration: 0, Type: "step"})
	retriever.Index("step1", "unrelated weather content", SegmentMeta{Iteration: 1, Type: "step"})

	// Query — provider unreachable, falls back to BM25
	results := retriever.Query("relative path write_file", 3)
	if len(results) == 0 {
		t.Fatal("expected BM25 fallback results, got none")
	}
	if results[0].ID != "step0" {
		t.Errorf("expected step0 as top result, got %s", results[0].ID)
	}
}

func TestNewRetriever_NilProviderReturnsBM25(t *testing.T) {
	cfg := config.RetrievalConfig{ErrorBias: 2.0}
	r := NewRetriever(cfg, nil)
	if r.Backend() != "bm25" {
		t.Errorf("want bm25, got %q", r.Backend())
	}
}
