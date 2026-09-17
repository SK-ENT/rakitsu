package debug

import (
	"encoding/json"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// ReconstructHistory rebuilds a conversation history from stored agent events
// up to the given event index. This enables replay from any iteration.
// Returns the reconstructed message history, the iteration number at that point,
// the agent name, and the original query.
func ReconstructHistory(events []telemetry.AgentEvent, upToIndex int) ([]llm.Message, int, string, string) {
	var history []llm.Message
	var agentName string
	var query string
	iteration := 0

	for i := 0; i <= upToIndex && i < len(events); i++ {
		ev := events[i]

		switch ev.EventType {
		case telemetry.EventAgentStart:
			agentName = ev.AgentName
			// The query is typically the first user message added after AGENT_START.
			// We extract it from the payload if available.
			var payload telemetry.AgentStartPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				// Agent start doesn't contain query directly;
				// query comes from PIPELINE_START or is set externally.
			}

		case telemetry.EventPipelineStart:
			var payload telemetry.PipelineStartPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				query = payload.Query
			}

		case telemetry.EventThoughtStart:
			iteration = ev.Iteration

		case telemetry.EventThoughtEnd:
			var payload telemetry.ThoughtEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				msg := llm.NewTextMessage("assistant", payload.Reasoning)
				// Convert intended tool calls to LLM tool calls
				if len(payload.IntendedToolCalls) > 0 {
					for _, tc := range payload.IntendedToolCalls {
						msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
							ID:        tc.ID,
							Name:      tc.Name,
							Arguments: tc.Arguments,
						})
					}
				}
				history = append(history, msg)
			}

		case telemetry.EventToolCallEnd:
			var payload telemetry.ToolCallEndPayload
			if err := json.Unmarshal(ev.Payload, &payload); err == nil {
				history = append(history, llm.Message{
					Role:       "tool",
					Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: payload.Output}},
					ToolCallID: payload.ToolCallID,
					Name:       payload.ToolName,
				})
			}
		}
	}

	return history, iteration, agentName, query
}
