package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// collectEvents subscribes to bus and returns a snapshot getter + cleanup
// that unsubscribes the channel. Tests should call cleanup before reading
// the snapshot to ensure all in-flight events have been delivered.
func collectEvents(bus *telemetry.EventBus) (func() []telemetry.AgentEvent, func()) {
	ch := bus.Subscribe()
	var mu sync.Mutex
	var events []telemetry.AgentEvent
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()
	cleanup := func() {
		bus.Unsubscribe(ch)
		<-done
	}
	getter := func() []telemetry.AgentEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]telemetry.AgentEvent, len(events))
		copy(out, events)
		return out
	}
	return getter, cleanup
}

// decodeSalvagedPayload extracts the SalvagedOutputPayload from the raw event.
func decodeSalvagedPayload(t *testing.T, raw json.RawMessage) telemetry.SalvagedOutputPayload {
	t.Helper()
	var p telemetry.SalvagedOutputPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode SalvagedOutputPayload: %v", err)
	}
	return p
}

// decodeAgentEndPayload extracts the AgentEndPayload from the raw event.
func decodeAgentEndPayload(t *testing.T, raw json.RawMessage) telemetry.AgentEndPayload {
	t.Helper()
	var p telemetry.AgentEndPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode AgentEndPayload: %v", err)
	}
	return p
}

// TestAgent_B53_SalvagedRecoveryInjectsDirective verifies: when the LLM
// returns Salvaged=true (the format adapter promoted reasoning-only output
// to content because no committed answer could be extracted), the agent
// does NOT terminate as success. Instead it injects a recovery directive
// into history and continues the loop. The next iteration gets a chance to
// call a tool or commit.
//
// Repro context: a reasoning-model run that read two files, then produced
// several thousand characters of reasoning with no tool call. The format
// adapter promoted the reasoning as Content with the
// [REASONING-ONLY OUTPUT] marker prefix. Agent reported success. Zero
// files written.
func TestAgent_B53_SalvagedRecoveryInjectsDirective(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	provider := &recordingProvider{
		name:  "test",
		model: "test-model",
		scenario: []llm.GenerateResult{
			// Iter 0: salvaged — adapter fell through to marker prefix.
			{
				Response:     "[REASONING-ONLY OUTPUT — the model did not emit a content stream; …]\n\nWe need to think about this further. Hmm.",
				FinishReason: "length",
				Salvaged:     true,
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 100, TotalTokens: 150},
			},
			// Iter 1: model recovered with a committed answer.
			{
				Response:     "Done — the task is complete and here is the committed result.",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 60, OutputTokens: 20, TotalTokens: 80},
			},
		},
	}

	def := &config.AgentDefinition{Name: "TestAgent", Role: "worker", SystemPrompt: "test"}
	a := agent.NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)

	snapshot, cleanup := collectEvents(bus)
	defer cleanup()

	result, err := a.Run(context.Background(), "anything")
	cleanup()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "committed result") {
		t.Errorf("expected recovered committed answer, got: %q", result)
	}
	if strings.HasPrefix(result, "[REASONING-ONLY") {
		t.Errorf("recovered result should NOT keep the marker prefix, got: %q", result)
	}

	sawRecovery := false
	sawAgentEndSuccess := false
	for _, ev := range snapshot() {
		switch ev.EventType {
		case telemetry.EventSalvagedOutput:
			p := decodeSalvagedPayload(t, ev.Payload)
			if p.Recovery {
				sawRecovery = true
			}
		case telemetry.EventAgentEnd:
			p := decodeAgentEndPayload(t, ev.Payload)
			if p.Status == "success" {
				sawAgentEndSuccess = true
			}
		}
	}
	if !sawRecovery {
		t.Error("expected SALVAGED_OUTPUT event with Recovery=true on iter 0")
	}
	if !sawAgentEndSuccess {
		t.Error("expected AGENT_END status=success after recovery — iter 1 produced a committed answer")
	}
}

