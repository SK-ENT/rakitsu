package agent

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// embeddingCallTimeout bounds a single Embed() call. ContextRetriever has no
// ctx parameter (Index/Query are called deep inside the ReAct loop with no
// caller cancellation threaded through), so without this a slow or hung
// embedding provider blocks the calling goroutine indefinitely — this
// caps the worst case instead of relying on caller cancellation that
// structurally can't reach here today.
const embeddingCallTimeout = 30 * time.Second

// ContextRetriever indexes step content and retrieves relevant segments.
type ContextRetriever interface {
	Index(id string, text string, meta SegmentMeta)
	Query(query string, topK int) []RetrievedSegment
	Backend() string // "bm25" or "ollama"
}

// SegmentMeta carries metadata about an indexed segment.
type SegmentMeta struct {
	Iteration int
	Type      string // "step", "thinking"
	IsError   bool   // eligible for errorBias weight boost
}

// RetrievedSegment is a ranked result from a retrieval query.
type RetrievedSegment struct {
	ID    string
	Text  string
	Meta  SegmentMeta
	Score float64
}

// NewRetriever returns a ContextRetriever. Pass a non-nil EmbeddingProvider for
// semantic retrieval; nil falls back to pure BM25.
func NewRetriever(cfg config.RetrievalConfig, ep llm.EmbeddingProvider) ContextRetriever {
	errorBias := cfg.ErrorBias
	if errorBias <= 0 {
		errorBias = 2.0
	}
	bm25 := &BM25Retriever{errorBias: errorBias}
	if ep != nil {
		return &EmbeddingRetriever{bm25: bm25, provider: ep, errorBias: errorBias}
	}
	return bm25
}

// ---------------------------------------------------------------------------
// BM25Retriever — pure Go BM25 (zero external dependencies).
// Parameters: k1=1.5, b=0.75 (standard Robertson et al. values).
// ---------------------------------------------------------------------------

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

type bm25Doc struct {
	id        string
	text      string
	meta      SegmentMeta
	tokens    []string
	termFreq  map[string]int
	length    int
}

// BM25Retriever implements ContextRetriever using BM25 keyword scoring.
type BM25Retriever struct {
	mu        sync.RWMutex
	docs      []*bm25Doc
	df        map[string]int // document frequency per term
	errorBias float64
}

func (r *BM25Retriever) Backend() string { return "bm25" }

func (r *BM25Retriever) Index(id string, text string, meta SegmentMeta) {
	tokens := bm25Tokenize(text)
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	doc := &bm25Doc{id: id, text: text, meta: meta, tokens: tokens, termFreq: tf, length: len(tokens)}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.docs = append(r.docs, doc)
	if r.df == nil {
		r.df = make(map[string]int)
	}
	for term := range tf {
		r.df[term]++
	}
}

func (r *BM25Retriever) Query(query string, topK int) []RetrievedSegment {
	queryTerms := bm25Tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}

	// Held for the whole scoring loop below, not just this snapshot: df is
	// the same map object as r.df (maps are reference types), and Index
	// mutates it under r.mu.Lock(). Releasing the lock here and reading
	// df[term] later, after unlocking, would be an unsynchronized
	// concurrent map read/write against a live Index() call — a runtime
	// throw, not a recoverable panic.
	r.mu.RLock()
	defer r.mu.RUnlock()
	docs := r.docs
	df := r.df
	n := len(docs)

	if n == 0 {
		return nil
	}

	// Average document length
	totalLen := 0
	for _, d := range docs {
		totalLen += d.length
	}
	avgDL := float64(totalLen) / float64(n)

	const k1 = 1.5
	const b = 0.75

	type scored struct {
		seg   RetrievedSegment
		score float64
	}
	results := make([]scored, 0, n)

	for _, doc := range docs {
		score := 0.0
		for _, term := range queryTerms {
			tf := float64(doc.termFreq[term])
			if tf == 0 {
				continue
			}
			dfTerm := float64(df[term])
			idf := math.Log(1 + (float64(n)-dfTerm+0.5)/(dfTerm+0.5))
			dl := float64(doc.length)
			tfNorm := tf * (k1 + 1) / (tf + k1*(1-b+b*dl/avgDL))
			score += idf * tfNorm
		}
		if score == 0 {
			continue
		}
		if doc.meta.IsError && r.errorBias > 1 {
			score *= r.errorBias
		}
		results = append(results, scored{
			seg:   RetrievedSegment{ID: doc.id, Text: doc.text, Meta: doc.meta, Score: score},
			score: score,
		})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	out := make([]RetrievedSegment, len(results))
	for i, r := range results {
		out[i] = r.seg
	}
	return out
}

