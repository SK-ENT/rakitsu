package memory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Conversation memory: instead of re-feeding the whole transcript every chat
// turn, the model sees a rolling summary plus the last KeepRecentTurns turns
// verbatim. The summary is maintained by a post-turn summarizer inference and
// persisted as a "summary" node in the session scope, so resume keeps the
// compressed context.
//
// Loss-safety invariant: a turn is only dropped from the model-visible window
// after it has been folded into the summary. When the summarizer fails, the
// window simply stays uncompressed for that turn — degraded cost, no lost
// context.

// ConversationSummaryNodeID is the well-known node ID holding the rolling
// summary inside a "session:<id>" scope.
const ConversationSummaryNodeID = "conversation-summary"

// foldedTagPrefix encodes how many leading turns the persisted summary covers
// (e.g. "folded:7"), so resume knows where the verbatim window starts.
const foldedTagPrefix = "folded:"

// turnMsgMaxChars bounds each message's contribution to the summarizer
// prompt so one enormous answer can't blow the summarizer's own context.
const turnMsgMaxChars = 4000

// SummarizeFunc runs one summarizer inference: prompt in, updated rolling
// summary out. See ProviderSummarize for the standard adapter.
type SummarizeFunc func(ctx context.Context, prompt string) (string, error)

// ConversationOptions configures the summarized-context window.
type ConversationOptions struct {
	KeepRecentTurns int  // verbatim turns per inference (default 2, min 1)
	SummaryMaxChars int  // rolling summary size cap (default 2000)
	DisableSummary  bool // pure-memory mode: drop folded turns without summarizing
}

// ConversationMemory maintains the rolling summary for one chat session.
type ConversationMemory struct {
	store *Store
	scope string
	opts  ConversationOptions

	mu      sync.Mutex
	summary string
	folded  int // leading turns already folded into summary
}

// NewConversationMemory binds a session's rolling summary to store. An
// existing summary node (chat resume) is loaded, including how many turns it
// already covers.
func NewConversationMemory(store *Store, sessionID string, opts ConversationOptions) *ConversationMemory {
	if opts.KeepRecentTurns < 1 {
		opts.KeepRecentTurns = 2
	}
	if opts.SummaryMaxChars <= 0 {
		opts.SummaryMaxChars = 2000
	}
	c := &ConversationMemory{store: store, scope: SessionScope(sessionID), opts: opts}
	if n, err := store.Get(c.scope, ConversationSummaryNodeID); err == nil {
		c.summary = n.Content
		for _, tag := range n.Tags {
			if rest, ok := strings.CutPrefix(tag, foldedTagPrefix); ok {
				if v, err := strconv.Atoi(rest); err == nil && v > 0 {
					c.folded = v
				}
			}
		}
	}
	return c
}

// Summary returns the current rolling summary ("" when nothing folded yet).
func (c *ConversationMemory) Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.summary
}

// ComposeHistory returns the model-visible prior history: the rolling summary
// (as a user/assistant pair, keeping roles alternating for providers that
// require it) followed by the not-yet-folded turns verbatim. prior is the
// surface's complete transcript excluding the current query. Falls back to
// prior unchanged while nothing has been folded.
func (c *ConversationMemory) ComposeHistory(prior []llm.Message) []llm.Message {
	c.mu.Lock()
	summary, folded := c.summary, c.folded
	c.mu.Unlock()

	turns := splitTurns(prior)
	// Never compose away turns the summary doesn't cover, and always keep
	// the verbatim window.
	if max := len(turns) - c.opts.KeepRecentTurns; folded > max {
		folded = max
	}

	// Pure-memory mode: emit only the verbatim window, no summary block.
	// Folded turns are gone from the model's view — recoverable solely via
	// the memory/KG tools.
	if c.opts.DisableSummary {
		if folded <= 0 {
			return prior
		}
		var out []llm.Message
		for _, turn := range turns[folded:] {
			out = append(out, turn...)
		}
		return out
	}

	if folded <= 0 || summary == "" {
		return prior
	}

	out := []llm.Message{
		llm.NewTextMessage("user", "## Conversation summary\nEarlier turns of this conversation were compressed into this rolling summary (they are not repeated verbatim):\n\n"+summary),
		llm.NewTextMessage("assistant", "Understood. I have the summary of the earlier conversation and will continue from it."),
	}
	for _, turn := range turns[folded:] {
		out = append(out, turn...)
	}
	return out
}

