package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

func threadModel(t *testing.T) Model {
	t.Helper()
	r := agentchat.New(config.AgentChatConfig{}, nil, nil)
	r.AddHost("Coordinator", "m", "p")
	r.AddConfig("Reviewer", "m", "p")
	r.MarkRunning("Researcher-1", agentchat.KindSpawned, "m", "p")
	r.RecordTranscript("Researcher-1", []llm.Message{llm.NewTextMessage("user", "q"), llm.NewTextMessage("assistant", "a")}, nil)

	ta := textarea.New()
	return Model{
		width: 80, height: 24, ready: true,
		spinner:    spinner.New(),
		textarea:   ta,
		agentUsage: map[string]*agentUsageSnapshot{},
		roster:     r,
		threads:    map[string]agentThread{"": {}},
	}
}

func TestSwitchThreadPreservesEachTranscript(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main question"}}

	m = m.switchThread("Researcher-1")
	if m.thread != "Researcher-1" {
		t.Fatalf("thread = %q, want Researcher-1", m.thread)
	}
	if len(m.blocks) != 0 {
		t.Errorf("a fresh thread should start empty, got %+v", m.blocks)
	}
	m.blocks = append(m.blocks, ContentBlock{Type: BlockUser, Text: "side question"})

	m = m.switchThread("")
	if len(m.blocks) != 1 || m.blocks[0].Text != "main question" {
		t.Fatalf("main transcript not restored: %+v", m.blocks)
	}

	m = m.switchThread("Researcher-1")
	if len(m.blocks) != 1 || m.blocks[0].Text != "side question" {
		t.Fatalf("side transcript not restored: %+v", m.blocks)
	}
}

func TestSwitchThreadSwapsGenerationState(t *testing.T) {
	m := threadModel(t)
	m.generating = true
	m.genToken = 7

	m = m.switchThread("Reviewer")
	if m.generating {
		t.Error("a fresh thread must not inherit the main thread's generating flag")
	}
	if m.genToken != 0 {
		t.Errorf("genToken = %d, want 0 for a fresh thread", m.genToken)
	}

	m = m.switchThread("")
	if !m.generating || m.genToken != 7 {
		t.Errorf("main generation state not restored: generating=%v genToken=%d", m.generating, m.genToken)
	}
}

func TestAgentChatDoneAppendsToInactiveThread(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.genToken = 3
	m.generating = true
	m = m.switchThread("") // leave Reviewer generating in the background

	updated, _ := m.Update(AgentChatDoneMsg{Agent: "Reviewer", Token: 3, Reply: "looks fine"})
	m = updated.(Model)

	if len(m.blocks) != 0 {
		t.Errorf("a background thread's reply must not land in the main transcript: %+v", m.blocks)
	}
	tr := m.threads["Reviewer"]
	if len(tr.blocks) != 1 || tr.blocks[0].Text != "looks fine" {
		t.Fatalf("reply not stored on the background thread: %+v", tr.blocks)
	}
	if tr.generating {
		t.Error("background thread should no longer be generating")
	}
	if tr.lastReply != "looks fine" {
		t.Errorf("lastReply = %q", tr.lastReply)
	}
}

func TestAgentChatDoneStaleTokenIgnored(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.genToken = 5
	m.generating = true

	updated, _ := m.Update(AgentChatDoneMsg{Agent: "Reviewer", Token: 4, Reply: "stale"})
	m = updated.(Model)
	if len(m.blocks) != 0 {
		t.Errorf("stale reply should be dropped, got %+v", m.blocks)
	}
	if !m.generating {
		t.Error("stale reply should not clear the generating flag")
	}
}

func TestInterruptingOneThreadLeavesAnotherAlone(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.genToken = 2
	m.generating = true
	m = m.switchThread("Researcher-1")
	m.genToken = 9
	m.generating = true

	// Researcher-1's run settles; Reviewer must be untouched.
	updated, _ := m.Update(AgentChatDoneMsg{Agent: "Researcher-1", Token: 9, Reply: "done"})
	m = updated.(Model)
	if m.generating {
		t.Error("active thread should have stopped generating")
	}
	if rv := m.threads["Reviewer"]; !rv.generating || rv.genToken != 2 {
		t.Errorf("other thread disturbed: %+v", rv)
	}
}