func bm25Tokenize(text string) []string {
	lower := strings.ToLower(text)
	parts := nonAlphanumRe.Split(lower, -1)
	out := parts[:0]
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// EmbeddingRetriever — cosine similarity over any llm.EmbeddingProvider.
// Falls back to BM25 silently when the provider is unreachable.
// ---------------------------------------------------------------------------

type embeddedDoc struct {
	id        string
	text      string
	meta      SegmentMeta
	embedding []float32
}

// EmbeddingRetriever uses any llm.EmbeddingProvider for semantic search,
// falling back to BM25 when the provider returns an error.
type EmbeddingRetriever struct {
	bm25      *BM25Retriever
	provider  llm.EmbeddingProvider
	errorBias float64

	mu        sync.RWMutex
	docs      []embeddedDoc
	failedIDs map[string]struct{} // ids whose Embed() call failed; still in bm25, just not in docs
}

func (r *EmbeddingRetriever) Backend() string { return r.provider.GetName() }

func (r *EmbeddingRetriever) Index(id string, text string, meta SegmentMeta) {
	// Always index in BM25 for fallback.
	r.bm25.Index(id, text, meta)

	ctx, cancel := context.WithTimeout(context.Background(), embeddingCallTimeout)
	defer cancel()
	emb, err := r.provider.Embed(ctx, text)
	if err != nil {
		// Provider unreachable — BM25 fallback is already indexed. Remember
		// the id so Query can still surface it via BM25 even after other
		// documents have embedded successfully (see failedIDs on the struct).
		r.mu.Lock()
		if r.failedIDs == nil {
			r.failedIDs = make(map[string]struct{})
		}
		r.failedIDs[id] = struct{}{}
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.docs = append(r.docs, embeddedDoc{id: id, text: text, meta: meta, embedding: emb})
	delete(r.failedIDs, id)
	r.mu.Unlock()
}

func (r *EmbeddingRetriever) Query(query string, topK int) []RetrievedSegment {
	ctx, cancel := context.WithTimeout(context.Background(), embeddingCallTimeout)
	defer cancel()
	queryEmb, err := r.provider.Embed(ctx, query)
	if err != nil {
		// Provider unreachable — fall back to BM25.
		return r.bm25.Query(query, topK)
	}

	r.mu.RLock()
	docs := r.docs
	hasFailed := len(r.failedIDs) > 0
	// Snapshot into a fresh map rather than copying the map reference:
	// r.failedIDs is a live map that Index() mutates under r.mu.Lock(), so
	// reading failedIDs[id] below after releasing RLock would be an
	// unsynchronized read against that same map — the identical hazard
	// BM25Retriever.Query holds its lock for its full duration to avoid.
	var failedIDs map[string]struct{}
	if hasFailed {
		failedIDs = make(map[string]struct{}, len(r.failedIDs))
		for id := range r.failedIDs {
			failedIDs[id] = struct{}{}
		}
	}
	r.mu.RUnlock()

	if len(docs) == 0 {
		return r.bm25.Query(query, topK)
	}

	type scored struct {
		seg   RetrievedSegment
		score float64
	}
	results := make([]scored, 0, len(docs))
	for _, doc := range docs {
		sim := cosineSimilarity(queryEmb, doc.embedding)
		if doc.meta.IsError && r.errorBias > 1 {
			sim *= r.errorBias
		}
		results = append(results, scored{
			seg:   RetrievedSegment{ID: doc.id, Text: doc.text, Meta: doc.meta, Score: sim},
			score: sim,
		})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	out := make([]RetrievedSegment, len(results))
	for i, r := range results {
		out[i] = r.seg
	}

	// Documents whose Embed() call failed never made it into docs above, so
	// they're otherwise permanently invisible once at least one document has
	// embedded successfully (the len(docs)==0 fallback above only fires on
	// the very first query). Recover them via BM25 and append after the
	// embedding-ranked results — the two scores aren't on comparable scales,
	// so this is a supplementary tier, not a re-ranked merge.
	if hasFailed && (topK <= 0 || len(out) < topK) {
		bm25Results := r.bm25.Query(query, 0) // unbounded; filter below
		for _, seg := range bm25Results {
			if _, failed := failedIDs[seg.ID]; !failed {
				continue
			}
			out = append(out, seg)
			if topK > 0 && len(out) >= topK {
				break
			}
		}
	}

	return out
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ---------------------------------------------------------------------------
// Helpers used by agent.go to build index text from a StepLog Step.
// ---------------------------------------------------------------------------

// FormatStepForIndex builds a concise text representation of a step for indexing.
func FormatStepForIndex(step *Step) string {
	var sb strings.Builder
	if step.Thought != "" {
		sb.WriteString("Thought: ")
		sb.WriteString(step.Thought)
		sb.WriteString("\n")
	}
	for _, tc := range step.ToolCalls {
		sb.WriteString(fmt.Sprintf("Tool: %s\n", tc.Name))
	}
	for _, tr := range step.ToolResults {
		out := tr.RawOutput
		if len(out) > 500 {
			out = truncateUTF8(out, 500) + "…"
		}
		if tr.Error != "" {
			sb.WriteString(fmt.Sprintf("Error: %s\n", tr.Error))
		} else {
			sb.WriteString(fmt.Sprintf("Output: %s\n", out))
		}
	}
	if step.Reflection != "" {
		sb.WriteString("Reflection: ")
		sb.WriteString(step.Reflection)
		sb.WriteString("\n")
	}
	return sb.String()
}

// StepHasErrors returns true if any tool result in the step has an error.
func StepHasErrors(step *Step) bool {
	for _, tr := range step.ToolResults {
		if tr.Error != "" {
			return true
		}
	}
	return false
}

// BuildRetrievalBlock converts retrieved segments into a user message block.
func BuildRetrievalBlock(segments []RetrievedSegment) string {
	var sb strings.Builder
	sb.WriteString("## Retrieved Context\n")
	for _, seg := range segments {
		label := fmt.Sprintf("[Step %d (%s)]", seg.Meta.Iteration+1, seg.Meta.Type)
		sb.WriteString(label)
		sb.WriteString(": ")
		text := seg.Text
		if len(text) > 800 {
			text = truncateUTF8(text, 800) + "…"
		}
		sb.WriteString(text)
		sb.WriteString("\n")
	}

	return sb.String()
}
