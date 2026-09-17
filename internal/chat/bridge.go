package chat

import (
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// StartBridge subscribes to the event bus and converts AgentEvents
// into bubbletea messages on the returned channel.
// The caller should use waitForEvent() to drain the channel.
//
// stop ends the bridge: closing it unsubscribes from eventBus and returns
// the goroutine, which in turn closes the returned channel. EventBus has no
// bus-wide shutdown (only per-channel Unsubscribe), so without a stop
// signal this goroutine would run for the life of the process regardless of
// whether the TUI has quit — the caller (Model.Close, called after
// tea.Program.Run returns) owns closing it exactly once.
func StartBridge(eventBus *telemetry.EventBus, stop <-chan struct{}) <-chan tea.Msg {
	eventCh := eventBus.Subscribe()
	msgCh := make(chan tea.Msg, 64)

	go func() {
		defer close(msgCh)
		defer eventBus.Unsubscribe(eventCh)
		for {
			select {
			case <-stop:
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}
				msg := convertEvent(event)
				if msg != nil {
					msgCh <- msg
				}
			}
		}
	}()

	return msgCh
}

// directed reports whether the event was produced by a directed agent chat
// turn. Roster.Send runs the rebuilt agent on an origin-tagged view of the
// same bus, so this is a fact about the event's source rather than a guess
// from its agent name — see telemetry.AgentEvent.Origin.
func directed(event telemetry.AgentEvent) bool { return event.Origin != "" }

func convertEvent(event telemetry.AgentEvent) tea.Msg {
	switch event.EventType {
	case telemetry.EventTokenChunk:
		var p telemetry.TokenChunkPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		return TokenChunkMsg{Text: p.Text, AgentName: p.AgentName, Directed: directed(event)}

	case telemetry.EventReasoningChunk:
		var p telemetry.ReasoningChunkPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		return ReasoningChunkMsg{Text: p.Text, AgentName: p.AgentName, Directed: directed(event)}

	case telemetry.EventTokenUsage:
		if event.TokenUsage == nil {
			return nil
		}
		return TokenUsageMsg{
			Input:  event.TokenUsage.InputTokens,
			Output: event.TokenUsage.OutputTokens,
			Total:  event.TokenUsage.TotalTokens,
		}

	case telemetry.EventToolCallStart:
		var p telemetry.ToolCallStartPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		argsJSON, _ := json.Marshal(p.Arguments)
		return ToolCallStartMsg{
			ID:        p.ToolCallID,
			Name:      p.ToolName,
			Args:      string(argsJSON),
			AgentName: event.AgentName,
			Directed:  directed(event),
		}

	case telemetry.EventAgentStart:
		var p telemetry.AgentStartPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		return AgentStartMsg{Name: event.AgentName, Parent: p.ParentAgent, Model: p.Model, Provider: p.Provider, Directed: directed(event)}

	case telemetry.EventAgentEnd:
		var p telemetry.AgentEndPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		return AgentEndMsg{
			Name:         event.AgentName,
			Status:       p.Status,
			Tokens:       p.TotalTokens,
			Duration:     event.Duration,
			Cost:         p.TotalCost,
			MaxTokens:    p.MaxTokens,
			MaxCost:      p.MaxCost,
			Iterations:   p.Iterations,
			PricingKnown: p.PricingKnown,
			Directed:     directed(event),
		}

	case telemetry.EventToolCallEnd:
		var p telemetry.ToolCallEndPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil
		}
		return ToolCallEndMsg{
			ID:        p.ToolCallID,
			Name:      p.ToolName,
			Output:    p.Output,
			Error:     p.Error,
			Duration:  event.Duration,
			AgentName: event.AgentName,
			Directed:  directed(event),
		}

	default:
		return nil
	}
}
