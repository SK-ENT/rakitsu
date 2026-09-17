package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// TestIntegration_NestedOrchestrator_FullStack simulates the full nested orchestrator
// flow as it would run from CLI or web UI:
// ProjectLead (Pipeline) → BackendPipeline (Pipeline) → agents → FrontendPipeline (Pipeline) → agents → Integrator
func TestIntegration_NestedOrchestrator_FullStack(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	// Track which agents ran and in what order
	var runOrder []string
	makeProvider := func(name, answer string) llm.LLMProvider {
		return &trackingProvider{name: name, answer: answer, ran: &runOrder}
	}

	// Build agents (same as nested-orchestrator-fullstack example)
	agentDefs := []struct {
		name   string
		answer string
	}{
		{"BackendDesigner", "API design: /users, /items endpoints with PostgreSQL"},
		{"BackendDeveloper", "Implemented REST API in Go with GORM"},
		{"BackendTester", "All 12 backend tests pass, no regressions"},
		{"FrontendDesigner", "UI wireframes: dashboard, login, item list pages"},
		{"FrontendDeveloper", "Built React frontend with Tailwind CSS"},
		{"FrontendTester", "Lighthouse score 95, all accessibility checks pass"},
		{"Integrator", "API integration verified: all frontend→backend calls working"},
	}

	agentMap := make(map[string]*Agent)
	for _, ad := range agentDefs {
		def := &config.AgentDefinition{
			Name:         ad.name,
			Role:         "worker",
			SystemPrompt: "test agent",
		}
		ag := NewAgent(def, makeProvider(ad.name, ad.answer), tools.NewToolRegistry(), bus, nil)
		agentMap[ad.name] = ag
	}

	// Build runners map
	runners := make(map[string]Runner, len(agentMap))
	for k, v := range agentMap {
		runners[k] = v
	}

	// Create BackendPipeline sub-orchestrator
	backendRunners := map[string]Runner{
		"BackendDesigner":  agentMap["BackendDesigner"],
		"BackendDeveloper": agentMap["BackendDeveloper"],
		"BackendTester":    agentMap["BackendTester"],
	}
	backendOrch := NewOrchestrator(&config.OrchestratorConfig{
		Name:     "BackendPipeline",
		Strategy: "Pipeline",
		Agents:   []string{"BackendDesigner", "BackendDeveloper", "BackendTester"},
		Pipeline: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "design", Agent: "BackendDesigner", Task: "Design the backend API"},
				{Name: "implement", Agent: "BackendDeveloper", Task: "Implement the backend"},
				{Name: "test", Agent: "BackendTester", Task: "Test the backend"},
			},
		},
	}, makeProvider("BackendPipeline", ""), bus, backendRunners)

	// Create FrontendPipeline sub-orchestrator
	frontendRunners := map[string]Runner{
		"FrontendDesigner":  agentMap["FrontendDesigner"],
		"FrontendDeveloper": agentMap["FrontendDeveloper"],
		"FrontendTester":    agentMap["FrontendTester"],
	}
	frontendOrch := NewOrchestrator(&config.OrchestratorConfig{
		Name:     "FrontendPipeline",
		Strategy: "Pipeline",
		Agents:   []string{"FrontendDesigner", "FrontendDeveloper", "FrontendTester"},
		Pipeline: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "design", Agent: "FrontendDesigner", Task: "Design the frontend UI"},
				{Name: "implement", Agent: "FrontendDeveloper", Task: "Implement the frontend"},
				{Name: "test", Agent: "FrontendTester", Task: "Test the frontend"},
			},
		},
	}, makeProvider("FrontendPipeline", ""), bus, frontendRunners)

	// Add sub-orchestrators to runners
	runners["BackendPipeline"] = backendOrch
	runners["FrontendPipeline"] = frontendOrch

	// Create root orchestrator (ProjectLead)
	rootOrch := NewOrchestrator(&config.OrchestratorConfig{
		Name:     "ProjectLead",
		Strategy: "Pipeline",
		Agents:   []string{"BackendPipeline", "FrontendPipeline", "Integrator"},
		Pipeline: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{Name: "backend", Agent: "BackendPipeline", Task: "Build the backend"},
				{Name: "frontend", Agent: "FrontendPipeline", Task: "Build the frontend"},
				{Name: "integrate", Agent: "Integrator", Task: "Integrate backend and frontend"},
			},
		},
	}, makeProvider("ProjectLead", ""), bus, runners)

	// Run
	result, err := rootOrch.Run(context.Background(), "Build a weather dashboard app")
	if err != nil {
		t.Fatalf("nested orchestrator run failed: %v", err)
	}

	// Verify execution order: backend team first, then frontend, then integrator
	expectedOrder := []string{
		"BackendDesigner", "BackendDeveloper", "BackendTester",
		"FrontendDesigner", "FrontendDeveloper", "FrontendTester",
		"Integrator",
	}

	if len(runOrder) != len(expectedOrder) {
		t.Fatalf("expected %d agents to run, got %d: %v", len(expectedOrder), len(runOrder), runOrder)
	}
	for i, expected := range expectedOrder {
		if runOrder[i] != expected {
			t.Errorf("step %d: expected %q, got %q (full order: %v)", i, expected, runOrder[i], runOrder)
		}
	}

	// Verify final result contains integrator's output
	if !strings.Contains(result, "integration verified") {
		t.Errorf("final result should contain integrator output, got: %q", result)
	}

	t.Logf("Execution order: %v", runOrder)
	t.Logf("Final result: %.100s...", result)
}

