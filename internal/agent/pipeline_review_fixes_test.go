package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// countingProvider records every Generate() invocation, regardless of what
// it returns. Unlike sequenceProvider's callCount() (which is really
// "successful responses served" — its internal counter only increments
// AFTER the panic-on-overflow check, so a call that panics never gets
// counted), this is what "the guard fired before dispatch, not after a
// race" actually needs to assert.
type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &llm.GenerateResult{Response: "should not have been called", FinishReason: "stop"}, nil
}
func (p *countingProvider) GetName() string  { return "counting" }
func (p *countingProvider) GetModel() string { return "stub" }
func (p *countingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ============================================================
// executeLevelParallel / executeParallelStep: duplicate-agent guard
// ============================================================

// TestExecuteLevelParallel_DuplicateAgentRejected regression-guards the
// CRITICAL finding: two DAG-level steps naming the same agent used to run
// concurrently on the same singleton *Agent — with retrieval enabled and
// the BM25 backend, that reaches an unsynchronized concurrent map
// read/write and crashes the process. The guard must reject this before
// either goroutine starts, not race them.
func TestExecuteLevelParallel_DuplicateAgentRejected(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	provider := &countingProvider{}
	agents := map[string]*Agent{"w": newTestAgent("w", provider, bus)}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	level := []config.PipelineStep{
		{Name: "s1", Agent: "w", Task: "t1"},
		{Name: "s2", Agent: "w", Task: "t2"},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeLevelParallel(context.Background(), level, pctx)
	if err == nil {
		t.Fatal("expected error for duplicate agent in a parallel level")
	}
	if !strings.Contains(err.Error(), "w") {
		t.Errorf("error should name the duplicated agent, got %v", err)
	}
	if got := provider.count(); got != 0 {
		t.Errorf("agent.Run() called %d times, want 0 (must fail before dispatch, not race)", got)
	}
}

// TestExecuteParallelStep_DuplicateAgentRejected is the same regression
// guard for a `type: parallel` step's sub-steps.
func TestExecuteParallelStep_DuplicateAgentRejected(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	provider := &countingProvider{}
	agents := map[string]*Agent{"w": newTestAgent("w", provider, bus)}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "par",
		Type: "parallel",
		Steps: []config.PipelineStep{
			{Name: "sub-1", Agent: "w", Task: "t1"},
			{Name: "sub-2", Agent: "w", Task: "t2"},
		},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeParallelStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected error for duplicate agent in a parallel step's sub-steps")
	}
	if got := provider.count(); got != 0 {
		t.Errorf("agent.Run() called %d times, want 0 (must fail before dispatch, not race)", got)
	}
}

// TestExecuteLevelParallel_DistinctAgentsStillRun verifies the guard only
// rejects genuine collisions — distinct agents at the same level still run
// concurrently and succeed, same as before this fix.
func TestExecuteLevelParallel_DistinctAgentsStillRun(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "out-a"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "out-b"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	level := []config.PipelineStep{
		{Name: "s1", Agent: "a", Task: "t1"},
		{Name: "s2", Agent: "b", Task: "t2"},
	}
	pctx := newPipelineContext("q")

	out, err := o.executeLevelParallel(context.Background(), level, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "out-a") || !strings.Contains(out, "out-b") {
		t.Errorf("output missing results: %q", out)
	}
}

// ============================================================
// executeLevelParallel: merge separator (finding: undelimited concat)
// ============================================================

// TestExecuteLevelParallel_MergeUsesHeaderSeparator regression-guards
// against executeLevelParallel concatenating adjacent outputs with no
// separator (e.g. "Result A" + "Result B" -> "Result AResult B"),
// inconsistent with executeParallelStep's "## name\noutput\n\n" merge.
func TestExecuteLevelParallel_MergeUsesHeaderSeparator(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "Result A"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "Result B"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	level := []config.PipelineStep{
		{Name: "step-a", Agent: "a", Task: "t"},
		{Name: "step-b", Agent: "b", Task: "t"},
	}
	pctx := newPipelineContext("q")

	out, err := o.executeLevelParallel(context.Background(), level, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "Result AResult B") || strings.Contains(out, "Result BResult A") {
		t.Errorf("outputs were concatenated with no separator: %q", out)
	}
	if !strings.Contains(out, "## step-a") || !strings.Contains(out, "## step-b") {
		t.Errorf("expected header-formatted merge (\"## name\"), got %q", out)
	}
}

// ============================================================
// executeParallelStep: nested composite sub-steps (finding: dispatched
// via executeSequentialStep instead of executeStep)
// ============================================================

// TestExecuteParallelStep_NestedParallelSubStepSupported regression-guards
// against a sub-step nested inside a `type: parallel` step being unable to
// itself be `type: parallel` (or `type: loop`) — config.PipelineStep.Steps
// is a recursive structure that structurally permits this, but dispatching
// sub-steps via executeSequentialStep (which only knows how to run a leaf
// step.Agent != "" step) failed a composite sub-step with the misleading
// error `agent "" not found` instead of actually recursing.
func TestExecuteParallelStep_NestedParallelSubStepSupported(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "result-a"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "result-b"}, bus),
		"c": newTestAgent("c", &fastProvider{answer: "result-c"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "outer",
		Type: "parallel",
		Steps: []config.PipelineStep{
			{Name: "leaf-c", Agent: "c", Task: "t"},
			{
				Name: "inner",
				Type: "parallel",
				Steps: []config.PipelineStep{
					{Name: "leaf-a", Agent: "a", Task: "t"},
					{Name: "leaf-b", Agent: "b", Task: "t"},
				},
			},
		},
	}
	pctx := newPipelineContext("q")

	out, err := o.executeParallelStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("nested parallel sub-step should be supported, got error: %v", err)
	}
	if !strings.Contains(out, "result-a") || !strings.Contains(out, "result-b") || !strings.Contains(out, "result-c") {
		t.Errorf("output missing nested results: %q", out)
	}
}

