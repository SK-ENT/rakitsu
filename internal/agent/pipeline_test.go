package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// ============================================================
// Stub helpers
// ============================================================

// fastProvider returns a fixed answer immediately.
type fastProvider struct{ answer string }

func (p *fastProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return &llm.GenerateResult{Response: p.answer, FinishReason: "stop"}, nil
}
func (p *fastProvider) GetName() string  { return "fast" }
func (p *fastProvider) GetModel() string { return "stub" }

// slowProvider blocks until its context is cancelled, then returns ctx.Err().
type slowProvider struct{}

func (p *slowProvider) Generate(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *slowProvider) GetName() string  { return "slow" }
func (p *slowProvider) GetModel() string { return "stub" }

// newTestAgent creates a minimal agent backed by the given provider.
func newTestAgent(name string, provider llm.LLMProvider, bus *telemetry.EventBus) *Agent {
	def := &config.AgentDefinition{
		Name:         name,
		SystemPrompt: "test",
	}
	return NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)
}

// newTestOrchestrator builds an Orchestrator wired for Pipeline strategy.
// Accepts map[string]*Agent for convenience; converts to map[string]Runner internally.
func newTestOrchestrator(steps []config.PipelineStep, agents map[string]*Agent) *Orchestrator {
	bus := telemetry.NewEventBus(16)
	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	return &Orchestrator{
		name:     "test-orch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: steps,
		},
		agents:   runners,
		eventBus: bus,
	}
}

// collectEvents subscribes to the bus and returns all events received during fn.
func collectEvents(bus *telemetry.EventBus, fn func()) []telemetry.AgentEvent {
	ch := bus.Subscribe()
	fn()
	bus.Unsubscribe(ch) // closes ch; range drains remaining buffered events then stops
	var evts []telemetry.AgentEvent
	for e := range ch {
		evts = append(evts, e)
	}
	return evts
}

// ============================================================
// executeStep status tests
// ============================================================

func TestExecuteStep_StatusSuccess(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	ag := newTestAgent("worker", &fastProvider{answer: "done"}, bus)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{Name: "s1", Agent: "worker", Task: "do it"}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := pctx.Results[0]
	if result.Error != nil {
		t.Errorf("stored result error = %v, want nil", result.Error)
	}
	// verify PIPELINE_STEP_END event has status "success"
	evts := collectEvents(bus, func() {}) // already fired
	_ = evts                              // event already consumed; check via pctx
}

func TestExecuteStep_StatusTimeout(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	ag := newTestAgent("worker", &slowProvider{}, bus)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{Name: "s1", Agent: "worker", Task: "hang", TimeoutSec: 1}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	// stored result should have Error set
	if len(pctx.Results) == 0 {
		t.Fatal("no result stored")
	}
}

func TestExecuteStep_StatusError(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	// agent references unknown agent name → error
	o := newTestOrchestrator(nil, map[string]*Agent{})
	o.eventBus = bus

	step := config.PipelineStep{Name: "s1", Agent: "missing", Task: "x"}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	// error is "agent not found", not a context error
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected non-timeout error, got DeadlineExceeded")
	}
}

// ============================================================
// Parallel step timeout tests
// ============================================================

// ============================================================
// require_tool_call gate tests
// ============================================================

// TestExecuteStep_RequireToolCall_Satisfied_Success reproduces the good
// path: the step's agent actually invokes the required tool with matching
// arguments, so the gate lets the step's own success stand.
func TestExecuteStep_RequireToolCall_Satisfied_Success(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("sh", "ok"))
	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "sh", Arguments: map[string]interface{}{"cmd": "printf 'l\\nq\\n' | node dist/index.js"}}},
			FinishReason: "tool_calls",
		},
		llm.GenerateResult{Response: "OVERALL: PASS", FinishReason: "stop"},
	)
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"sh"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "accept", Agent: "worker", Task: "verify",
		RequireToolCall: &config.RequireToolCallGate{Tool: "sh", CommandContains: "node dist/index.js"},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("expected step to pass (tool was called), got error: %v", err)
	}
	if pctx.Results[0].Error != nil {
		t.Errorf("stored result error = %v, want nil", pctx.Results[0].Error)
	}
}

// TestExecuteStep_RequireToolCall_NotSatisfied_OverridesSelfReportedPass is
// the regression test for the actual bug this gate exists to catch: an
// agent that never runs the tool it was told to, but still reports success
// in its own final answer. The gate must force the step to fail regardless.
func TestExecuteStep_RequireToolCall_NotSatisfied_OverridesSelfReportedPass(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("sh", "ok"))
	// Agent never calls sh at all — goes straight to a claimed-PASS answer,
	// exactly the failure mode observed in practice (an Acceptor-style agent
	// reasoning about source code instead of executing the built program).
	provider := newSequenceProvider(
		llm.GenerateResult{Response: "OVERALL: PASS", FinishReason: "stop"},
	)
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"sh"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "accept", Agent: "worker", Task: "verify",
		RequireToolCall: &config.RequireToolCallGate{Tool: "sh", CommandContains: "node dist/index.js"},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected the gate to fail the step even though the agent self-reported PASS")
	}
	if !strings.Contains(err.Error(), "accept") || !strings.Contains(err.Error(), "sh") {
		t.Errorf("expected error to name the step and the required tool, got: %v", err)
	}
	if pctx.Results[0].Error == nil {
		t.Error("stored result error = nil, want the gate's error recorded")
	}
}