// TestIntegration_NestedOrchestrator_ParallelSubTeams verifies that sub-orchestrators
// inside a parallel step run concurrently.
func TestIntegration_NestedOrchestrator_ParallelSubTeams(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	var runOrder []string
	makeProvider := func(name, answer string) llm.LLMProvider {
		return &trackingProvider{name: name, answer: answer, ran: &runOrder}
	}

	// Backend sub-orchestrator
	backAg := NewAgent(&config.AgentDefinition{Name: "BackDev", Role: "worker", SystemPrompt: "dev"}, makeProvider("BackDev", "backend-ok"), tools.NewToolRegistry(), bus, nil)
	backOrch := NewOrchestrator(&config.OrchestratorConfig{
		Name: "BackTeam", Strategy: "Pipeline",
		Agents: []string{"BackDev"},
		Pipeline: &config.PipelineConfig{Steps: []config.PipelineStep{{Name: "dev", Agent: "BackDev", Task: "code"}}},
	}, makeProvider("BackTeam", ""), bus, map[string]Runner{"BackDev": backAg})

	// Frontend sub-orchestrator
	frontAg := NewAgent(&config.AgentDefinition{Name: "FrontDev", Role: "worker", SystemPrompt: "dev"}, makeProvider("FrontDev", "frontend-ok"), tools.NewToolRegistry(), bus, nil)
	frontOrch := NewOrchestrator(&config.OrchestratorConfig{
		Name: "FrontTeam", Strategy: "Pipeline",
		Agents: []string{"FrontDev"},
		Pipeline: &config.PipelineConfig{Steps: []config.PipelineStep{{Name: "dev", Agent: "FrontDev", Task: "code"}}},
	}, makeProvider("FrontTeam", ""), bus, map[string]Runner{"FrontDev": frontAg})

	// Root with parallel step
	root := NewOrchestrator(&config.OrchestratorConfig{
		Name: "Lead", Strategy: "Pipeline",
		Agents: []string{"BackTeam", "FrontTeam"},
		Pipeline: &config.PipelineConfig{
			Steps: []config.PipelineStep{
				{
					Name: "dev-phase", Type: "parallel",
					Steps: []config.PipelineStep{
						{Name: "back", Agent: "BackTeam", Task: "build backend"},
						{Name: "front", Agent: "FrontTeam", Task: "build frontend"},
					},
				},
			},
		},
	}, makeProvider("Lead", ""), bus, map[string]Runner{"BackTeam": backOrch, "FrontTeam": frontOrch})

	result, err := root.Run(context.Background(), "build app")
	if err != nil {
		t.Fatalf("parallel nested run failed: %v", err)
	}

	// Both should have run
	if !strings.Contains(result, "backend-ok") || !strings.Contains(result, "frontend-ok") {
		t.Errorf("expected both sub-team outputs, got: %q", result)
	}

	// Both agents should have run (order may vary due to parallelism)
	if len(runOrder) != 2 {
		t.Errorf("expected 2 agents to run, got %d: %v", len(runOrder), runOrder)
	}
	t.Logf("Parallel execution order: %v", runOrder)
}

// TestIntegration_ConfigLoad_NestedOrchestrator tests loading the actual YAML config
// file and verifying that orchestrators are parsed correctly.
func TestIntegration_ConfigLoad_NestedOrchestrator(t *testing.T) {
	const fixture = "../../examples/single/07-nested-orchestrators/config.yaml"
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Skipf("skipping: %s not present in this checkout (e.g. a partial snapshot without examples/)", fixture)
	}

	cfg, err := config.Load(fixture)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Verify main orchestrator
	if cfg.Orchestrator == nil {
		t.Fatal("expected orchestrator to be set")
	}
	if cfg.Orchestrator.Name != "ProjectLead" {
		t.Errorf("expected main orchestrator 'ProjectLead', got %q", cfg.Orchestrator.Name)
	}

	// Verify sub-orchestrators (inline + auto-discovered from orchestrators/ dir)
	if len(cfg.Orchestrators) < 2 {
		t.Fatalf("expected at least 2 sub-orchestrators, got %d", len(cfg.Orchestrators))
	}

	orchNames := make(map[string]bool)
	for _, o := range cfg.Orchestrators {
		orchNames[o.Name] = true
	}
	if !orchNames["BackendPipeline"] {
		t.Error("missing sub-orchestrator BackendPipeline")
	}
	if !orchNames["FrontendPipeline"] {
		t.Error("missing sub-orchestrator FrontendPipeline")
	}

	// Verify agents
	if len(cfg.Agents) < 7 {
		t.Errorf("expected at least 7 agents, got %d", len(cfg.Agents))
	}

	// Verify main orchestrator references sub-orchestrators
	mainAgents := cfg.Orchestrator.Agents
	hasBackend := false
	hasFrontend := false
	for _, a := range mainAgents {
		if a == "BackendPipeline" {
			hasBackend = true
		}
		if a == "FrontendPipeline" {
			hasFrontend = true
		}
	}
	if !hasBackend || !hasFrontend {
		t.Errorf("main orchestrator agents should include BackendPipeline and FrontendPipeline, got %v", mainAgents)
	}

	t.Logf("Config loaded: %d agents, %d sub-orchestrators, main=%s",
		len(cfg.Agents), len(cfg.Orchestrators), cfg.Orchestrator.Name)
}

// trackingProvider records which agent used it and returns a fixed answer.
type trackingProvider struct {
	name   string
	answer string
	ran    *[]string
}

func (p *trackingProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	*p.ran = append(*p.ran, p.name)
	return &llm.GenerateResult{
		Response:     fmt.Sprintf("[%s] %s", p.name, p.answer),
		FinishReason: "stop",
		TokenUsage:   &llm.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
	}, nil
}
func (p *trackingProvider) GetName() string  { return "tracking" }
func (p *trackingProvider) GetModel() string { return "mock" }
