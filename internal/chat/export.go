package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// reportsDir returns ~/.rakitsu/reports, creating it if necessary.
func reportsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".rakitsu", "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// nowFunc is time.Now, overridable in tests to force two writeReport calls
// onto the identical timestamp deterministically (a real collision can't be
// reproduced on demand, but the fallback path that handles one still needs
// coverage).
var nowFunc = time.Now

// writeReport writes body to ~/.rakitsu/reports/rakitsu-<kind>-<timestamp>.<format>
// and returns the full path.
func writeReport(kind, format, body string) (string, error) {
	dir, err := reportsDir()
	if err != nil {
		return "", err
	}
	// Nanosecond precision (not just seconds) makes a same-timestamp
	// collision between two exports of the same report type astronomically
	// unlikely — but not impossible (coarser clock resolution on some
	// systems, or two separate rakitsu processes racing the same export).
	// O_EXCL turns that remaining case into "try the next suffix" instead
	// of a silent overwrite.
	name := fmt.Sprintf("rakitsu-%s-%s.%s", kind, nowFunc().Format("20060102-150405.000000000"), format)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		for i := 1; i <= 100; i++ {
			altPath := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
			f, err = os.OpenFile(altPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				path = altPath
				break
			}
			if !os.IsExist(err) {
				return "", err
			}
		}
		if err != nil {
			return "", fmt.Errorf("writeReport: no free filename for %q after 100 attempts", name)
		}
	}
	defer f.Close()
	if _, err := f.Write([]byte(body)); err != nil {
		return "", err
	}
	return path, nil
}

