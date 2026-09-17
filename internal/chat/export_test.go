package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// The markdown export shares the popup's totals-row rule: never print a
// confident dollar figure that no agent contributed a real price to.
func TestUsageReportMarkdown_TotalCostUnknownWhenNoPricing(t *testing.T) {
	m := Model{
		agentUsage: map[string]*agentUsageSnapshot{
			"Local": {Model: "llama3.3", Tokens: 500, PricingKnown: false},
		},
	}
	out := m.usageReportMarkdown()
	if !strings.Contains(out, "unknown (no pricing set)") {
		t.Errorf("markdown totals row should admit an unknown cost, got:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("markdown totals row must not print a fabricated $0.0000, got:\n%s", out)
	}
}

func TestWriteReport_WritesToRakitsuReportsDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	path, err := writeReport("usage", "md", "# hello")
	if err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	wantDir := filepath.Join(tmpHome, ".rakitsu", "reports")
	if !strings.HasPrefix(path, wantDir) {
		t.Errorf("path = %q, want prefix %q", path, wantDir)
	}
	if !strings.HasPrefix(filepath.Base(path), "rakitsu-usage-") || !strings.HasSuffix(path, ".md") {
		t.Errorf("path = %q, want rakitsu-usage-<timestamp>.md", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "# hello" {
		t.Errorf("file content = %q, want %q", got, "# hello")
	}
}

// TestWriteReport_RapidExportsDontCollide regression-guards: the filename's
// only differentiator beyond kind/format was a second-precision timestamp,
// and os.WriteFile is a plain overwrite (no O_EXCL) — two exports of the
// same report type within the same second silently clobbered the first
// file with no warning.
func TestWriteReport_RapidExportsDontCollide(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path1, err := writeReport("usage", "md", "# first")
	if err != nil {
		t.Fatalf("writeReport (1st): %v", err)
	}
	path2, err := writeReport("usage", "md", "# second")
	if err != nil {
		t.Fatalf("writeReport (2nd): %v", err)
	}

	if path1 == path2 {
		t.Fatalf("two rapid exports produced the same filename: %q — the second silently overwrote the first", path1)
	}
	got1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("ReadFile(path1): %v", err)
	}
	if string(got1) != "# first" {
		t.Fatalf("path1 content = %q, want %q — got overwritten by the second export", got1, "# first")
	}
}

// TestWriteReport_ExactTimestampCollisionDoesNotOverwrite regression-guards
// the gap the nanosecond-precision fix (above) narrowed but didn't close:
// two calls that land on the IDENTICAL nowFunc() value — plausible on a
// system whose clock resolution is coarser than a nanosecond, or from two
// separate rakitsu processes racing the same export — must not have the
// second silently clobber the first just because their timestamps matched.
// nowFunc is swapped to a fixed clock so the collision is forced
// deterministically rather than relying on an actual race.
func TestWriteReport_ExactTimestampCollisionDoesNotOverwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	origNow := nowFunc
	fixed := time.Date(2026, 8, 25, 6, 19, 1, 123456789, time.UTC)
	nowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { nowFunc = origNow })

	path1, err := writeReport("usage", "md", "# first")
	if err != nil {
		t.Fatalf("writeReport (1st): %v", err)
	}
	path2, err := writeReport("usage", "md", "# second")
	if err != nil {
		t.Fatalf("writeReport (2nd): %v", err)
	}

	if path1 == path2 {
		t.Fatalf("two exports with an identical timestamp produced the same filename: %q", path1)
	}
	got1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("ReadFile(path1): %v", err)
	}
	if string(got1) != "# first" {
		t.Fatalf("path1 content = %q, want %q — got overwritten by the second export", got1, "# first")
	}
	got2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("ReadFile(path2): %v", err)
	}
	if string(got2) != "# second" {
		t.Fatalf("path2 content = %q, want %q", got2, "# second")
	}
}

func TestUsageReportMarkdown_ContainsPerAgentRows(t *testing.T) {
	m := Model{agentUsage: map[string]*agentUsageSnapshot{
		"Coordinator": {Model: "gemini-3.5-flash-lite", Tokens: 2000, Cost: 0.0031, PricingKnown: true, MaxTokens: 1000},
	}}
	md := m.usageReportMarkdown()
	if !strings.Contains(md, "Coordinator") || !strings.Contains(md, "Rakitsu budget") {
		t.Errorf("usageReportMarkdown() = %q, missing expected content", md)
	}
}

// TestUsageReportMarkdown_CostOnlyBudgetShown is the export-surface
// regression test for Fix 2: an agent with only max_cost configured must
// render a real cost budget in the markdown report, not "no budget set".
func TestUsageReportMarkdown_CostOnlyBudgetShown(t *testing.T) {
	m := Model{agentUsage: map[string]*agentUsageSnapshot{
		"Budgeted": {Model: "gemini-3.5-flash-lite", Tokens: 2000, Cost: 0.42, MaxCost: 1.0, PricingKnown: true},
	}}
	md := m.usageReportMarkdown()
	if strings.Contains(md, "no budget set") {
		t.Errorf("usageReportMarkdown() shows \"no budget set\" for a cost-only budget, got:\n%s", md)
	}
	if !strings.Contains(md, "42%") {
		t.Errorf("usageReportMarkdown() should show the cost budget percentage, got:\n%s", md)
	}
}

