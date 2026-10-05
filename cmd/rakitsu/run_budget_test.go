package main

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

type budgetWiringProvider struct {
	model string
	in    int
	out   int
	calls int
}

func (p *budgetWiringProvider) Generate(context.Context, string, []llm.Message, []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.calls++
	return &llm.GenerateResult{
		Response:     "finished",
		TokenUsage:   &llm.TokenUsage{InputTokens: p.in, OutputTokens: p.out, TotalTokens: p.in + p.out},
		FinishReason: "stop",
	}, nil
}

func (p *budgetWiringProvider) GetName() string  { return "budget-test" }
func (p *budgetWiringProvider) GetModel() string { return p.model }

func runWithWiredBudget(t *testing.T, cfg *config.Config, provider *budgetWiringProvider, maxCostOverride float64) string {
	t.Helper()
	def := &cfg.Agents[0]
	ag := agent.NewAgent(def, provider, tools.NewToolRegistry(), telemetry.NewEventBus(32), nil)
	wireAgentBudget(ag, cfg, def, provider.GetModel(), newRunRootGuard(cfg, maxCostOverride))
	result, err := ag.Run(context.Background(), "test query")
	if err != nil {
		t.Fatalf("Agent.Run: %v", err)
	}
	return result
}

func TestWireAgentBudget_EnforcesGlobalTokenLimit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.Execution.MaxTotalTokens = 5
	cfg.Agents = []config.AgentDefinition{{Name: "solo", Role: "worker"}}
	provider := &budgetWiringProvider{model: "test-model", in: 10, out: 5}

	result := runWithWiredBudget(t, cfg, provider, 0)
	if !strings.Contains(result, "budget exceeded") {
		t.Fatalf("result = %q, want budget exceeded", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestWireAgentBudget_EnforcesAgentTokenLimit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents = []config.AgentDefinition{{
		Name: "solo", Role: "worker",
		Settings: &config.AgentSettings{MaxTotalTokens: 5},
	}}
	provider := &budgetWiringProvider{model: "test-model", in: 10, out: 5}

	result := runWithWiredBudget(t, cfg, provider, 0)
	if !strings.Contains(result, "budget exceeded") {
		t.Fatalf("result = %q, want budget exceeded", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestWireAgentBudget_EnforcesMaxCostOverrideWithoutConfigBudget(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.Pricing = map[string]config.PricingConfig{
		"test-model": {Input: 1000, Output: 1000},
	}
	cfg.Agents = []config.AgentDefinition{{Name: "solo", Role: "worker"}}
	provider := &budgetWiringProvider{model: "test-model", in: 100, out: 0}

	result := runWithWiredBudget(t, cfg, provider, 0.05)
	if !strings.Contains(result, "budget exceeded") {
		t.Fatalf("result = %q, want cost budget exceeded", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}