func TestAgentChatDoneErrorRendersAndKeepsLastReply(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.genToken = 1
	m.generating = true
	tr := m.threads["Reviewer"]
	tr.lastReply = "earlier good reply"
	m.threads["Reviewer"] = tr

	updated, _ := m.Update(AgentChatDoneMsg{Agent: "Reviewer", Token: 1, Err: errStub{}})
	m = updated.(Model)
	if len(m.blocks) != 1 || m.blocks[0].Type != BlockSystem {
		t.Fatalf("error should render as a system block: %+v", m.blocks)
	}
	if !strings.Contains(m.blocks[0].Text, "boom") {
		t.Errorf("error text not shown: %q", m.blocks[0].Text)
	}
	if got := m.threads["Reviewer"].lastReply; got != "earlier good reply" {
		t.Errorf("a failed turn must not clobber lastReply; got %q", got)
	}
}

type errStub struct{}

func (errStub) Error() string { return "boom" }

func TestTabCyclesThreadsWhenInputEmpty(t *testing.T) {
	m := threadModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.thread != "Reviewer" {
		t.Errorf("Tab from main should enter the first agent thread, got %q", m.thread)
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.thread != "Researcher-1" {
		t.Errorf("Tab should advance to the next thread, got %q", m.thread)
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(Model)
	if m.thread != "Reviewer" {
		t.Errorf("Shift+Tab should go back, got %q", m.thread)
	}
}

func TestTabCompletesAgentNameMidTyping(t *testing.T) {
	m := threadModel(t)
	m.textarea.SetValue("/agent Res")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if got := m.textarea.Value(); got != "/agent Researcher-1" {
		t.Errorf("textarea = %q, want /agent Researcher-1", got)
	}
	if m.thread != "" {
		t.Errorf("completing a name must not switch threads; thread = %q", m.thread)
	}
}

func TestEscInThreadReturnsToMain(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.thread != "" {
		t.Errorf("Esc in a thread should return to main, thread = %q", m.thread)
	}
}

// --- Review-fix regression tests ---

// TestAgentDoneMsg_TokenCollisionDoesNotCorruptVisibleThread proves Finding
// 1: AgentDoneMsg is always the main conversation's completion. Per-thread
// genToken counters are independent and every thread starts at 0, so a
// collision (main and a side thread both at token 1) is the normal case,
// not a corner case — the handler must resolve against the main thread's
// token/blocks/generating/cancel state, never the mirrored fields of
// whatever thread happens to be on screen.
func TestAgentDoneMsg_TokenCollisionDoesNotCorruptVisibleThread(t *testing.T) {
	m := threadModel(t)
	// Main conversation has a run in flight, token 1.
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main question"}, {Type: BlockAssistant}}
	m.genToken = 1
	m.generating = true

	// Switch to Reviewer, leaving main generating in the background.
	m = m.switchThread("Reviewer")
	// Reviewer's independent counter also happens to be at token 1 — a
	// real collision, not a contrived one.
	m.genToken = 1
	m.generating = true
	m.cancelGen = func() {}
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "reviewer question"}}

	updated, cmd := m.Update(AgentDoneMsg{Token: 1, Response: "main's answer"})
	m = updated.(Model)

	if len(m.blocks) != 1 || m.blocks[0].Text != "reviewer question" {
		t.Fatalf("main's AgentDoneMsg must not write into the visible side thread: %+v", m.blocks)
	}
	if !m.generating {
		t.Error("Reviewer's generating flag must not be cleared by main's completion")
	}
	if m.cancelGen == nil {
		t.Error("Reviewer's cancel func must not be dropped by main's completion")
	}

	main := m.threads[""]
	if main.generating {
		t.Error("main thread should no longer be generating")
	}
	if len(main.blocks) != 2 || main.blocks[1].Text != "main's answer" {
		t.Fatalf("main's reply should land on the main thread's parked blocks: %+v", main.blocks)
	}
	if cmd == nil {
		t.Error("AgentDoneMsg must re-arm waitForEvent, not return a bare nil cmd")
	}
}

