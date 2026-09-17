package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// ============================================================
// Runner interface compliance
// ============================================================

func TestAgent_ImplementsRunner(t *testing.T) {
	var _ Runner = (*Agent)(nil)
}

func TestOrchestrator_ImplementsRunner(t *testing.T) {
	var _ Runner = (*Orchestrator)(nil)
}

// ============================================================
// Orchestrator getter methods
// ============================================================

func TestOrchestrator_GetName(t *testing.T) {
	o := &Orchestrator{name: "lead"}
	if o.GetName() != "lead" {
		t.Errorf("GetName() = %q, want %q", o.GetName(), "lead")
	}
}

func TestOrchestrator_GetRole(t *testing.T) {
	o := &Orchestrator{}
	if o.GetRole() != RoleSupervisor {
		t.Errorf("GetRole() = %q, want %q", o.GetRole(), RoleSupervisor)
	}
}

// ============================================================
// Nested orchestrator tests
// ============================================================

// TestNestedOrchestrator_PipelineWithSubOrchestrator verifies that a parent
// orchestrator can delegate to a sub-orchestrator (which runs its own pipeline).
func TestNestedOrchestrator_PipelineWithSubOrchestrator(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	// Create leaf agents
	designer := newTestAgent("Designer", &fastProvider{answer: "design-doc"}, bus)
	implementer := newTestAgent("Implementer", &fastProvider{answer: "code-impl"}, bus)
	tester := newTestAgent("Tester", &fastProvider{answer: "tests-pass"}, bus)

	// Create sub-orchestrator (BackendPipeline) with its own pipeline
	subRunners := map[string]Runner{
		"Designer":    designer,
		"Implementer": implementer,
		"Tester":      tester,
	}
	subOrch := &Orchestrator{
		name:     "BackendPipeline",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "design", Agent: "Designer", Task: "create design"},
				{Name: "implement", Agent: "Implementer", Task: "write code"},
				{Name: "test", Agent: "Tester", Task: "run tests"},
			},
		},
		agents:   subRunners,
		eventBus: bus,
	}

	// Create parent orchestrator that delegates to the sub-orchestrator via pipeline
	parentRunners := map[string]Runner{
		"BackendPipeline": subOrch,
	}
	parent := &Orchestrator{
		name:     "ProjectLead",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "backend", Agent: "BackendPipeline", Task: "build the backend"},
			},
		},
		agents:   parentRunners,
		eventBus: bus,
	}

	result, err := parent.Run(context.Background(), "build a web app")
	if err != nil {
		t.Fatalf("nested orchestrator failed: %v", err)
	}
	// The result should be the output of the last sub-step (Tester)
	if !strings.Contains(result, "tests-pass") {
		t.Errorf("expected result to contain 'tests-pass', got %q", result)
	}
}

// TestNestedOrchestrator_TwoSubOrchestrators verifies parallel sub-orchestrators.
func TestNestedOrchestrator_TwoSubOrchestrators(t *testing.T) {
	bus := telemetry.NewEventBus(128)

	// Backend agents
	backDesigner := newTestAgent("BackDesigner", &fastProvider{answer: "back-design"}, bus)
	backCoder := newTestAgent("BackCoder", &fastProvider{answer: "back-code"}, bus)

	// Frontend agents
	frontDesigner := newTestAgent("FrontDesigner", &fastProvider{answer: "front-design"}, bus)
	frontCoder := newTestAgent("FrontCoder", &fastProvider{answer: "front-code"}, bus)

	// Sub-orchestrators
	backendOrch := &Orchestrator{
		name:     "BackendPipeline",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "back-design", Agent: "BackDesigner", Task: "design backend"},
				{Name: "back-code", Agent: "BackCoder", Task: "code backend"},
			},
		},
		agents: map[string]Runner{
			"BackDesigner": backDesigner,
			"BackCoder":    backCoder,
		},
		eventBus: bus,
	}

	frontendOrch := &Orchestrator{
		name:     "FrontendPipeline",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "front-design", Agent: "FrontDesigner", Task: "design frontend"},
				{Name: "front-code", Agent: "FrontCoder", Task: "code frontend"},
			},
		},
		agents: map[string]Runner{
			"FrontDesigner": frontDesigner,
			"FrontCoder":    frontCoder,
		},
		eventBus: bus,
	}

	// Parent pipeline: backend then frontend (sequential)
	parent := &Orchestrator{
		name:     "ProjectLead",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "backend", Agent: "BackendPipeline", Task: "build backend"},
				{Name: "frontend", Agent: "FrontendPipeline", Task: "build frontend"},
			},
		},
		agents: map[string]Runner{
			"BackendPipeline":  backendOrch,
			"FrontendPipeline": frontendOrch,
		},
		eventBus: bus,
	}

	result, err := parent.Run(context.Background(), "build full stack")
	if err != nil {
		t.Fatalf("nested orchestrator failed: %v", err)
	}
	if !strings.Contains(result, "front-code") {
		t.Errorf("expected final result to contain 'front-code', got %q", result)
	}
}

