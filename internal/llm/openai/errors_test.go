package openai

import (
	"errors"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	openai "github.com/sashabaranov/go-openai"
)

// TestWrapAPIError_ExtractsStatusFromAPIError verifies wrapAPIError carries
// forward the HTTP status code from an *openai.APIError (the SDK's type for
// a response with a parsed JSON error body), and that the result satisfies
// llm.StatusCoder — the interface internal/agent/retry.go relies on to
// classify retryability from the real status instead of pattern-matching
// the message text.
func TestWrapAPIError_ExtractsStatusFromAPIError(t *testing.T) {
	sdkErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Invalid 'max_tokens': integer below minimum value. Expected a value <= 500.",
	}
	wrapped := wrapAPIError("openai API error", sdkErr)

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 400 {
		t.Errorf("StatusCode() = %d, want 400", got)
	}
	if !errors.Is(wrapped, sdkErr) {
		t.Error("wrapped error does not unwrap to the original SDK error")
	}
}

// TestWrapAPIError_ExtractsStatusFromRequestError covers the SDK's fallback
// error type (used when the error body isn't parseable JSON), which carries
// the status code under the same field name but a different concrete type.
func TestWrapAPIError_ExtractsStatusFromRequestError(t *testing.T) {
	sdkErr := &openai.RequestError{
		HTTPStatusCode: 503,
		Body:           []byte("<html>upstream error</html>"),
	}
	wrapped := wrapAPIError("openai API error", sdkErr)

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 503 {
		t.Errorf("StatusCode() = %d, want 503", got)
	}
}

// TestWrapAPIError_ExtractsParamFromAPIError verifies wrapAPIError carries
// forward the `param` field an APIError names (e.g. "temperature" on a
// reasoning-tier model's 400) — the field omitRejectedParam reads to decide
// whether to retry without it.
func TestWrapAPIError_ExtractsParamFromAPIError(t *testing.T) {
	param := "temperature"
	sdkErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Unsupported parameter: 'temperature' is not supported with this model.",
		Param:          &param,
	}
	wrapped := wrapAPIError("openai API error", sdkErr)

	var pc paramCoder
	if !errors.As(wrapped, &pc) {
		t.Fatal("wrapped error does not implement paramCoder")
	}
	got, ok := pc.Param()
	if !ok || got != "temperature" {
		t.Errorf("Param() = (%q, %v), want (\"temperature\", true)", got, ok)
	}
}

// TestWrapAPIError_ExtractsParamFromNestedMessage covers a real shape seen
// against the DGX LiteLLM proxy: the outer APIError has no top-level `param`
// (the proxy didn't propagate it), but its `Message` string double-encodes
// the upstream provider's own JSON error body as literal text, which does
// name the param. wrapAPIError must fall back to scanning the message for it
// rather than reporting "absent" just because the top-level field is empty.
func TestWrapAPIError_ExtractsParamFromNestedMessage(t *testing.T) {
	sdkErr := &openai.APIError{
		HTTPStatusCode: 400,
		Message: `litellm.BadRequestError: OpenAIException - {
  "error": {
    "message": "Unsupported parameter: 'temperature' is not supported with this model.",
    "type": "invalid_request_error",
    "param": "temperature",
    "code": null
  }
}No fallback model group found for original model_group=openai/gpt-5.6-luna.`,
		Param: nil,
	}
	wrapped := wrapAPIError("openai stream error", sdkErr)

	var pc paramCoder
	if !errors.As(wrapped, &pc) {
		t.Fatal("wrapped error does not implement paramCoder")
	}
	got, ok := pc.Param()
	if !ok || got != "temperature" {
		t.Errorf("Param() = (%q, %v), want (\"temperature\", true)", got, ok)
	}
}

// TestWrapAPIError_NoParamReportsAbsent verifies an APIError with no `param`
// field (or an error type without one at all) reports absence rather than an
// empty-but-present param — omitRejectedParam must not mistake "" for a real
// field name.
func TestWrapAPIError_NoParamReportsAbsent(t *testing.T) {
	wrapped := wrapAPIError("openai API error", &openai.APIError{HTTPStatusCode: 400, Message: "bad request"})

	var pc paramCoder
	if !errors.As(wrapped, &pc) {
		t.Fatal("wrapped error does not implement paramCoder")
	}
	if got, ok := pc.Param(); ok {
		t.Errorf("Param() = (%q, true), want ok=false when the API named no param", got)
	}
}

// TestWrapAPIError_UnknownErrorTypeReportsZero verifies an error that isn't
// one of the SDK's known status-carrying types (e.g. a raw network error
// from the underlying transport) reports status 0 rather than a wrong code,
// so callers fall back to text-based classification instead of trusting a
// fabricated status.
func TestWrapAPIError_UnknownErrorTypeReportsZero(t *testing.T) {
	wrapped := wrapAPIError("openai API error", errors.New("dial tcp: connection refused"))

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 0 {
		t.Errorf("StatusCode() = %d, want 0 (unknown)", got)
	}
}
