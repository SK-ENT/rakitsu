package agent

// Regression tests for the whitespace-trim gap in the agent's
// unproductive-outcome contract. Reasoning models exit terminal branches
// (max_iterations, iteration-level budget_exceeded) with
// `lastResponse = "\n"` rather than literal "". Before the TrimSpace fix the
// agent returned a non-empty "[max_iterations|budget exceeded — partial
// result]\n\n\n" marker without flipping LastRunUnproductive(), which made
// the orchestrator's post-unproductive cap unreachable.
//
// A prior fix closed the max_iterations branch. This file's
// TestAgent_BudgetExceeded_* tests cover the parallel iteration-level
// budget_exceeded branch closed in the same audit, and the
// TestAgent_MaxIterations_* tests pin down the existing max_iterations
// behavior so the contract can't quietly regress in either direction.

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// drive runs the agent to completion with the given sequence of provider
// responses and an optional token guard, returning the final output, error,
// and the LastRunUnproductive flag.
func drive(t *testing.T, name string, maxIter int, guardTokens int, responses ...llm.GenerateResult) (string, error, bool) {
	t.Helper()
	bus := telemetry.NewEventBus(64)
	tool := newMockTool("noop", "ok")
	ag := newE2EAgent(name, newSequenceProvider(responses...), bus, tool)
	ag.maxIterations = maxIter
	if guardTokens > 0 {
		tg := NewTokenGuard(guardTokens, 0)
		ag.SetTokenGuard(tg)
		ag.SetGuard(NewCompositeGuard(nil, tg))
	}
	out, err := ag.Run(context.Background(), "test query")
	return out, err, ag.LastRunUnproductive()
}

