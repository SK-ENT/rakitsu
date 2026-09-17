package gemini

import (
	"errors"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"google.golang.org/genai"
)

// TestWrapAPIError_ExtractsStatusFromAPIError verifies wrapAPIError carries
// forward the HTTP status code from a genai.APIError (the SDK's type for a
// response with a non-2xx status), and that the result satisfies
// llm.StatusCoder — the interface internal/agent/retry.go relies on to
// classify retryability from the real status instead of pattern-matching
// the message text.
func TestWrapAPIError_ExtractsStatusFromAPIError(t *testing.T) {
	sdkErr := genai.APIError{
		Code:    400,
		Message: "maxOutputTokens must be <= 500",
		Status:  "INVALID_ARGUMENT",
	}
	wrapped := wrapAPIError("gemini API error", sdkErr)

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 400 {
		t.Errorf("StatusCode() = %d, want 400", got)
	}
	// genai.APIError has a []map[string]any field, so it's not a comparable
	// type — errors.Is can't be used on it directly. Confirm the unwrap
	// chain reaches it by extracting it back out with errors.As instead.
	var unwrapped genai.APIError
	if !errors.As(wrapped, &unwrapped) {
		t.Fatal("wrapped error does not unwrap to a genai.APIError")
	}
	if unwrapped.Message != sdkErr.Message {
		t.Errorf("unwrapped Message = %q, want %q", unwrapped.Message, sdkErr.Message)
	}
}

// TestWrapAPIError_UnknownErrorTypeReportsZero verifies an error that isn't
// a genai.APIError (e.g. a raw network error from the underlying transport)
// reports status 0 rather than a wrong code, so callers fall back to
// text-based classification instead of trusting a fabricated status.
func TestWrapAPIError_UnknownErrorTypeReportsZero(t *testing.T) {
	wrapped := wrapAPIError("gemini API error", errors.New("dial tcp: connection refused"))

	var sc llm.StatusCoder
	if !errors.As(wrapped, &sc) {
		t.Fatal("wrapped error does not implement llm.StatusCoder")
	}
	if got := sc.StatusCode(); got != 0 {
		t.Errorf("StatusCode() = %d, want 0 (unknown)", got)
	}
}
