package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Regression: one-shot `run --resume` used to fork the session record but
// feed the model a fresh context. HistoryForResume is the CLI's resume
// loader; these tests pin its three sources: the .chat.json envelope,
// CHAT_TURN_* event reconstruction, and the one-shot fallback
// (SessionMeta.Query + root-level final answer), which covers sessions that
// emit neither CHAT_TURN_* nor PIPELINE_START events — exactly the shape of
// a one-shot single-agent run.

// persistOneShotFixture writes a session in the one-shot single-agent shape:
// meta line with the user query, then AGENT_START / AGENT_END /
// EXECUTION_COMPLETE. finalAnswer == "" omits the end events (a run killed
// before answering).
func persistOneShotFixture(t *testing.T, sessionID, query, finalAnswer string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".rakitsu", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := store.SessionMeta{
		ID:        sessionID,
		Query:     query,
		StartTime: time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC),
	}
	idx, err := json.MarshalIndent([]store.SessionMeta{meta}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), idx, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Create(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(meta); err != nil {
		t.Fatal(err)
	}
	events := []telemetry.AgentEvent{
		{EventType: telemetry.EventAgentStart, AgentName: "Assistant", Timestamp: meta.StartTime,
			Payload: mustPayload(t, telemetry.AgentStartPayload{Role: "worker", Model: "test-model"})},
	}
	if finalAnswer != "" {
		events = append(events,
			telemetry.AgentEvent{EventType: telemetry.EventAgentEnd, AgentName: "Assistant",
				Timestamp: meta.StartTime.Add(time.Second),
				Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: finalAnswer})},
			telemetry.AgentEvent{EventType: telemetry.EventExecutionComplete, AgentName: "Assistant",
				Timestamp: meta.StartTime.Add(time.Second),
				Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: finalAnswer})},
		)
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

func newHistoryTestStore(t *testing.T) *store.SessionStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.rakitsu/sessions
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	return ss
}

func wantHistory(t *testing.T, got []llm.Message, want ...llm.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("history length = %d, want %d — got %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].AsText() != want[i].AsText() {
			t.Errorf("history[%d] = {%s %q}, want {%s %q}",
				i, got[i].Role, got[i].AsText(), want[i].Role, want[i].AsText())
		}
	}
}

// The exact repro shape from the regression above: single-agent one-shot
// parent, no CHAT_TURN_* and no PIPELINE_START — the turn must be recovered
// from meta.Query plus the root-level final answer.
func TestHistoryForResumeOneShotSingleAgent(t *testing.T) {
	ss := newHistoryTestStore(t)
	persistOneShotFixture(t, "oneshot-1", "Reply with exactly the two words: HYBRID OK", "HYBRID OK")

	got, err := HistoryForResume(ss, "oneshot-1")
	if err != nil {
		t.Fatalf("HistoryForResume: %v", err)
	}
	wantHistory(t, got,
		llm.NewTextMessage("user", "Reply with exactly the two words: HYBRID OK"),
		llm.NewTextMessage("assistant", "HYBRID OK"),
	)
}

// Chat-style parent (TUI / web): CHAT_TURN_START/END pairs reconstruct into
// alternating user/assistant messages in turn order.
func TestHistoryForResumeChatTurnEvents(t *testing.T) {
	ss := newHistoryTestStore(t)
	persistJSONLFixture(t, "chat-hist-1")

	got, err := HistoryForResume(ss, "chat-hist-1")
	if err != nil {
		t.Fatalf("HistoryForResume: %v", err)
	}
	wantHistory(t, got,
		llm.NewTextMessage("user", "first question"),
		llm.NewTextMessage("assistant", "first answer"),
		llm.NewTextMessage("user", "second question"),
		llm.NewTextMessage("assistant", "second answer"),
	)
}

// Envelope parent: the .chat.json active path wins, dead branches excluded.
func TestHistoryForResumeEnvelope(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	persistForkFixture(t, m, "env-hist-1", cfgID)

	got, err := HistoryForResume(m.sessionStore, "env-hist-1")
	if err != nil {
		t.Fatalf("HistoryForResume: %v", err)
	}
	wantHistory(t, got,
		llm.NewTextMessage("user", "first question"),
		llm.NewTextMessage("assistant", "first answer"),
		llm.NewTextMessage("user", "second question"),
		llm.NewTextMessage("assistant", "second answer"),
	)
	for _, msg := range got {
		if strings.Contains(msg.AsText(), "wrong question") {
			t.Errorf("dead sibling leaked into history: %+v", msg)
		}
	}
}

// The shape `run --resume` itself writes: replayed CHAT_TURN pairs plus the
// run's own turn wrapped in CHAT_TURN events (with the usual AGENT_* events
// in between). Resuming a resumed session must replay the whole chain, not
// just its last hop.
func TestHistoryForResumeChainedOneShot(t *testing.T) {
	ss := newHistoryTestStore(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".rakitsu", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := store.SessionMeta{ID: "resumed-1", Query: "second question",
		StartTime: time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)}
	f, err := os.Create(filepath.Join(dir, "resumed-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(meta); err != nil {
		t.Fatal(err)
	}
	events := []telemetry.AgentEvent{
		{EventType: telemetry.EventChatTurnStart, Payload: mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "first question"})},
		{EventType: telemetry.EventChatTurnEnd, Payload: mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "first answer"})},
		{EventType: telemetry.EventChatTurnStart, Payload: mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 2, Text: "second question"})},
		{EventType: telemetry.EventAgentStart, AgentName: "Assistant", Payload: mustPayload(t, telemetry.AgentStartPayload{Role: "worker", Model: "test-model"})},
		{EventType: telemetry.EventAgentEnd, AgentName: "Assistant", Payload: mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: "second answer"})},
		{EventType: telemetry.EventChatTurnEnd, Payload: mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 2, Final: "second answer"})},
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := HistoryForResume(ss, "resumed-1")
	if err != nil {
		t.Fatalf("HistoryForResume: %v", err)
	}
	wantHistory(t, got,
		llm.NewTextMessage("user", "first question"),
		llm.NewTextMessage("assistant", "first answer"),
		llm.NewTextMessage("user", "second question"),
		llm.NewTextMessage("assistant", "second answer"),
	)
}

func TestHistoryForResumeUnknownSession(t *testing.T) {
	ss := newHistoryTestStore(t)
	if _, err := HistoryForResume(ss, "no-such-session"); err == nil {
		t.Fatal("expected error for unknown session, got nil")
	}
}

// A parent with a query but no answer yet must error, not hand back a
// half-turn — silent fresh context is the bug this guards against.
func TestHistoryForResumeNoAnswer(t *testing.T) {
	ss := newHistoryTestStore(t)
	persistOneShotFixture(t, "oneshot-empty", "some query", "")

	if _, err := HistoryForResume(ss, "oneshot-empty"); err == nil {
		t.Fatal("expected error for session with no final answer, got nil")
	}
}
