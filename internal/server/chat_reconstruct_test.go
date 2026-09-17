package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

func mustPayload(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return json.RawMessage(b)
}

func TestReconstructTreeFromEvents_EmptyOrNil(t *testing.T) {
	if got := reconstructTreeFromEvents(nil); got != nil {
		t.Errorf("nil events: want nil, got %v nodes", len(got.Nodes))
	}
	if got := reconstructTreeFromEvents([]telemetry.AgentEvent{}); got != nil {
		t.Errorf("empty events: want nil, got %v nodes", len(got.Nodes))
	}
}

// TestReconstructTreeFromEvents_Modern covers the CHAT_TURN_START/END path.
// Two turns: first completes cleanly, second is interrupted (no end event)
// to verify the "trailing turn marked interrupted" salvage.
func TestReconstructTreeFromEvents_Modern(t *testing.T) {
	t0 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	events := []telemetry.AgentEvent{
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
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0.Add(3 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 2, Text: "second"}),
		},
		// No CHAT_TURN_END for turn 2 — server crashed mid-stream.
	}
	tree := reconstructTreeFromEvents(events)
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if got, want := len(tree.Nodes), 2; got != want {
		t.Fatalf("node count: got %d, want %d", got, want)
	}

	// Both nodes should be on the active path in insertion order.
	path := tree.ActivePath()
	if len(path) != 2 {
		t.Fatalf("active path length: got %d, want 2", len(path))
	}
	if got, want := path[0].UserText, "hello"; got != want {
		t.Errorf("turn 1 UserText: got %q, want %q", got, want)
	}
	if got, want := path[0].Status, turntree.StatusComplete; got != want {
		t.Errorf("turn 1 Status: got %v, want %v", got, want)
	}
	if len(path[0].Entries) != 1 || path[0].Entries[0].Text != "Hi there!" {
		t.Errorf("turn 1 assistant entry missing or wrong: %+v", path[0].Entries)
	}
	if got, want := path[1].UserText, "second"; got != want {
		t.Errorf("turn 2 UserText: got %q, want %q", got, want)
	}
	if got, want := path[1].Status, turntree.StatusInterrupted; got != want {
		t.Errorf("turn 2 Status (no CHAT_TURN_END): got %v, want %v", got, want)
	}
	if len(path[1].Entries) != 0 {
		t.Errorf("turn 2 should have no entries: %+v", path[1].Entries)
	}
}

// TestReconstructTreeFromEvents_ModernErrorNoFinal — regression:
// a turn that fails with NO partial text (empty Final, non-empty Err) must
// still synthesize an assistant entry carrying the error, not silently
// produce zero entries. Covers the crash/restart recovery path.
func TestReconstructTreeFromEvents_ModernErrorNoFinal(t *testing.T) {
	t0 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	events := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "hello"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(1 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "", Interrupted: true, Err: "boom"}),
		},
	}
	tree := reconstructTreeFromEvents(events)
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	path := tree.ActivePath()
	if len(path) != 1 {
		t.Fatalf("active path length: got %d, want 1", len(path))
	}
	if got, want := path[0].Status, turntree.StatusInterrupted; got != want {
		t.Errorf("Status: got %v, want %v", got, want)
	}
	if len(path[0].Entries) != 1 {
		t.Fatalf("expected one assistant entry even with empty Final, got %d: %+v", len(path[0].Entries), path[0].Entries)
	}
	if got, want := path[0].Entries[0].Err, "boom"; got != want {
		t.Errorf("assistant entry Err: got %q, want %q", got, want)
	}
	if !path[0].Entries[0].Interrupted {
		t.Errorf("assistant entry Interrupted = false, want true")
	}
}

