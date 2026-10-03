package format

import (
	"strings"
	"testing"
)

// ---------- ThinkTagInline direct tests ----------

func TestThinkTagInline_NoTagsPassesThrough(t *testing.T) {
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyDelta(state, RawDelta{Content: "just a normal response"})
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})

	r := a.Finalize(state)
	if r.Content != "just a normal response" {
		t.Errorf("content = %q, want unchanged", r.Content)
	}
	if r.Reasoning != "" {
		t.Errorf("reasoning should be empty, got %q", r.Reasoning)
	}
}

func TestThinkTagInline_SingleThinkBlock(t *testing.T) {
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyDelta(state, RawDelta{Content: "<think>I need to think about this</think>\nHere is my answer."})
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})

	r := a.Finalize(state)
	if r.Content != "Here is my answer." {
		t.Errorf("content = %q, want %q", r.Content, "Here is my answer.")
	}
	if r.Reasoning != "I need to think about this" {
		t.Errorf("reasoning = %q", r.Reasoning)
	}
}

func TestThinkTagInline_MultipleBlocks(t *testing.T) {
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      "<think>first thought</think>\nSome text\n<think>second thought</think>\nFinal answer.",
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if r.Reasoning != "first thought\n\nsecond thought" {
		t.Errorf("reasoning = %q", r.Reasoning)
	}
	if r.Content != "Some text\n\nFinal answer." {
		t.Errorf("content = %q", r.Content)
	}
}

func TestThinkTagInline_MultilineBlock(t *testing.T) {
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      "<think>\nline one\nline two\n</think>\nAnswer.",
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if r.Reasoning == "" {
		t.Error("reasoning should not be empty for multiline block")
	}
	if r.Content != "Answer." {
		t.Errorf("content = %q", r.Content)
	}
}

func TestThinkTagInline_OnlyThinkBlock(t *testing.T) {
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      "<think>internal reasoning only</think>",
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if r.Reasoning != "internal reasoning only" {
		t.Errorf("reasoning = %q", r.Reasoning)
	}
	if r.Content != "" {
		t.Errorf("content should be empty, got %q", r.Content)
	}
}

// ---------- ThinkTagInline streaming-parser tests (inline think tags) ----------
//
// These exercise the per-delta <think>-stripping path: reasoning must be
// extracted live (visible to state.OnReasoningDelta) and the inner adapter
// must receive content WITHOUT the tags. Boundary handling across deltas
// is the core risk — a tag split between chunks must not leak.

// streamDeltas applies a slice of content strings to the given adapter as
// separate ApplyDelta calls, collecting everything emitted via
// state.OnReasoningDelta. Returns the captured live-reasoning chunks plus
// the finalized result.
func streamDeltas(t *testing.T, a ResponseFormat, deltas []string) ([]string, Result) {
	t.Helper()
	state := NewState()
	var captured []string
	state.OnReasoningDelta = func(s string) { captured = append(captured, s) }
	for _, d := range deltas {
		a.ApplyDelta(state, RawDelta{Content: d})
	}
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})
	return captured, a.Finalize(state)
}

func TestThinkTagInline_Streaming_SingleDelta(t *testing.T) {
	captured, r := streamDeltas(t, ThinkTagInline{}, []string{
		"<think>weighing options</think>The answer is 42.",
	})
	if r.Content != "The answer is 42." {
		t.Errorf("content = %q, want %q", r.Content, "The answer is 42.")
	}
	if r.Reasoning != "weighing options" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "weighing options")
	}
	if len(captured) != 1 || captured[0] != "weighing options" {
		t.Errorf("OnReasoningDelta captured = %v, want [\"weighing options\"]", captured)
	}
}

