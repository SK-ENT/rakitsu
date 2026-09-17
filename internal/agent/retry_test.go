package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// --- isRetryable ---

func TestIsRetryable_RateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 code", errors.New("HTTP 429 Too Many Requests"), true},
		{"rate limit text", errors.New("rate limit exceeded"), true},
		{"too many requests", errors.New("Too Many Requests"), true},
		{"500 server error", errors.New("HTTP 500 Internal Server Error"), true},
		{"502 bad gateway", errors.New("502 Bad Gateway"), true},
		{"503 unavailable", errors.New("503 Service Unavailable"), true},
		{"504 timeout", errors.New("504 Gateway Timeout"), true},
		{"timeout word", errors.New("request timeout after 30s"), true},
		{"deadline exceeded", errors.New("context deadline exceeded"), true},
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"eof", errors.New("unexpected EOF"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		// Non-retryable
		{"400 bad request", errors.New("HTTP 400 Bad Request"), false},
		{"401 unauthorized", errors.New("HTTP 401 Unauthorized"), false},
		{"context canceled", context.Canceled, false},
		{"nil error", nil, false},
		{"generic error", errors.New("something went wrong"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryable(tc.err)
			if got != tc.want {
				t.Errorf("isRetryable(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsRetryable_DoesNotFalsePositiveOnEmbeddedDigits regression-guards
// finding 27: isRetryable/isRateLimit used to match status codes with bare
// strings.Contains, so a code's digits appearing as part of a larger
// number or an alphanumeric token elsewhere in the message (not the actual
// HTTP status) would wrongly classify a non-retryable error as transient —
// retrying it 5 times with escalating backoff instead of failing fast.
func TestIsRetryable_DoesNotFalsePositiveOnEmbeddedDigits(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"500 embedded in a duration", "finish_reason=length after 500ms"},
		{"500 embedded in a larger number", "max_tokens must be between 1 and 4096, got 50000"},
		{"500 embedded in a port number", "connect tcp 127.0.0.1:8500: no route to host"},
		{"429 embedded in a duration", "429ms elapsed while waiting for stream"},
		{"429 embedded in a larger number", "waited 14290 ms for the upstream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := errors.New(tc.msg)
			if isRetryable(err) {
				t.Errorf("isRetryable(%q) = true, want false (status code digits are embedded in a larger token, not a real status code)", tc.msg)
			}
			if isRateLimit(err) {
				t.Errorf("isRateLimit(%q) = true, want false (429 digits are embedded in a larger token, not a real 429)", tc.msg)
			}
		})
	}
}

// TestIsRetryable_StillMatchesWordBoundedStatusCodes verifies the
// word-boundary fix didn't regress the real, word-bounded status codes it's
// meant to keep matching (a bare digit sequence flanked by non-word
// characters — start/end of string, spaces, punctuation).
func TestIsRetryable_StillMatchesWordBoundedStatusCodes(t *testing.T) {
	cases := []string{
		"429 Too Many Requests",
		"HTTP/1.1 429",
		"status: 500",
		"error (502)",
		"503.",
		"504,",
	}
	for _, msg := range cases {
		if !isRetryable(errors.New(msg)) {
			t.Errorf("isRetryable(%q) = false, want true (word-bounded status code)", msg)
		}
	}
}

// fakeStatusError is a minimal llm.StatusCoder implementation for testing
// the structured-status-code path in isRetryable/isRateLimit without
// depending on any real provider SDK's error type.
type fakeStatusError struct {
	msg  string
	code int
}

func (e *fakeStatusError) Error() string   { return e.msg }
func (e *fakeStatusError) StatusCode() int { return e.code }

// TestIsRetryable_TrustsStructuredStatusOverMessageText regression-guards a
// gap the word-boundary fix (finding 27) left open: a word-bounded status
// code is still ambiguous when it's not actually a status code at all — e.g.
// a 400 error whose message happens to contain a standalone "500" for an
// unrelated reason (a token limit, a quota, ...). Text alone can't tell
// these apart, but a provider error implementing llm.StatusCoder carries the
// real status, and that must win over whatever numbers appear in the text.
func TestIsRetryable_TrustsStructuredStatusOverMessageText(t *testing.T) {
	cases := []struct {
		name          string
		code          int
		msg           string
		wantRetryable bool
		wantRateLimit bool
	}{
		{
			name:          "400 with a coincidental word-bounded 500 in the message",
			code:          400,
			msg:           "error, status code: 400, status: 400 Bad Request, message: Invalid 'max_tokens': integer below minimum value. Expected a value <= 500, but got a much larger value instead.",
			wantRetryable: false,
			wantRateLimit: false,
		},
		{
			name:          "404 with a coincidental word-bounded 429 in the message",
			code:          404,
			msg:           "error, status code: 404, message: model 'gpt-429-preview' not found",
			wantRetryable: false,
			wantRateLimit: false,
		},
		{
			name:          "real 500 via structured status, no digits in the text at all",
			code:          500,
			msg:           "error, status code: 500, message: internal error",
			wantRetryable: true,
			wantRateLimit: false,
		},
		{
			name:          "real 429 via structured status",
			code:          429,
			msg:           "error, status code: 429, message: rate limited",
			wantRetryable: true,
			wantRateLimit: true,
		},
		{
			name:          "501 is not in the retryable set even though it's 5xx",
			code:          501,
			msg:           "error, status code: 501, message: not implemented",
			wantRetryable: false,
			wantRateLimit: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &fakeStatusError{msg: tc.msg, code: tc.code}
			if got := isRetryable(err); got != tc.wantRetryable {
				t.Errorf("isRetryable() = %v, want %v", got, tc.wantRetryable)
			}
			if got := isRateLimit(err); got != tc.wantRateLimit {
				t.Errorf("isRateLimit() = %v, want %v", got, tc.wantRateLimit)
			}
		})
	}
}

// TestIsRetryable_FallsBackToTextWhenStatusUnknown verifies an error that
// implements llm.StatusCoder but reports code 0 (unknown/not an HTTP
// response — e.g. a network error a provider still chose to wrap) falls
// through to the same text-based heuristics used for a plain error.
func TestIsRetryable_FallsBackToTextWhenStatusUnknown(t *testing.T) {
	err := &fakeStatusError{msg: "connection refused", code: 0}
	if !isRetryable(err) {
		t.Error("isRetryable() = false, want true (falls back to text match on unknown status)")
	}
}

// --- retryDelay ---

func TestRetryDelay_ExponentialBackoff(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Second,
		MaxDelay:    8 * time.Second,
	}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second}, // capped at MaxDelay
		{4, 8 * time.Second}, // still capped
	}
	for _, tc := range cases {
		got := retryDelay(cfg, tc.attempt, false)
		if got != tc.want {
			t.Errorf("retryDelay(attempt=%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestRetryDelay_MaxDelayCap(t *testing.T) {
	cfg := RetryConfig{BaseDelay: 1 * time.Second, MaxDelay: 3 * time.Second}
	for attempt := 5; attempt < 10; attempt++ {
		d := retryDelay(cfg, attempt, false)
		if d > cfg.MaxDelay {
			t.Errorf("retryDelay(attempt=%d) = %v exceeds MaxDelay %v", attempt, d, cfg.MaxDelay)
		}
	}
}

func TestRetryDelay_ZeroBase(t *testing.T) {
	cfg := RetryConfig{BaseDelay: 0, MaxDelay: 5 * time.Second}
	for attempt := 0; attempt < 5; attempt++ {
		d := retryDelay(cfg, attempt, false)
		if d != 0 {
			t.Errorf("retryDelay(attempt=%d) = %v, want 0 for zero base", attempt, d)
		}
	}
}

func TestRetryDelay_SingleAttempt(t *testing.T) {
	cfg := RetryConfig{BaseDelay: 500 * time.Millisecond, MaxDelay: 500 * time.Millisecond}
	for attempt := 0; attempt < 10; attempt++ {
		d := retryDelay(cfg, attempt, false)
		if d != 500*time.Millisecond {
			t.Errorf("retryDelay(attempt=%d) = %v, want 500ms (capped)", attempt, d)
		}
	}
}

// --- isRateLimit ---

func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 code", errors.New("HTTP 429 Too Many Requests"), true},
		{"rate limit phrase", errors.New("rate limit exceeded"), true},
		{"too many requests phrase", errors.New("Too Many Requests"), true},
		{"openai stream 429", errors.New("openai stream error: error, status code: 429, status: 429 Too Many Requests"), true},
		{"500 server error (not rate limit)", errors.New("HTTP 500 Internal Server Error"), false},
		{"connection refused (not rate limit)", errors.New("connection refused"), false},
		{"nil error", nil, false},
		{"generic error", errors.New("something went wrong"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimit(tc.err); got != tc.want {
				t.Errorf("isRateLimit(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// --- retryDelay rate-limited path ---

func TestRetryDelay_RateLimitedUsesLongerBase(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Second,
		MaxDelay:    60 * time.Second,
	}
	// Non-rate-limited should use BaseDelay (1s) exponential: 1s, 2s, 4s, 8s, 16s
	for attempt, want := range []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second} {
		if got := retryDelay(cfg, attempt, false); got != want {
			t.Errorf("retryDelay(attempt=%d, false) = %v, want %v", attempt, got, want)
		}
	}
	// Rate-limited should use rateLimitBaseDelay (10s) exponential: 10s, 20s, 40s, 60s (capped)
	rateLimitCases := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second}
	for attempt, want := range rateLimitCases {
		if got := retryDelay(cfg, attempt, true); got != want {
			t.Errorf("retryDelay(attempt=%d, true) = %v, want %v (rate-limited path)", attempt, got, want)
		}
	}
}

func TestRetryDelay_RateLimitedRespectsMaxDelay(t *testing.T) {
	// Even with rate-limit base, MaxDelay must cap the wait
	cfg := RetryConfig{BaseDelay: 1 * time.Second, MaxDelay: 5 * time.Second}
	for attempt := 0; attempt < 10; attempt++ {
		d := retryDelay(cfg, attempt, true)
		if d > cfg.MaxDelay {
			t.Errorf("retryDelay(attempt=%d, true) = %v exceeds MaxDelay %v", attempt, d, cfg.MaxDelay)
		}
	}
}

// --- retryWithBackoff ---

func TestRetryWithBackoff_FirstSuccess(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		return nil
	}, nil)
	if err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryWithBackoff_RetriesOnTransient(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		if calls < 3 {
			return errors.New("HTTP 429 Too Many Requests")
		}
		return nil
	}, nil)
	if err != nil {
		t.Errorf("expected eventual success, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryWithBackoff_NonRetryableImmediateFail(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxAttempts: 5, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	hardErr := errors.New("HTTP 401 Unauthorized")
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		return hardErr
	}, nil)
	if !errors.Is(err, hardErr) {
		t.Errorf("expected hard error to bubble up, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-retryable should fail-fast at 1 call, got %d", calls)
	}
}

