package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// reconstructFixtureEvents returns a minimal event stream with one successful turn.
func reconstructFixtureEvents(t *testing.T) []telemetry.AgentEvent {
	t.Helper()
	t0 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	return []telemetry.AgentEvent{
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "hello"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(2 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "Hi there!"}),
		},
	}
}

// Wake events in a session JSONL must not break tree reconstruction or add turns.
func TestReconstructTreeIgnoresWakeEvents(t *testing.T) {
	// Build the event stream the same way the existing reconstruct tests do:
	// grep internal/server/chat_reconstruct_test.go for reconstructTreeFromEvents and
	// reuse its helper that makes a user-prompt event and a final-answer event.
	ev := func(typ telemetry.EventType, payload any) telemetry.AgentEvent {
		b, _ := json.Marshal(payload)
		return telemetry.AgentEvent{EventType: typ, Timestamp: time.Unix(1, 0), Payload: b}
	}
	wakeTick := ev(telemetry.EventWakeTick, telemetry.WakeTickPayload{Tick: 1, Outcome: "alarm"})
	wakeEsc := ev(telemetry.EventWakeEscalate, telemetry.WakeEscalatePayload{Tick: 1, Reason: "alarm", Level: "L2"})

	base := reconstructFixtureEvents(t)
	withWake := append(append([]telemetry.AgentEvent{}, base[:1]...), append([]telemetry.AgentEvent{wakeTick, wakeEsc}, base[1:]...)...)

	a := reconstructTreeFromEvents(base)
	b := reconstructTreeFromEvents(withWake)
	if (a == nil) != (b == nil) {
		t.Fatalf("wake events changed reconstruction success: %v vs %v", a == nil, b == nil)
	}
	if a != nil && len(a.ActivePath()) != len(b.ActivePath()) {
		t.Fatalf("wake events must not add or drop turns: %d vs %d", len(a.ActivePath()), len(b.ActivePath()))
	}
}
