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
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
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
func TestAgent_EmptyAnswer_NonLengthFinish_DoesNotGetTruncatedMarker(t *testing.T) {
	resp := stopResponse("")
	out, err, unproductive := drive(t, "empty-stop", 3, 0, resp)

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