func TestRetryWithBackoff_ExhaustedReturnsLastErr(t *testing.T) {
	calls := 0
	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		return fmt.Errorf("attempt %d: 429 Too Many Requests", calls)
	}, nil)
	if err == nil {
		t.Error("expected error after exhaustion")
	}
	if calls != cfg.MaxAttempts {
		t.Errorf("expected %d calls (full exhaustion), got %d", cfg.MaxAttempts, calls)
	}
	if !strings.Contains(err.Error(), "attempt 3") {
		t.Errorf("expected last error message, got %v", err)
	}
}

func TestRetryWithBackoff_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := RetryConfig{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 100 * time.Millisecond}
	calls := 0
	err := retryWithBackoff(ctx, cfg, func() error {
		calls++
		return errors.New("HTTP 429")
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled after first retry sleep, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call before cancellation kicked in, got %d", calls)
	}
}

func TestRetryWithBackoff_ZeroMaxAttemptsUsesDefault(t *testing.T) {
	calls := 0
	cfg := RetryConfig{} // zero value
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		return nil
	}, nil)
	if err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	// Default has MaxAttempts > 0, so first call should succeed
	if calls != 1 {
		t.Errorf("expected 1 call (first success), got %d", calls)
	}
}

func TestRetryWithBackoff_OnRetryFiresWithEachAttempt(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	var events []RetryEvent
	calls := 0
	err := retryWithBackoff(context.Background(), cfg, func() error {
		calls++
		return fmt.Errorf("attempt %d 429 Too Many Requests", calls)
	}, func(re RetryEvent) {
		events = append(events, re)
	})
	if err == nil {
		t.Fatal("expected error after exhaustion")
	}
	// MaxAttempts=3 means 3 fn() calls and 2 retry-sleeps (between attempt 1→2 and 2→3),
	// so onRetry fires twice.
	if len(events) != 2 {
		t.Errorf("expected 2 onRetry callbacks, got %d", len(events))
	}
	if len(events) >= 1 {
		if events[0].Attempt != 1 || events[0].MaxAttempts != 3 {
			t.Errorf("first event: Attempt=%d MaxAttempts=%d, want 1/3", events[0].Attempt, events[0].MaxAttempts)
		}
		if events[0].Delay <= 0 {
			t.Errorf("first event Delay = %v, want > 0", events[0].Delay)
		}
		if events[0].Err == nil {
			t.Error("first event Err should not be nil (the error that triggered the retry)")
		}
	}
}