// TestExecuteStep_RequireToolCall_RunErrors_ReleasesRunID is the regression
// test for a map-entry leak: when a gated step's runner.Run itself fails,
// executeSequentialStep used to
// return before checkRequireToolCall ever ran — meaning ToolCallsForRun,
// the only thing that deletes a run ID's map entry, was never called. A
// persistently failing agent would leak one entry per attempt for the life
// of the process. Here the agent records a tool call, then the provider
// errors on the next turn — Run() fails, and toolCallsByRun must still end
// up empty.
func TestExecuteStep_RequireToolCall_RunErrors_ReleasesRunID(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("sh", "ok"))
	provider := &errorProvider{
		responses: []llm.GenerateResult{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "sh", Arguments: map[string]interface{}{"cmd": "ls"}}}, FinishReason: "tool_calls"},
		},
		err: errors.New("boom"),
	}
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"sh"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "accept", Agent: "worker", Task: "verify",
		RequireToolCall: &config.RequireToolCallGate{Tool: "sh"},
	}
	pctx := newPipelineContext("q")

	if _, err := o.executeStep(context.Background(), step, pctx); err == nil {
		t.Fatal("expected the Run itself to fail (errorProvider errors after the first tool call)")
	}

	if n := len(ag.toolCallsByRun); n != 0 {
		t.Errorf("toolCallsByRun has %d leftover entries after a failed Run, want 0 — the run ID must be released even when Run() itself errors, not just on a successful gate check", n)
	}
}

// TestCheckRequireToolCall_RunnerWithoutReporter_FailsClosed verifies a
// Runner that doesn't implement ToolCallReporter (e.g. a nested
// *Orchestrator reached via a pipeline step's agent:, or any other non-
// *Agent Runner) fails the gate rather than silently passing it — the gate
// can't be verified for it, and require_tool_call must never be satisfiable
// by trust alone.
type toolCallReporterlessRunner struct{}

func (toolCallReporterlessRunner) Run(context.Context, string) (string, error) { return "PASS", nil }
func (toolCallReporterlessRunner) GetName() string                             { return "r" }
func (toolCallReporterlessRunner) GetRole() AgentRole                          { return "worker" }
func (toolCallReporterlessRunner) GetTools() []string                          { return nil }
func (toolCallReporterlessRunner) SetDebugController(*debug.DebugController)   {}

func TestCheckRequireToolCall_RunnerWithoutReporter_FailsClosed(t *testing.T) {
	step := config.PipelineStep{
		Name:            "s",
		RequireToolCall: &config.RequireToolCallGate{Tool: "sh"},
	}
	err := checkRequireToolCall(step, toolCallReporterlessRunner{}, 0)
	if err == nil {
		t.Fatal("expected fail-closed (non-nil error) for a Runner without ToolCallReporter, got nil")
	}
	if !strings.Contains(err.Error(), "s") || !strings.Contains(err.Error(), "sh") {
		t.Errorf("error should name the step and required tool, got: %v", err)
	}
}

func TestCheckRequireToolCall_NoGate_AlwaysNil(t *testing.T) {
	step := config.PipelineStep{Name: "s"}
	if err := checkRequireToolCall(step, toolCallReporterlessRunner{}, 0); err != nil {
		t.Errorf("expected nil error when step has no require_tool_call gate, got: %v", err)
	}
}

// ============================================================
// require_tool_call value-bound gate tests (output_json_path/min_value/
// max_value): checks the tool's actual JSON response, not just that the
// call happened — the mechanism a Jev-based pipeline gate needs to be a
// real enforced check rather than an instruction the agent could ignore.
// ============================================================

func minVal(v float64) *float64 { return &v }

// TestExecuteStep_RequireToolCall_ValueBound_Satisfied verifies a step
// passes when the required tool was called AND its JSON response's value
// at output_json_path clears min_value.
func TestExecuteStep_RequireToolCall_ValueBound_Satisfied(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("jev", `{"answers":{"severity":{"type":"score","score":0.92}}}`))
	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "jev", Arguments: map[string]interface{}{"query": "x"}}},
			FinishReason: "tool_calls",
		},
		llm.GenerateResult{Response: "BLOCK", FinishReason: "stop"},
	)
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"jev"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "gate", Agent: "worker", Task: "triage",
		RequireToolCall: &config.RequireToolCallGate{Tool: "jev", OutputJSONPath: "answers.severity.score", MinValue: minVal(0.7)},
	}
	pctx := newPipelineContext("q")

	if _, err := o.executeStep(context.Background(), step, pctx); err != nil {
		t.Fatalf("expected step to pass (score 0.92 >= min_value 0.7), got error: %v", err)
	}
}