// TestUsageReportCSV_CostOnlyBudgetPct proves the CSV's budget_used_pct
// column falls back to the cost-budget percentage when MaxTokens is unset,
// instead of silently reporting 0 (which would look like "no budget used"
// rather than "cost-only budget at 42%").
func TestUsageReportCSV_CostOnlyBudgetPct(t *testing.T) {
	m := Model{agentUsage: map[string]*agentUsageSnapshot{
		"Budgeted": {Model: "gemini-3.5-flash-lite", Tokens: 2000, Cost: 0.42, MaxCost: 1.0, PricingKnown: true},
	}}
	csv := m.usageReportCSV()
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("usageReportCSV() has no data row, got:\n%s", csv)
	}
	fields := strings.Split(lines[1], ",")
	gotPct := fields[len(fields)-1]
	if gotPct != "42" {
		t.Errorf("budget_used_pct = %q, want \"42\" (cost-only budget), got row:\n%s", gotPct, lines[1])
	}
}

// TestCsvEscape_FormulaInjectionPrefixed is the regression test for Fix 10:
// spawn_agent's `name` argument is LLM-controlled and flows into the
// agent-name column of both CSV exports. A field starting with '=' (or
// '+', '-', '@') must be prefixed with a single quote so a spreadsheet app
// (Excel, Google Sheets) never interprets it as a formula.
func TestCsvEscape_FormulaInjectionPrefixed(t *testing.T) {
	got := csvEscape("=cmd|'/c calc'!A1")
	if !strings.HasPrefix(got, `'=`) && !strings.HasPrefix(got, `"'=`) {
		t.Errorf("csvEscape(%q) = %q, want a leading single-quote prefix before the formula char", "=cmd|'/c calc'!A1", got)
	}
}

// TestCsvEscape_BareCRTriggersQuoting proves a lone \r (not just \n) also
// triggers standard CSV quoting, since a bare CR can still break row
// parsing in some CSV readers.
func TestCsvEscape_BareCRTriggersQuoting(t *testing.T) {
	got := csvEscape("line1\rline2")
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("csvEscape(%q) = %q, want it quoted because of the bare \\r", "line1\rline2", got)
	}
}

func TestUsageReportCSV_HasExpectedColumns(t *testing.T) {
	m := Model{agentUsage: map[string]*agentUsageSnapshot{
		"Coordinator": {Model: "gemini-3.5-flash-lite", Provider: "litellm", Tokens: 2000, Cost: 0.0031, PricingKnown: true, MaxTokens: 1000, Turns: 4},
	}}
	csv := m.usageReportCSV()
	header := strings.SplitN(csv, "\n", 2)[0]
	for _, col := range []string{"agent", "model", "provider", "turns", "tokens_total", "cost_usd", "pricing_known", "budget_max_tokens", "budget_used_pct"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header %q missing column %q", header, col)
		}
	}
	if !strings.Contains(csv, "Coordinator") {
		t.Errorf("CSV body missing agent row, got:\n%s", csv)
	}
}

func TestContextReportMarkdown_ContainsCategories(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := agent.NewAgent(&config.AgentDefinition{Name: "Coordinator", Role: "worker", Model: "gemini-2.5-flash", SystemPrompt: "You coordinate."}, nil, tools.NewToolRegistry(), bus, nil)
	m := Model{
		singleAgent: a, runner: a,
		agentUsage: map[string]*agentUsageSnapshot{"Coordinator": {Model: "gemini-2.5-flash", Tokens: 50}},
	}
	md := m.contextReportMarkdown()
	for _, want := range []string{"Coordinator", "System prompt", "Tools", "History", "Rakitsu budget", "Model window"} {
		if !strings.Contains(md, want) {
			t.Errorf("contextReportMarkdown() missing %q, got:\n%s", want, md)
		}
	}
}

func TestContextReportCSV_HasExpectedColumns(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := agent.NewAgent(&config.AgentDefinition{Name: "Coordinator", Role: "worker", Model: "gemini-2.5-flash", SystemPrompt: "You coordinate."}, nil, tools.NewToolRegistry(), bus, nil)
	m := Model{
		singleAgent: a, runner: a,
		agentUsage: map[string]*agentUsageSnapshot{"Coordinator": {Model: "gemini-2.5-flash", Tokens: 50}},
	}
	csv := m.contextReportCSV()
	header := strings.SplitN(csv, "\n", 2)[0]
	for _, col := range []string{"agent", "category", "tokens", "rakitsu_budget_pct", "model_window_tokens", "model_window_known", "model_window_used_pct"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header %q missing column %q", header, col)
		}
	}
	if !strings.Contains(csv, "TOTAL") {
		t.Errorf("CSV body missing a TOTAL category row, got:\n%s", csv)
	}
}