func TestRetryWithBackoff_OnRetryNotCalledOnFirstSuccess(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 1 * time.Millisecond}
	called := false
	err := retryWithBackoff(context.Background(), cfg, func() error {
		return nil // success on attempt 0
	}, func(re RetryEvent) {
		called = true
	})
	if err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	if called {
		t.Error("onRetry should NOT fire when fn succeeds on first attempt")
	}
}

// --- isRetryable edge cases ---

func TestIsRetryable_WrappedErrors(t *testing.T) {
	base := errors.New("connection refused")
	wrapped := fmt.Errorf("dial failed: %w", base)
	if !isRetryable(wrapped) {
		t.Error("isRetryable should match wrapped error containing 'connection refused'")
	}
}

func TestIsRetryable_CaseInsensitive(t *testing.T) {
	cases := []string{
		"RATE LIMIT EXCEEDED",
		"Rate Limit Exceeded",
		"CONNECTION RESET BY PEER",
		"BROKEN PIPE",
		"HTTP 429",
		"Timeout",
	}
	for _, msg := range cases {
		if !isRetryable(errors.New(msg)) {
			t.Errorf("isRetryable(%q) = false, want true (case-insensitive match)", msg)
		}
	}
}

func TestIsRetryable_DeadlineExceededContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	// context.DeadlineExceeded message contains "deadline exceeded" — retryable
	if !isRetryable(ctx.Err()) {
		t.Errorf("isRetryable(context.DeadlineExceeded) = false, want true")
	}
}