// TestReconstructTreeFromEvents_Legacy covers the PIPELINE_START / AGENT_END
// fallback for legacy sessions predating CHAT_TURN_* events.
func TestReconstructTreeFromEvents_Legacy(t *testing.T) {
	t0 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	events := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventPipelineStart,
			AgentName: "Orchestrator",
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.PipelineStartPayload{StepCount: 1, Query: "audit this repo"}),
		},
		{
			// Nested AGENT_END — should be ignored (ParentID != "").
			EventType: telemetry.EventAgentEnd,
			AgentName: "Worker",
			ParentID:  "some-parent",
			Timestamp: t0.Add(1 * time.Second),
			Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: "worker partial"}),
		},
		{
			EventType: telemetry.EventExecutionComplete,
			AgentName: "Orchestrator",
			Timestamp: t0.Add(2 * time.Second),
			Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: "Done."}),
		},
	}
	tree := reconstructTreeFromEvents(events)
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if got, want := len(tree.Nodes), 1; got != want {
		t.Fatalf("node count: got %d, want %d", got, want)
	}
	path := tree.ActivePath()
	if got, want := path[0].UserText, "audit this repo"; got != want {
		t.Errorf("UserText: got %q, want %q", got, want)
	}
	if len(path[0].Entries) != 1 {
		t.Fatalf("expected exactly one assistant entry, got %d", len(path[0].Entries))
	}
	if got, want := path[0].Entries[0].Text, "Done."; got != want {
		t.Errorf("assistant text: got %q, want %q", got, want)
	}
	if got, want := path[0].Status, turntree.StatusComplete; got != want {
		t.Errorf("Status: got %v, want %v", got, want)
	}
}

// TestReconstructTreeFromEvents_PipelineNoiseInsideModernTurnIgnored
// verifies that a PIPELINE_START/AGENT_END pair belonging to the SAME
// modern turn's Pipeline-strategy execution doesn't produce a duplicate
// node. This replaces a previous version of this test
// (TestReconstructTreeFromEvents_ModernPreferredOverLegacy) that put the
// PIPELINE_START BEFORE the CHAT_TURN_START in the fixture — an ordering
// that can't actually occur: chat_session.go's executeTurn always emits
// CHAT_TURN_START before invoking the runner, so a real Pipeline-strategy
// turn's own PIPELINE_START is always nested INSIDE its CHAT_TURN_START/END
// pair, never before it. The realistic ordering below is what the fix for
// the mixed-history bug (see the sibling test just below) actually needs to
// get right: PIPELINE_START/AGENT_END while a modern turn is open must be
// ignored as pipeline-internal noise, not mistaken for a legacy turn.
func TestReconstructTreeFromEvents_PipelineNoiseInsideModernTurnIgnored(t *testing.T) {
	t0 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	events := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "modern-text"}),
		},
		{
			// Pipeline-strategy's own internal telemetry for THIS turn —
			// must not produce its own node.
			EventType: telemetry.EventPipelineStart,
			Timestamp: t0.Add(500 * time.Millisecond),
			Payload:   mustPayload(t, telemetry.PipelineStartPayload{Query: "pipeline-internal-noise"}),
		},
		{
			EventType: telemetry.EventAgentEnd,
			Timestamp: t0.Add(1 * time.Second),
			Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: "step output, not the turn's answer"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(2 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "reply"}),
		},
	}
	tree := reconstructTreeFromEvents(events)
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if got, want := len(tree.Nodes), 1; got != want {
		t.Fatalf("node count: got %d, want %d (nested PipelineStart/AgentEnd should not contribute their own node)", got, want)
	}
	if got, want := tree.ActivePath()[0].UserText, "modern-text"; got != want {
		t.Errorf("UserText: got %q, want %q", got, want)
	}
	if got, want := tree.ActivePath()[0].Entries[0].Text, "reply"; got != want {
		t.Errorf("assistant text: got %q, want %q (CHAT_TURN_END's Final, not the nested AgentEnd's FinalAnswer)", got, want)
	}
}

