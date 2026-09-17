package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestUsagePopup_PerAgentRowsAndTotal(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Coordinator": {Model: "gemini-3.5-flash-lite", Provider: "litellm", Tokens: 2000, Cost: 0.0031, PricingKnown: true, MaxTokens: 1000, Turns: 4, TotalIterations: 4, Status: "success"},
			"Researcher":  {Model: "gemini-3.5-flash-lite", Provider: "litellm", Tokens: 4500, Cost: 0.0071, PricingKnown: true, Turns: 2, TotalIterations: 2, Status: "success"},
		},
	}
	out := m.usagePopup()

	for _, want := range []string{"Coordinator", "Researcher", "Rakitsu budget", "gemini-3.5-flash-lite"} {
		if !strings.Contains(out, want) {
			t.Errorf("usagePopup() missing %q, got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "no budget set") {
		t.Errorf("Researcher has no MaxTokens configured, expected \"no budget set\", got:\n%s", out)
	}
	if !strings.Contains(out, "6500") && !strings.Contains(out, "6.5K") {
		t.Errorf("expected a totals row summing 2000+4500 tokens, got:\n%s", out)
	}
}

func TestUsagePopup_PricingNotSet(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Local": {Model: "llama3.3", Tokens: 500, Cost: 0, PricingKnown: false, Status: "success"},
		},
	}
	out := m.usagePopup()
	if !strings.Contains(out, "pricing not set") {
		t.Errorf("expected \"pricing not set\" for a model with PricingKnown=false, got:\n%s", out)
	}
}

// TestUsagePopup_TotalCostUnknownWhenNoPricing is the regression test for the
// totals row fabricating a number: every per-agent row correctly reported
// "pricing not set", then the total confidently printed "$0.0000" — a figure
// no agent had contributed a real price to. Found by manual TUI verification.
func TestUsagePopup_TotalCostUnknownWhenNoPricing(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Coordinator": {Model: "llama3.3", Tokens: 500, PricingKnown: false},
			"Researcher":  {Model: "llama3.3", Tokens: 300, PricingKnown: false},
		},
	}
	out := m.usagePopup()
	if !strings.Contains(out, "unknown (no pricing set)") {
		t.Errorf("totals row should admit an unknown cost, got:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("totals row must not print a fabricated $0.0000, got:\n%s", out)
	}
}

// A mixed run — some agents priced, some not — sums to a real but incomplete
// figure. Printing it bare would understate the true cost silently.
func TestUsagePopup_TotalCostPartialWhenSomePricingMissing(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Priced":   {Model: "gemini-3.5-flash-lite", Tokens: 1000, Cost: 0.0025, PricingKnown: true},
			"Unpriced": {Model: "llama3.3", Tokens: 500, PricingKnown: false},
		},
	}
	out := m.usagePopup()
	if !strings.Contains(out, "1 of 2 agents have no pricing") {
		t.Errorf("totals row should flag the unpriced agent count, got:\n%s", out)
	}
}

// TestFmtBudget_CostOnlyBudget is the regression test for Fix 2: a
// cost-only budget (MaxTokens unconfigured, MaxCost set from YAML
// max_cost) must render a real cost budget, never "no budget set" — the
// agent is still actively budget-enforced and can terminate with
// Status: budget_exceeded purely on the cost dial.
func TestFmtBudget_CostOnlyBudget(t *testing.T) {
	got := fmtBudget(0, 0, 0.42, 1.0)
	if strings.Contains(got, "no budget set") {
		t.Errorf("fmtBudget with MaxCost=1.0 rendered \"no budget set\", got %q", got)
	}
	if !strings.Contains(got, "0.42") || !strings.Contains(got, "1.00") && !strings.Contains(got, "1.0000") {
		t.Errorf("fmtBudget with cost-only budget should show 0.42 and the 1.00 max, got %q", got)
	}
	if !strings.Contains(got, "42%") {
		t.Errorf("fmtBudget with cost-only budget should show 42%%, got %q", got)
	}
}

// TestFmtBudget_NoBudgetAtAll proves the "no budget set" fallback still
// holds when neither tokens nor cost budgets are configured.
func TestFmtBudget_NoBudgetAtAll(t *testing.T) {
	got := fmtBudget(500, 0, 0.1, 0)
	if got != "no budget set" {
		t.Errorf("fmtBudget with no MaxTokens/MaxCost = %q, want \"no budget set\"", got)
	}
}

// TestFmtBudget_TokenBudgetTakesPrecedence proves that when both a token
// and a cost budget are configured, the token budget still renders (the
// documented precedence), unchanged from before Fix 2.
func TestFmtBudget_TokenBudgetTakesPrecedence(t *testing.T) {
	got := fmtBudget(500, 1000, 0.1, 1.0)
	if !strings.Contains(got, "500") || !strings.Contains(got, "50%") {
		t.Errorf("fmtBudget with both budgets set should still render the token budget, got %q", got)
	}
}