func TestIsRetryable_CanceledNotRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if isRetryable(ctx.Err()) {
		t.Error("isRetryable(context.Canceled) = true, want false")
	}
}

// --- Stress tests ---

// TestIsRetryable_Concurrent verifies isRetryable is safe for concurrent use
// (no shared mutable state — this is a data race check).
func TestIsRetryable_Concurrent(t *testing.T) {
	errs := []error{
		errors.New("HTTP 429 Too Many Requests"),
		errors.New("connection refused"),
		errors.New("HTTP 400 Bad Request"),
		context.Canceled,
		nil,
		errors.New("unexpected EOF"),
		errors.New("something went wrong"),
	}

	const goroutines = 100
	const callsPerGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				_ = isRetryable(errs[(i+j)%len(errs)])
			}
		}(i)
	}
	wg.Wait()
}

// TestRetryDelay_Concurrent verifies retryDelay is safe for concurrent use.
func TestRetryDelay_Concurrent(t *testing.T) {
	cfg := RetryConfig{BaseDelay: 100 * time.Millisecond, MaxDelay: 2 * time.Second}
	const goroutines = 50
	const callsPerGoroutine = 2000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				d := retryDelay(cfg, j%10, j%2 == 0)
				if d > cfg.MaxDelay {
					// Use atomic to signal failure without t.Fatal in goroutine
					panic(fmt.Sprintf("retryDelay exceeded MaxDelay: %v > %v", d, cfg.MaxDelay))
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestRetryDelay_StressHighAttempts verifies no overflow or unexpected values at large attempt numbers.
func TestRetryDelay_StressHighAttempts(t *testing.T) {
	cfg := RetryConfig{BaseDelay: 1 * time.Second, MaxDelay: 30 * time.Second}
	for attempt := 0; attempt < 10000; attempt++ {
		d := retryDelay(cfg, attempt, false)
		if d < 0 {
			t.Errorf("retryDelay(attempt=%d) = %v, want >= 0 (no underflow)", attempt, d)
		}
		if d > cfg.MaxDelay {
			t.Errorf("retryDelay(attempt=%d) = %v, exceeds MaxDelay %v", attempt, d, cfg.MaxDelay)
		}
	}
}

// TestIsRetryable_StressAllPatterns runs all known retryable patterns at high volume.
func TestIsRetryable_StressAllPatterns(t *testing.T) {
	retryable := []string{
		"HTTP 429 Too Many Requests",
		"rate limit exceeded",
		"Too Many Requests",
		"HTTP 500 Internal Server Error",
		"502 Bad Gateway",
		"503 Service Unavailable",
		"504 Gateway Timeout",
		"request timeout after 30s",
		"context deadline exceeded",
		"dial tcp: connection refused",
		"connection reset by peer",
		"unexpected EOF",
		"write: broken pipe",
	}
	nonRetryable := []string{
		"HTTP 400 Bad Request",
		"HTTP 401 Unauthorized",
		"HTTP 403 Forbidden",
		"something went wrong",
		"invalid API key",
		"model not found",
	}

	var retryCount, nonRetryCount atomic.Int64
	const iterations = 10000

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			err := errors.New(retryable[i%len(retryable)])
			if !isRetryable(err) {
				t.Errorf("stress: isRetryable(%q) = false, want true", err)
			}
			retryCount.Add(1)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			err := errors.New(nonRetryable[i%len(nonRetryable)])
			if isRetryable(err) {
				t.Errorf("stress: isRetryable(%q) = true, want false", err)
			}
			nonRetryCount.Add(1)
		}
	}()

	wg.Wait()

	if retryCount.Load() != iterations || nonRetryCount.Load() != iterations {
		t.Errorf("stress: expected %d iterations each, got retryable=%d non-retryable=%d",
			iterations, retryCount.Load(), nonRetryCount.Load())
	}
}