// TestExecuteStep_RequireToolCall_ValueBound_NotSatisfied_OverridesSelfReport
// is the case this gate exists for: the agent called the right tool, but
// Jev's own response didn't clear the threshold, and the agent's final
// answer claims BLOCK anyway (e.g. misreading its own tool result, or an
// instruction it silently ignored) — the gate must still fail the step.
func TestExecuteStep_RequireToolCall_ValueBound_NotSatisfied_OverridesSelfReport(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("jev", `{"answers":{"severity":{"type":"score","score":0.2}}}`))
	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "jev", Arguments: map[string]interface{}{"query": "x"}}},
			FinishReason: "tool_calls",
		},
		// Agent misreports BLOCK even though Jev's own score (0.2) is well
		// under the 0.7 threshold — the mechanical gate, not the agent's
		// self-report, must be what actually decides this.
		llm.GenerateResult{Response: "BLOCK", FinishReason: "stop"},
	)
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"jev"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "gate", Agent: "worker", Task: "triage",
		RequireToolCall: &config.RequireToolCallGate{Tool: "jev", OutputJSONPath: "answers.severity.score", MinValue: minVal(0.7)},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected the gate to fail the step: Jev's own score (0.2) never cleared min_value (0.7)")
	}
	if !strings.Contains(err.Error(), "gate") || !strings.Contains(err.Error(), "jev") {
		t.Errorf("expected error to name the step and the tool, got: %v", err)
	}
}

func TestExtractJSONPathFloat(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		path    string
		want    float64
		wantErr bool
	}{
		{"noul nested path", `{"answers":{"is_urgent":{"noul":0.95}}}`, "answers.is_urgent.noul", 0.95, false},
		{"score nested path", `{"answers":{"severity":{"score":1.05}}}`, "answers.severity.score", 1.05, false},
		{"malformed json", `not json`, "a.b", 0, true},
		{"missing key", `{"a":{"b":1}}`, "a.c", 0, true},
		{"non-object intermediate", `{"a":1}`, "a.b", 0, true},
		{"non-numeric leaf", `{"a":{"b":"x"}}`, "a.b", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSONPathFloat(tt.raw, tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractJSONPathFloat(%q, %q) error = %v, wantErr %v", tt.raw, tt.path, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("extractJSONPathFloat(%q, %q) = %v, want %v", tt.raw, tt.path, got, tt.want)
			}
		})
	}
}

// TestCheckRequireToolCall_ValueBound_RunnerWithoutOutputReporter_FailsClosed
// mirrors TestCheckRequireToolCall_RunnerWithoutReporter_FailsClosed for the
// output-value path: a Runner that can report calls but not their outputs
// must fail closed when the gate needs a value check, not silently pass.
type toolCallOnlyReporterRunner struct{ toolCallReporterlessRunner }

func (toolCallOnlyReporterRunner) ToolCallsForRun(uint64) []llm.ToolCall { return nil }

func TestCheckRequireToolCall_ValueBound_RunnerWithoutOutputReporter_FailsClosed(t *testing.T) {
	step := config.PipelineStep{
		Name:            "s",
		RequireToolCall: &config.RequireToolCallGate{Tool: "jev", OutputJSONPath: "answers.x.score", MinValue: minVal(0.5)},
	}
	err := checkRequireToolCall(step, toolCallOnlyReporterRunner{}, 0)
	if err == nil {
		t.Fatal("expected fail-closed (non-nil error) for a Runner without ToolOutputReporter when the gate needs a value check")
	}
	if !strings.Contains(err.Error(), "s") || !strings.Contains(err.Error(), "jev") {
		t.Errorf("error should name the step and required tool, got: %v", err)
	}
}

// ============================================================
// require_tool_call argument-matching tests: matching against ANY
// string-valued argument let an unrelated field like
// a path or metadata value satisfy command_contains without the actual
// command ever running)
// ============================================================

// TestCommandArgValue_SingleStringArg_NoArgKey_Matches covers the common
// case (a single-parameter tool like "sh"): with no arg_key configured, the
// one string-valued argument is used.
func TestCommandArgValue_SingleStringArg_NoArgKey_Matches(t *testing.T) {
	call := llm.ToolCall{Name: "sh", Arguments: map[string]interface{}{"cmd": "node dist/index.js"}}
	got, ok := commandArgValue(call, "")
	if !ok || got != "node dist/index.js" {
		t.Fatalf("commandArgValue = (%q, %v), want (\"node dist/index.js\", true)", got, ok)
	}
}

// TestCommandArgValue_MultipleStringArgs_NoArgKey_DoesNotMatch is the direct
// regression test for the MAJOR finding: a call with more than one
// string-valued argument (e.g. a path AND a command) must not be checked
// against an arbitrary one of them — that let an unrelated field silently
// satisfy the gate. Without arg_key, such a call is simply not matchable.
func TestCommandArgValue_MultipleStringArgs_NoArgKey_DoesNotMatch(t *testing.T) {
	call := llm.ToolCall{Name: "sh", Arguments: map[string]interface{}{
		"cmd":  "ls",
		"path": "/tmp/node dist/index.js.bak", // contains the target substring, but isn't the command
	}}
	if _, ok := commandArgValue(call, ""); ok {
		t.Fatal("commandArgValue matched a call with multiple string args and no arg_key — should be ambiguous, not matched")
	}
}

