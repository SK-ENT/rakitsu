package agent

// Regression test for the truncated-empty-answer bug: an LLM turn that ends
// with FinishReason == "length" (the model exhausted its output-token
// budget — common with reasoning models such as gpt-5-nano, whose hidden
// reasoning tokens count against the same budget as visible content),
// produces zero tool calls, and a genuinely empty Response, used to exit the
// no-tool-calls branch in RunWithAttachments with status="success" and
// finalAnswer="" — no error, no visible text. In the chat TUI this renders
// as total silence: AgentDoneMsg{Response: "", Err: nil} hits neither the
// error branch nor the msg.Response != "" branch in internal/chat/model.go,
// so the pre-existing empty BlockAssistant placeholder never receives text
// and prints nothing (internal/chat/blocks.go).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestAgent_TruncatedEmpty_LengthFinishNoToolCalls_FlagsUnproductiveAndSynthesizesMarker
// pins the fix: a single-iteration turn ending with FinishReason="length",
// no tool calls, and Response="" must not return silently. It must flip
// LastRunUnproductive() and return a non-empty marker mentioning the
// --max-tokens / settings.defaults.max_tokens remediation, matching the
// budget_exceeded/max_iterations marker idiom elsewhere in this package.
func TestAgent_TruncatedEmpty_LengthFinishNoToolCalls_FlagsUnproductiveAndSynthesizesMarker(t *testing.T) {
	resp := llm.GenerateResult{
		Response:     "",
		FinishReason: "length",
		TokenUsage:   &llm.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
	}
	out, err, unproductive := drive(t, "truncated-empty", 3, 0, resp)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unproductive {
		t.Errorf("LastRunUnproductive() = false, want true (empty Response with FinishReason=length)")
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("output must not be empty — silent AGENT_END is the exact bug under test")
	}
	if !strings.Contains(out, "max-tokens") && !strings.Contains(out, "max_tokens") {
		t.Errorf("output = %q, want it to mention the --max-tokens/max_tokens remediation", out)
	}
}

// TestAgent_EmptyAnswer_NonLengthFinish_DoesNotGetTruncatedMarker guards the
// scope of the fix above: an empty final answer with a finish_reason other
// than "length" (e.g. "stop") must still flip LastRunUnproductive()
// (pre-existing contract) but must NOT get the new truncated-empty marker —
// that diagnosis is specific to length-truncation and would be misleading
// here.
//
// An empty, non-length "stop" response is also exactly the shape
// generateWithRetry now retries (see isEmptyNonToolResult and the live
// Gemini quirk it guards against), so this must program defaultRetryConfig's
// full MaxAttempts worth of identical empty responses — otherwise
// sequenceProvider panics on being called past what was programmed. This
// still exercises the case under test: once retries are exhausted, the
// last (still empty) result reaches the same unproductive-marking path.
func TestAgent_EmptyAnswer_NonLengthFinish_DoesNotGetTruncatedMarker(t *testing.T) {
	resp := stopResponse("")
	responses := make([]llm.GenerateResult, defaultRetryConfig.MaxAttempts)
	for i := range responses {
		responses[i] = resp
	}
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	ag := newE2EAgent("empty-stop", newSequenceProvider(responses...), bus, tool)
	ag.maxIterations = 3
	// Fast, deterministic retry timing: this test cares about the
	// unproductive-marking outcome once retries are exhausted, not about
	// exercising real backoff delays.
	ag.SetRetryConfig(RetryConfig{MaxAttempts: defaultRetryConfig.MaxAttempts, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	out, err := ag.Run(context.Background(), "test query")
	unproductive := ag.LastRunUnproductive()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unproductive {
		t.Errorf("LastRunUnproductive() = false, want true (empty Response regardless of finish_reason)")
	}
	if strings.Contains(out, "max-tokens") || strings.Contains(out, "max_tokens") {
		t.Errorf("output = %q, must not carry the length-truncation marker for finish_reason=stop", out)
	}
}