// TestNestedOrchestrator_ParallelSubOrchestrators verifies sub-orchestrators
// running concurrently inside a parallel step.
func TestNestedOrchestrator_ParallelSubOrchestrators(t *testing.T) {
	bus := telemetry.NewEventBus(128)

	makeSubOrch := func(name, agentName, answer string) *Orchestrator {
		ag := newTestAgent(agentName, &fastProvider{answer: answer}, bus)
		return &Orchestrator{
			name:     name,
			strategy: "Pipeline",
			pipelineConfig: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{Name: "work", Agent: agentName, Task: "do work"},
				},
			},
			agents:   map[string]Runner{agentName: ag},
			eventBus: bus,
		}
	}

	backendOrch := makeSubOrch("BackendPipeline", "BackDev", "backend-done")
	frontendOrch := makeSubOrch("FrontendPipeline", "FrontDev", "frontend-done")

	// Parent with parallel step containing both sub-orchestrators
	parent := &Orchestrator{
		name:     "ProjectLead",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{
					Name: "development",
					Type: "parallel",
					Steps: []config.PipelineStep{
						{Name: "backend", Agent: "BackendPipeline", Task: "build backend"},
						{Name: "frontend", Agent: "FrontendPipeline", Task: "build frontend"},
					},
				},
			},
		},
		agents: map[string]Runner{
			"BackendPipeline":  backendOrch,
			"FrontendPipeline": frontendOrch,
		},
		eventBus: bus,
	}

	result, err := parent.Run(context.Background(), "build all")
	if err != nil {
		t.Fatalf("parallel nested orchestrator failed: %v", err)
	}
	if !strings.Contains(result, "backend-done") || !strings.Contains(result, "frontend-done") {
		t.Errorf("expected both sub-orchestrator outputs, got %q", result)
	}
}

// TestNestedOrchestrator_ThreeLevels verifies 3-level deep nesting:
// root → mid-orchestrator → leaf-orchestrator → agent
func TestNestedOrchestrator_ThreeLevels(t *testing.T) {
	bus := telemetry.NewEventBus(128)

	// Level 3: leaf agent
	worker := newTestAgent("Worker", &fastProvider{answer: "leaf-result"}, bus)

	// Level 2: leaf orchestrator
	leafOrch := &Orchestrator{
		name:     "LeafOrch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "work", Agent: "Worker", Task: "do leaf work"},
			},
		},
		agents:   map[string]Runner{"Worker": worker},
		eventBus: bus,
	}

	// Level 1: mid orchestrator
	midOrch := &Orchestrator{
		name:     "MidOrch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "delegate", Agent: "LeafOrch", Task: "run leaf"},
			},
		},
		agents:   map[string]Runner{"LeafOrch": leafOrch},
		eventBus: bus,
	}

	// Level 0: root orchestrator
	root := &Orchestrator{
		name:     "Root",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "top", Agent: "MidOrch", Task: "run mid"},
			},
		},
		agents:   map[string]Runner{"MidOrch": midOrch},
		eventBus: bus,
	}

	result, err := root.Run(context.Background(), "deep nesting test")
	if err != nil {
		t.Fatalf("3-level nesting failed: %v", err)
	}
	if !strings.Contains(result, "leaf-result") {
		t.Errorf("expected 'leaf-result' from deep nesting, got %q", result)
	}
}

// ============================================================
// Stress tests
// ============================================================