// TestAgent_B53_SalvagedExhaustsMaxIterEmitsNoProgress verifies that when
// every iteration produces a salvaged result, the loop runs to
// max_iterations and emits status="salvaged_no_progress" rather than
// silently reporting success. This is the case where the recovery
// directive doesn't help — the model is stuck.
func TestAgent_B53_SalvagedExhaustsMaxIterEmitsNoProgress(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	salvagedResp := llm.GenerateResult{
		Response:     "[REASONING-ONLY OUTPUT — …]\n\nstill thinking, no commitment",
		FinishReason: "length",
		Salvaged:     true,
		TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 100, TotalTokens: 150},
	}

	provider := &recordingProvider{
		name:  "test",
		model: "test-model",
		// Provide enough salvaged results to exhaust the default max_iterations.
		// Default for worker agents is small (single-digit) but we'll provide
		// enough headroom; the fallback in recordAndReply also returns a
		// non-salvaged "done" result, but we explicitly want to test the
		// salvaged-exhaust path so we provide many salvaged responses.
		scenario: []llm.GenerateResult{
			salvagedResp, salvagedResp, salvagedResp, salvagedResp, salvagedResp,
			salvagedResp, salvagedResp, salvagedResp, salvagedResp, salvagedResp,
			salvagedResp, salvagedResp, salvagedResp, salvagedResp, salvagedResp,
		},
	}

	def := &config.AgentDefinition{
		Name:         "TestAgent",
		Role:         "worker",
		SystemPrompt: "test",
		Settings:     &config.AgentSettings{MaxIterations: 3},
	}
	a := agent.NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)

	snapshot, cleanup := collectEvents(bus)
	defer cleanup()

	// Run with a short timeout — agent should exit on its own well before this.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := a.Run(ctx, "anything")
	cleanup()

	// Agent should NOT error out — it terminates gracefully with the
	// salvaged_no_progress status. (max-iterations exhaustion in this
	// adapter path is a legitimate exit, not an error.)
	if err != nil {
		t.Logf("Run returned error (acceptable in some configurations): %v", err)
	}

	sawTerminal := false
	sawNoProgressStatus := false
	for _, ev := range snapshot() {
		switch ev.EventType {
		case telemetry.EventSalvagedOutput:
			p := decodeSalvagedPayload(t, ev.Payload)
			if !p.Recovery {
				sawTerminal = true
			}
		case telemetry.EventAgentEnd:
			p := decodeAgentEndPayload(t, ev.Payload)
			if p.Status == "salvaged_no_progress" {
				sawNoProgressStatus = true
			}
			if p.Status == "success" {
				t.Errorf("AGENT_END status should NOT be 'success' when last iteration was salvaged, got: %s", p.Status)
			}
		}
	}
	if !sawTerminal {
		t.Error("expected SALVAGED_OUTPUT event with Recovery=false on the terminal iteration")
	}
	if !sawNoProgressStatus {
		t.Error("expected AGENT_END status=salvaged_no_progress after max_iterations exhausted with salvaged outputs")
	}
}

// TestAgent_B53_NonSalvagedResultStillReportsSuccess is the regression guard:
// when the LLM returns a normal (non-salvaged) response, AGENT_END status
// must remain "success". The Mitigation A code only activates on
// Salvaged=true.
func TestAgent_B53_NonSalvagedResultStillReportsSuccess(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	provider := &recordingProvider{
		name:  "test",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response:     "Normal committed answer.",
				FinishReason: "stop",
				// Salvaged defaults to false.
				TokenUsage: &llm.TokenUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15},
			},
		},
	}

	def := &config.AgentDefinition{Name: "TestAgent", Role: "worker", SystemPrompt: "test"}
	a := agent.NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)

	snapshot, cleanup := collectEvents(bus)
	defer cleanup()

	_, err := a.Run(context.Background(), "anything")
	cleanup()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range snapshot() {
		switch ev.EventType {
		case telemetry.EventSalvagedOutput:
			t.Error("non-salvaged response should NOT emit SALVAGED_OUTPUT")
		case telemetry.EventAgentEnd:
			p := decodeAgentEndPayload(t, ev.Payload)
			if p.Status != "success" {
				t.Errorf("non-salvaged result should keep status=success, got: %s", p.Status)
			}
		}
	}
}