// TestExecuteParallelStep_NestedCollisionAcrossLevelsRejected verifies the
// duplicate-agent guard (finding above) sees through nesting too: an agent
// used both by a direct sub-step and, separately, inside a nested composite
// sub-step still collides — they run concurrently as siblings of the outer
// wg.Wait() batch regardless of nesting depth.
func TestExecuteParallelStep_NestedCollisionAcrossLevelsRejected(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	provider := &countingProvider{}
	agents := map[string]*Agent{"shared": newTestAgent("shared", provider, bus)}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "outer",
		Type: "parallel",
		Steps: []config.PipelineStep{
			{Name: "leaf-direct", Agent: "shared", Task: "t"},
			{
				Name: "inner",
				Type: "parallel",
				Steps: []config.PipelineStep{
					{Name: "leaf-nested", Agent: "shared", Task: "t"},
				},
			},
		},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeParallelStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected error: \"shared\" is referenced both directly and inside a nested parallel sub-step")
	}
	if got := provider.count(); got != 0 {
		t.Errorf("agent.Run() called %d times, want 0 (must fail before dispatch)", got)
	}
}

// ============================================================
// buildLevels: depends_on: [] as an explicit DAG root
// ============================================================

// TestBuildLevels_ExplicitEmptyDependsOnIsParallelRoot regression-guards
// against len(DependsOn) == 0 conflating "not specified" (nil — inject the
// implicit sequential dependency) with "explicitly declared empty"
// ([]string{}, via depends_on: [] — a true DAG root, skip injection). Only
// steps[0] could ever be a zero-dependency root under the old len() check.
func TestBuildLevels_ExplicitEmptyDependsOnIsParallelRoot(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a"},                        // implicit root: DependsOn is nil
		{Name: "b", DependsOn: []string{}}, // explicit root: DependsOn is non-nil empty
	}
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(levels) != 1 {
		t.Fatalf("expected 1 level (both steps are independent roots), got %d: %v", len(levels), levels)
	}
	if len(levels[0]) != 2 {
		t.Errorf("expected level 0 to contain both steps, got %v", levelNames(levels[0]))
	}
}

// TestBuildLevels_OmittedDependsOnStillChains verifies the ordinary
// implicit-chain behavior (DependsOn omitted, i.e. nil) is unchanged by the
// nil-check fix.
func TestBuildLevels_OmittedDependsOnStillChains(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a"},
		{Name: "b"},
	}
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(levels) != 2 {
		t.Errorf("expected 2 levels (implicit chain a -> b), got %d: %v", len(levels), levels)
	}
}

// ============================================================
// executeLoopStep: condition check requires a leading PASS verdict
// ============================================================

// countLoopIterationStarts counts PIPELINE_STEP_START events with
// StepType == "loop_iteration".
func countLoopIterationStarts(t *testing.T, events []telemetry.AgentEvent) int {
	t.Helper()
	n := 0
	for _, e := range events {
		if e.EventType != telemetry.EventPipelineStepStart {
			continue
		}
		var p telemetry.PipelineStepStartPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil && p.StepType == "loop_iteration" {
			n++
		}
	}
	return n
}

