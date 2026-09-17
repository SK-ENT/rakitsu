package agent

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// RetryConfig controls LLM call retry behaviour.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Defaults tuned for cloud providers with 60s-window rate limits (NVIDIA NIM
// free tier, OpenAI tier-1, Anthropic dev). With rate-limit-aware backoff
// the curve becomes 10s/20s/40s/60s — 5 attempts span ~130s, enough for one
// 60s rate window to recover. Non-rate-limit retryable errors (500s, network)
// still use BaseDelay (1s → 2s → 4s → 8s → 16s).
var defaultRetryConfig = RetryConfig{
	MaxAttempts: 5,
	BaseDelay:   1 * time.Second,
	MaxDelay:    60 * time.Second,
}

// rateLimitBaseDelay is the starting backoff for 429 / rate-limit errors.
// Longer than BaseDelay because rate-limit windows are typically measured in
// minutes (60s on NVIDIA NIM free, 60s on OpenAI, 60s on Anthropic), not
// seconds like network blips.
const rateLimitBaseDelay = 10 * time.Second

// http429Re and http5xxRe match a bare, word-bounded HTTP status code in a
// lowercased error message. Word-bounded so a status code's digits don't
// false-positive when they're actually part of a larger number or an
// alphanumeric token elsewhere in the message — a plain
// strings.Contains(msg, "500") also matches "500ms", "port 8500", or
// "5003", wrongly classifying a non-retryable error (e.g. a 400 complaining
// that max_tokens must be <= 500) as a transient server error worth
// retrying 5 times with escalating backoff instead of failing fast.
var (
	http429Re = regexp.MustCompile(`\b429\b`)
	http5xxRe = regexp.MustCompile(`\b(500|502|503|504)\b`)
)

// isRateLimit returns true iff err is specifically a rate-limit signal
// (HTTP 429 or an equivalent textual marker). Callers that need a longer
// backoff curve for 429s key off this rather than the general isRetryable.
//
// If err (or something it wraps) reports its own HTTP status via
// llm.StatusCoder — every built-in provider's Generate error does — that
// status is authoritative and the text-matching fallback below is skipped
// entirely: a status code is either 429 or it isn't, and a coincidental
// "429"/"500"-looking number elsewhere in the message text (a token limit,
// a duration, a quota count, ...) must not override what the provider
// actually reported. The fallback only runs for errors with no known
// status, e.g. a raw network error that never reached a provider response.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var sc llm.StatusCoder
	if errors.As(err, &sc) {
		if code := sc.StatusCode(); code > 0 {
			return code == http.StatusTooManyRequests
		}
	}
	msg := strings.ToLower(err.Error())
	return http429Re.MatchString(msg) ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests")
}

// isRetryable returns true for transient errors that are worth retrying.
// See isRateLimit's comment — the same status-code-first, text-as-fallback
// reasoning applies to the 5xx check below.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation is not retryable — caller is shutting down.
	if errors.Is(err, context.Canceled) {
		return false
	}
	if isRateLimit(err) {
		return true
	}
	var sc llm.StatusCoder
	if errors.As(err, &sc) {
		if code := sc.StatusCode(); code > 0 {
			switch code {
			case http.StatusInternalServerError, http.StatusBadGateway,
				http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				return true
			}
			// A known, non-retryable status (400, 401, 404, ...) is
			// authoritative — don't let the text fallback below second-guess it.
			return false
		}
	}
	msg := strings.ToLower(err.Error())
	// Server errors
	if http5xxRe.MatchString(msg) {
		return true
	}
	// Network / timeout
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
}

// RetryConfigFromSettings builds a RetryConfig from YAML settings.
// Zero values fall back to defaults; legacyAttempts honours the old
// settings.execution.retry_attempts field.
func RetryConfigFromSettings(s config.RetrySettings, legacyAttempts int) RetryConfig {
	cfg := defaultRetryConfig
	if s.MaxAttempts > 0 {
		cfg.MaxAttempts = s.MaxAttempts
	} else if legacyAttempts > 0 {
		cfg.MaxAttempts = legacyAttempts
	}
	if d, err := time.ParseDuration(s.BaseDelay); err == nil && d > 0 {
		cfg.BaseDelay = d
	}
	if d, err := time.ParseDuration(s.MaxDelay); err == nil && d > 0 {
		cfg.MaxDelay = d
	}
	return cfg
}

// RetryEvent describes a pending retry attempt — passed to onRetry
// callbacks so callers can emit telemetry (e.g. EventRetryAttempt) before
// the delay starts. Without this, synthesis-level 429 backoffs would be
// invisible to the TUI / debugger and look like a hang.
type RetryEvent struct {
	Attempt     int           // 1-indexed retry attempt number (0 = first call)
	MaxAttempts int           // total attempts allowed
	Err         error         // last error that triggered the retry
	Delay       time.Duration // about-to-sleep duration
}

// retryWithBackoff calls fn repeatedly with rate-limit-aware exponential
// backoff. Returns nil on first success, the underlying error immediately
// for non-retryable errors, or the last error after cfg.MaxAttempts
// retryable failures. onRetry (optional) fires before each retry sleep so
// callers can emit telemetry; pass nil if not needed.
//
// Currently used by Orchestrator.synthesize (pipeline-level synthesis
// call). Agent.generateWithRetry has its own near-equivalent loop with
// rateLimiter.Wait integration; future cleanup may unify them.
func retryWithBackoff(ctx context.Context, cfg RetryConfig, fn func() error, onRetry func(RetryEvent)) error {
	if cfg.MaxAttempts <= 0 {
		cfg = defaultRetryConfig
	}
	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			delay := retryDelay(cfg, attempt-1, isRateLimit(lastErr))
			if onRetry != nil {
				onRetry(RetryEvent{
					Attempt:     attempt,
					MaxAttempts: cfg.MaxAttempts,
					Err:         lastErr,
					Delay:       delay,
				})
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return lastErr
}

// retryDelay returns exponential backoff duration for the given attempt (0-indexed).
// When rateLimited is true, a longer base delay is used because rate-limit
// windows are typically measured in minutes, not seconds.
func retryDelay(cfg RetryConfig, attempt int, rateLimited bool) time.Duration {
	base := cfg.BaseDelay
	if rateLimited {
		base = rateLimitBaseDelay
	}
	raw := float64(base) * math.Pow(2, float64(attempt))
	// Clamp to MaxDelay before int64 conversion to prevent overflow
	if raw > float64(cfg.MaxDelay) || raw < 0 || math.IsInf(raw, 0) {
		return cfg.MaxDelay
	}
	return time.Duration(raw)
}