func TestThinkTagInline_Streaming_NestedThinkTagsDoNotLeakStrayCloseTag(t *testing.T) {
	// Regression: the streaming parser tracked only a bool insideThink flag.
	// A second <think> before the first closed (a known glitch mode on
	// small/quantized reasoning models) made the FIRST </think> found close
	// the block, leaving the second, now-unmatched </think> to fall through
	// as regular content instead of being stripped.
	captured, r := streamDeltas(t, ThinkTagInline{}, []string{
		"<think>outer <think>inner</think> more outer text</think>Done.",
	})
	if r.Content != "Done." {
		t.Errorf("content = %q, want %q", r.Content, "Done.")
	}
	if strings.Contains(r.Content, "<think>") || strings.Contains(r.Content, "</think>") {
		t.Errorf("content leaked a think tag: %q", r.Content)
	}
	if r.Reasoning != "outer inner more outer text" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "outer inner more outer text")
	}
	if joinChunks(captured) != "outer inner more outer text" {
		t.Errorf("captured reasoning = %v, want one concatenated chunk %q", captured, "outer inner more outer text")
	}
}

func TestThinkTagInline_Streaming_OpenTagSplitAcrossDeltas(t *testing.T) {
	// "<th" + "ink>foo</think>bar" — partial open tag at end of first delta.
	captured, r := streamDeltas(t, ThinkTagInline{}, []string{
		"hello <th",
		"ink>foo</think>bar",
	})
	if r.Content != "hello bar" {
		t.Errorf("content = %q, want %q", r.Content, "hello bar")
	}
	if r.Reasoning != "foo" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "foo")
	}
	if joinChunks(captured) != "foo" {
		t.Errorf("captured reasoning = %v, want one chunk \"foo\"", captured)
	}
}

func TestThinkTagInline_Streaming_CloseTagSplitAcrossDeltas(t *testing.T) {
	// "<think>foo</th" + "ink>bar" — partial close tag at end of second delta.
	captured, r := streamDeltas(t, ThinkTagInline{}, []string{
		"<think>foo</th",
		"ink>bar",
	})
	if r.Content != "bar" {
		t.Errorf("content = %q, want %q", r.Content, "bar")
	}
	if r.Reasoning != "foo" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "foo")
	}
	if joinChunks(captured) != "foo" {
		t.Errorf("captured reasoning = %v, want \"foo\"", captured)
	}
}

func TestThinkTagInline_Streaming_TagSpansThreeDeltas(t *testing.T) {
	// Pathological split right inside the tag characters.
	_, r := streamDeltas(t, ThinkTagInline{}, []string{
		"<",
		"think>secret",
		"</think>done",
	})
	if r.Content != "done" {
		t.Errorf("content = %q, want %q", r.Content, "done")
	}
	if r.Reasoning != "secret" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "secret")
	}
}

func TestThinkTagInline_Streaming_TokenAtATime(t *testing.T) {
	// Simulate a real SSE stream where each character is its own delta.
	src := "pre <think>think a</think> mid <think>think b</think> post"
	deltas := make([]string, 0, len(src))
	for _, r := range src {
		deltas = append(deltas, string(r))
	}
	captured, r := streamDeltas(t, ThinkTagInline{}, deltas)
	wantContent := "pre  mid  post"
	if r.Content != wantContent {
		t.Errorf("content = %q, want %q", r.Content, wantContent)
	}
	if r.Reasoning != "think a\n\nthink b" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "think a\n\nthink b")
	}
	// Combined incremental capture must equal the union of both block bodies.
	if joinChunks(captured) != "think athink b" {
		t.Errorf("captured = %q, want %q", joinChunks(captured), "think athink b")
	}
}

func TestThinkTagInline_Streaming_NilCallbackSafe(t *testing.T) {
	// state.OnReasoningDelta unset — adapter must not panic, and the
	// aggregate Result must still match the existing single-delta semantics.
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyDelta(state, RawDelta{Content: "<think>silent</think>visible"})
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})
	r := a.Finalize(state)
	if r.Content != "visible" {
		t.Errorf("content = %q, want %q", r.Content, "visible")
	}
	if r.Reasoning != "silent" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "silent")
	}
}

