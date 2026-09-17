package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// newSlidingWindowMonitor creates a ContextMonitor pinned to the
// sliding_window strategy with the given window size.
func newSlidingWindowMonitor(windowSize int) *ContextMonitor {
	return NewContextMonitor(config.ContextConfig{
		Strategy:   "sliding_window",
		WindowSize: windowSize,
	})
}

// TestBuildSlidingWindow_KeepsTrailingCorrectiveMessage regression-guards
// against a standalone "user" message injected right before a retry
// (reasoning-only-retry, ground-check retry, turn-guard retry — all append
// a corrective directive and `continue` without an assistant reply yet)
// being silently dropped by sliding-window reconstruction.
func TestBuildSlidingWindow_KeepsTrailingCorrectiveMessage(t *testing.T) {
	cm := newSlidingWindowMonitor(20)
	history := []llm.Message{
		llm.NewTextMessage("user", "query"),
		llm.NewTextMessage("assistant", "first attempt"),
		llm.NewTextMessage("tool", "tool result"),
		llm.NewTextMessage("user", "corrective directive: try again"),
	}
	// Force sliding-window path even though history is short.
	cm.windowSize = 2

	result := cm.buildSlidingWindow(history)

	found := false
	for _, m := range result {
		if m.Role == "user" && strings.Contains(m.AsText(), "corrective directive") {
			found = true
		}
	}
	if !found {
		t.Errorf("trailing corrective message was dropped; result = %+v", result)
	}
}

// TestBuildSlidingWindow_OversizedTurnKeepsPartialWindow regression-guards
// against a single turn larger than windowSize (e.g. many parallel
// tool-result messages in one iteration) discarding the entire
// conversation instead of keeping that turn.
func TestBuildSlidingWindow_OversizedTurnKeepsPartialWindow(t *testing.T) {
	cm := newSlidingWindowMonitor(5)

	history := []llm.Message{llm.NewTextMessage("user", "query")}
	history = append(history, llm.NewTextMessage("assistant", "dispatching many tools"))
	for i := 0; i < 10; i++ {
		history = append(history, llm.NewTextMessage("tool", "result"))
	}

	result := cm.buildSlidingWindow(history)

	if len(result) <= 1 {
		t.Fatalf("expected the oversized turn to be kept (partial window), got only the query: %+v", result)
	}
	hasAssistant := false
	for _, m := range result {
		if m.Role == "assistant" {
			hasAssistant = true
		}
	}
	if !hasAssistant {
		t.Error("expected the kept turn's assistant message to be present, got none")
	}
}

// TestTruncateUTF8_NeverSplitsARune locks in that truncateUTF8 always
// returns valid UTF-8, even when the byte cutoff lands mid-codepoint.
func TestTruncateUTF8_NeverSplitsARune(t *testing.T) {
	s := strings.Repeat("あ", 10) // each rune is 3 bytes
	for cut := 0; cut <= len(s); cut++ {
		got := truncateUTF8(s, cut)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateUTF8(s, %d) = %q, not valid UTF-8", cut, got)
		}
		if len(got) > cut {
			t.Fatalf("truncateUTF8(s, %d) returned %d bytes, want <= %d", cut, len(got), cut)
		}
	}
}

func TestTruncateUTF8_NoTruncationBelowLimit(t *testing.T) {
	s := "short"
	if got := truncateUTF8(s, 100); got != s {
		t.Errorf("truncateUTF8(%q, 100) = %q, want unchanged", s, got)
	}
}

// TestSanitizeOutput_TruncatesOnRuneBoundary locks in that SanitizeOutput's
// truncation never emits invalid UTF-8 for multi-byte content.
func TestSanitizeOutput_TruncatesOnRuneBoundary(t *testing.T) {
	cm := NewContextMonitor(config.ContextConfig{MaxToolOutput: 10})
	output := strings.Repeat("あ", 10) // 30 bytes; cutoff at 10 lands mid-rune

	got := cm.SanitizeOutput("tool", "call-1", output)

	// The kept prefix (before the "... [truncated" marker) must be valid UTF-8.
	prefix := strings.SplitN(got, "\n... [truncated", 2)[0]
	if !utf8.ValidString(prefix) {
		t.Errorf("SanitizeOutput truncated prefix is not valid UTF-8: %q", prefix)
	}
}