// TestAgentDoneMsg_StaleTokenStillRearmsBridge proves the other half of
// Finding 1: when the token genuinely doesn't match (an interrupted main
// run), the message must be dropped without mutating either thread, while the
// bridge pump is still re-armed.
//
// To be accurate about what the old `return m, nil` did: it did NOT stop
// event delivery for the session. waitForEvent is a blocking receive, and
// every bridge-driven handler re-arms itself, so the pump survives. What it
// did was consume one armed reader without replacing it, which is a leak of
// pump depth rather than a stall. Re-arming is still the right behaviour —
// the cost is one extra parked reader, and the alternative depends on a
// subtle invariant holding forever.
func TestAgentDoneMsg_StaleTokenStillRearmsBridge(t *testing.T) {
	m := threadModel(t)
	ch := make(chan tea.Msg, 1)
	ch <- StatusMsg{Text: "queued"}
	m.bridgeCh = ch
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main question"}, {Type: BlockAssistant}}
	m.genToken = 2
	m.generating = true

	m = m.switchThread("Reviewer")
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "reviewer question"}}

	updated, cmd := m.Update(AgentDoneMsg{Token: 1, Response: "late/stale"})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("a stale AgentDoneMsg must still re-arm waitForEvent — losing the bridge pump is worse than losing one message")
	}
	got := cmd()
	if sm, ok := got.(StatusMsg); !ok || sm.Text != "queued" {
		t.Errorf("re-armed cmd should drain the bridge channel, got %#v", got)
	}
	main := m.threads[""]
	if !main.generating || len(main.blocks) != 2 {
		t.Errorf("a stale message must not mutate the main thread's state: %+v", main)
	}
	if len(m.blocks) != 1 {
		t.Errorf("a stale message must not touch the visible side thread either: %+v", m.blocks)
	}
}

// TestTokenChunkLandsOnMainThreadWhileSideThreadVisible proves Finding 2: a
// stream event (chunks, tool calls, reasoning, spawned-agent lifecycle)
// always belongs to the main run — Roster.Send settles rather than
// streams — so it must land on the main thread's blocks even while a side
// thread is on screen, and must leave that side thread's blocks alone.
func TestTokenChunkLandsOnMainThreadWhileSideThreadVisible(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main q"}, {Type: BlockAssistant}}
	m.genToken = 1
	m.generating = true

	m = m.switchThread("Reviewer")
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "reviewer q"}}

	updated, _ := m.Update(TokenChunkMsg{Text: "hello "})
	m = updated.(Model)

	if len(m.blocks) != 1 || m.blocks[0].Text != "reviewer q" {
		t.Fatalf("a main-run chunk must not touch the visible side thread's blocks: %+v", m.blocks)
	}
	main := m.threads[""]
	if len(main.blocks) != 2 || main.blocks[1].Text != "hello " {
		t.Fatalf("chunk should land on the main thread's parked assistant block: %+v", main.blocks)
	}
}

// TestSideThreadReplyNotDuplicatedByMainStream proves the other half of
// Finding 2: before the fix, a main-run chunk arriving while a side thread
// was visible would walk back via appendToCurrentAssistant into that
// thread's own (possibly prior-turn) assistant block, and the thread's real
// AgentChatDoneMsg reply would then land as a second, separate block —
// reading as a duplicate. With chunks routed to the main thread instead,
// the side thread must end up with exactly one new reply block.
func TestSideThreadReplyNotDuplicatedByMainStream(t *testing.T) {
	m := threadModel(t)
	// Main conversation is generating in the background.
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "main q"}, {Type: BlockAssistant}}
	m.genToken = 5
	m.generating = true

	m = m.switchThread("Reviewer")
	// A prior turn already completed on this thread.
	m.blocks = []ContentBlock{
		{Type: BlockUser, Text: "first question"},
		{Type: BlockAssistant, Text: "first reply"},
	}

	updated, _ := m.submitToAgent("Reviewer", "second question")
	m = updated.(Model)
	if len(m.blocks) != 3 {
		t.Fatalf("submitToAgent should only add a user block, got %+v", m.blocks)
	}
	token := m.genToken

	// A main-run chunk arrives while Reviewer is on screen.
	updated, _ = m.Update(TokenChunkMsg{Text: "stray main text"})
	m = updated.(Model)
	if len(m.blocks) != 3 || m.blocks[1].Text != "first reply" {
		t.Fatalf("Reviewer's prior reply must not be touched by a main-run chunk: %+v", m.blocks)
	}

	// Reviewer's own reply settles.
	updated, _ = m.Update(AgentChatDoneMsg{Agent: "Reviewer", Token: token, Reply: "second reply"})
	m = updated.(Model)
	if len(m.blocks) != 4 {
		t.Fatalf("expected exactly one new reply block (no duplication), got %d: %+v", len(m.blocks), m.blocks)
	}
	if m.blocks[3].Text != "second reply" {
		t.Errorf("last block should be the settled reply, got %+v", m.blocks[3])
	}
}