func TestThinkTagInline_Streaming_UnclosedThinkFlushesOnFinalize(t *testing.T) {
	// Model opened <think> but the stream ended before closing — Finalize
	// should still preserve the accumulated reasoning bytes.
	captured, r := streamDeltas(t, ThinkTagInline{}, []string{
		"intro <think>still thinking",
	})
	if r.Content != "intro" {
		t.Errorf("content = %q, want %q", r.Content, "intro")
	}
	if r.Reasoning != "still thinking" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "still thinking")
	}
	if joinChunks(captured) != "still thinking" {
		t.Errorf("captured = %v", captured)
	}
}

func TestThinkTagInline_Streaming_ComposedWithReasoningContentField(t *testing.T) {
	// nemotron-elastic-30b shape: ThinkTagInline wraps ReasoningContentField.
	// Bytes arrive on Content with inline <think>; ReasoningContent stays empty.
	// The streaming parser must split inline-think from content BEFORE delegating
	// to ReasoningContentField so the inner adapter sees clean content only.
	a := ThinkTagInline{Inner: ReasoningContentField{}}
	state := NewState()
	var captured []string
	state.OnReasoningDelta = func(s string) { captured = append(captured, s) }
	a.ApplyDelta(state, RawDelta{Content: "<think>weighing</think>The answer is X."})
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})
	r := a.Finalize(state)
	if r.Content != "The answer is X." {
		t.Errorf("content = %q, want %q", r.Content, "The answer is X.")
	}
	if r.Reasoning != "weighing" {
		t.Errorf("reasoning = %q, want %q", r.Reasoning, "weighing")
	}
	if joinChunks(captured) != "weighing" {
		t.Errorf("captured = %v", captured)
	}
}

func TestThinkTagInline_Streaming_AlsoSurfacesReasoningContentField(t *testing.T) {
	// Hybrid model that emits BOTH delta.reasoning_content AND inline <think>.
	// Both reasoning channels must arrive — delta-field via inner adapter's
	// accumulation, inline-<think> via our streaming parser.
	a := ThinkTagInline{Inner: ReasoningContentField{}}
	state := NewState()
	var captured []string
	state.OnReasoningDelta = func(s string) { captured = append(captured, s) }
	// Delta 1: only delta-field reasoning (no inline tags yet).
	a.ApplyDelta(state, RawDelta{ReasoningContent: "field reasoning "})
	// Delta 2: inline-think arrives on the content channel.
	a.ApplyDelta(state, RawDelta{Content: "<think>inline reasoning</think>final."})
	a.ApplyDelta(state, RawDelta{FinishReason: "stop"})
	r := a.Finalize(state)
	if r.Content != "final." {
		t.Errorf("content = %q, want %q", r.Content, "final.")
	}
	// Both reasoning sources should be present in aggregate state.Reasoning.
	// The delta-field reasoning is accumulated by ReasoningContentField directly;
	// the inline-think is committed by our streaming parser with "\n\n" separator
	// (only between inline blocks — the delta-field portion comes first verbatim).
	if !contains(r.Reasoning, "field reasoning ") || !contains(r.Reasoning, "inline reasoning") {
		t.Errorf("reasoning = %q, missing one of the two sources", r.Reasoning)
	}
	// OnReasoningDelta only fires for inline-think (delta-field flows via the
	// provider's separate peer-emit path, not through state.OnReasoningDelta).
	if joinChunks(captured) != "inline reasoning" {
		t.Errorf("captured inline reasoning = %v, want [\"inline reasoning\"]", captured)
	}
}

func TestThinkTagInline_Streaming_ToolCallsPassThrough(t *testing.T) {
	// Tool calls inside the same delta as a <think> block must reach the inner
	// adapter (they're orthogonal to inline reasoning extraction).
	a := ThinkTagInline{}
	state := NewState()
	a.ApplyDelta(state, RawDelta{
		Content: "<think>plan</think>",
		ToolCalls: []RawDeltaToolCall{
			{Index: 0, ID: "call_1", Name: "search", Arguments: `{"q":"x"}`},
		},
	})
	a.ApplyDelta(state, RawDelta{FinishReason: "tool_calls"})
	r := a.Finalize(state)
	if r.Reasoning != "plan" {
		t.Errorf("reasoning = %q", r.Reasoning)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "search" {
		t.Errorf("tool call lost: %+v", r.ToolCalls)
	}
}

// joinChunks concatenates a slice of strings — used to verify the cumulative
// content of OnReasoningDelta callbacks without asserting exact chunk boundaries.
func joinChunks(chunks []string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c)
	}
	return b.String()
}