// usageReportMarkdown renders the same data as usagePopup() as clean GFM —
// a shareable report rather than a terminal-box render.
func (m Model) usageReportMarkdown() string {
	var b strings.Builder
	b.WriteString("# Rakitsu usage report\n\n")
	b.WriteString("| Agent | Model | Provider | Turns | Tokens | Cost | Rakitsu budget |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	var totalTokens, totalTurns, pricedAgents int
	var totalCost float64
	names := m.sortedAgentNames()
	for _, name := range names {
		s := m.agentUsage[name]
		totalTokens += s.Tokens
		totalTurns += s.Turns
		totalCost += s.Cost
		if s.PricingKnown {
			pricedAgents++
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s | %s |\n",
			name, s.Model, orDash(s.Provider), s.Turns, fmtTok(s.Tokens), fmtCost(s.Cost, s.PricingKnown), fmtBudget(s.Tokens, s.MaxTokens, s.Cost, s.MaxCost))
	}
	fmt.Fprintf(&b, "| **Total** | | | %d | %s | %s | |\n",
		totalTurns, fmtTok(totalTokens), fmtTotalCost(totalCost, pricedAgents, len(names)))
	return b.String()
}

// usageReportCSV renders long-format CSV: one row per agent, one column set
// per data point (not a literal dump of the popup's visual table).
func (m Model) usageReportCSV() string {
	var b strings.Builder
	b.WriteString("agent,model,provider,turns,tokens_in,tokens_out,tokens_total,cost_usd,pricing_known,budget_max_tokens,budget_used_pct\n")
	for _, name := range m.sortedAgentNames() {
		s := m.agentUsage[name]
		pct := budgetPct(s.Tokens, s.MaxTokens, s.Cost, s.MaxCost)
		fmt.Fprintf(&b, "%s,%s,%s,%d,,,%d,%.6f,%t,%d,%d\n",
			csvEscape(name), csvEscape(s.Model), csvEscape(s.Provider), s.Turns, s.Tokens, s.Cost, s.PricingKnown, s.MaxTokens, pct)
	}
	return b.String()
}

// csvEscape wraps a field in quotes if it contains a comma, quote, newline,
// or bare CR, and — defense in depth against CSV/spreadsheet formula
// injection — prefixes a leading '=', '+', '-', or '@' with a single quote
// so Excel/Google Sheets never interprets the field as a formula. The real
// (narrow) vector: spawn_agent's `name` argument is LLM-controlled and
// flows into the agent-name column of both CSV exports.
func csvEscape(s string) string {
	if len(s) > 0 && strings.ContainsRune("=+-@", rune(s[0])) {
		s = "'" + s
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// contextReportMarkdown renders the same data as contextPopup() as clean
// GFM — one section per agent, one table row per category.
//
// An agent's section is never dropped, even when it isn't reachable via
// buildContextSnapshot (true for every spawn_agent child, which is never
// registered into the orchestrator's worker map or this chat Model's
// runner/singleAgent fields — see contextPopup() for the same fix applied
// first). Category tokens, the total, and the model-window numerator render
// the literal text "unknown" in that case rather than a fabricated 0 — the
// Rakitsu budget and model-window lookup don't need live introspection
// (they come from agentUsage telemetry), so they stay accurate regardless.
func (m Model) contextReportMarkdown() string {
	var b strings.Builder
	b.WriteString("# Rakitsu context report\n\n")
	for _, name := range m.sortedAgentNames() {
		usage := m.agentUsage[name]
		fmt.Fprintf(&b, "## %s — %s\n\n", name, usage.Model)

		model := usage.Model
		categories := []contextCategory{{Name: "System prompt"}, {Name: "Tools"}, {Name: "History"}}
		total := 0
		live := false
		if snap, ok := m.buildContextSnapshot(name); ok {
			model, categories, total, live = snap.Model, snap.Categories, snap.TotalTokens, true
		} else {
			b.WriteString("_(live context introspection unavailable for this agent — category tokens unknown, not zero)_\n\n")
		}

		b.WriteString("| Category | Tokens |\n|---|---|\n")
		for _, cat := range categories {
			tok := "unknown"
			if live {
				tok = fmtTok(cat.Tokens)
			}
			fmt.Fprintf(&b, "| %s | %s |\n", cat.Name, tok)
		}

		totalStr := "unknown"
		if live {
			totalStr = fmtTok(total)
		}

		windowTokens, windowKnown := llm.LookupContextWindow(model)
		windowStr := "unknown"
		if windowKnown {
			windowStr = fmt.Sprintf("%s / %s", totalStr, fmtTok(windowTokens))
		}

		fmt.Fprintf(&b, "\n**Total loaded:** %s  \n**Rakitsu budget:** %s  \n**Model window:** %s\n\n",
			totalStr, fmtBudget(usage.Tokens, usage.MaxTokens, usage.Cost, usage.MaxCost), windowStr)
	}
	return b.String()
}

// contextReportCSV renders long-format CSV: one row per category per agent,
// plus a TOTAL row per agent.
//
// Same unreachable-agent fix as contextReportMarkdown: a spawn_agent
// child's row is never dropped. The tokens column (per category and the
// TOTAL row) and model_window_used_pct render the literal text "unknown"
// when live introspection isn't available — never a silently dropped row,
// never a fabricated 0. model_window_tokens/model_window_known stay real
// regardless, since they don't depend on the live total.
func (m Model) contextReportCSV() string {
	var b strings.Builder
	b.WriteString("agent,category,tokens,rakitsu_budget_pct,model_window_tokens,model_window_known,model_window_used_pct\n")
	for _, name := range m.sortedAgentNames() {
		usage := m.agentUsage[name]

		model := usage.Model
		categories := []contextCategory{{Name: "System prompt"}, {Name: "Tools"}, {Name: "History"}}
		total := 0
		live := false
		if snap, ok := m.buildContextSnapshot(name); ok {
			model, categories, total, live = snap.Model, snap.Categories, snap.TotalTokens, true
		}

		pct := budgetPct(usage.Tokens, usage.MaxTokens, usage.Cost, usage.MaxCost)

		windowTokens, windowKnown := llm.LookupContextWindow(model)
		windowPctStr := "unknown"
		if live && windowKnown && windowTokens > 0 {
			windowPctStr = fmt.Sprintf("%d", total*100/windowTokens)
		}

		for _, cat := range categories {
			tokStr := "unknown"
			if live {
				tokStr = fmt.Sprintf("%d", cat.Tokens)
			}
			fmt.Fprintf(&b, "%s,%s,%s,%d,%d,%t,%s\n", csvEscape(name), csvEscape(cat.Name), tokStr, pct, windowTokens, windowKnown, windowPctStr)
		}

		totalStr := "unknown"
		if live {
			totalStr = fmt.Sprintf("%d", total)
		}
		fmt.Fprintf(&b, "%s,TOTAL,%s,%d,%d,%t,%s\n", csvEscape(name), totalStr, pct, windowTokens, windowKnown, windowPctStr)
	}
	return b.String()
}
