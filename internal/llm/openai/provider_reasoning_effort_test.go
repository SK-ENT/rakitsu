package openai

import (
	"errors"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// Regression tests: GPT-5.6 models reject function tools on
// /v1/chat/completions unless reasoning_effort is sent explicitly. rakitsu
// never set the field, so every tool-using agent on gpt-5.6-* failed with
// a 400 before doing any work (verified live 2026-09-12).

func TestApplyReasoningEffort_SetsWhenConfigured(t *testing.T) {
	req := &openai.ChatCompletionRequest{}
	applyReasoningEffort(req, "none")
	if req.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want %q", req.ReasoningEffort, "none")
	}
}

func TestApplyReasoningEffort_OmitsWhenEmpty(t *testing.T) {
	req := &openai.ChatCompletionRequest{}
	applyReasoningEffort(req, "")
	if req.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want empty (field must be omitted when unset)", req.ReasoningEffort)
	}
}

func TestWrapAPIError_ReasoningEffortHint(t *testing.T) {
	apiErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Function tools with reasoning_effort are not supported for gpt-5.6-terra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.",
	}
	wrapped := wrapAPIError("openai stream error", apiErr)
	if wrapped == nil {
		t.Fatal("wrapAPIError returned nil")
	}
	if !strings.Contains(wrapped.Error(), "reasoning_effort: none") {
		t.Errorf("error should hint at the YAML knob, got: %v", wrapped)
	}
	var back *openai.APIError
	if !errors.As(wrapped, &back) {
		t.Errorf("wrapped error must still unwrap to *openai.APIError")
	}
}

func TestWrapAPIError_NoHintForUnrelatedError(t *testing.T) {
	apiErr := &openai.APIError{HTTPStatusCode: 401, Message: "Incorrect API key provided"}
	wrapped := wrapAPIError("openai stream error", apiErr)
	if strings.Contains(wrapped.Error(), "reasoning_effort") {
		t.Errorf("unrelated error must not carry the reasoning_effort hint, got: %v", wrapped)
	}
}

// applyReasoningEffortFallback: self-heals the exact failure reproduced
// live instead of requiring the user to hand-edit config after
// hitting a hard failure — mirrors the existing omitRejectedParam
// retry-once pattern used for reasoning-tier temperature/top_p rejections.

func TestApplyReasoningEffortFallback_SetsNoneWhenAPINamesIt(t *testing.T) {
	req := &openai.ChatCompletionRequest{Model: "gpt-5.6-luna"}
	apiErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Function tools with reasoning_effort are not supported for gpt-5.6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.",
	}
	wrapped := wrapAPIError("openai stream error", apiErr)

	retried := applyReasoningEffortFallback(req, wrapped)
	if !retried {
		t.Fatal("expected applyReasoningEffortFallback to report a retry")
	}
	if req.ReasoningEffort != "none" {
		t.Errorf("ReasoningEffort = %q, want %q", req.ReasoningEffort, "none")
	}
}

// Never override a value the user (or config) already set explicitly —
// respect their choice (including a real non-"none" level) rather than
// silently downgrading it, and avoid ever retrying the same request twice.
func TestApplyReasoningEffortFallback_DoesNotOverrideExplicitValue(t *testing.T) {
	req := &openai.ChatCompletionRequest{Model: "gpt-5.6-luna", ReasoningEffort: "high"}
	apiErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Function tools with reasoning_effort are not supported for gpt-5.6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.",
	}
	wrapped := wrapAPIError("openai stream error", apiErr)

	retried := applyReasoningEffortFallback(req, wrapped)
	if retried {
		t.Fatal("must not retry when reasoning_effort was already explicitly set")
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want unchanged %q", req.ReasoningEffort, "high")
	}
}

func TestApplyReasoningEffortFallback_IgnoresUnrelatedError(t *testing.T) {
	req := &openai.ChatCompletionRequest{Model: "gpt-4o-mini"}
	apiErr := &openai.APIError{HTTPStatusCode: 401, Message: "Incorrect API key provided"}
	wrapped := wrapAPIError("openai stream error", apiErr)

	retried := applyReasoningEffortFallback(req, wrapped)
	if retried {
		t.Fatal("must not retry for an unrelated error")
	}
	if req.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want empty", req.ReasoningEffort)
	}
}
