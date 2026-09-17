package anthropic

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// TestWrapAPIError_ExtractsStatusFromAnthropicError verifies wrapAPIError
// carries forward the HTTP status code from an *anthropic.Error (the SDK's
// type for a response with a non-2xx status), and that the result satisfies
// llm.StatusCoder — the interface internal/agent/retry.go relies on to
// classify retryability from the real status instead of pattern-matching
// the message text.
func TestWrapAPIError_ExtractsStatusFromAnthropicError(t *testing.T) {
	sdkErr := &anthropic.Error{
		StatusCode: 400,
		Request:    &http.Request{Method: "POST", URL: &url.URL{Path: "/v1/messages"}},
		Response:   &http.Response{StatusCode: 400},
	}
	wrapped := wrapAPIError("anthropic API error", sdkErr)

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

// TestWrapAPIError_UnknownErrorTypeReportsZero verifies an error that isn't
// an *anthropic.Error (e.g. a raw network error from the underlying
// transport) reports status 0 rather than a wrong code, so callers fall
// back to text-based classification instead of trusting a fabricated status.
func TestWrapAPIError_UnknownErrorTypeReportsZero(t *testing.T) {
	wrapped := wrapAPIError("anthropic API error", errors.New("dial tcp: connection refused"))

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 0 {
		t.Errorf("StatusCode() = %d, want 0 (unknown)", got)
	}
}
