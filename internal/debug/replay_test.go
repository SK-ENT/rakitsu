package debug

import (
	"encoding/json"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestReconstructHistory_ToolCallIDRoundTrips regression-guards finding 24:
// the assistant message's reconstructed llm.ToolCall never carried an ID
// (ToolCallSignature had no ID field, and convertToolCalls never populated
// one), so the following tool-role message's ToolCallID had nothing
// matching in the preceding assistant turn's tool_calls — providers that
// require tool_call_id to match an id from that same assistant turn
// (OpenAI, Anthropic) would reject or mishandle a replayed multi-tool-call
// conversation reconstructed this way.
func TestReconstructHistory_ToolCallIDRoundTrips(t *testing.T) {
	events := []telemetry.AgentEvent{
		{
			EventType: telemetry.EventThoughtEnd,
			Iteration: 1,
			Payload: mustMarshal(t, telemetry.ThoughtEndPayload{
				StructuredThought: telemetry.StructuredThought{
					Reasoning: "I'll read the file.",
					IntendedToolCalls: []telemetry.ToolCallSignature{
						{ID: "call_abc123", Name: "read_file", Arguments: map[string]interface{}{"path": "x.go"}},
					},
				},
				Iteration: 1,
			}),
		},
		{
			EventType: telemetry.EventToolCallEnd,
			Iteration: 1,
			Payload: mustMarshal(t, telemetry.ToolCallEndPayload{
				ToolCallID: "call_abc123",
				ToolName:   "read_file",
				Output:     "file contents",
			}),
		},
	}

	history, _, _, _ := ReconstructHistory(events, len(events)-1)

	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2 (assistant + tool)", len(history))
	}

	assistant := history[0]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant.ToolCalls len = %d, want 1", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_abc123" {
		t.Errorf("assistant.ToolCalls[0].ID = %q, want %q", assistant.ToolCalls[0].ID, "call_abc123")
	}

	toolMsg := history[1]
	if toolMsg.ToolCallID != assistant.ToolCalls[0].ID {
		t.Errorf("tool message ToolCallID = %q does not match preceding assistant tool_call id %q",
			toolMsg.ToolCallID, assistant.ToolCalls[0].ID)
	}
}