// TestExecuteLoopStep_ConditionRequiresLeadingPass regression-guards
// against a bare strings.Contains(upper, "PASS") check: a condition
// agent's free-text explanation of a FAILURE — "The step FAILED, it does
// NOT PASS the requirements" — contains the substring "PASS" too, and
// would be misread as passing, exiting the loop before the work actually
// succeeded.
func TestExecuteLoopStep_ConditionRequiresLeadingPass(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"worker":   newTestAgent("worker", &fastProvider{answer: "work done"}, bus),
		"reviewer": newTestAgent("reviewer", &fastProvider{answer: "The step FAILED, it does NOT PASS the requirements"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:            "loop",
		Type:            "loop",
		MaxIterations:   2,
		ConditionAgent:  "reviewer",
		ConditionPrompt: "check",
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "worker", Task: "iterate"},
		},
	}
	pctx := newPipelineContext("q")

	events := collectEvents(bus, func() {
		if _, err := o.executeLoopStep(context.Background(), step, pctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if got := countLoopIterationStarts(t, events); got != 2 {
		t.Errorf("loop ran %d iterations, want 2 (a \"FAILED ... does NOT PASS\" verdict must not exit early)", got)
	}
}

// TestExecuteLoopStep_ConditionExitsOnLeadingPass verifies the ordinary
// case still works: a genuine leading PASS verdict exits the loop early.
func TestExecuteLoopStep_ConditionExitsOnLeadingPass(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"worker":   newTestAgent("worker", &fastProvider{answer: "work done"}, bus),
		"reviewer": newTestAgent("reviewer", &fastProvider{answer: "PASS - looks good"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:            "loop",
		Type:            "loop",
		MaxIterations:   5,
		ConditionAgent:  "reviewer",
		ConditionPrompt: "check",
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "worker", Task: "iterate"},
		},
	}
	pctx := newPipelineContext("q")

	events := collectEvents(bus, func() {
		if _, err := o.executeLoopStep(context.Background(), step, pctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if got := countLoopIterationStarts(t, events); got != 1 {
		t.Errorf("loop ran %d iterations, want 1 (a leading PASS verdict must exit after the first)", got)
	}
}

// ============================================================
// runPipeline: debug rerun of an already-checkpointed step must
// actually re-execute, not replay the stale checkpoint
// ============================================================

// TestRunPipeline_RerunAlreadyCheckpointedStepReexecutes regression-guards
// the MAJOR finding: pctx.clearFrom() resets the in-memory PipelineContext
// for a debug rerun, but o.checkpoint.Results (a separate cache consulted
// by the "RESUME: inject checkpointed results" block) was never cleared —
// so the very next loop iteration found the rerun target's name still
// present there and re-injected the stale cached output instead of
// actually re-executing it, silently defeating the rerun request.
func TestRunPipeline_RerunAlreadyCheckpointedStepReexecutes(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	stepAProvider := newSequenceProvider(llm.GenerateResult{Response: "stepA rerun output", FinishReason: "stop"})
	agents := map[string]*Agent{
		"agentA": newTestAgent("agentA", stepAProvider, bus),
		"agentB": newTestAgent("agentB", &fastProvider{answer: "stepB output"}, bus),
	}
	steps := []config.PipelineStep{
		{Name: "stepA", Agent: "agentA", Task: "t"},
		{Name: "stepB", Agent: "agentB", Task: "t", DependsOn: []string{"stepA"}},
	}
	o := newTestOrchestrator(steps, agents)
	o.eventBus = bus

	dc := debug.NewDebugController(bus)
	o.SetDebugController(dc)
	o.SetCheckpoint(&CheckpointData{
		Results: map[string]CheckpointStep{
			"stepA": {Name: "stepA", Output: "stale checkpoint output"},
		},
	})

	// Queue a rerun request for stepA up front — runPipeline checks
	// RerunCh() non-blockingly after each level executes (stepB's level,
	// since stepA's level is skipped entirely via checkpoint injection), so
	// by the time it gets there this is already waiting.
	dc.RerunFromStep("stepA", nil)

	if _, err := o.runPipeline(context.Background(), "q"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := stepAProvider.callCount(); got != 1 {
		t.Errorf("agentA.Run() called %d times, want 1 (rerun must actually re-execute stepA, not replay the stale checkpoint)", got)
	}
}
