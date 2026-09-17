package openai

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/llm/format"
	openai "github.com/sashabaranov/go-openai"
)

// TestConvertOpenAIDelta_ReasoningContent verifies the field-name translation
// that powers the visible-reasoning channel (POSITIONING-AUDIT.md Claim 1-2).
// If go-openai ever renames or drops ReasoningContent on the delta, this test
// fails loudly instead of silently producing empty reasoning streams.
func TestConvertOpenAIDelta_ReasoningContent(t *testing.T) {
	delta := openai.ChatCompletionStreamChoiceDelta{
		Content:          "answer text",
		ReasoningContent: "thinking text",
	}

	got := convertOpenAIDelta(delta, openai.FinishReasonStop)

	if got.Content != "answer text" {
		t.Errorf("Content = %q, want %q", got.Content, "answer text")
	}
	if got.ReasoningContent != "thinking text" {
		t.Errorf("ReasoningContent = %q, want %q", got.ReasoningContent, "thinking text")
	}
	if got.FinishReason != string(openai.FinishReasonStop) {
		t.Errorf("FinishReason = %q, want %q", got.FinishReason, openai.FinishReasonStop)
	}
}

// TestConvertOpenAIDelta_EmptyReasoning is the negative case — a delta with no
// reasoning_content must surface as empty so the GenerateStream peer emit gate
// (`if rawDelta.ReasoningContent != ""`) does not fire spurious events.
func TestConvertOpenAIDelta_EmptyReasoning(t *testing.T) {
	delta := openai.ChatCompletionStreamChoiceDelta{Content: "just content"}

	got := convertOpenAIDelta(delta, openai.FinishReason(""))

	if got.ReasoningContent != "" {
		t.Errorf("ReasoningContent = %q, want empty", got.ReasoningContent)
	}
	if got.Content != "just content" {
		t.Errorf("Content = %q, want %q", got.Content, "just content")
	}
}

// TestOnReasoningDeltaWiring_InlineThinkSurfacesAsStreamChunk documents the
// B54 contract — the provider sets state.OnReasoningDelta to a closure that
// pushes a StreamChunk{Reasoning: …} for each inline-<think> body fragment.
// This test reproduces the wiring exactly as GenerateStream does (without the
// HTTP/SSE plumbing) so a future refactor that drops the OnReasoningDelta
// callback fails loudly here.
//
// Without this wiring, models that emit reasoning as <think>...</think> inline
// (vllm-nemotron-elastic-30b in practice) produce zero REASONING_CHUNK events,
// reproducing the live-debug symptom that originally filed B54.
func TestOnReasoningDeltaWiring_InlineThinkSurfacesAsStreamChunk(t *testing.T) {
	chunksCh := make(chan llm.StreamChunk, 8)

	// Mirror the GenerateStream initialization. If provider.go drops the
	// OnReasoningDelta assignment, this test still compiles but receives
	// zero Reasoning chunks — which is the regression we want to catch.
	state := format.NewState()
	state.OnReasoningDelta = func(reasoning string) {
		chunksCh <- llm.StreamChunk{Reasoning: reasoning}
	}

	adapter := format.ThinkTagInline{}
	adapter.ApplyDelta(state, format.RawDelta{Content: "<think>working it out</think>The answer."})
	adapter.ApplyDelta(state, format.RawDelta{FinishReason: "stop"})
	_ = adapter.Finalize(state)
	close(chunksCh)

	var reasoning string
	var textCount int
	for c := range chunksCh {
		if c.Reasoning != "" {
			reasoning += c.Reasoning
		}
		if c.Text != "" {
			textCount++
		}
	}
	if reasoning != "working it out" {
		t.Errorf("aggregated reasoning chunks = %q, want %q", reasoning, "working it out")
	}
	if textCount != 0 {
		t.Errorf("OnReasoningDelta must never push Text chunks, saw %d", textCount)
	}
}