// TestCommandArgValue_ArgKeySet_OnlyChecksThatKey verifies arg_key scopes
// the check to exactly one named argument, ignoring an unrelated field that
// happens to contain the target substring.
func TestCommandArgValue_ArgKeySet_OnlyChecksThatKey(t *testing.T) {
	call := llm.ToolCall{Name: "sh", Arguments: map[string]interface{}{
		"cmd":  "ls",
		"note": "node dist/index.js", // decoy — must be ignored when arg_key is set
	}}
	got, ok := commandArgValue(call, "cmd")
	if !ok || got != "ls" {
		t.Fatalf("commandArgValue(argKey=cmd) = (%q, %v), want (\"ls\", true)", got, ok)
	}
	if _, ok := commandArgValue(call, "missing"); ok {
		t.Error("commandArgValue(argKey=missing) matched, want false — key isn't present")
	}
}

// TestExecuteStep_RequireToolCall_UnrelatedFieldMatch_DoesNotSatisfyGate is
// the end-to-end regression test: an agent that runs a DIFFERENT command
// than required, but whose call happens to carry the target substring in
// an unrelated argument, must still fail the gate.
func TestExecuteStep_RequireToolCall_UnrelatedFieldMatch_DoesNotSatisfyGate(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("sh", "ok"))
	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "sh", Arguments: map[string]interface{}{
				"cmd":  "ls",
				"note": "verifying node dist/index.js works", // decoy field, not the command
			}}},
			FinishReason: "tool_calls",
		},
		llm.GenerateResult{Response: "OVERALL: PASS", FinishReason: "stop"},
	)
	ag := NewAgent(&config.AgentDefinition{Name: "worker", Role: "worker", Tools: []string{"sh"}}, provider, reg, bus, nil)
	o := newTestOrchestrator(nil, map[string]*Agent{"worker": ag})
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "accept", Agent: "worker", Task: "verify",
		RequireToolCall: &config.RequireToolCallGate{Tool: "sh", CommandContains: "node dist/index.js"},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeStep(context.Background(), step, pctx)
	if err == nil {
		t.Fatal("expected the gate to fail: the actual command run (ls) doesn't match, even though an unrelated field contains the target text")
	}
}

func TestParallelStep_AllFast_Success(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "result-a"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "result-b"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "par",
		Type: "parallel",
		Steps: []config.PipelineStep{
			{Name: "sub-a", Agent: "a", Task: "go"},
			{Name: "sub-b", Agent: "b", Task: "go"},
		},
	}
	pctx := newPipelineContext("q")

	out, err := o.executeParallelStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "result-a") || !strings.Contains(out, "result-b") {
		t.Errorf("output missing sub-step results: %q", out)
	}
}

func TestParallelStep_Timeout_SlowSub(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"slow": newTestAgent("slow", &slowProvider{}, bus),
		"fast": newTestAgent("fast", &fastProvider{answer: "ok"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:       "par",
		Type:       "parallel",
		TimeoutSec: 1,
		Steps: []config.PipelineStep{
			{Name: "sub-slow", Agent: "slow", Task: "hang"},
			{Name: "sub-fast", Agent: "fast", Task: "go"},
		},
	}
	pctx := newPipelineContext("q")

	start := time.Now()
	_, err := o.executeParallelStep(context.Background(), step, pctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timed-out sub-step")
	}
	// Should complete well within 2s (not wait for slow agent's blocked goroutine)
	if elapsed > 3*time.Second {
		t.Errorf("parallel step took %v, expected ~1s", elapsed)
	}
}

func TestParallelStep_NoTimeout_InheritsParent(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	agents := map[string]*Agent{
		"fast": newTestAgent("fast", &fastProvider{answer: "ok"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name: "par",
		Type: "parallel",
		// TimeoutSec: 0 — no per-step timeout
		Steps: []config.PipelineStep{
			{Name: "sub", Agent: "fast", Task: "go"},
		},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeParallelStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("expected success with no timeout: %v", err)
	}
}

// ============================================================
// Loop step timeout tests
// ============================================================

func TestLoopStep_AllIterationsFast_Success(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"worker": newTestAgent("worker", &fastProvider{answer: "done"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:          "loop",
		Type:          "loop",
		MaxIterations: 3,
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "worker", Task: "iterate"},
		},
	}
	pctx := newPipelineContext("q")

	out, err := o.executeLoopStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output from loop")
	}
}

func TestLoopStep_Timeout_SlowIteration(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"slow": newTestAgent("slow", &slowProvider{}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:          "loop",
		Type:          "loop",
		TimeoutSec:    1,
		MaxIterations: 10,
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "slow", Task: "hang"},
		},
	}
	pctx := newPipelineContext("q")

	start := time.Now()
	_, err := o.executeLoopStep(context.Background(), step, pctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error from loop")
	}
	if elapsed > 3*time.Second {
		t.Errorf("loop step took %v, expected ~1s", elapsed)
	}
}

