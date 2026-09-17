package chat

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

func TestBuildQueryWithHistory_Empty(t *testing.T) {
	got := buildQueryWithHistory(nil, "hello")
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestBuildQueryWithHistory_SingleTurn(t *testing.T) {
	h := []llm.Message{
		llm.NewTextMessage("user", "hi"),
		llm.NewTextMessage("assistant", "hello there"),
	}
	got := buildQueryWithHistory(h, "what's next?")
	if !strings.Contains(got, "User: hi") {
		t.Errorf("missing user turn: %q", got)
	}
	if !strings.Contains(got, "Assistant: hello there") {
		t.Errorf("missing assistant turn: %q", got)
	}
	if !strings.Contains(got, "Current question:\nwhat's next?") {
		t.Errorf("missing current question: %q", got)
	}
	if !strings.HasPrefix(got, "Previous conversation:") {
		t.Errorf("missing header: %q", got)
	}
}

func TestBuildQueryWithHistory_MultiTurn(t *testing.T) {
	h := []llm.Message{
		llm.NewTextMessage("user", "q1"),
		llm.NewTextMessage("assistant", "a1"),
		llm.NewTextMessage("user", "q2"),
		llm.NewTextMessage("assistant", "a2"),
		llm.NewTextMessage("user", "q3"),
		llm.NewTextMessage("assistant", "a3"),
	}
	got := buildQueryWithHistory(h, "q4")
	for _, want := range []string{"q1", "a1", "q2", "a2", "q3", "a3", "q4"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output: %q", want, got)
		}
	}
}

func TestBuildQueryWithHistory_Truncation(t *testing.T) {
	var h []llm.Message
	for i := 0; i < 15; i++ {
		h = append(h, llm.NewTextMessage("user", "q"+string(rune('a'+i))))
		h = append(h, llm.NewTextMessage("assistant", "a"+string(rune('a'+i))))
	}
	got := buildQueryWithHistory(h, "final")
	// First 5 turns should be dropped; last 10 kept.
	// q + "a".."e" = qa,qb,qc,qd,qe dropped
	for _, dropped := range []string{"qa", "qb", "qc", "qd", "qe"} {
		if strings.Contains(got, "User: "+dropped+"\n") {
			t.Errorf("expected %q dropped but found in: %q", dropped, got)
		}
	}
	// qf..qo kept
	for i := 5; i < 15; i++ {
		want := "q" + string(rune('a'+i))
		if !strings.Contains(got, want) {
			t.Errorf("expected %q kept but missing: %q", want, got)
		}
	}
}

func TestBuildQueryWithHistory_LongAssistantTruncated(t *testing.T) {
	longAnswer := strings.Repeat("x", 5000)
	h := []llm.Message{
		llm.NewTextMessage("user", "tell me everything"),
		llm.NewTextMessage("assistant", longAnswer),
	}
	got := buildQueryWithHistory(h, "next")
	if !strings.Contains(got, "...[truncated]") {
		t.Errorf("long assistant text should be truncated, got: %q (len=%d)", got[:200], len(got))
	}
	if len(got) > 6000 {
		t.Errorf("composed output too long: %d chars", len(got))
	}
}

func TestBuildQueryWithHistory_OnlyUserUnpaired(t *testing.T) {
	// Trailing user message with no assistant response (shouldn't happen
	// in practice, but be defensive).
	h := []llm.Message{
		llm.NewTextMessage("user", "first"),
		llm.NewTextMessage("assistant", "reply"),
		llm.NewTextMessage("user", "trailing"),
	}
	got := buildQueryWithHistory(h, "now")
	if !strings.Contains(got, "User: trailing") {
		t.Errorf("unpaired trailing user turn should still appear: %q", got)
	}
}
