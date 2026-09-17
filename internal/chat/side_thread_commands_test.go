package chat

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// TestClearInSideThreadDoesNotWipeMainHistory is the C3 repro. m.blocks
// cleared the visible (side) thread while m.history cleared the MAIN
// conversation's LLM history, which is Model-level and not per-thread. The
// user asked to clear a side thread and instead the host's entire memory was
// wiped, with the main transcript still rendering every prior turn when they
// switched back and nothing on screen saying it had happened.
func TestClearInSideThreadDoesNotWipeMainHistory(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main q"}, {Type: BlockAssistant, Text: "main a"}}
	m.history = []llm.Message{llm.NewTextMessage("user", "main q"), llm.NewTextMessage("assistant", "main a")}
	m.totalTokens = 1234
	m = m.switchThread("Researcher-1")
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "side q"}}

	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "clear"})
	m = updated.(Model)

	if len(m.history) != 2 {
		t.Errorf("/clear in a side thread wiped the main conversation's LLM history: %+v", m.history)
	}
	if len(m.threads[""].blocks) != 2 {
		t.Errorf("/clear in a side thread must not touch the main transcript: %+v", m.threads[""].blocks)
	}
	if m.totalTokens != 1234 {
		t.Errorf("totalTokens = %d, want 1234 — the session counter is not this thread's to reset", m.totalTokens)
	}
	if len(m.blocks) != 0 {
		t.Errorf("/clear must clear the visible side thread: %+v", m.blocks)
	}
	if _, ok := m.roster.LastReply("Researcher-1"); ok {
		t.Error("/clear must also clear the roster's retained transcript, or the next Send replays everything the user just cleared")
	}
}

// TestClearOnMainIsUnchanged pins that the main conversation's /clear keeps
// today's behaviour exactly.
func TestClearOnMainIsUnchanged(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main q"}}
	m.history = []llm.Message{llm.NewTextMessage("user", "main q")}
	m.totalTokens = 99

	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "clear"})
	m = updated.(Model)

	if len(m.blocks) != 0 || len(m.history) != 0 || m.totalTokens != 0 {
		t.Errorf("/clear on main = blocks %d, history %d, tokens %d; want 0/0/0",
			len(m.blocks), len(m.history), m.totalTokens)
	}
}

// TestRetryInSideThreadRetriesThatThread is the C4 repro. /retry dispatched
// m.submitQuery(m.lastQuery) with no thread check, so it set generating and
// bumped the genToken on the VISIBLE (side) thread while running the query on
// the main runner. The resulting AgentDoneMsg carried the side thread's token,
// failed the mainGenToken comparison, and was dropped — leaving the side
// thread generating forever, anyThreadGenerating permanently true (so Ctrl+C
// could never quit), and main's history holding a user turn with no reply.
func TestRetryInSideThreadRetriesThatThread(t *testing.T) {
	m := threadModel(t)
	m.lastQuery = "main's last query"
	m.genToken = 4
	m = m.switchThread("Reviewer")

	next, _ := m.submitToAgent("Reviewer", "why skip the API docs?")
	m = next.(Model)
	settled, _ := m.Update(AgentChatDoneMsg{Agent: "Reviewer", Token: m.genToken, Reply: "out of scope"})
	m = settled.(Model)

	before := m.genToken
	out, cmd := m.handleSlashCommand(&SlashCommand{Name: "retry"})
	m = out.(Model)

	if cmd == nil {
		t.Fatal("/retry in a side thread must dispatch a run")
	}
	if !m.generating || m.genToken != before+1 {
		t.Errorf("generating=%v genToken=%d, want true / %d", m.generating, m.genToken, before+1)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.Type != BlockUser || last.Text != "why skip the API docs?" {
		t.Errorf("retried query = %+v, want this thread's own last message, not main's", last)
	}
	if len(m.history) != 0 {
		t.Errorf("/retry in a side thread appended to the main conversation's history: %+v", m.history)
	}
	if m.threads[""].genToken != 4 {
		t.Errorf("main's parked genToken = %d, want 4 — a side-thread retry must not touch it", m.threads[""].genToken)
	}
	// The dispatched Cmd must be a roster Send, not a main-runner turn. The
	// test roster has no Builder, so running it settles immediately as an
	// AgentChatDoneMsg for this thread.
	switch reply := cmd().(type) {
	case AgentChatDoneMsg:
		if reply.Agent != "Reviewer" {
			t.Errorf("/retry dispatched to %q, want Reviewer", reply.Agent)
		}
	default:
		t.Errorf("/retry produced %T, want AgentChatDoneMsg — it must go through the roster, not the main runner", reply)
	}
}

// TestRetryInSideThreadWithNothingToRetry: a thread the user has not sent
// anything to yet must say so rather than silently borrowing main's last
// query.
func TestRetryInSideThreadWithNothingToRetry(t *testing.T) {
	m := threadModel(t)
	m.lastQuery = "main's last query"
	m = m.switchThread("Reviewer")

	out, _ := m.handleSlashCommand(&SlashCommand{Name: "retry"})
	m = out.(Model)

	if m.generating {
		t.Error("/retry with nothing to retry must not start a run")
	}
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].Text, "Nothing to retry") {
		t.Errorf("blocks = %+v, want a 'Nothing to retry' notice", m.blocks)
	}
}

// TestCopyInSideThreadCopiesThatThread is part of the C4 audit sweep: /copy
// reads the visible blocks, so it was already thread-correct. Pinned so it
// stays that way.
func TestCopyInSideThreadCopiesThatThread(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockAssistant, Text: "main reply"}}
	m = m.switchThread("Reviewer")
	m.blocks = []ContentBlock{{Type: BlockAssistant, Text: "side reply"}}

	out, cmd := m.copySelectedReply()
	m = out.(Model)
	if cmd == nil {
		t.Fatal("/copy must dispatch a clipboard command")
	}
	if !strings.Contains(m.notice, "1/1") {
		t.Errorf("notice = %q, want the side thread's single reply, not main's", m.notice)
	}
}