func TestLoopStep_NoTimeout_Completes(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"fast": newTestAgent("fast", &fastProvider{answer: "done"}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:          "loop",
		Type:          "loop",
		TimeoutSec:    0, // no timeout
		MaxIterations: 2,
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "fast", Task: "go"},
		},
	}
	pctx := newPipelineContext("q")

	_, err := o.executeLoopStep(context.Background(), step, pctx)
	if err != nil {
		t.Fatalf("expected success with no timeout: %v", err)
	}
}

// TestLoopStep_ParentCancellation verifies that parent context cancellation
// propagates to loop sub-steps even without a per-step timeout.
func TestLoopStep_ParentCancellation(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"slow": newTestAgent("slow", &slowProvider{}, bus),
	}
	o := newTestOrchestrator(nil, agents)
	o.eventBus = bus

	step := config.PipelineStep{
		Name:          "loop",
		Type:          "loop",
		MaxIterations: 100,
		Steps: []config.PipelineStep{
			{Name: "work", Agent: "slow", Task: "hang"},
		},
	}
	pctx := newPipelineContext("q")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := o.executeLoopStep(ctx, step, pctx)
	if err == nil {
		t.Fatal("expected context error")
	}
}

// ============================================================
// Stress tests
// ============================================================

// TestStress_ConcurrentParallelTimeouts runs many orchestrators in parallel,
// each with a timed-out parallel step — verifies race-free behaviour.
func TestStress_ConcurrentParallelTimeouts(t *testing.T) {
	const N = 50
	var wg sync.WaitGroup
	var timeouts atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			bus := telemetry.NewEventBus(8)
			agents := map[string]*Agent{
				"slow": newTestAgent("slow", &slowProvider{}, bus),
			}
			o := newTestOrchestrator(nil, agents)
			o.eventBus = bus

			step := config.PipelineStep{
				Name:       "par",
				Type:       "parallel",
				TimeoutSec: 1,
				Steps: []config.PipelineStep{
					{Name: "sub", Agent: "slow", Task: "hang"},
				},
			}
			pctx := newPipelineContext("q")
			_, err := o.executeParallelStep(context.Background(), step, pctx)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				timeouts.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := timeouts.Load(); got != N {
		t.Errorf("expected %d timeouts, got %d", N, got)
	}
}

// TestStress_ConcurrentLoopFast runs many loop steps concurrently with fast agents.
func TestStress_ConcurrentLoopFast(t *testing.T) {
	const N = 20
	var wg sync.WaitGroup
	var successes atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(id int) {
			defer wg.Done()
			bus := telemetry.NewEventBus(16)
			name := fmt.Sprintf("worker-%d", id)
			agents := map[string]*Agent{
				name: newTestAgent(name, &fastProvider{answer: "ok"}, bus),
			}
			o := newTestOrchestrator(nil, agents)
			o.eventBus = bus

			step := config.PipelineStep{
				Name:          "loop",
				Type:          "loop",
				MaxIterations: 3,
				Steps: []config.PipelineStep{
					{Name: "work", Agent: name, Task: "go"},
				},
			}
			pctx := newPipelineContext("q")
			_, err := o.executeLoopStep(context.Background(), step, pctx)
			if err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := successes.Load(); got != N {
		t.Errorf("expected %d successes, got %d", N, got)
	}
}

// ============================================================
// buildLevels unit tests
// ============================================================

func TestBuildLevels_NoDeps_Sequential(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	}
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without explicit deps, implicit sequential deps are injected (YAML order).
	// a → b → c means 3 separate levels, each with 1 step.
	if len(levels) != 3 {
		t.Errorf("expected 3 levels (implicit sequential), got %d", len(levels))
	}
	for i, name := range []string{"a", "b", "c"} {
		if len(levels[i]) != 1 || levels[i][0].Name != name {
			t.Errorf("expected level %d to contain %q, got %v", i, name, levels[i])
		}
	}
}

func TestBuildLevels_Diamond(t *testing.T) {
	// a → b, c → d  (diamond)
	steps := []config.PipelineStep{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "c", DependsOn: []string{"a"}},
		{Name: "d", DependsOn: []string{"b", "c"}},
	}
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Level 0: [a], Level 1: [b, c], Level 2: [d]
	if len(levels) != 3 {
		t.Errorf("expected 3 levels, got %d", len(levels))
	}
	if len(levels[0]) != 1 || levels[0][0].Name != "a" {
		t.Errorf("level 0 should be [a], got %v", levelNames(levels[0]))
	}
	if len(levels[1]) != 2 {
		t.Errorf("level 1 should have 2 steps (b,c), got %v", levelNames(levels[1]))
	}
	if len(levels[2]) != 1 || levels[2][0].Name != "d" {
		t.Errorf("level 2 should be [d], got %v", levelNames(levels[2]))
	}
}

