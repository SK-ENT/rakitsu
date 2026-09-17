package chat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// popupKind identifies which floating popup (if any) is currently active.
type popupKind int

const (
	popupNone popupKind = iota
	popupUsage
	popupContext
	popupAgents
)

// fmtTok renders a token count the same way subagent rows already do
// elsewhere in this package (see ChatPanel.vue's fmtSubTokens for the web
// equivalent): 1.2K above 1000, exact below.
func fmtTok(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// fmtBudget renders the "Rakitsu budget" column with token-then-cost
// precedence: maxTokens > 0 renders the token budget ("used/max (pct%)");
// else maxCost > 0 renders the cost budget ("$used/$max (pct%)"); else "no
// budget set". A cost-only budget (settings.execution.max_cost or a
// per-agent max_cost with no max_total_tokens) must never fall through to
// "no budget set" — that agent is still actively budget-enforced and can
// terminate with Status: budget_exceeded on cost alone.
func fmtBudget(usedTokens, maxTokens int, cost, maxCost float64) string {
	switch {
	case maxTokens > 0:
		return fmt.Sprintf("%s/%s (%d%%)", fmtTok(usedTokens), fmtTok(maxTokens), budgetPct(usedTokens, maxTokens, cost, maxCost))
	case maxCost > 0:
		return fmt.Sprintf("$%.4f/$%.4f (%d%%)", cost, maxCost, budgetPct(usedTokens, maxTokens, cost, maxCost))
	default:
		return "no budget set"
	}
}

// budgetPct computes the Rakitsu-budget percentage with the same
// token-then-cost precedence as fmtBudget, for CSV export columns that need
// a bare percentage rather than the full "used/max" string.
func budgetPct(usedTokens, maxTokens int, cost, maxCost float64) int {
	switch {
	case maxTokens > 0:
		return usedTokens * 100 / maxTokens
	case maxCost > 0:
		return int(cost / maxCost * 100)
	default:
		return 0
	}
}

// fmtCost renders the cost column, distinguishing "genuinely computed to
// $0.00" from "pricing was never configured for this model."
func fmtCost(cost float64, known bool) string {
	if !known {
		return "$0.00 (pricing not set)"
	}
	return fmt.Sprintf("$%.4f", cost)
}

// fmtTotalCost renders the totals-row cost across every agent. Summing costs
// erases which of them were real: with pricing configured nowhere, a bare
// "$0.0000" is a fabricated number, and with pricing on only some agents the
// sum silently understates the true cost. Both cases are labelled instead of
// rendered as a confident figure — the same rule fmtCost applies per row.
func fmtTotalCost(total float64, pricedAgents, totalAgents int) string {
	switch {
	case totalAgents == 0 || pricedAgents == 0:
		return "unknown (no pricing set)"
	case pricedAgents < totalAgents:
		return fmt.Sprintf("$%.4f (partial — %d of %d agents have no pricing)",
			total, totalAgents-pricedAgents, totalAgents)
	default:
		return fmt.Sprintf("$%.4f", total)
	}
}

// sortedAgentNames returns agentUsage keys in stable (alphabetical) order so
// popup output — and its tests — are deterministic.
func (m Model) sortedAgentNames() []string {
	names := make([]string, 0, len(m.agentUsage))
	for name := range m.agentUsage {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// usagePopup renders the /usage popup body: one row per agent that has run
// this session, plus a totals row. Detail mode (m.popupDetail) adds an
// "Iterations (total)" and "Status" line per agent.
func (m Model) usagePopup() string {
	var b strings.Builder
	names := m.sortedAgentNames()
	if len(names) == 0 {
		b.WriteString("No agents have run yet this session.\n")
	}

	var totalTokens, totalTurns, pricedAgents int
	var totalCost float64
	for _, name := range names {
		s := m.agentUsage[name]
		totalTokens += s.Tokens
		totalTurns += s.Turns
		totalCost += s.Cost
		if s.PricingKnown {
			pricedAgents++
		}
		fmt.Fprintf(&b, "%s — %s @ %s\n", name, s.Model, orDash(s.Provider))
		fmt.Fprintf(&b, "  Turns: %d  Tokens: %s  Cost: %s  Rakitsu budget: %s\n",
			s.Turns, fmtTok(s.Tokens), fmtCost(s.Cost, s.PricingKnown), fmtBudget(s.Tokens, s.MaxTokens, s.Cost, s.MaxCost))
		if m.popupDetail {
			fmt.Fprintf(&b, "  Iterations (total): %d  Status: %s\n", s.TotalIterations, orDash(s.Status))
		}
	}

	if len(names) > 0 {
		b.WriteString(strings.Repeat("─", 40) + "\n")
		fmt.Fprintf(&b, "Total — Turns: %d  Tokens: %s  Cost: %s\n",
			totalTurns, fmtTok(totalTokens), fmtTotalCost(totalCost, pricedAgents, len(names)))
	}

	b.WriteString("\n")
	if m.popupDetail {
		b.WriteString("[d] summary  [e] export .md  [c] export .csv  [Esc] close")
	} else {
		b.WriteString("[d] detail  [e] export .md  [c] export .csv  [Esc] close")
	}
	return b.String()
}

// orDash returns "-" for an empty string, otherwise s unchanged — used for
// optional fields (Provider, Status) that may be blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// contextPopup renders the /context popup body: per-agent category
// breakdown (System prompt / Tools / History), Rakitsu budget, and a
// best-effort model-window estimate. Summary by default; m.popupDetail
// expands each category to its per-item breakdown.
func (m Model) contextPopup() string {
	var b strings.Builder
	names := m.sortedAgentNames()
	if len(names) == 0 {
		b.WriteString("No agents have run yet this session.\n")
	}

	for _, name := range names {
		usage := m.agentUsage[name]
		fmt.Fprintf(&b, "%s — %s\n", name, usage.Model)

		// Category breakdown needs a live *Agent (buildContextSnapshot
		// fails for spawn_agent children, which are never re-reachable by
		// name after — or even during — their run). The Rakitsu budget and
		// Model window lines below don't need live introspection: both
		// come from agentUsage telemetry, so they stay accurate regardless.
		model := usage.Model
		categories := []contextCategory{{Name: "System prompt"}, {Name: "Tools"}, {Name: "History"}}
		total := 0
		live := false

		if snap, ok := m.buildContextSnapshot(name); ok {
			model, categories, total, live = snap.Model, snap.Categories, snap.TotalTokens, true
		} else {
			b.WriteString("  (live context introspection unavailable for this agent — category tokens unknown, not zero)\n")
		}

		for _, cat := range categories {
			tok := "unknown"
			if live {
				tok = fmtTok(cat.Tokens)
			}
			fmt.Fprintf(&b, "  %-14s %s\n", cat.Name, tok)
			if m.popupDetail {
				for _, d := range cat.Detail {
					fmt.Fprintf(&b, "    %s\n", d)
				}
			}
		}

		totalStr := "unknown"
		if live {
			totalStr = fmtTok(total)
		}

		windowTokens, windowKnown := llm.LookupContextWindow(model)
		windowStr := "unknown window"
		if windowKnown {
			if !live {
				windowStr = fmt.Sprintf("unknown/%s", fmtTok(windowTokens))
			} else {
				pct := 0
				if windowTokens > 0 {
					pct = total * 100 / windowTokens
				}
				windowStr = fmt.Sprintf("%s/%s (~%d%%)", fmtTok(total), fmtTok(windowTokens), pct)
			}
		}

		fmt.Fprintf(&b, "  %-14s %s\n", "Total loaded", totalStr)
		fmt.Fprintf(&b, "  %-14s %s\n", "Rakitsu budget", fmtBudget(usage.Tokens, usage.MaxTokens, usage.Cost, usage.MaxCost))
		fmt.Fprintf(&b, "  %-14s %s\n\n", "Model window", windowStr)
	}

	if m.popupDetail {
		b.WriteString("[d] summary  [e] export .md  [c] export .csv  [Esc] close")
	} else {
		b.WriteString("[d] detail  [e] export .md  [c] export .csv  [Esc] close")
	}
	return b.String()
}

// popupBoxWidth is the width every popup box is rendered at: the terminal
// less a margin, floored so a very narrow terminal still gets a box and
// capped so a very wide one does not stretch a popup across the screen.
//
// Callers that lay out columns need this too — the agent picker sizes its
// own columns against it — so the clamp lives here rather than inline in
// renderPopupBox, where a second copy would drift out of step with it.
func (m Model) popupBoxWidth() int {
	w := m.width - 10
	if w < 20 {
		w = m.width
	}
	if w > 100 {
		w = 100
	}
	return w
}

// renderPopupBox composites title+body into a centered, bordered floating
// box over the full terminal area — the true-overlay approach: called from
// View() as a full replacement for the normal layout, not a viewport swap.
func (m Model) renderPopupBox(title, body string) string {
	content := titleStyle.Render(title) + "\n\n" + body
	box := borderStyle.Width(m.popupBoxWidth()).Padding(1, 2).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