// TestStress_NestedOrchestrators runs many nested orchestrator pipelines
// concurrently to verify race-free behavior.
func TestStress_NestedOrchestrators(t *testing.T) {
	const N = 20
	var wg sync.WaitGroup
	var successes atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(id int) {
			defer wg.Done()
			bus := telemetry.NewEventBus(64)

			agName := fmt.Sprintf("worker-%d", id)
			ag := newTestAgent(agName, &fastProvider{answer: fmt.Sprintf("result-%d", id)}, bus)

			subOrch := &Orchestrator{
				name:     fmt.Sprintf("sub-%d", id),
				strategy: "Pipeline",
				pipelineConfig: &config.PipelineConfig{
					Steps: []config.PipelineStep{
						{Name: "work", Agent: agName, Task: "go"},
					},
				},
				agents:   map[string]Runner{agName: ag},
				eventBus: bus,
			}

			parent := &Orchestrator{
				name:     fmt.Sprintf("parent-%d", id),
				strategy: "Pipeline",
				pipelineConfig: &config.PipelineConfig{
					Steps: []config.PipelineStep{
						{Name: "delegate", Agent: fmt.Sprintf("sub-%d", id), Task: "run sub"},
					},
				},
				agents:   map[string]Runner{fmt.Sprintf("sub-%d", id): subOrch},
				eventBus: bus,
			}

			result, err := parent.Run(context.Background(), "stress")
			if err == nil && strings.Contains(result, fmt.Sprintf("result-%d", id)) {
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
// NewOrchestrator constructor test
// ============================================================

func TestNewOrchestrator_AcceptsRunnerMap(t *testing.T) {
	bus := telemetry.NewEventBus(16)

	// Verify NewOrchestrator accepts map[string]Runner with mixed types
	ag := newTestAgent("Worker", &fastProvider{answer: "ok"}, bus)
	subOrch := &Orchestrator{
		name:     "SubOrch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "w", Agent: "Worker", Task: "t"},
			},
		},
		agents:   map[string]Runner{"Worker": ag},
		eventBus: bus,
	}

	runners := map[string]Runner{
		"Worker":  ag,
		"SubOrch": subOrch,
	}

	orchCfg := &config.OrchestratorConfig{
		Name:     "Main",
		Strategy: "Pipeline",
		Agents:   []string{"Worker", "SubOrch"},
		Pipeline: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "step1", Agent: "Worker", Task: "first"},
				{Name: "step2", Agent: "SubOrch", Task: "second"},
			},
		},
	}

	orch := NewOrchestrator(orchCfg, &fastProvider{answer: "ok"}, bus, runners)
	if orch.GetName() != "Main" {
		t.Errorf("GetName() = %q, want %q", orch.GetName(), "Main")
	}

	result, err := orch.Run(context.Background(), "mixed runners")
	if err != nil {
		t.Fatalf("mixed runner orchestrator failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ============================================================
// Config test: orchestrators array
// ============================================================

func TestConfig_OrchestratorsParsing(t *testing.T) {
	// Verify the Orchestrators field exists and is properly typed
	cfg := config.Config{
		Orchestrators: []config.OrchestratorConfig{
			{
				Name:     "SubOrch1",
				Strategy: "Pipeline",
				Agents:   []string{"A", "B"},
			},
			{
				Name:     "SubOrch2",
				Strategy: "Hierarchical",
				Agents:   []string{"C"},
			},
		},
	}
	if len(cfg.Orchestrators) != 2 {
		t.Errorf("expected 2 sub-orchestrators, got %d", len(cfg.Orchestrators))
	}
	if cfg.Orchestrators[0].Name != "SubOrch1" {
		t.Errorf("expected SubOrch1, got %q", cfg.Orchestrators[0].Name)
	}
}

// ============================================================
// DelegationTool with Runner
// ============================================================

func TestDelegationTool_WithSubOrchestrator(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	// Create a sub-orchestrator that acts as a delegation target
	worker := newTestAgent("Worker", &fastProvider{answer: "delegated-result"}, bus)
	subOrch := &Orchestrator{
		name:     "SubOrch",
		strategy: "Pipeline",
		pipelineConfig: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "work", Agent: "Worker", Task: "do it"},
			},
		},
		agents:   map[string]Runner{"Worker": worker},
		eventBus: bus,
	}

	// Create a delegation tool targeting the sub-orchestrator
	dt := &DelegationTool{
		agent:     subOrch,
		eventBus:  bus,
		fromAgent: "Supervisor",
	}

	if dt.GetName() != "delegate_to_suborch" {
		t.Errorf("GetName() = %q, want %q", dt.GetName(), "delegate_to_suborch")
	}

	result, err := dt.Execute(context.Background(), map[string]interface{}{
		"task": "build something",
	})
	if err != nil {
		t.Fatalf("delegation to sub-orchestrator failed: %v", err)
	}
	if !strings.Contains(result, "delegated-result") {
		t.Errorf("expected 'delegated-result', got %q", result)
	}
}

// ============================================================
// Delegation tool name collisions
// ============================================================

