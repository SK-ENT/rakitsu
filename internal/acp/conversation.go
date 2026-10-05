package acp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
)

// ConvTurn carries settings.memory.conversation state across the RunFunc
// boundary for one session/prompt turn. A nil *ConvTurn means "not engaged":
// the RunFunc gets the text-composed query exactly as before.
//
// History is filled by the ACP server (the rolling summary pair plus recent
// verbatim turns, from memory.ConversationMemory.ComposeHistory). Summarize is
// filled by the RunFunc: only it knows which agent, and therefore which LLM
// provider, runs the turn. A RunFunc that does not set it simply skips the
// post-turn fold.
type ConvTurn struct {
	History   []llm.Message
	Summarize memory.SummarizeFunc
}

// convSummarizeTimeout bounds the post-turn summarizer inference.
const convSummarizeTimeout = 60 * time.Second

// sessionConv is one session's conversation-memory state: the (text-bounded)
// transcript and the rolling summary over it.
type sessionConv struct {
	mem *memory.ConversationMemory

	mu         sync.Mutex
	transcript []llm.Message
}

// convEngaged mirrors the chat surfaces' gate: memory and conversation both
// enabled, and a single bare agent runs the turn. Chat only engages for a
// plain agent runner; a config with an orchestrator has no single agent whose
// provider owns the turn, so it keeps the text-composed history.
//
// Orchestrator configs keep the text-composed history in ACP (single-agent
// only). Orchestrator support is deliberately not implemented here.
func (s *Server) convEngaged() bool {
	m := s.cfg.Settings.Memory
	if !m.Enabled || !m.Conversation.Enabled {
		return false
	}
	return len(s.cfg.Agents) > 0 && s.cfg.Orchestrator == nil
}

// convFor returns the session's conversation state, creating it on first use,
// or nil when the setting is off (or the memory store cannot be opened).
func (s *Server) convFor(sess *Session) *sessionConv {
	if !s.convEngaged() {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.conv != nil {
		return sess.conv
	}
	m := s.cfg.Settings.Memory
	store, err := memory.OpenShared(m.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rakitsu acp: session %s: memory store init failed: %v (conversation memory disabled)\n", sess.ID, err)
		return nil
	}
	sess.conv = &sessionConv{mem: memory.NewConversationMemory(store, sess.ID, memory.ConversationOptions{
		KeepRecentTurns: m.Conversation.KeepRecentTurns,
		SummaryMaxChars: m.Conversation.SummaryMaxChars,
		DisableSummary:  m.Conversation.DisableSummary,
	})}
	return sess.conv
}

// compose returns the model-visible prior history: the rolling summary plus
// recent verbatim turns once turns have been folded. Until then (or while the
// summarizer keeps failing) ComposeHistory returns the whole transcript, so
// the result is capped at historyMaxTurns turns — the same bound the
// text-composed path has — so a failing summarizer degrades to bounded recent
// turns, never to unbounded re-feed.
func (c *sessionConv) compose() []llm.Message {
	c.mu.Lock()
	prior := append([]llm.Message(nil), c.transcript...)
	c.mu.Unlock()
	return capTurns(c.mem.ComposeHistory(prior), historyMaxTurns)
}

// capTurns keeps at most max trailing turns (a turn starts at a user
// message). A leading rolling-summary pair is preserved.
func capTurns(msgs []llm.Message, max int) []llm.Message {
	var head []llm.Message
	if len(msgs) >= 2 && msgs[0].Role == "user" && strings.HasPrefix(msgs[0].AsText(), "## Conversation summary") {
		head, msgs = msgs[:2], msgs[2:]
	}
	var starts []int
	for i, m := range msgs {
		if m.Role == "user" {
			starts = append(starts, i)
		}
	}
	if len(starts) > max {
		msgs = msgs[starts[len(starts)-max]:]
	}
	return append(append([]llm.Message(nil), head...), msgs...)
}

func (c *sessionConv) record(query, response string) {
	c.mu.Lock()
	c.transcript = append(c.transcript,
		llm.NewTextMessage("user", truncateHistoryText(query)),
		llm.NewTextMessage("assistant", truncateHistoryText(response)))
	c.mu.Unlock()
}

// recordTurn stores a finished turn in whichever history this session uses.
func (s *Server) recordTurn(sess *Session, conv *sessionConv, query, response string) {
	if conv != nil {
		conv.record(query, response)
		return
	}
	sess.recordTurn(query, response)
}

// foldConversation folds turns that left the verbatim window into the rolling
// summary. Runs after the prompt response is written, so summarizer latency
// is not visible to the client. A failure folds nothing (the next compose
// stays capped but unsummarized) and never affects the finished turn.
func (s *Server) foldConversation(ctx context.Context, conv *sessionConv, turn *ConvTurn) {
	if conv == nil || turn == nil || turn.Summarize == nil {
		return
	}
	conv.mu.Lock()
	full := append([]llm.Message(nil), conv.transcript...)
	conv.mu.Unlock()
	sumCtx, cancel := context.WithTimeout(ctx, convSummarizeTimeout)
	defer cancel()
	if err := conv.mem.Update(sumCtx, full, turn.Summarize); err != nil {
		fmt.Fprintf(os.Stderr, "rakitsu acp: conversation summary not updated: %v\n", err)
	}
}
