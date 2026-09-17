package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

func msg(role, content string) llm.Message { return llm.NewTextMessage(role, content) }

// transcript builds n complete user/assistant turns.
func transcript(n int) []llm.Message {
	var out []llm.Message
	for i := 1; i <= n; i++ {
		out = append(out, msg("user", fmt.Sprintf("question %d", i)))
		out = append(out, msg("assistant", fmt.Sprintf("answer %d", i)))
	}
	return out
}

func fakeSummarizer(t *testing.T, captured *[]string) SummarizeFunc {
	t.Helper()
	return func(ctx context.Context, prompt string) (string, error) {
		if captured != nil {
			*captured = append(*captured, prompt)
		}
		return "SUMMARY", nil
	}
}

func TestSplitTurns(t *testing.T) {
	turns := splitTurns(transcript(3))
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if turns[1][0].AsText() != "question 2" || turns[1][1].AsText() != "answer 2" {
		t.Errorf("turn 2 = %+v", turns[1])
	}
	// Leading assistant message attaches to an implicit first turn.
	turns = splitTurns([]llm.Message{msg("assistant", "hi"), msg("user", "q"), msg("assistant", "a")})
	if len(turns) != 2 {
		t.Errorf("implicit first turn: got %d turns", len(turns))
	}
}

func TestComposeFallsBackUntilFolded(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2})
	prior := transcript(5)
	got := c.ComposeHistory(prior)
	if len(got) != len(prior) {
		t.Errorf("nothing folded yet: compose must return prior unchanged (%d != %d)", len(got), len(prior))
	}
}

func TestUpdateFoldsAndComposeCompresses(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2})
	full := transcript(5)

	var prompts []string
	if err := c.Update(context.Background(), full, fakeSummarizer(t, &prompts)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if c.Summary() != "SUMMARY" {
		t.Errorf("summary = %q", c.Summary())
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "question 3") || strings.Contains(prompts[0], "question 4") {
		t.Errorf("summarizer prompt must contain folded turns 1-3 only:\n%s", prompts[0])
	}

	// Compose for the next turn: prior = the same 5 turns.
	got := c.ComposeHistory(full)
	// Expect: summary pair + last 2 turns verbatim = 2 + 4 messages.
	if len(got) != 6 {
		t.Fatalf("composed length = %d, want 6: %+v", len(got), got)
	}
	if got[0].Role != "user" || !strings.Contains(got[0].AsText(), "SUMMARY") {
		t.Errorf("first message must carry the summary: %+v", got[0])
	}
	if got[1].Role != "assistant" {
		t.Errorf("second message must be the assistant ack for role alternation")
	}
	if got[2].AsText() != "question 4" || got[5].AsText() != "answer 5" {
		t.Errorf("verbatim window wrong: %+v", got[2:])
	}

	// No new foldable turns -> Update is a no-op (summarizer not called).
	called := false
	err := c.Update(context.Background(), full, func(ctx context.Context, p string) (string, error) {
		called = true
		return "X", nil
	})
	if err != nil || called {
		t.Errorf("Update should no-op when window unchanged (err=%v called=%v)", err, called)
	}
}

func TestDisableSummaryDropsWithoutSummarizer(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2, DisableSummary: true})
	full := transcript(5)

	// Drop-mode advances the window WITHOUT calling the summarizer.
	if err := c.Update(context.Background(), full, func(ctx context.Context, p string) (string, error) {
		t.Fatal("summarizer must not run in drop-mode")
		return "", nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Compose: only the last 2 turns verbatim, no summary pair = 4 messages.
	got := c.ComposeHistory(full)
	if len(got) != 4 {
		t.Fatalf("drop-mode composed length = %d, want 4: %+v", len(got), got)
	}
	if got[0].AsText() != "question 4" || got[3].AsText() != "answer 5" {
		t.Errorf("verbatim window wrong: %+v", got)
	}
	for _, m := range got {
		if strings.Contains(m.AsText(), "summary") || strings.Contains(m.AsText(), "SUMMARY") {
			t.Errorf("drop-mode must emit no summary block: %+v", m)
		}
	}

	// Resume keeps the same window boundary (folded persisted).
	c2 := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2, DisableSummary: true})
	if got := c2.ComposeHistory(full); len(got) != 4 {
		t.Errorf("resumed drop-mode compose = %d, want 4", len(got))
	}
}

