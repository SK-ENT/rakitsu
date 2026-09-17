package chat

import (
	"encoding/json"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestStartBridge_StopsOnSignal regression-guards: StartBridge subscribed to
// the event bus but returned only the derived <-chan tea.Msg — the
// underlying eventCh was never exposed to the caller, so nothing outside
// this function could ever call eventBus.Unsubscribe(eventCh). Since
// EventBus has no bus-wide Close(), the goroutine's range loop had no way to
// ever return; it ran for the life of the process regardless of whether the
// TUI had quit. A stop channel closes the loop: closing stop must make the
// returned msgCh close in turn (its own deferred close(msgCh) firing once
// the goroutine returns).
func TestStartBridge_StopsOnSignal(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	stop := make(chan struct{})
	msgCh := StartBridge(bus, stop)

	close(stop)

	select {
	case _, ok := <-msgCh:
		if ok {
			t.Fatal("expected msgCh to close on stop, got a message instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartBridge did not stop after the stop channel closed — goroutine leaked")
	}
}

func mkEvent(t *testing.T, agentName string, et telemetry.EventType, payload interface{}) telemetry.AgentEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return telemetry.AgentEvent{
		AgentName: agentName,
		EventType: et,
		Payload:   raw,
	}
}

// TestConvertEvent_ReasoningChunk verifies the bridge maps REASONING_CHUNK
// events into a ReasoningChunkMsg. Before the fix this event had no case in
// convertEvent, so reasoning-model CoT was invisible in the chat TUI.
func TestConvertEvent_ReasoningChunk(t *testing.T) {
	ev := mkEvent(t, "Researcher", telemetry.EventReasoningChunk,
		telemetry.ReasoningChunkPayload{Text: "let me think", AgentName: "Researcher"})

	msg := convertEvent(ev)
	rc, ok := msg.(ReasoningChunkMsg)
	if !ok {
		t.Fatalf("expected ReasoningChunkMsg, got %T", msg)
	}
	if rc.Text != "let me think" {
		t.Errorf("reasoning text = %q, want %q", rc.Text, "let me think")
	}
	if rc.AgentName != "Researcher" {
		t.Errorf("reasoning agent = %q, want %q", rc.AgentName, "Researcher")
	}
}

// TestConvertEvent_ToolCallStartCarriesAgentName verifies the emitting
// agent name reaches the TUI so tool calls can be nested under it.
func TestConvertEvent_ToolCallStartCarriesAgentName(t *testing.T) {
	ev := mkEvent(t, "Researcher", telemetry.EventToolCallStart,
		telemetry.ToolCallStartPayload{ToolCallID: "tc1", ToolName: "search"})

	msg := convertEvent(ev)
	tc, ok := msg.(ToolCallStartMsg)
	if !ok {
		t.Fatalf("expected ToolCallStartMsg, got %T", msg)
	}
	if tc.AgentName != "Researcher" {
		t.Errorf("tool-call agent = %q, want %q (nesting signal lost)", tc.AgentName, "Researcher")
	}
}

func TestConvertEvent_AgentEndCarriesCostAndBudget(t *testing.T) {
	payload := telemetry.AgentEndPayload{
		Status:       "success",
		Iterations:   3,
		TotalTokens:  500,
		TotalCost:    0.0123,
		MaxTokens:    1000,
		MaxCost:      1.0,
		PricingKnown: true,
	}
	raw, _ := json.Marshal(payload)
	event := telemetry.AgentEvent{
		AgentName: "Researcher",
		EventType: telemetry.EventAgentEnd,
		Payload:   raw,
	}
	msg := convertEvent(event)
	end, ok := msg.(AgentEndMsg)
	if !ok {
		t.Fatalf("convertEvent returned %T, want AgentEndMsg", msg)
	}
	if end.Cost != 0.0123 || end.MaxTokens != 1000 || end.MaxCost != 1.0 || end.Iterations != 3 || !end.PricingKnown {
		t.Errorf("AgentEndMsg = %+v, want cost/budget/iterations/pricingKnown carried through", end)
	}
}

func TestConvertEvent_AgentStartCarriesProvider(t *testing.T) {
	payload := telemetry.AgentStartPayload{Role: "worker", Provider: "litellm", Model: "gemini-3.5-flash-lite"}
	raw, _ := json.Marshal(payload)
	event := telemetry.AgentEvent{AgentName: "Coordinator", EventType: telemetry.EventAgentStart, Payload: raw}
	msg := convertEvent(event)
	start, ok := msg.(AgentStartMsg)
	if !ok {
		t.Fatalf("convertEvent returned %T, want AgentStartMsg", msg)
	}
	if start.Provider != "litellm" {
		t.Errorf("Provider = %q, want litellm", start.Provider)
	}
}

// TestConvertEvent_CarriesOriginAsDirected pins the other end of the origin
// substrate: every message the main transcript renders has to be able to say
// whether it came from a directed agent chat, or the Update handlers are back
// to guessing from the agent name. One case per guarded message type, because
// a type that forgets the field is silently unfilterable.
func TestConvertEvent_CarriesOriginAsDirected(t *testing.T) {
	cases := []struct {
		name     string
		event    telemetry.AgentEvent
		directed func(tea.Msg) (bool, bool)
	}{
		{"TOKEN_CHUNK", mkEvent(t, "Reviewer", telemetry.EventTokenChunk,
			telemetry.TokenChunkPayload{Text: "x", AgentName: "Reviewer"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(TokenChunkMsg); return v.Directed, ok }},
		{"REASONING_CHUNK", mkEvent(t, "Reviewer", telemetry.EventReasoningChunk,
			telemetry.ReasoningChunkPayload{Text: "x", AgentName: "Reviewer"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(ReasoningChunkMsg); return v.Directed, ok }},
		{"TOOL_CALL_START", mkEvent(t, "Reviewer", telemetry.EventToolCallStart,
			telemetry.ToolCallStartPayload{ToolCallID: "call_1", ToolName: "fs_read"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(ToolCallStartMsg); return v.Directed, ok }},
		{"TOOL_CALL_END", mkEvent(t, "Reviewer", telemetry.EventToolCallEnd,
			telemetry.ToolCallEndPayload{ToolCallID: "call_1", ToolName: "fs_read"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(ToolCallEndMsg); return v.Directed, ok }},
		{"AGENT_START", mkEvent(t, "Reviewer", telemetry.EventAgentStart,
			telemetry.AgentStartPayload{ParentAgent: "Coordinator"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(AgentStartMsg); return v.Directed, ok }},
		{"AGENT_END", mkEvent(t, "Reviewer", telemetry.EventAgentEnd,
			telemetry.AgentEndPayload{Status: "success"}),
			func(m tea.Msg) (bool, bool) { v, ok := m.(AgentEndMsg); return v.Directed, ok }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			main, ok := tc.directed(convertEvent(tc.event))
			if !ok {
				t.Fatalf("%s did not convert", tc.name)
			}
			if main {
				t.Errorf("an untagged %s reported Directed — the main run's own rows would be dropped", tc.name)
			}

			tagged := tc.event
			tagged.Origin = "direct-Reviewer-1"
			side, ok := tc.directed(convertEvent(tagged))
			if !ok {
				t.Fatalf("%s did not convert", tc.name)
			}
			if !side {
				t.Errorf("an origin-tagged %s did not report Directed — a side chat's rows would land in the main transcript", tc.name)
			}
		})
	}
}