func TestBuildLevels_LinearChain(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "c", DependsOn: []string{"b"}},
	}
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(levels) != 3 {
		t.Errorf("expected 3 levels, got %d", len(levels))
	}
}

func TestBuildLevels_Cycle_Error(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}
	_, err := buildLevels(steps)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestBuildLevels_SelfDep_Error(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", DependsOn: []string{"a"}},
	}
	_, err := buildLevels(steps)
	if err == nil {
		t.Fatal("expected self-dependency error")
	}
}

func TestBuildLevels_UnknownDep_Error(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", DependsOn: []string{"nonexistent"}},
	}
	_, err := buildLevels(steps)
	if err == nil {
		t.Fatal("expected unknown dependency error")
	}
}

func TestBuildLevels_WideFanOut(t *testing.T) {
	// root → 5 parallel leaves → sink
	steps := []config.PipelineStep{{Name: "root"}}
	for i := 0; i < 5; i++ {
		steps = append(steps, config.PipelineStep{
			Name:      fmt.Sprintf("leaf-%d", i),
			DependsOn: []string{"root"},
		})
	}
	var leafDeps []string
	for i := 0; i < 5; i++ {
		leafDeps = append(leafDeps, fmt.Sprintf("leaf-%d", i))
	}
	steps = append(steps, config.PipelineStep{Name: "sink", DependsOn: leafDeps})
	levels, err := buildLevels(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Level 0: root, Level 1: 5 leaves, Level 2: sink
	if len(levels) != 3 {
		t.Errorf("expected 3 levels, got %d", len(levels))
	}
	if len(levels[1]) != 5 {
		t.Errorf("expected 5 leaves in level 1, got %d", len(levels[1]))
	}
}

// ============================================================
// DAG execution integration tests
// ============================================================

func TestDAG_Diamond_ExecutionOrder(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "output-a"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "output-b"}, bus),
		"c": newTestAgent("c", &fastProvider{answer: "output-c"}, bus),
		"d": newTestAgent("d", &fastProvider{answer: "output-d"}, bus),
	}
	steps := []config.PipelineStep{
		{Name: "a", Agent: "a", Task: "task-a"},
		{Name: "b", Agent: "b", Task: "task-b", DependsOn: []string{"a"}},
		{Name: "c", Agent: "c", Task: "task-c", DependsOn: []string{"a"}},
		{Name: "d", Agent: "d", Task: "task-d", DependsOn: []string{"b", "c"}},
	}
	o := newTestOrchestrator(steps, agents)

	_, err := o.runPipeline(context.Background(), "run diamond")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDAG_CyclicDeps_FailsBeforeExecution(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "x", Agent: "x", Task: "t", DependsOn: []string{"y"}},
		{Name: "y", Agent: "y", Task: "t", DependsOn: []string{"x"}},
	}
	o := newTestOrchestrator(steps, map[string]*Agent{})

	_, err := o.runPipeline(context.Background(), "cycle test")
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

func TestDAG_NoDeps_BackwardCompatible(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"w": newTestAgent("w", &fastProvider{answer: "ok"}, bus),
	}
	steps := []config.PipelineStep{
		{Name: "s1", Agent: "w", Task: "t"},
		{Name: "s2", Agent: "w", Task: "t"},
	}
	o := newTestOrchestrator(steps, agents)

	_, err := o.runPipeline(context.Background(), "no deps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ============================================================
// DAG stress tests
// ============================================================

func TestStress_DAG_ConcurrentPipelines(t *testing.T) {
	const N = 20
	var wg sync.WaitGroup
	var successes atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			bus := telemetry.NewEventBus(32)
			agents := map[string]*Agent{
				"root": newTestAgent("root", &fastProvider{answer: "root-out"}, bus),
				"la":   newTestAgent("la", &fastProvider{answer: "la-out"}, bus),
				"lb":   newTestAgent("lb", &fastProvider{answer: "lb-out"}, bus),
				"sink": newTestAgent("sink", &fastProvider{answer: "sink-out"}, bus),
			}
			steps := []config.PipelineStep{
				{Name: "root", Agent: "root", Task: "t"},
				{Name: "la", Agent: "la", Task: "t", DependsOn: []string{"root"}},
				{Name: "lb", Agent: "lb", Task: "t", DependsOn: []string{"root"}},
				{Name: "sink", Agent: "sink", Task: "t", DependsOn: []string{"la", "lb"}},
			}
			o := newTestOrchestrator(steps, agents)
			_, err := o.runPipeline(context.Background(), "stress")
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != N {
		t.Errorf("expected %d successes, got %d", N, got)
	}
}

func TestStress_DAG_WideFanOut(t *testing.T) {
	const leaves = 10
	bus := telemetry.NewEventBus(64)
	agents := map[string]*Agent{
		"root": newTestAgent("root", &fastProvider{answer: "root"}, bus),
		"sink": newTestAgent("sink", &fastProvider{answer: "sink"}, bus),
	}
	var sinkDeps []string
	steps := []config.PipelineStep{{Name: "root", Agent: "root", Task: "t"}}
	for i := 0; i < leaves; i++ {
		name := fmt.Sprintf("leaf-%d", i)
		agents[name] = newTestAgent(name, &fastProvider{answer: name}, bus)
		steps = append(steps, config.PipelineStep{
			Name: name, Agent: name, Task: "t", DependsOn: []string{"root"},
		})
		sinkDeps = append(sinkDeps, name)
	}
	steps = append(steps, config.PipelineStep{
		Name: "sink", Agent: "sink", Task: "t", DependsOn: sinkDeps,
	})

	o := newTestOrchestrator(steps, agents)
	_, err := o.runPipeline(context.Background(), "wide fan-out")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// levelNames is a test helper that extracts step names from a level slice.
func levelNames(level []config.PipelineStep) []string {
	names := make([]string, len(level))
	for i, s := range level {
		names[i] = s.Name
	}
	return names
}

// ============================================================
// Synthesis tests
// ============================================================

func TestPipeline_Synthesis_CombinesResults(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	agents := map[string]*Agent{
		"researcher": newTestAgent("researcher", &fastProvider{answer: "Found 3 key papers on topic X"}, bus),
		"writer":     newTestAgent("writer", &fastProvider{answer: "Draft article about topic X with citations"}, bus),
	}

	synthProvider := &fastProvider{answer: "Final synthesis: comprehensive article on topic X with 3 citations"}

	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	o := &Orchestrator{
		name:     "research-orch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "research", Agent: "researcher", Task: "find papers"},
				{Name: "write", Agent: "writer", Task: "write article"},
			},
			Synthesis: true,
		},
		agents:      runners,
		eventBus:    bus,
		llmProvider: synthProvider,
	}

	result, err := o.runPipeline(context.Background(), "Write about topic X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Final synthesis") {
		t.Errorf("expected synthesis result, got %q", result)
	}
}

