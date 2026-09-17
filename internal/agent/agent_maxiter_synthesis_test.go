package agent

// Regression tests: a ReAct agent that burned its entire
// max_iterations budget on tool calls used to return
// "[max_iterations reached — no response produced]" even when the tool
// results it had already gathered were enough to answer. The fix is
// two-part:
//  1. a one-time convergence nudge injected into history when only 2
//     iterations remain, telling the model to stop exploring and answer;
//  2. a forced no-tools synthesis call at the max_iterations exit when the
//     loop never committed any response text, so the run returns an answer
//     grounded in the gathered context instead of nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestAgent_MaxIterations_ForcedSynthesis_ProducesAnswer is the core
// repro: every loop iteration is a bare tool call (Response=""), the budget
// runs out, and the forced synthesis pass turns the gathered context into a
// real answer. The run must NOT be flagged unproductive — there is usable
// output now.
func TestAgent_MaxIterations_ForcedSynthesis_ProducesAnswer(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("fs_read", "ROADMAP.md contents")
	resp := toolCallResponse("", tc("fs_read", map[string]interface{}{"query": "docs"}))
	synth := stopResponse("The docs describe a Go+Vue agentic system.")
	provider := newSequenceProvider(resp, resp, synth)
	ag := newE2EAgent("synth", provider, bus, tool)
	ag.maxIterations = 2

	out, err := ag.Run(context.Background(), "analyze the docs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "synthesized answer") {
		t.Errorf("output = %q, want the 'synthesized answer' marker", out)
	}
	if !strings.Contains(out, "The docs describe a Go+Vue agentic system.") {
		t.Errorf("output = %q, want it to carry the synthesized body", out)
	}
	if ag.LastRunUnproductive() {
		t.Error("LastRunUnproductive() = true, want false — synthesis produced usable output")
	}
	if provider.callCount() != 3 {
		t.Fatalf("expected 3 LLM calls (2 loop + 1 synthesis), got %d", provider.callCount())
	}

	// The synthesis call must offer no tools and end on the wrap-up directive.
	call := provider.getCall(2)
	if len(call.Tools) != 0 {
		t.Errorf("synthesis call offered %d tools, want 0", len(call.Tools))
	}
	last := call.History[len(call.History)-1]
	if last.Role != "user" || !strings.Contains(last.AsText(), "tool-call budget") {
		t.Errorf("synthesis call must end on the wrap-up directive, got role=%q content=%q", last.Role, last.AsText())
	}
}

// TestAgent_MaxIterations_SynthesisEmpty_FallsBackUnproductive: when the
// synthesis pass itself produces only whitespace, the run degrades to the
// original contract — "no response produced" marker + unproductive flag —
// so the orchestrator's unproductive cap still engages.
func TestAgent_MaxIterations_SynthesisEmpty_FallsBackUnproductive(t *testing.T) {
	resp := toolCallResponse("", tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "synth-empty", 2, 0, resp, resp, stopResponse("\n"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unproductive {
		t.Error("LastRunUnproductive() = false, want true (synthesis produced nothing)")
	}
	if !strings.Contains(out, "no response produced") {
		t.Errorf("output = %q, want 'no response produced' fallback", out)
	}
}

// TestAgent_MaxIterations_SynthesisError_FallsBack: an LLM error during the
// synthesis pass must not fail the run — max_iterations stays a graceful
// degradation for every caller, exactly as before the fix.
func TestAgent_MaxIterations_SynthesisError_FallsBack(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	resp := toolCallResponse("", tc("noop", map[string]interface{}{"query": "x"}))
	provider := &errorProvider{
		responses: []llm.GenerateResult{resp, resp},
		err:       errors.New("provider down"),
	}
	ag := newE2EAgent("synth-err", provider, bus, tool)
	ag.maxIterations = 2

	out, err := ag.Run(context.Background(), "analyze")
	if err != nil {
		t.Fatalf("synthesis error must not fail the run, got: %v", err)
	}
	if !strings.Contains(out, "no response produced") {
		t.Errorf("output = %q, want 'no response produced' fallback", out)
	}
	if !ag.LastRunUnproductive() {
		t.Error("LastRunUnproductive() = false, want true after failed synthesis")
	}
}

// TestAgent_MaxIterations_PartialResponse_SkipsSynthesis: when the loop DID
// commit response text, the partial-result wrapper is returned as before and
// no synthesis call happens — the sequenceProvider (programmed with exactly
// maxIterations responses) panics on any extra call, so passing proves the
// call count.
func TestAgent_MaxIterations_PartialResponse_SkipsSynthesis(t *testing.T) {
	resp := toolCallResponse("scanned the docs so far", tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "synth-skip", 2, 0, resp, resp)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unproductive {
		t.Error("LastRunUnproductive() = true, want false (real partial content)")
	}
	if !strings.Contains(out, "partial result") || !strings.Contains(out, "scanned the docs so far") {
		t.Errorf("output = %q, want the partial-result wrapper with the committed body", out)
	}
}

// TestAgent_ConvergenceNudge_InjectedWhenTwoIterationsRemain: the nudge must
// appear in the LLM history exactly once, first visible on the iteration
// where 2 iterations remain (i == maxIterations-2), and stay in history for
// the final iteration without being re-injected.
func TestAgent_ConvergenceNudge_InjectedWhenTwoIterationsRemain(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	resp := toolCallResponse("scanning", tc("noop", map[string]interface{}{"query": "x"}))
	provider := newSequenceProvider(resp, resp, resp, resp)
	ag := newE2EAgent("nudge", provider, bus, tool)
	ag.maxIterations = 4

	if _, err := ag.Run(context.Background(), "explore"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.callCount() != 4 {
		t.Fatalf("expected 4 LLM calls, got %d", provider.callCount())
	}

	countNudges := func(c mockCall) int {
		n := 0
		for _, m := range c.History {
			if strings.Contains(m.AsText(), "Iteration budget") {
				n++
			}
		}
		return n
	}
	if got := countNudges(provider.getCall(1)); got != 0 {
		t.Errorf("call 1 history has %d nudges, want 0 (threshold not reached yet)", got)
	}
	if got := countNudges(provider.getCall(2)); got != 1 {
		t.Errorf("call 2 history has %d nudges, want 1 (i == maxIterations-2)", got)
	}
	if got := countNudges(provider.getCall(3)); got != 1 {
		t.Errorf("call 3 history has %d nudges, want exactly 1 (no re-injection)", got)
	}
}

// TestAgent_ConvergenceNudge_SkippedForTinyBudgets: with maxIterations < 3
// the nudge would fire on the very first iteration and waste the model's
// only exploration step, so it must not fire at all.
func TestAgent_ConvergenceNudge_SkippedForTinyBudgets(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	resp := toolCallResponse("working", tc("noop", map[string]interface{}{"query": "x"}))
	provider := newSequenceProvider(resp, resp)
	ag := newE2EAgent("nudge-tiny", provider, bus, tool)
	ag.maxIterations = 2

	if _, err := ag.Run(context.Background(), "explore"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < provider.callCount(); i++ {
		for _, m := range provider.getCall(i).History {
			if strings.Contains(m.AsText(), "Iteration budget") {
				t.Fatalf("call %d history contains a nudge, want none for maxIterations=2", i)
			}
		}
	}
}
