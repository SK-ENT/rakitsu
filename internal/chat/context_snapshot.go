package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/tokenizer"
)

// contextCategory is one row of a /context breakdown: a named slice of
// what's loaded into an agent's context, its token cost, and (in detail
// mode) a per-item breakdown.
type contextCategory struct {
	Name   string
	Tokens int
	Detail []string
}

// contextSnapshot is a full on-demand /context read for one agent, computed
// fresh at popup-open time (see Model.buildContextSnapshot).
type contextSnapshot struct {
	AgentName   string
	Model       string
	Categories  []contextCategory
	TotalTokens int
}

// tokenCount tokenizes text for the given model, falling back to 0 on any
// tokenizer error (an unreachable/misconfigured encoding must never crash
// the popup — it just under-reports that one category). This is a
// deliberate, accepted exception to this file's "unknown, not a fabricated
// 0" convention: the error path is near-unreachable (a genuinely unknown
// model still resolves to an approximate encoding), and crashing the popup
// over it would be worse than a rare under-report.
func tokenCount(text, model string) int {
	if text == "" {
		return 0
	}
	resp, err := tokenizer.Tokenize(text, model)
	if err != nil {
		return 0
	}
	return resp.Total
}

// previewLine renders a rune-safe, single-line preview of text: newlines
// collapsed to spaces, then truncated to at most maxRunes runes. Slicing by
// byte offset (text[:60]) can cut a multi-byte UTF-8 rune in half — a real
// risk here since chat input is realistically non-ASCII (e.g. Japanese) —
// so this converts to []rune first.
func previewLine(text string, maxRunes int) string {
	text = strings.ReplaceAll(text, "\n", " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return text
}

// buildContextSnapshot computes a fresh, on-demand category breakdown for
// the named agent: System prompt, Tools (as actually sent to the LLM, via
// GetEffectiveToolDefs), and History.
//
// History reflects what the NEXT turn would actually compose and send, not
// the raw m.history transcript — mirroring Model.submitQuery's own
// dispatch: single-agent mode with conversation memory tokenizes
// convMem.ComposeHistory(m.history) (older turns folded into a rolling
// summary, or dropped in DisableSummary mode); single-agent mode without
// conversation memory tokenizes m.history verbatim (nothing folds it);
// orchestrator mode tokenizes the single string buildQueryWithHistory
// returns, since that's the actual last-N-turns/truncated composition every
// orchestrator invocation sends. Popups always open between turns, so
// m.history at that moment IS priorHistory for the next turn — no need to
// simulate a pending query.
//
// Skills are deliberately NOT a category: Rakitsu's `skills:` YAML block is
// parsed but never resolved into the system prompt or messages at runtime
// (see the scope note in this plan's Global Constraints) — there is nothing
// to tokenize because it costs zero context tokens today.
func (m Model) buildContextSnapshot(agentName string) (contextSnapshot, bool) {
	a, ok := lookupSwappableAgent(SlashCommandContext{
		Runner:      m.runner,
		InnerRunner: m.innerRunner,
		SingleAgent: m.singleAgent,
	}, agentName)
	if !ok {
		return contextSnapshot{}, false
	}

	model := a.Model()

	sysPromptTokens := tokenCount(a.GetSystemPrompt(), model)

	toolDefs := a.GetEffectiveToolDefs()
	var toolTokens int
	toolDetail := make([]string, 0, len(toolDefs))
	for _, td := range toolDefs {
		paramsJSON, _ := json.Marshal(td.Parameters)
		text := td.Name + "\n" + td.Description + "\n" + string(paramsJSON)
		n := tokenCount(text, model)
		toolTokens += n
		toolDetail = append(toolDetail, td.Name+": "+fmtTok(n))
	}

	var historyTokens int
	var historyDetail []string
	switch {
	case m.singleAgent != nil && m.convMem != nil:
		// Conversation memory folds older turns into a rolling summary (or
		// drops them entirely in DisableSummary mode) before the next turn
		// is sent — see submitQuery. ComposeHistory only reads c.summary/
		// c.folded under its mutex before computing, so it's safe to call
		// from this read-only popup path.
		for _, msg := range m.convMem.ComposeHistory(m.history) {
			text := msg.AsText()
			n := tokenCount(text, model)
			historyTokens += n
			historyDetail = append(historyDetail, msg.Role+" ("+fmtTok(n)+"): "+previewLine(text, 60))
		}
	case m.singleAgent != nil:
		// No conversation memory: the next turn sends m.history verbatim.
		for _, msg := range m.history {
			text := msg.AsText()
			n := tokenCount(text, model)
			historyTokens += n
			historyDetail = append(historyDetail, msg.Role+" ("+fmtTok(n)+"): "+previewLine(text, 60))
		}
	default:
		// Orchestrator mode: the next turn sends one composed string — the
		// last historyMaxTurns turns, each assistant reply truncated to
		// historyMaxAssistantChars — not the raw per-message transcript.
		// The current draft (m.textarea.Value()) is the real "query" arg
		// submitQuery passes here (model.go:1306/1376) — using "" instead
		// undercounted this popup's own History tokens by the draft's size.
		composed := buildQueryWithHistory(m.history, m.textarea.Value())
		if composed != "" {
			historyTokens = tokenCount(composed, model)
			historyDetail = []string{fmt.Sprintf(
				"composed/truncated window (last %d turns, replies capped at %d chars, %s): %s",
				historyMaxTurns, historyMaxAssistantChars, fmtTok(historyTokens), previewLine(composed, 60),
			)}
		}
	}

	categories := []contextCategory{
		{Name: "System prompt", Tokens: sysPromptTokens},
		{Name: "Tools", Tokens: toolTokens, Detail: toolDetail},
		{Name: "History", Tokens: historyTokens, Detail: historyDetail},
	}
	total := sysPromptTokens + toolTokens + historyTokens

	return contextSnapshot{AgentName: agentName, Model: model, Categories: categories, TotalTokens: total}, true
}