// Update folds turns that fell out of the verbatim window into the rolling
// summary via one summarizer inference, then persists the summary node. full
// is the complete transcript including the just-finished turn. No-op while
// the transcript still fits the window. On error nothing is folded — the
// next ComposeHistory degrades to a fuller history instead of losing turns.
func (c *ConversationMemory) Update(ctx context.Context, full []llm.Message, summarize SummarizeFunc) error {
	c.mu.Lock()
	turns := splitTurns(full)
	target := len(turns) - c.opts.KeepRecentTurns
	if target <= c.folded {
		c.mu.Unlock()
		return nil
	}

	// Pure-memory mode: advance the window without a summarizer inference.
	// Dropped turns leave the model-visible context permanently (the
	// loss-safety invariant is intentionally waived here); persist the folded
	// count so a resumed session keeps the same window boundary.
	if c.opts.DisableSummary {
		defer c.mu.Unlock()
		if _, err := c.store.Add(Node{
			ID:      ConversationSummaryNodeID,
			Type:    "summary",
			Title:   "Conversation summary (disabled)",
			Content: "[summary disabled: older turns dropped, recoverable via memory]",
			Tags:    []string{foldedTagPrefix + strconv.Itoa(target)},
			Scope:   c.scope,
			Source:  "conversation-memory",
		}); err != nil {
			return fmt.Errorf("cannot persist conversation fold marker: %w", err)
		}
		c.folded = target
		return nil
	}

	// Snapshot what the summarizer call needs, then release the lock — the
	// LLM call below can run for seconds, and Summary()/ComposeHistory() are
	// read-only and shouldn't block on it.
	priorSummary := c.summary
	newTurns := turns[c.folded:target]
	c.mu.Unlock()

	prompt := buildSummaryPrompt(priorSummary, newTurns, c.opts.SummaryMaxChars)
	updated, err := summarize(ctx, prompt)
	if err != nil {
		return fmt.Errorf("conversation summarizer failed: %w", err)
	}
	updated = strings.TrimSpace(updated)
	if updated == "" {
		return fmt.Errorf("conversation summarizer returned an empty summary")
	}
	// Models overshoot character budgets; tolerate 25% then hard-cap so the
	// "bounded per-turn context" contract holds. Rune-safe: this is
	// model-generated text and routinely contains multi-byte characters.
	if r := []rune(updated); len(r) > c.opts.SummaryMaxChars*5/4 {
		updated = string(r[:c.opts.SummaryMaxChars]) + "…"
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if target <= c.folded {
		// Another Update already folded past this point while we were
		// summarizing (e.g. a concurrent caller) — its result stands, don't
		// clobber it with one built from a now-stale prior summary.
		return nil
	}
	if _, err := c.store.Add(Node{
		ID:      ConversationSummaryNodeID,
		Type:    "summary",
		Title:   "Conversation summary",
		Content: updated,
		Tags:    []string{foldedTagPrefix + strconv.Itoa(target)},
		Scope:   c.scope,
		Source:  "conversation-memory",
	}); err != nil {
		return fmt.Errorf("cannot persist conversation summary: %w", err)
	}
	c.summary = updated
	c.folded = target
	return nil
}

// ProviderSummarize is the standard SummarizeFunc body over an
// llm.LLMProvider. Surfaces wrap it in a closure that re-reads the provider
// per call so mid-session /model swaps are honored.
func ProviderSummarize(ctx context.Context, provider llm.LLMProvider, prompt string) (string, error) {
	res, err := provider.Generate(ctx, summarizerSystemPrompt,
		[]llm.Message{llm.NewTextMessage("user", prompt)}, nil)
	if err != nil {
		return "", err
	}
	return res.Response, nil
}

const summarizerSystemPrompt = "You maintain a rolling summary of an ongoing conversation so future turns can continue without the full transcript. Reply with ONLY the updated summary text — no preamble, no markdown fences."

func buildSummaryPrompt(summary string, newTurns [][]llm.Message, maxChars int) string {
	var sb strings.Builder
	sb.WriteString("Current rolling summary (empty if none):\n<<<\n")
	sb.WriteString(summary)
	sb.WriteString("\n>>>\n\nNew conversation turns to fold into the summary:\n<<<\n")
	for _, turn := range newTurns {
		for _, m := range turn {
			label := "User"
			if m.Role == "assistant" {
				label = "Assistant"
			}
			content := m.AsText()
			if r := []rune(content); len(r) > turnMsgMaxChars {
				content = string(r[:turnMsgMaxChars]) + "…[truncated]"
			}
			fmt.Fprintf(&sb, "%s: %s\n", label, content)
		}
	}
	fmt.Fprintf(&sb, ">>>\n\nRewrite the rolling summary to incorporate the new turns. Preserve every fact, decision, requirement, name, number, file path, and open question that later turns might need; prefer dropping pleasantries over content. Maximum %d characters.", maxChars)
	return sb.String()
}

// splitTurns groups a flat transcript into turns: each user message starts a
// new turn; assistant (and any other) messages attach to the current one.
func splitTurns(msgs []llm.Message) [][]llm.Message {
	var turns [][]llm.Message
	for _, m := range msgs {
		if m.Role == "user" || len(turns) == 0 {
			turns = append(turns, []llm.Message{m})
		} else {
			last := len(turns) - 1
			turns[last] = append(turns[last], m)
		}
	}
	return turns
}