// TestReconstructTreeFromEvents_LegacyTurnsBeforeModernCutoverPreserved
// regression-guards: hasModern used to route the ENTIRE event stream to
// reconstructTreeModern the moment any CHAT_TURN_START appeared anywhere in
// it — reconstructTreeLegacy never ran at all, even partially. A session
// that's been continuously active since before the 2026-04-18 CHAT_TURN_*
// cutover has genuine legacy-format turns (PIPELINE_START/root AGENT_END,
// fully closed, no CHAT_TURN_START wrapping them at all) preceding the
// modern ones in the same log — those were silently dropped on
// reconstruction instead of being recovered and prepended.
func TestReconstructTreeFromEvents_LegacyTurnsBeforeModernCutoverPreserved(t *testing.T) {
	t0 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC) // before the 04-18 cutover
	events := []telemetry.AgentEvent{
		// A genuine legacy turn, fully closed, well before any CHAT_TURN_*
		// event appears anywhere in the log.
		{
			EventType: telemetry.EventPipelineStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.PipelineStartPayload{Query: "legacy turn from before the cutover"}),
		},
		{
			EventType: telemetry.EventAgentEnd,
			Timestamp: t0.Add(1 * time.Second),
			Payload:   mustPayload(t, telemetry.AgentEndPayload{Status: "success", FinalAnswer: "legacy answer"}),
		},
		// The session keeps going after the format switch.
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0.Add(30 * 24 * time.Hour),
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "modern turn after the cutover"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(30*24*time.Hour + time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "modern answer"}),
		},
	}
	tree := reconstructTreeFromEvents(events)
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	path := tree.ActivePath()
	if len(path) != 2 {
		t.Fatalf("active path length: got %d, want 2 (the legacy turn was dropped): %+v", len(path), path)
	}
	if got, want := path[0].UserText, "legacy turn from before the cutover"; got != want {
		t.Errorf("path[0].UserText: got %q, want %q", got, want)
	}
	if got, want := path[0].Entries[0].Text, "legacy answer"; got != want {
		t.Errorf("path[0] assistant text: got %q, want %q", got, want)
	}
	if got, want := path[1].UserText, "modern turn after the cutover"; got != want {
		t.Errorf("path[1].UserText: got %q, want %q", got, want)
	}
	if got, want := path[1].Entries[0].Text, "modern answer"; got != want {
		t.Errorf("path[1] assistant text: got %q, want %q", got, want)
	}
}

// TestReconstructTreeFromEvents_DeterministicIDs pins the contract that two
// reconstructions of the same event stream yield identical node IDs. The
// /tree and /turn/{id} REST handlers each rebuild independently when no
// .chat.json exists — random per-rebuild IDs made every /turn lookup 404
// (Sessions tab Tree sub-tab: selecting a node failed on reconstructed
// sessions).
func TestReconstructTreeFromEvents_DeterministicIDs(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	modern := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "first"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "one"}),
		},
		{
			EventType: telemetry.EventChatTurnStart,
			Timestamp: t0.Add(2 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 2, Text: "second"}),
		},
		{
			EventType: telemetry.EventChatTurnEnd,
			Timestamp: t0.Add(3 * time.Second),
			Payload:   mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 2, Final: "two"}),
		},
	}
	legacy := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventPipelineStart,
			Timestamp: t0,
			Payload:   mustPayload(t, telemetry.PipelineStartPayload{Query: "q"}),
		},
		{
			EventType: telemetry.EventAgentEnd,
			Timestamp: t0.Add(time.Second),
			Payload:   mustPayload(t, telemetry.AgentEndPayload{FinalAnswer: "a", Status: "completed"}),
		},
	}
	for name, events := range map[string][]telemetry.AgentEvent{"modern": modern, "legacy": legacy} {
		a := reconstructTreeFromEvents(events)
		b := reconstructTreeFromEvents(events)
		if a == nil || b == nil {
			t.Fatalf("%s: expected non-nil trees", name)
		}
		pa, pb := a.ActivePath(), b.ActivePath()
		if len(pa) != len(pb) {
			t.Fatalf("%s: path length mismatch: %d vs %d", name, len(pa), len(pb))
		}
		for i := range pa {
			if pa[i].ID != pb[i].ID {
				t.Errorf("%s: turn %d ID not stable across rebuilds: %q vs %q", name, i, pa[i].ID, pb[i].ID)
			}
			if _, ok := b.Get(pa[i].ID); !ok {
				t.Errorf("%s: ID %q from first rebuild not found in second rebuild", name, pa[i].ID)
			}
		}
	}
}
