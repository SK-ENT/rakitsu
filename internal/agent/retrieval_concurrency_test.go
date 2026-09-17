package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// TestBM25Retriever_ConcurrentIndexQuery regression-guards against an
// unsynchronized concurrent map read/write between Index (which mutates the
// df map under a write lock) and Query (which used to read it after
// releasing its read lock). Run with -race to be meaningful.
func TestBM25Retriever_ConcurrentIndexQuery(t *testing.T) {
	r := &BM25Retriever{errorBias: 2.0}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.Index(fmt.Sprintf("doc-%d", i), "concurrent write_file access path", SegmentMeta{Iteration: i, Type: "step"})
		}(i)
		go func() {
			defer wg.Done()
			r.Query("concurrent access", 5)
		}()
	}
	wg.Wait()
}

// flakyProvider fails Embed for any text containing failMarker, succeeds
// otherwise (with a fixed-size embedding so cosine similarity is well-defined).
type flakyProvider struct {
	failMarker string
}

func (f *flakyProvider) GetName() string { return "flaky" }
func (f *flakyProvider) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, f.failMarker) {
		return nil, fmt.Errorf("embed failed for %q", text)
	}
	// Trivial deterministic embedding: presence of "write_file" -> [1,0], else [0,1].
	if strings.Contains(text, "write_file") {
		return []float32{1, 0}, nil
	}
	return []float32{0, 1}, nil
}

// TestEmbeddingRetriever_RecoversFailedEmbedViaBM25 regression-guards
// against a document whose Embed() call failed being permanently invisible
// once at least one other document has embedded successfully.
func TestEmbeddingRetriever_RecoversFailedEmbedViaBM25(t *testing.T) {
	cfg := config.RetrievalConfig{Enabled: true, TopK: 5, ErrorBias: 1.0}
	provider := &flakyProvider{failMarker: "FAILME"}
	retriever := NewRetriever(cfg, provider)

	// First doc embeds successfully -> the len(docs)==0 fallback no longer applies.
	retriever.Index("ok-doc", "write_file succeeded", SegmentMeta{Iteration: 0, Type: "step"})
	// Second doc's embed fails, but it's still indexed in BM25 underneath.
	retriever.Index("failed-doc", "FAILME unique_marker_content", SegmentMeta{Iteration: 1, Type: "step"})

	results := retriever.Query("unique_marker_content", 5)

	found := false
	for _, seg := range results {
		if seg.ID == "failed-doc" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected failed-doc to be recovered via BM25 fallback, got results: %+v", results)
	}
}

// TestEmbeddingRetriever_ConcurrentIndexQuery regression-guards against an
// unsynchronized concurrent map read/write on failedIDs between Index
// (which mutates it under a write lock on every failed or later-recovered
// embed) and Query's BM25-fallback-recovery block, which used to copy the
// map reference under a read lock and then read failedIDs[id] after
// releasing it. Run with -race to be meaningful.
func TestEmbeddingRetriever_ConcurrentIndexQuery(t *testing.T) {
	cfg := config.RetrievalConfig{Enabled: true, TopK: 5, ErrorBias: 1.0}
	provider := &flakyProvider{failMarker: "FAIL"}
	retriever := NewRetriever(cfg, provider)

	// One doc must embed successfully first so len(docs) > 0 and Query's
	// fallback-recovery block — the one that reads failedIDs — actually
	// runs on every subsequent Query call below.
	retriever.Index("seed", "write_file succeeded", SegmentMeta{Iteration: 0, Type: "step"})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			retriever.Index(fmt.Sprintf("doc-%d", i), "FAIL concurrent_marker", SegmentMeta{Iteration: i, Type: "step"})
		}(i)
		go func() {
			defer wg.Done()
			retriever.Query("concurrent_marker", 5)
		}()
	}
	wg.Wait()
}

// deadlineCapturingProvider records whether the context passed to Embed had
// a deadline set, to verify EmbeddingRetriever bounds the call instead of
// using context.Background() (which never times out).
type deadlineCapturingProvider struct {
	mu        sync.Mutex
	sawDeadln bool
}

func (d *deadlineCapturingProvider) GetName() string { return "deadline-capturing" }
func (d *deadlineCapturingProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	d.mu.Lock()
	if _, ok := ctx.Deadline(); ok {
		d.sawDeadln = true
	}
	d.mu.Unlock()
	return []float32{1, 0}, nil
}

func TestEmbeddingRetriever_EmbedCallsAreBounded(t *testing.T) {
	provider := &deadlineCapturingProvider{}
	cfg := config.RetrievalConfig{Enabled: true, TopK: 5, ErrorBias: 1.0}
	retriever := NewRetriever(cfg, provider)

	retriever.Index("a", "some content", SegmentMeta{Iteration: 0, Type: "step"})
	retriever.Query("some content", 5)

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.sawDeadln {
		t.Error("Embed() was called with a context that has no deadline — a hung provider would block indefinitely")
	}
}

// TestFormatStepForIndex_TruncatesOnRuneBoundary and
// TestBuildRetrievalBlock_TruncatesOnRuneBoundary regression-guard against
// byte-offset truncation splitting a multi-byte UTF-8 character mid-codepoint.
func TestFormatStepForIndex_TruncatesOnRuneBoundary(t *testing.T) {
	step := &Step{
		Iteration: 0,
		ToolResults: []ToolOutput{
			{ToolName: "read_file", RawOutput: strings.Repeat("あ", 300)}, // 900 bytes, well over the 500-byte cap
		},
	}
	got := FormatStepForIndex(step)
	if !utf8.ValidString(got) {
		t.Errorf("FormatStepForIndex produced invalid UTF-8")
	}
}

func TestBuildRetrievalBlock_TruncatesOnRuneBoundary(t *testing.T) {
	segments := []RetrievedSegment{
		{ID: "a", Text: strings.Repeat("思", 300), Meta: SegmentMeta{Iteration: 0, Type: "step"}}, // 900 bytes, over the 800-byte cap
	}
	got := BuildRetrievalBlock(segments)
	if !utf8.ValidString(got) {
		t.Errorf("BuildRetrievalBlock produced invalid UTF-8")
	}
}