// TestAgent_MaxIterations_WhitespaceLastResponse_FlagsUnproductive pins the
// fix: a reasoning model that exits every iteration with Response="\n"
// and a tool call burns the iteration budget without producing usable output.
// The terminal path must flag the run as unproductive AND return the
// "no response produced" marker (not the "partial result" wrapper).
func TestAgent_MaxIterations_WhitespaceLastResponse_FlagsUnproductive(t *testing.T) {
	// Two iterations of (whitespace response + tool call). After iter 2 the
	// loop exhausts maxIterations and exits via the max_iterations path.
	// That path first runs a forced synthesis call; program it to
	// also return whitespace so the original fallback contract stays pinned.
	resp := toolCallResponse("\n", tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "max-iter-whitespace", 2, 0, resp, resp, stopResponse("\n"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unproductive {
		t.Errorf("LastRunUnproductive() = false, want true (lastResponse was whitespace-only)")
	}
	if !strings.Contains(out, "no response produced") {
		t.Errorf("output = %q, want it to contain 'no response produced'", out)
	}
	if strings.Contains(out, "partial result") {
		t.Errorf("output = %q, must not contain 'partial result' for whitespace lastResponse", out)
	}
}

// TestAgent_MaxIterations_RealLastResponse_DoesNotFlag is the inverse: when
// the model committed actual content into Response before running out of
// iterations, the run is NOT unproductive — there's a real partial answer to
// surface and the orchestrator should let the supervisor decide what to do.
func TestAgent_MaxIterations_RealLastResponse_DoesNotFlag(t *testing.T) {
	resp := toolCallResponse("real partial work in progress", tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "max-iter-real", 2, 0, resp, resp)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unproductive {
		t.Errorf("LastRunUnproductive() = true, want false (lastResponse was real content)")
	}
	if !strings.Contains(out, "partial result") {
		t.Errorf("output = %q, want it to contain 'partial result' wrapper", out)
	}
	if !strings.Contains(out, "real partial work") {
		t.Errorf("output = %q, want it to surface the partial response body", out)
	}
}

// TestAgent_BudgetExceeded_EmptyLastResponse_FlagsUnproductive: iter 1's
// addTokenUsage immediately overshoots the budget, the iteration-level guard
// check fires before lastResponse is updated (it's still ""), and the agent
// returns the "[budget exceeded: ...]" marker. The flag must flip.
func TestAgent_BudgetExceeded_EmptyLastResponse_FlagsUnproductive(t *testing.T) {
	// 80 in + 40 out = 120 tokens used; budget = 100 → guard fires on iter 1.
	resp := toolCallResponseTokens("", 80, 40, tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "budget-empty", 5, 100, resp)

	if err != nil {
		t.Fatalf("unexpected error from iteration-level budget path: %v", err)
	}
	if !unproductive {
		t.Errorf("LastRunUnproductive() = false, want true (budget_exceeded with empty lastResponse)")
	}
	if !strings.Contains(out, "budget exceeded") {
		t.Errorf("output = %q, want it to contain 'budget exceeded' marker", out)
	}
	if strings.Contains(out, "partial result") {
		t.Errorf("output = %q, must not contain 'partial result' wrapper for empty lastResponse", out)
	}
}

// TestAgent_BudgetExceeded_WhitespaceLastResponse_FlagsUnproductive is the
// core of this audit: a reasoning model emits whitespace-only Response in
// iter 1 (which sets lastResponse="\n"), then iter 2 trips the budget. Before
// the TrimSpace fix the agent would return a "[budget exceeded — partial
// result]\n\n\n" marker (44 non-whitespace chars) and leave the unproductive
// flag clear, hiding the failure from the unproductive cap.
func TestAgent_BudgetExceeded_WhitespaceLastResponse_FlagsUnproductive(t *testing.T) {
	iter1 := toolCallResponseTokens("\n", 30, 20, tc("noop", map[string]interface{}{"query": "x"}))
	iter2 := toolCallResponseTokens("\n", 60, 40, tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "budget-whitespace", 5, 100, iter1, iter2)

	if err != nil {
		t.Fatalf("unexpected error from iteration-level budget path: %v", err)
	}
	if !unproductive {
		t.Errorf("LastRunUnproductive() = false, want true (whitespace lastResponse must trim to empty)")
	}
	if !strings.Contains(out, "budget exceeded") {
		t.Errorf("output = %q, want it to contain 'budget exceeded' marker", out)
	}
	if strings.Contains(out, "partial result") {
		t.Errorf("output = %q, must not contain 'partial result' wrapper for whitespace-only lastResponse", out)
	}
}

// TestAgent_BudgetExceeded_RealPartialResponse_DoesNotFlag: when a prior
// iteration committed real content, budget_exceeded surfaces it as a partial
// result and the run is NOT unproductive — the supervisor decides next steps.
//
// Note on which iteration's content surfaces: agent.go addTokenUsage and
// guard.Check (the iteration-level check) run BEFORE lastResponse is updated
// from the current iteration's Response. So when iter 2's tokens push over
// budget, the wrapper surfaces iter 1's response — that's the contract this
// test pins down.
func TestAgent_BudgetExceeded_RealPartialResponse_DoesNotFlag(t *testing.T) {
	iter1 := toolCallResponseTokens("Step 1: scanned the docs.", 30, 20, tc("noop", map[string]interface{}{"query": "x"}))
	iter2 := toolCallResponseTokens("Step 2: about to summarize.", 60, 40, tc("noop", map[string]interface{}{"query": "x"}))
	out, err, unproductive := drive(t, "budget-real", 5, 100, iter1, iter2)

	if err != nil {
		t.Fatalf("unexpected error from iteration-level budget path: %v", err)
	}
	if unproductive {
		t.Errorf("LastRunUnproductive() = true, want false (lastResponse held real content)")
	}
	if !strings.Contains(out, "partial result") {
		t.Errorf("output = %q, want the partial-result wrapper", out)
	}
	if !strings.Contains(out, "Step 1: scanned the docs.") {
		t.Errorf("output = %q, want it to surface the previously-committed partial response", out)
	}
}