// TestUserInputOnlyAnsweredFromMainThread proves Finding 3: a pending
// user_input request belongs to the main conversation (a side thread's
// agent never has the user_input tool — see agentChatBuilder), so pressing
// Enter in a side thread must never be interpreted as answering it. It
// should submit to that thread's agent instead, leaving the request queued.
func TestUserInputOnlyAnsweredFromMainThread(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.waitingForInput = true
	m.textarea.SetValue("some answer")
	respCh := make(chan string, 1)
	m.userInputRespCh = respCh

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	select {
	case v := <-respCh:
		t.Fatalf("a user_input request must not be answered from a side thread, got %q", v)
	default:
	}
	if !m.waitingForInput {
		t.Error("waitingForInput must stay true until answered from the main thread")
	}
	if !m.generating {
		t.Error("Enter in a side thread should submit to that thread's agent instead")
	}
	if len(m.blocks) != 1 || m.blocks[0].Text != "some answer" {
		t.Fatalf("the input should have been submitted to the side thread, got %+v", m.blocks)
	}
}

// TestUserInputAnsweredFromMainThreadStillWorks is the byte-identical
// regression check for Finding 3's fix: on the main conversation, Enter
// must still answer a pending user_input request exactly as before.
func TestUserInputAnsweredFromMainThreadStillWorks(t *testing.T) {
	m := threadModel(t)
	m.waitingForInput = true
	m.textarea.SetValue("42")
	respCh := make(chan string, 1)
	m.userInputRespCh = respCh

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	select {
	case v := <-respCh:
		if v != "42" {
			t.Errorf("response = %q, want %q", v, "42")
		}
	default:
		t.Fatal("a main-thread answer should be sent to userInputRespCh")
	}
	if m.waitingForInput {
		t.Error("waitingForInput should clear after answering")
	}
}

// TestView_LongThreadNameFitsWidth proves Finding 4: the prompt prefix
// naming the thread must not push the input row wider than m.width. An
// overrun there is invisible to clampToHeight (which only counts logical
// lines) — the terminal wraps the overlong line into an extra visual row,
// and the status bar, being last, is what falls off the bottom.
func TestView_LongThreadNameFitsWidth(t *testing.T) {
	m := Model{width: 80, height: 24, historyIdx: -1, spinner: spinner.New()}
	m.textarea = textarea.New()
	m = m.handleResize()
	m.thread = "AVeryLongAgentNameThatWouldOtherwiseWrapTheInputRowAndEatTheStatusBar"
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "hi"}}
	m.updateViewport()

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != m.height {
		t.Fatalf("View() produced %d lines, want exactly %d", len(lines), m.height)
	}
	inputFirstLine := lines[1+stickyHeaderHeight+m.viewport.Height]
	if w := lipgloss.Width(inputFirstLine); w > m.width {
		t.Errorf("input row width = %d, want <= %d (a long thread name must not overflow the row): %q", w, m.width, inputFirstLine)
	}
	last := lines[len(lines)-1]
	if strings.TrimSpace(last) == "" {
		t.Errorf("status bar must not be truncated to blank by the overflow: got %q", last)
	}
}

// TestCompleteAgentName_ExcludesHostEntries proves Finding 5: completion
// must be restricted to the same set threadOrder offers (host entries
// excluded), so Tab never completes to a name Tab-cycling itself cannot
// reach.
func TestCompleteAgentName_ExcludesHostEntries(t *testing.T) {
	m := threadModel(t)
	m.textarea.SetValue("/agent Co")
	updated, done := m.completeAgentName()
	if done {
		t.Errorf("completion must not match the host entry 'Coordinator', got %q", updated.textarea.Value())
	}
}

// TestSubmitToAgent_RecordsInputHistory proves Finding 6: a query submitted
// to a side thread must be recallable with Up, matching submitQuery.
func TestSubmitToAgent_RecordsInputHistory(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")

	updated, _ := m.submitToAgent("Reviewer", "how does this look?")
	m = updated.(Model)

	if len(m.inputHistory) != 1 || m.inputHistory[0] != "how does this look?" {
		t.Fatalf("submitToAgent should record the query in inputHistory, got %+v", m.inputHistory)
	}
	if m.historyIdx != -1 {
		t.Errorf("historyIdx should reset to -1 after a submit, got %d", m.historyIdx)
	}

	m.textarea.SetValue("draft")
	m.recallPrev()
	if got := m.textarea.Value(); got != "how does this look?" {
		t.Errorf("Up should recall the side-thread query, got %q", got)
	}
}