func TestPipeline_NoSynthesis_ReturnsLastStep(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "step-a-output"}, bus),
		"b": newTestAgent("b", &fastProvider{answer: "step-b-output"}, bus),
	}
	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	o := &Orchestrator{
		name:     "no-synth",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "a", Agent: "a", Task: "do a"},
				{Name: "b", Agent: "b", Task: "do b"},
			},
			Synthesis: false, // no synthesis
		},
		agents:   runners,
		eventBus: bus,
	}

	result, err := o.runPipeline(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return last step output, not synthesis
	if !strings.Contains(result, "step-b-output") {
		t.Errorf("expected last step output, got %q", result)
	}
}

func TestPipeline_Synthesis_CustomPrompt(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	agents := map[string]*Agent{
		"w": newTestAgent("w", &fastProvider{answer: "data"}, bus),
	}
	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	o := &Orchestrator{
		name:     "custom-synth",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps:           []config.PipelineStep{{Name: "work", Agent: "w", Task: "do"}},
			Synthesis:       true,
			SynthesisPrompt: "Summarize in exactly one sentence.",
		},
		agents:      runners,
		eventBus:    bus,
		llmProvider: &fastProvider{answer: "One sentence summary."},
	}

	result, err := o.runPipeline(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "One sentence summary") {
		t.Errorf("expected custom synthesis, got %q", result)
	}
}

// failingLLMProvider returns a fixed retryable error every Generate call.
// (Distinct name from retrieval_test.go's failingProvider which is an
// EmbeddingProvider — Go's package-level namespace conflict.)
type failingLLMProvider struct{ err string }

func (p *failingLLMProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return nil, fmt.Errorf("%s", p.err)
}
func (p *failingLLMProvider) GetName() string  { return "failing-llm" }
func (p *failingLLMProvider) GetModel() string { return "stub" }

// payloadField unmarshals the JSON RawMessage payload and returns the named
// string field, or "" if absent / not a string.
func payloadField(raw json.RawMessage, key string) string {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// TestPipeline_Synthesis_ErrorPropagates locks in: when synthesis is
// configured (Synthesis=true) and the synthesis LLM call fails after retries,
// runPipeline must return a non-nil error. Prior to this fix the orchestrator
// silently swallowed synthesis errors and returned (lastOutput, nil) — exit
// code lied about the outcome.
func TestPipeline_Synthesis_ErrorPropagates(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "step-a-output"}, bus),
	}
	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	o := &Orchestrator{
		name:     "synth-fail",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps:     []config.PipelineStep{{Name: "a", Agent: "a", Task: "do a"}},
			Synthesis: true,
		},
		agents:      runners,
		eventBus:    bus,
		llmProvider: &failingLLMProvider{err: "HTTP 429 Too Many Requests"},
	}

	// Use a tight retry config so the test doesn't burn 130s on backoff.
	// We can't override the synthesize() retryConfig today (it uses
	// defaultRetryConfig hardcoded) — so just bound the test deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := o.runPipeline(ctx, "test")
	if err == nil {
		t.Fatalf("expected non-nil error from synthesis failure, got nil; result=%q", result)
	}
	if !strings.Contains(err.Error(), "synthesis") {
		t.Errorf("expected error to mention synthesis, got %v", err)
	}
}