// TestUsagePopup_CostOnlyBudgetShown is the popup-surface regression test
// for Fix 2: an agent with only max_cost configured must show a real
// budget line, not "no budget set".
func TestUsagePopup_CostOnlyBudgetShown(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Budgeted": {Model: "gemini-3.5-flash-lite", Tokens: 2000, Cost: 0.42, MaxCost: 1.0, PricingKnown: true, Status: "success"},
		},
	}
	out := m.usagePopup()
	if strings.Contains(out, "no budget set") {
		t.Errorf("usagePopup() shows \"no budget set\" for an agent with MaxCost configured, got:\n%s", out)
	}
	if !strings.Contains(out, "42%") {
		t.Errorf("usagePopup() should show the cost budget percentage, got:\n%s", out)
	}
}

func TestUsagePopup_EmptySession(t *testing.T) {
	m := Model{width: 100, height: 30, spinner: spinner.New(), agentUsage: map[string]*agentUsageSnapshot{}}
	out := m.usagePopup()
	if strings.TrimSpace(out) == "" {
		t.Errorf("usagePopup() must not be empty even with no agents run yet")
	}
}

func TestRenderPopupBox_ContainsTitleAndBody(t *testing.T) {
	m := Model{width: 100, height: 30}
	out := m.renderPopupBox("Usage", "hello world")
	if !strings.Contains(out, "Usage") || !strings.Contains(out, "hello world") {
		t.Errorf("renderPopupBox missing title or body, got:\n%s", out)
	}
}

func TestContextPopup_SummaryShowsBudgetAndWindow(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Coordinator": {Model: "gemini-2.5-flash", Tokens: 2000, MaxTokens: 1000},
		},
	}
	out := m.contextPopup()
	for _, want := range []string{"Coordinator", "System prompt", "Tools", "History", "Rakitsu budget", "Model window"} {
		if !strings.Contains(out, want) {
			t.Errorf("contextPopup() missing %q, got:\n%s", want, out)
		}
	}
}

func TestContextPopup_UnknownModelShowsUnknownWindow(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Local": {Model: "my-custom-litellm-route", Tokens: 100},
		},
	}
	out := m.contextPopup()
	if !strings.Contains(out, "unknown window") {
		t.Errorf("expected \"unknown window\" for an unmapped model, got:\n%s", out)
	}
}

func TestContextPopup_UnreachableAgentShowsUnknownNotZero(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{
			"Spawned-Researcher": {Model: "gemini-2.5-flash", Tokens: 500, MaxTokens: 2000},
		},
	}
	out := m.contextPopup()
	if strings.Contains(out, "System prompt  0") || strings.Contains(out, "Tools          0") || strings.Contains(out, "Total loaded   0") {
		t.Errorf("category/total rendered as 0 for an unreachable agent — should render \"unknown\", not a fabricated zero:\n%s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected \"unknown\" markers for an unreachable agent's category tokens, got:\n%s", out)
	}
	// Rakitsu budget must still show real telemetry data even when live
	// introspection fails — that line doesn't depend on buildContextSnapshot.
	if !strings.Contains(out, "500") || !strings.Contains(out, "2.0K") {
		t.Errorf("expected the real Rakitsu budget numbers (500/2.0K) to still appear, got:\n%s", out)
	}
}

func TestContextPopup_LiveAgentShowsRealCategoryTokens(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := agent.NewAgent(&config.AgentDefinition{
		Name:         "Coordinator",
		Role:         "worker",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "You are a helpful assistant that coordinates work.",
	}, nil, tools.NewToolRegistry(), bus, nil)

	m := Model{
		width: 100, height: 30, spinner: spinner.New(),
		singleAgent: a,
		runner:      a,
		agentUsage: map[string]*agentUsageSnapshot{
			"Coordinator": {Model: "gemini-2.5-flash", Tokens: 50, MaxTokens: 1000},
		},
	}
	out := m.contextPopup()

	if strings.Contains(out, "unavailable") {
		t.Errorf("expected live introspection to succeed (no unavailable warning), got:\n%s", out)
	}
	if strings.Contains(out, "unknown") {
		t.Errorf("live agent's categories should render real token counts, not \"unknown\", got:\n%s", out)
	}
	// System prompt is non-empty, so it must tokenize to a nonzero count —
	// this is the assertion Finding 1 says was never actually exercised.
	if strings.Contains(out, "System prompt  0") {
		t.Errorf("expected a nonzero System prompt token count for a live agent, got:\n%s", out)
	}
	if !strings.Contains(out, "Total loaded") {
		t.Errorf("missing Total loaded line, got:\n%s", out)
	}
}

func TestContextPopup_DetailListsPerToolAndPerMessage(t *testing.T) {
	m := Model{
		width: 100, height: 30, spinner: spinner.New(), popupDetail: true,
		agentUsage: map[string]*agentUsageSnapshot{
			"Coordinator": {Model: "gemini-2.5-flash", Tokens: 100},
		},
	}
	// No live *Agent is wired for this synthetic snapshot-only Model, so
	// buildContextSnapshot returns ok=false and contextPopup must degrade
	// gracefully rather than panic — this asserts that path, not real tool
	// listing (Task 3's own tests cover the tokenization itself).
	out := m.contextPopup()
	if !strings.Contains(out, "Coordinator") {
		t.Errorf("expected the agent name even when live introspection is unavailable, got:\n%s", out)
	}
}
