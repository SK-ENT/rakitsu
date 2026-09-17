package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// streamingMockProvider emits a programmed sequence of StreamChunks then a
// final GenerateResult. Used to verify the agent's stream-reader forwards
// reasoning_content chunks as EventReasoningChunk on the bus (POSITIONING-
// AUDIT.md Claim 1-2 fix).
type streamingMockProvider struct {
	chunks []llm.StreamChunk
	final  llm.GenerateResult
}

func (m *streamingMockProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	r := m.final
	return &r, nil
}

func (m *streamingMockProvider) GetName() string  { return "streaming-mock" }
func (m *streamingMockProvider) GetModel() string { return "mock-stream" }

func (m *streamingMockProvider) GenerateStream(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.StreamResult, error) {
	chunksCh := make(chan llm.StreamChunk, len(m.chunks)+1)
	finalCh := make(chan *llm.GenerateResult, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(chunksCh)
		defer close(finalCh)
		defer close(errCh)

		for _, c := range m.chunks {
			chunksCh <- c
		}
		chunksCh <- llm.StreamChunk{Done: true}

		r := m.final
		finalCh <- &r
	}()

	return &llm.StreamResult{Chunks: chunksCh, Final: finalCh, Err: errCh}, nil
}

// drainEvents collects all events from a subscription channel until the
// deadline passes. We don't know exactly how many events the agent emits, so
// we drain on a short timer and return what's accumulated.
func drainEvents(ch <-chan telemetry.AgentEvent, d time.Duration) []telemetry.AgentEvent {
	var events []telemetry.AgentEvent
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, evt)
		case <-deadline.C:
			return events
		}
	}
}

// TestAgent_StreamingForwardsReasoningChunk verifies the audit Claim 1-2 fix
// end-to-end through the agent layer: a StreamChunk{Reasoning} from the
// provider must surface as a REASONING_CHUNK event on the bus, separate from
// the TOKEN_CHUNK answer stream. Without this, the entire reasoning phase of
// a reasoning model is invisible to every UI consumer.
func TestAgent_StreamingForwardsReasoningChunk(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	provider := &streamingMockProvider{
		chunks: []llm.StreamChunk{
			{Reasoning: "Let me think..."},
			{Reasoning: " about this."},
			{Text: "The answer is 42."},
		},
		final: llm.GenerateResult{
			Response:     "The answer is 42.",
			FinishReason: "stop",
			TokenUsage:   &llm.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	}

	agent := newE2EAgent("reasoner", provider, bus)

	result, err := agent.generateWithStreaming(context.Background(), provider, "sys", nil, nil, 0)
	if err != nil {
		t.Fatalf("generateWithStreaming: %v", err)
	}
	if result.Response != "The answer is 42." {
		t.Errorf("Response = %q, want %q", result.Response, "The answer is 42.")
	}

	events := drainEvents(ch, 200*time.Millisecond)

	reasoning := filterEvents(events, telemetry.EventReasoningChunk)
	if len(reasoning) != 2 {
		t.Fatalf("EventReasoningChunk count = %d, want 2; all events: %v",
			len(reasoning), eventTypes(events))
	}

	answers := filterEvents(events, telemetry.EventTokenChunk)
	if len(answers) != 1 {
		t.Fatalf("EventTokenChunk count = %d, want 1; all events: %v",
			len(answers), eventTypes(events))
	}

	// Payload content must round-trip cleanly: the visible-answer event must
	// carry the answer text and the reasoning events must carry reasoning,
	// not the other way around (catches a swapped-field regression).
	var rp telemetry.ReasoningChunkPayload
	if err := json.Unmarshal(reasoning[0].Payload, &rp); err != nil {
		t.Fatalf("unmarshal reasoning payload: %v", err)
	}
	if rp.Text != "Let me think..." {
		t.Errorf("first reasoning payload Text = %q, want %q", rp.Text, "Let me think...")
	}
	if rp.AgentName != "reasoner" {
		t.Errorf("reasoning payload AgentName = %q, want reasoner", rp.AgentName)
	}

	var tp telemetry.TokenChunkPayload
	if err := json.Unmarshal(answers[0].Payload, &tp); err != nil {
		t.Fatalf("unmarshal token payload: %v", err)
	}
	if tp.Text != "The answer is 42." {
		t.Errorf("token payload Text = %q, want answer text", tp.Text)
	}
}

// TestAgent_StreamingEmptyReasoning_NoEvent verifies the !="" gate. Some
// providers emit empty reasoning deltas during keepalives; those must not
// produce REASONING_CHUNK events (otherwise the bus floods with empty payloads
// and downstream consumers can't count salvaged occurrences accurately).
func TestAgent_StreamingEmptyReasoning_NoEvent(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	provider := &streamingMockProvider{
		chunks: []llm.StreamChunk{
			{Reasoning: ""},
			{Text: ""},
			{Text: "hi"},
		},
		final: llm.GenerateResult{Response: "hi", FinishReason: "stop"},
	}
	agent := newE2EAgent("reasoner", provider, bus)

	if _, err := agent.generateWithStreaming(context.Background(), provider, "sys", nil, nil, 0); err != nil {
		t.Fatalf("generateWithStreaming: %v", err)
	}

	events := drainEvents(ch, 200*time.Millisecond)
	if n := len(filterEvents(events, telemetry.EventReasoningChunk)); n != 0 {
		t.Errorf("empty reasoning chunks must not emit events; got %d", n)
	}
	if n := len(filterEvents(events, telemetry.EventTokenChunk)); n != 1 {
		t.Errorf("expected exactly one non-empty answer chunk event; got %d", n)
	}
}