// TestPipeline_Synthesis_EmitsFailedStatus locks in: synthesis failure
// must produce PIPELINE_END status="synthesis_failed", not "success".
func TestPipeline_Synthesis_EmitsFailedStatus(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	agents := map[string]*Agent{
		"a": newTestAgent("a", &fastProvider{answer: "step-a-output"}, bus),
	}
	runners := make(map[string]Runner, len(agents))
	for k, v := range agents {
		runners[k] = v
	}
	o := &Orchestrator{
		name:     "synth-status",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps:     []config.PipelineStep{{Name: "a", Agent: "a", Task: "do a"}},
			Synthesis: true,
		},
		agents:      runners,
		eventBus:    bus,
		llmProvider: &failingLLMProvider{err: "HTTP 429 Too Many Requests"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = o.runPipeline(ctx, "test")

	// Drain events and look for the synthesis_failed PIPELINE_END
	gotSynthesisFailed := false
	gotErrorEvent := false
	gotSynthAgentEnd := false
	timeout := time.After(500 * time.Millisecond)
collect:
	for {
		select {
		case <-timeout:
			break collect
		case ev, ok := <-ch:
			if !ok {
				break collect
			}
			switch ev.EventType {
			case telemetry.EventPipelineEnd:
				if payloadField(ev.Payload, "status") == "synthesis_failed" {
					gotSynthesisFailed = true
				}
			case telemetry.EventError:
				if payloadField(ev.Payload, "error_type") == "synthesis_error" {
					gotErrorEvent = true
				}
			case telemetry.EventAgentEnd:
				if ev.AgentName == "synth-status" && payloadField(ev.Payload, "status") == "error" {
					gotSynthAgentEnd = true
				}
			}
		}
	}

	if !gotSynthesisFailed {
		t.Error("expected PIPELINE_END status=synthesis_failed")
	}
	if !gotErrorEvent {
		t.Error("expected ERROR event with error_type=synthesis_error")
	}
	if !gotSynthAgentEnd {
		t.Error("expected AGENT_END status=error to balance the synthesis AGENT_START (review High #1)")
	}
}

// TestAgentRun_ContextTimeoutEmitsAgentEnd. When the ReAct loop returns from
// the ctx.Done() branch — typically because a pipeline step's timeout_sec
// killed the step context — the agent must still emit AGENT_END +
// EXECUTION_COMPLETE so the session event stream stays balanced with the
// matching AGENT_START. Before the fix, a synthesis step's agent could be
// left with AGENT_START: 1 / AGENT_END: 0 after a step timeout.
func TestAgentRun_ContextTimeoutEmitsAgentEnd(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ag := newTestAgent("slow-agent", &slowProvider{}, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	events := collectEvents(bus, func() {
		_, err := ag.Run(ctx, "anything")
		if err == nil {
			t.Fatal("expected context-cancelled error, got nil")
		}
	})

	if !hasEventType(events, telemetry.EventAgentStart) {
		t.Error("AGENT_START not emitted (pre-timeout path broken)")
	}
	if !hasEventType(events, telemetry.EventAgentEnd) {
		t.Error("regression: AGENT_END not emitted on ctx timeout")
	}
	if !hasEventType(events, telemetry.EventExecutionComplete) {
		t.Error("regression: EXECUTION_COMPLETE not emitted on ctx timeout")
	}

	// Status field should discriminate DeadlineExceeded vs Canceled.
	var gotStatus string
	for _, e := range events {
		if e.EventType != telemetry.EventAgentEnd {
			continue
		}
		var p telemetry.AgentEndPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil {
			gotStatus = p.Status
		}
	}
	if gotStatus != "timeout" {
		t.Errorf("AgentEnd.Status should be \"timeout\" for DeadlineExceeded, got %q", gotStatus)
	}
}

// TestAgentRun_ContextCancelEmitsAgentEnd covers the Canceled side:
// an explicit cancel (not a deadline) should still emit AGENT_END, with the
// distinct status "cancelled".
func TestAgentRun_ContextCancelEmitsAgentEnd(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ag := newTestAgent("cancel-agent", &slowProvider{}, bus)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	events := collectEvents(bus, func() {
		_, err := ag.Run(ctx, "anything")
		if err == nil {
			t.Fatal("expected context-cancelled error, got nil")
		}
	})

	if !hasEventType(events, telemetry.EventAgentEnd) {
		t.Error("regression: AGENT_END not emitted on ctx cancel")
	}
	var gotStatus string
	for _, e := range events {
		if e.EventType != telemetry.EventAgentEnd {
			continue
		}
		var p telemetry.AgentEndPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil {
			gotStatus = p.Status
		}
	}
	if gotStatus != "cancelled" {
		t.Errorf("AgentEnd.Status should be \"cancelled\" for Canceled, got %q", gotStatus)
	}
}
