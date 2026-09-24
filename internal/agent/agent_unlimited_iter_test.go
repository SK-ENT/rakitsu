package agent

// max_iterations < 0 means "no iteration cap" — the agent keeps
// looping until the model answers (or ctx / a budget guard stops it).

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestNewAgent_NegativeMaxIterationsIsUnlimited(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	ag := newE2EAgentWithSettings("u", newSequenceProvider(), bus, &config.AgentSettings{MaxIterations: -1})
	if ag.maxIterations != 0 {
		t.Fatalf("maxIterations = %d, want 0 (unlimited)", ag.maxIterations)
	}
	if def := newE2EAgent("d", newSequenceProvider(), bus); def.maxIterations != 10 {
		t.Fatalf("unset max_iterations = %d, want the default 10", def.maxIterations)
	}
}

// TestAgent_UnlimitedIterations_RunsPastDefaultCap: 15 tool-call rounds (more
// than the default 10) and then a final answer. With no cap the run must
// finish on the model's answer — no max_iterations marker, no forced
// synthesis, and no convergence nudge (there is no budget to converge on).
func TestAgent_UnlimitedIterations_RunsPastDefaultCap(t *testing.T) {
	bus := telemetry.NewEventBus(256)
	tool := newMockTool("noop", "ok")
	resp := toolCallResponse("working", tc("noop", map[string]interface{}{"query": "x"}))
	seq := make([]llm.GenerateResult, 0, 16)
	for i := 0; i < 15; i++ {
		seq = append(seq, resp)
	}
	seq = append(seq, stopResponse("all done"))
	provider := newSequenceProvider(seq...)
	ag := newE2EAgent("unlimited", provider, bus, tool)
	ag.maxIterations = 0

	out, err := ag.Run(context.Background(), "long task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "all done" {
		t.Fatalf("output = %q, want the model's final answer", out)
	}
	if provider.callCount() != 16 {
		t.Fatalf("expected 16 LLM calls, got %d", provider.callCount())
	}
	for i := 0; i < provider.callCount(); i++ {
		for _, m := range provider.getCall(i).History {
			if strings.Contains(m.AsText(), "Iteration budget") {
				t.Fatalf("call %d carries a convergence nudge, want none when unlimited", i)
			}
		}
	}
}

// TestAgent_UnlimitedIterations_StopsOnContextCancel: the loop must still end
// when the caller cancels (Ctrl+C / timeout), not spin forever.
func TestAgent_UnlimitedIterations_StopsOnContextCancel(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := newSequenceProvider(toolCallResponse("working", tc("noop", map[string]interface{}{"query": "x"})))
	ag := newE2EAgent("cancelled", provider, bus, tool)
	ag.maxIterations = 0

	if _, err := ag.Run(ctx, "long task"); err == nil {
		t.Fatal("expected an error from a cancelled context, got nil")
	}
}