// TestOrchestrator_SanitizedToolNameCollision_BothAgentsStayReachable
// regression-guards: distinct agent names that sanitize to the same tool
// name (e.g. "Foo.Bar" and "foo_bar" both become "foo_bar") used to let the
// second-registered agent silently overwrite the first's delegation tool —
// ToolRegistry.RegisterTool only logs on a same-name overwrite, it doesn't
// error — leaving the first agent permanently unreachable via delegation
// with nothing surfacing the collision anywhere. registerDelegationTools,
// getDelegationToolNames, and buildSystemPrompt must now all agree on a
// disambiguated (_2, _3, ...) name for the second agent, so both stay
// reachable and advertised consistently.
func TestOrchestrator_SanitizedToolNameCollision_BothAgentsStayReachable(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	agentA := &salvageFakeAgent{name: "Foo.Bar", answer: "from-a"}
	agentB := &salvageFakeAgent{name: "foo_bar", answer: "from-b"}

	orch := &Orchestrator{
		name:         "Supervisor",
		strategy:     "Hierarchical",
		systemPrompt: "base prompt",
		agentNames:   []string{"Foo.Bar", "foo_bar"},
		agents: map[string]Runner{
			"Foo.Bar": agentA,
			"foo_bar": agentB,
		},
		eventBus: bus,
	}

	// Both sanitize to "foo_bar" alone; resolvedToolNames must disambiguate.
	resolved := orch.resolvedToolNames()
	nameA, nameB := resolved["Foo.Bar"], resolved["foo_bar"]
	if nameA == "" || nameB == "" {
		t.Fatalf("resolvedToolNames() missing an entry: %+v", resolved)
	}
	if nameA == nameB {
		t.Fatalf("resolvedToolNames() gave both agents the same tool name %q — one is unreachable", nameA)
	}

	// getDelegationToolNames must return two distinct, "delegate_to_"-prefixed names.
	toolList := orch.getDelegationToolNames()
	if len(toolList) != 2 || toolList[0] == toolList[1] {
		t.Fatalf("getDelegationToolNames() = %v, want 2 distinct entries", toolList)
	}

	// registerDelegationTools must register both agents under distinct,
	// independently resolvable tool names — neither RegisterTool call may
	// overwrite the other.
	registry := tools.NewToolRegistry()
	orch.registerDelegationTools(registry, &orchestratorRunState{})

	toolA := registry.GetTool("delegate_to_" + nameA)
	toolB := registry.GetTool("delegate_to_" + nameB)
	if toolA == nil || toolB == nil {
		t.Fatalf("registry missing a tool: delegate_to_%s=%v delegate_to_%s=%v", nameA, toolA != nil, nameB, toolB != nil)
	}
	dtA, okA := toolA.(*DelegationTool)
	dtB, okB := toolB.(*DelegationTool)
	if !okA || !okB {
		t.Fatalf("registered tools are not *DelegationTool: %T, %T", toolA, toolB)
	}
	if dtA.agent.GetName() != "Foo.Bar" || dtB.agent.GetName() != "foo_bar" {
		t.Errorf("registered tools point at the wrong agents: %q, %q", dtA.agent.GetName(), dtB.agent.GetName())
	}

	// buildSystemPrompt must advertise the same two resolved names, not the
	// raw (colliding) sanitizeToolName output — otherwise the LLM would be
	// told about a tool name that was never actually registered.
	prompt := orch.buildSystemPrompt()
	if !strings.Contains(prompt, "delegate_to_"+nameA) || !strings.Contains(prompt, "delegate_to_"+nameB) {
		t.Errorf("buildSystemPrompt() doesn't mention both resolved tool names %q/%q:\n%s", nameA, nameB, prompt)
	}
}

// TestOrchestrator_DisambiguationSuffixCanItselfCollide regression-guards a
// second-order version of the bug above: resolvedToolNames' seen[base]
// counter only tracks occurrences of the pre-disambiguation base string, so
// a suffixed name (base_2) was never checked against the set of already-
// assigned final names. "foo.bar" and "foo_bar" both sanitize to "foo_bar";
// the second one gets disambiguated to "foo_bar_2" — which collides with a
// third agent, "Foo Bar 2", whose own natural sanitized name is also
// "foo_bar_2". All three pass config's exact-duplicate-name check
// individually (their raw names are all distinct), so this must be caught
// here or a delegation tool silently vanishes exactly like the base case.
func TestOrchestrator_DisambiguationSuffixCanItselfCollide(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	names := []string{"foo.bar", "foo_bar", "Foo Bar 2"}
	agents := make(map[string]Runner, len(names))
	for _, n := range names {
		agents[n] = &salvageFakeAgent{name: n, answer: "ok"}
	}

	orch := &Orchestrator{
		name:         "Supervisor",
		strategy:     "Hierarchical",
		systemPrompt: "base prompt",
		agentNames:   names,
		agents:       agents,
		eventBus:     bus,
	}

	resolved := orch.resolvedToolNames()
	seen := make(map[string]string, len(names))
	for _, agentName := range names {
		toolName := resolved[agentName]
		if toolName == "" {
			t.Fatalf("resolvedToolNames() missing an entry for %q: %+v", agentName, resolved)
		}
		if prior, exists := seen[toolName]; exists {
			t.Fatalf("resolvedToolNames() gave %q and %q the same tool name %q — one is unreachable: %+v",
				prior, agentName, toolName, resolved)
		}
		seen[toolName] = agentName
	}
}