// contains is a tiny convenience wrapper to keep test predicates readable.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// ---------- ToolCallTagInline direct tests ----------

func TestToolCallTagInline_ExtractsInlineToolCall(t *testing.T) {
	a := ToolCallTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      `I'll search for that. <tool_call>{"name":"search","arguments":{"q":"golang"}}</tool_call>`,
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if len(r.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(r.ToolCalls))
	}
	tc := r.ToolCalls[0]
	if tc.Name != "search" {
		t.Errorf("tool call name = %q", tc.Name)
	}
	if tc.Arguments["q"] != "golang" {
		t.Errorf("tool call args = %v", tc.Arguments)
	}
	if r.Content != "I'll search for that." {
		t.Errorf("content after extraction = %q", r.Content)
	}
	if r.FinishReason != "tool_calls" {
		t.Errorf("finish reason should be upgraded to tool_calls, got %q", r.FinishReason)
	}
}

func TestToolCallTagInline_DoesNotOverrideStructuredToolCalls(t *testing.T) {
	// If the inner adapter already produced structured tool calls (via the
	// standard OpenAI path), ToolCallTagInline should NOT second-guess them
	// even if there happen to be <tool_call> tags in the content.
	a := ToolCallTagInline{}
	state := NewState()
	a.ApplyDelta(state, RawDelta{
		Content: "I'll call search.",
		ToolCalls: []RawDeltaToolCall{
			{Index: 0, ID: "call_1", Name: "real_search", Arguments: `{"q":"real"}`},
		},
	})
	r := a.Finalize(state)
	if len(r.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call from structured path, got %d", len(r.ToolCalls))
	}
	if r.ToolCalls[0].Name != "real_search" {
		t.Errorf("should preserve structured tool call, got %q", r.ToolCalls[0].Name)
	}
	// Content should NOT have been cleaned (no inline tags to clean)
	if r.Content != "I'll call search." {
		t.Errorf("content should be unchanged, got %q", r.Content)
	}
}

func TestToolCallTagInline_InvalidJSONSkipped(t *testing.T) {
	a := ToolCallTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      `<tool_call>{broken json}</tool_call> some text`,
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if len(r.ToolCalls) != 0 {
		t.Errorf("broken JSON should produce no tool calls, got %d", len(r.ToolCalls))
	}
}

func TestToolCallTagInline_NoNameSkipped(t *testing.T) {
	a := ToolCallTagInline{}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content:      `<tool_call>{"arguments":{"x":1}}</tool_call>`,
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if len(r.ToolCalls) != 0 {
		t.Errorf("missing name should produce no tool calls, got %d", len(r.ToolCalls))
	}
}

// ---------- Composition tests (Phase 2 core) ----------

func TestComposition_ThinkPlusToolCall(t *testing.T) {
	// Simulates Qwen3 coder: <think> blocks AND <tool_call> blocks in content.
	// Order matters: ToolCallTagInline wraps ThinkTagInline (think strips first,
	// then tool_call parses what's left).
	a := ToolCallTagInline{Inner: ThinkTagInline{}}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content: `<think>Let me decide which tool to call.</think>
I'll use search.
<tool_call>{"name":"search","arguments":{"q":"rust"}}</tool_call>`,
		FinishReason: "stop",
	})
	r := a.Finalize(state)
	if r.Reasoning != "Let me decide which tool to call." {
		t.Errorf("reasoning should be think block content, got %q", r.Reasoning)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "search" {
		t.Errorf("tool call not extracted: %+v", r.ToolCalls)
	}
	if !strings.Contains(r.Content, "I'll use search.") {
		t.Errorf("content should retain the natural text, got %q", r.Content)
	}
	if strings.Contains(r.Content, "<think>") || strings.Contains(r.Content, "<tool_call>") {
		t.Errorf("content should not contain stripped tags: %q", r.Content)
	}
	if r.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", r.FinishReason)
	}
}