// TestContextReportMarkdown_UnreachableAgentShowsUnknown is the regression
// test for the design bug this task's brief originally shipped: an agent
// present only in m.agentUsage (e.g. a spawn_agent child, never registered
// into the orchestrator's worker map or the chat Model's runner/singleAgent
// fields) must still get a section in the report — with "unknown" markers
// for the live-only fields, never a silently dropped section and never a
// fabricated 0.
func TestContextReportMarkdown_UnreachableAgentShowsUnknown(t *testing.T) {
	m := Model{
		agentUsage: map[string]*agentUsageSnapshot{
			"Spawned-Researcher": {Model: "gemini-2.5-flash", Tokens: 500, MaxTokens: 2000},
		},
	}
	md := m.contextReportMarkdown()
	if !strings.Contains(md, "Spawned-Researcher") {
		t.Errorf("contextReportMarkdown() dropped the unreachable agent's section entirely, got:\n%s", md)
	}
	if strings.Contains(md, "| System prompt | 0 |") || strings.Contains(md, "| Tools | 0 |") ||
		strings.Contains(md, "| History | 0 |") || strings.Contains(md, "**Total loaded:** 0") {
		t.Errorf("contextReportMarkdown() fabricated a 0 for an unreachable agent's category tokens, got:\n%s", md)
	}
	if !strings.Contains(md, "unknown") {
		t.Errorf("contextReportMarkdown() missing \"unknown\" markers for an unreachable agent, got:\n%s", md)
	}
	// Rakitsu budget comes from agentUsage telemetry, not live introspection
	// — it must still show real numbers even when the agent is unreachable.
	if !strings.Contains(md, "500") || !strings.Contains(md, "2.0K") {
		t.Errorf("contextReportMarkdown() should still show real Rakitsu budget numbers (500/2.0K), got:\n%s", md)
	}
}

// TestContextReportCSV_LiveAgentUnknownModelShowsUnknownPct is the
// regression test for the fabricated-0 bug: when the agent IS live
// (buildContextSnapshot succeeds) but its model isn't in
// llm.contextWindows (any local/Ollama/custom/LiteLLM model), windowKnown
// is false and model_window_used_pct must render "unknown" — never "0",
// which would look like a real computed percentage.
func TestContextReportCSV_LiveAgentUnknownModelShowsUnknownPct(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := agent.NewAgent(&config.AgentDefinition{Name: "Coordinator", Role: "worker", Model: "my-custom-ollama-model", SystemPrompt: "You coordinate."}, nil, tools.NewToolRegistry(), bus, nil)
	m := Model{
		singleAgent: a, runner: a,
		agentUsage: map[string]*agentUsageSnapshot{"Coordinator": {Model: "my-custom-ollama-model", Tokens: 50}},
	}
	csv := m.contextReportCSV()
	if strings.Contains(csv, ",0\n") {
		t.Errorf("contextReportCSV() fabricated a 0 for an unknown model's window pct, got:\n%s", csv)
	}
	for _, line := range strings.Split(strings.TrimRight(csv, "\n"), "\n")[1:] {
		fields := strings.Split(line, ",")
		if len(fields) == 0 {
			continue
		}
		gotPct := fields[len(fields)-1]
		if gotPct != "unknown" {
			t.Errorf("row %q: model_window_used_pct = %q, want \"unknown\"", line, gotPct)
		}
	}
}

// TestContextReportCSV_UnreachableAgentRowNotDropped is the CSV counterpart:
// the brief's original implementation silently `continue`d on ok=false,
// meaning a spawned agent that ran and cost real tokens would not appear
// anywhere in the export. The row (and its TOTAL row) must always be
// present, with "unknown" text in the live-dependent columns.
func TestContextReportCSV_UnreachableAgentRowNotDropped(t *testing.T) {
	m := Model{
		agentUsage: map[string]*agentUsageSnapshot{
			"Spawned-Researcher": {Model: "gemini-2.5-flash", Tokens: 500, MaxTokens: 2000},
		},
	}
	csv := m.contextReportCSV()
	if !strings.Contains(csv, "Spawned-Researcher") {
		t.Errorf("contextReportCSV() dropped the unreachable agent's row entirely, got:\n%s", csv)
	}
	if !strings.Contains(csv, "TOTAL") {
		t.Errorf("contextReportCSV() dropped the TOTAL row for an unreachable agent, got:\n%s", csv)
	}
	if !strings.Contains(csv, "unknown") {
		t.Errorf("contextReportCSV() should render \"unknown\" (not a fabricated 0) for an unreachable agent's live-dependent columns, got:\n%s", csv)
	}
}