func TestSummarizerFailureLosesNothing(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 1})
	full := transcript(4)
	err := c.Update(context.Background(), full, func(ctx context.Context, p string) (string, error) {
		return "", errors.New("model down")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Nothing folded -> compose returns everything.
	if got := c.ComposeHistory(full); len(got) != len(full) {
		t.Errorf("after failed Update compose must return full history (%d != %d)", len(got), len(full))
	}
	// Empty summary is also a failure, not a fold.
	if err := c.Update(context.Background(), full, func(ctx context.Context, p string) (string, error) {
		return "  ", nil
	}); err == nil {
		t.Error("expected error for empty summary")
	}
}

func TestResumeReloadsSummaryAndFoldCount(t *testing.T) {
	s := newTestStore(t)
	c1 := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2})
	full := transcript(6)
	if err := c1.Update(context.Background(), full, fakeSummarizer(t, nil)); err != nil {
		t.Fatalf("Update: %v", err)
	}

	c2 := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2})
	if c2.Summary() != "SUMMARY" {
		t.Errorf("resume summary = %q", c2.Summary())
	}
	got := c2.ComposeHistory(full)
	if len(got) != 6 { // summary pair + 2 turns * 2 msgs
		t.Errorf("resume compose length = %d, want 6", len(got))
	}
	// A different session is isolated.
	c3 := NewConversationMemory(s, "other", ConversationOptions{})
	if c3.Summary() != "" {
		t.Errorf("other session must start empty, got %q", c3.Summary())
	}
}

func TestUpdateIncrementalFolding(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 2})
	if err := c.Update(context.Background(), transcript(4), fakeSummarizer(t, nil)); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	var prompts []string
	if err := c.Update(context.Background(), transcript(6), fakeSummarizer(t, &prompts)); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	// Second fold covers turns 3-4 only (1-2 folded already, 5-6 in window).
	if len(prompts) != 1 || strings.Contains(prompts[0], "question 2") || !strings.Contains(prompts[0], "question 3") || strings.Contains(prompts[0], "question 5") {
		t.Errorf("incremental fold prompt wrong:\n%s", prompts[0])
	}
}

func TestOversizedSummaryIsCapped(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 1, SummaryMaxChars: 100})
	err := c.Update(context.Background(), transcript(3), func(ctx context.Context, p string) (string, error) {
		return strings.Repeat("x", 1000), nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(c.Summary()) > 130 {
		t.Errorf("summary not capped: %d chars", len(c.Summary()))
	}
}

// TestOversizedSummaryCapIsRuneSafe: the hard-cap on an overshooting
// summarizer response used to slice by byte offset, which can split a
// multi-byte character and produce invalid UTF-8.
func TestOversizedSummaryCapIsRuneSafe(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess1", ConversationOptions{KeepRecentTurns: 1, SummaryMaxChars: 100})
	err := c.Update(context.Background(), transcript(3), func(ctx context.Context, p string) (string, error) {
		return strings.Repeat("要", 1000), nil // 3-byte rune, 3000 bytes total
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := c.Summary(); !utf8.ValidString(got) {
		t.Errorf("capped summary is not valid UTF-8: %q", got)
	}
}

// TestBuildSummaryPromptTruncatesMultiByteContentSafely: same class of bug
// as the cap above, in the per-turn message truncation.
func TestBuildSummaryPromptTruncatesMultiByteContentSafely(t *testing.T) {
	content := strings.Repeat("あ", turnMsgMaxChars+500) // 3-byte rune, exceeds the cap
	prompt := buildSummaryPrompt("", [][]llm.Message{{msg("user", content)}}, 500)
	if !utf8.ValidString(prompt) {
		t.Fatal("prompt is not valid UTF-8 after truncating multi-byte turn content")
	}
	if !strings.Contains(prompt, "…[truncated]") {
		t.Error("expected the truncation marker in the prompt")
	}
}

// TestUpdateReleasesLockDuringSummarize: Update used to hold the lock across
// the summarizer's network call, blocking the read-only Summary() and
// ComposeHistory() accessors for the full duration of that call.
func TestUpdateReleasesLockDuringSummarize(t *testing.T) {
	s := newTestStore(t)
	c := NewConversationMemory(s, "sess-concurrent", ConversationOptions{KeepRecentTurns: 1})

	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := func(ctx context.Context, prompt string) (string, error) {
		close(entered)
		<-release
		return "SUMMARY", nil
	}

	updateDone := make(chan error, 1)
	go func() { updateDone <- c.Update(context.Background(), transcript(3), blocking) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Update never reached the summarizer call")
	}

	readDone := make(chan struct{})
	go func() {
		c.Summary()
		c.ComposeHistory(transcript(3))
		close(readDone)
	}()

	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Summary()/ComposeHistory() blocked on Update's in-flight summarizer call — the lock must be released before calling summarize")
	}

	close(release)
	if err := <-updateDone; err != nil {
		t.Fatalf("Update: %v", err)
	}
}