func TestComposition_ReasoningPlusThinkFallback(t *testing.T) {
	// Simulates a reasoning model (nemotron-style) that ALSO happens to emit
	// a <think> tag in its final content. The wrapping chain should extract
	// reasoning_content first, then strip any stray <think> from the promoted content.
	a := ThinkTagInline{Inner: ReasoningContentField{}}
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content: "", // empty — reasoning gets promoted
		// The <think> block sits AFTER the "Final answer:" marker, so it's
		// part of the extracted candidate and the strip path actually runs —
		// putting it before the marker (as an earlier version of this test
		// did) meant the assertion below passed vacuously, since the
		// extracted candidate never contained a <think> tag to begin with.
		ReasoningContent: "Analyzing... Final answer: <think>wait, reconsider</think> The result is 42 with full certainty.",
		FinishReason:     "length",
	})
	r := a.Finalize(state)
	// ReasoningContentField should extract the final-answer marker first,
	// then ThinkTagInline should strip <think> from the extracted content.
	if strings.Contains(r.Content, "<think>") {
		t.Errorf("content should not contain <think> tags after strip, got %q", r.Content)
	}
	if !strings.Contains(r.Content, "42 with full certainty") {
		t.Errorf("extracted answer should be present, got %q", r.Content)
	}
}

func TestComposition_FullNemotronChainRegistryWired(t *testing.T) {
	// End-to-end: Detect returns the composed chain for nemotron, and it
	// handles a realistic response (reasoning-only with trailing markdown).
	a := Detect("vllm-nemotron-elastic-30b", "")
	state := NewState()
	a.ApplyFull(state, RawMessage{
		Content: "",
		ReasoningContent: `Let me think about this.
I need to write a short report.

## Summary
- Point one
- Point two
- Point three`,
		FinishReason: "length",
	})
	r := a.Finalize(state)
	if !strings.Contains(r.Content, "## Summary") {
		t.Errorf("expected extracted markdown report, got %q", r.Content)
	}
	if strings.Contains(r.Content, "Let me think about this") {
		t.Errorf("pre-report thinking should be excluded, got %q", r.Content)
	}
}

// ---------- Qwen3 registry entry tests ----------

func TestDetect_Qwen3ThinkingRoutesToInlineFallbacks(t *testing.T) {
	for _, m := range []string{"qwen3-7b-thinking", "qwen3-14b-coder", "qwen3-tool-use-32b"} {
		a := Detect(m, "")
		chain := ChainNames(a)
		// All qwen3 variants should get standardWithInlineFallbacks:
		// standard_openai → think_tag_inline → tool_call_tag_inline.
		if len(chain) != 3 {
			t.Errorf("%s chain length = %d, want 3 (got %v)", m, len(chain), chain)
			continue
		}
		if chain[0] != "standard_openai" {
			t.Errorf("%s chain[0] = %s, want standard_openai", m, chain[0])
		}
		if chain[1] != "think_tag_inline" {
			t.Errorf("%s chain[1] = %s, want think_tag_inline", m, chain[1])
		}
		if chain[2] != "tool_call_tag_inline" {
			t.Errorf("%s chain[2] = %s, want tool_call_tag_inline", m, chain[2])
		}
	}
}

// ---------- ChainName formatting ----------

func TestChainName_SingleAdapter(t *testing.T) {
	if ChainName(StandardOpenAI{}) != "standard_openai" {
		t.Errorf("single-adapter ChainName wrong: %s", ChainName(StandardOpenAI{}))
	}
}

func TestChainName_WrappedAdapter(t *testing.T) {
	a := ToolCallTagInline{Inner: ThinkTagInline{Inner: ReasoningContentField{}}}
	got := ChainName(a)
	want := "reasoning_content_field+think_tag_inline+tool_call_tag_inline"
	if got != want {
		t.Errorf("ChainName = %q, want %q", got, want)
	}
}