// --- RetryConfigFromSettings ---

func TestRetryConfigFromSettings_Defaults(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{}, 0)
	if rc != defaultRetryConfig {
		t.Fatalf("expected default config, got %+v", rc)
	}
}

func TestRetryConfigFromSettings_FullOverride(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{
		MaxAttempts: 5,
		BaseDelay:   "2s",
		MaxDelay:    "30s",
	}, 0)
	if rc.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", rc.MaxAttempts)
	}
	if rc.BaseDelay != 2*time.Second {
		t.Errorf("BaseDelay = %v, want 2s", rc.BaseDelay)
	}
	if rc.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", rc.MaxDelay)
	}
}

func TestRetryConfigFromSettings_PartialOverride(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{MaxAttempts: 7}, 0)
	if rc.MaxAttempts != 7 {
		t.Errorf("MaxAttempts = %d, want 7", rc.MaxAttempts)
	}
	if rc.BaseDelay != defaultRetryConfig.BaseDelay {
		t.Errorf("BaseDelay = %v, want default %v", rc.BaseDelay, defaultRetryConfig.BaseDelay)
	}
}

func TestRetryConfigFromSettings_LegacyFallback(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{}, 10)
	if rc.MaxAttempts != 10 {
		t.Errorf("MaxAttempts = %d, want 10 (legacy fallback)", rc.MaxAttempts)
	}
}

func TestRetryConfigFromSettings_SettingsOverrideLegacy(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{MaxAttempts: 5}, 10)
	if rc.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5 (settings takes precedence)", rc.MaxAttempts)
	}
}

func TestRetryConfigFromSettings_InvalidDuration(t *testing.T) {
	rc := RetryConfigFromSettings(config.RetrySettings{
		BaseDelay: "not-a-duration",
		MaxDelay:  "also-bad",
	}, 0)
	if rc.BaseDelay != defaultRetryConfig.BaseDelay {
		t.Errorf("BaseDelay = %v, want default on invalid input", rc.BaseDelay)
	}
	if rc.MaxDelay != defaultRetryConfig.MaxDelay {
		t.Errorf("MaxDelay = %v, want default on invalid input", rc.MaxDelay)
	}
}
