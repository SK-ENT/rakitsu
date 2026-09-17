package chat

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// isQuitCmd reports whether cmd is exactly tea.Quit, by function identity —
// NOT by calling it. Several of the Cmds a "should not quit" branch might
// legitimately return (e.g. clearNoticeAfter) arm a real timer; invoking
// those in a test to inspect the resulting Msg would block for real
// wall-clock time. tea.Quit is a named package-level function, so its entry
// pointer is stable and safe to compare.
func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	return reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

func TestPushAppendsLabelledUserMessageToMain(t *testing.T) {
	m := threadModel(t)
	m.blocks = []ContentBlock{{Type: BlockUser, Text: "compare HNSW vs BM25"}}
	m.history = []llm.Message{llm.NewTextMessage("user", "compare HNSW vs BM25")}

	m = m.switchThread("Researcher-1")
	tr := m.threads["Researcher-1"]
	tr.lastReply = "The task text named only the README."
	m.threads["Researcher-1"] = tr

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(Model)

	if len(m.history) != 2 {
		t.Fatalf("history = %d messages, want 2", len(m.history))
	}
	pushed := m.history[1]
	if pushed.Role != "user" {
		t.Errorf("pushed message role = %q, want user — the user is handing the host this, the host did not fetch it", pushed.Role)
	}
	want := "[from Researcher-1] The task text named only the README."
	if pushed.AsText() != want {
		t.Errorf("pushed content = %q, want %q", pushed.AsText(), want)
	}
	if m.history[0].AsText() != "compare HNSW vs BM25" {
		t.Error("prior host history must not be rewritten")
	}

	main := m.threads[""]
	if len(main.blocks) != 2 || main.blocks[1].Text != want {
		t.Fatalf("main transcript did not receive the labelled block: %+v", main.blocks)
	}
	if main.blocks[0].Text != "compare HNSW vs BM25" {
		t.Error("prior main blocks must not be rewritten")
	}
}

func TestPushConfirmsInTheSideThread(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Researcher-1")
	tr := m.threads["Researcher-1"]
	tr.lastReply = "answer"
	m.threads["Researcher-1"] = tr

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(Model)
	if len(m.blocks) != 1 || m.blocks[0].Type != BlockSystem {
		t.Fatalf("want a system confirmation in the side thread: %+v", m.blocks)
	}
	if !strings.Contains(m.blocks[0].Text, "main conversation") {
		t.Errorf("confirmation should say where it went: %q", m.blocks[0].Text)
	}
}

func TestPushWithNoReplyIsANoOpWithAMessage(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(Model)

	if len(m.history) != 0 {
		t.Errorf("nothing should be pushed when there is no reply: %+v", m.history)
	}
	if len(m.blocks) != 1 || !strings.Contains(m.blocks[0].Text, "no reply") {
		t.Fatalf("want a message explaining there is nothing to push: %+v", m.blocks)
	}
}

func TestPushFromMainConversationExplains(t *testing.T) {
	m := threadModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(Model)
	if len(m.blocks) != 1 || !strings.Contains(m.blocks[0].Text, "agent thread") {
		t.Fatalf("want a message explaining push only applies in a thread: %+v", m.blocks)
	}
}

func TestSlashAgentWithNameEntersThread(t *testing.T) {
	m := threadModel(t)
	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "agent", Args: "Reviewer"})
	m = updated.(Model)
	if m.thread != "Reviewer" {
		t.Errorf("thread = %q, want Reviewer", m.thread)
	}
}

func TestSlashAgentUnknownNameNamesTheValidOnes(t *testing.T) {
	m := threadModel(t)
	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "agent", Args: "Nobody"})
	m = updated.(Model)
	if m.thread != "" {
		t.Errorf("an unknown name must not switch threads, thread = %q", m.thread)
	}
	if len(m.blocks) != 1 {
		t.Fatalf("want one system block, got %+v", m.blocks)
	}
	if !strings.Contains(m.blocks[0].Text, "Reviewer") || !strings.Contains(m.blocks[0].Text, "Researcher-1") {
		t.Errorf("error should name the valid agents: %q", m.blocks[0].Text)
	}
}

func TestSlashAgentInThreadReturnsToMain(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "agent", Args: ""})
	m = updated.(Model)
	if m.thread != "" {
		t.Errorf("bare /agent in a thread should return to main, thread = %q", m.thread)
	}
}

func TestSlashAgentOnMainOpensPicker(t *testing.T) {
	m := threadModel(t)
	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "agent", Args: ""})
	m = updated.(Model)
	if m.activePopup != popupAgents {
		t.Errorf("bare /agent on main should open the picker, activePopup = %v", m.activePopup)
	}
}

func TestSlashPushMatchesCtrlG(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Researcher-1")
	tr := m.threads["Researcher-1"]
	tr.lastReply = "answer"
	m.threads["Researcher-1"] = tr

	updated, _ := m.handleSlashCommand(&SlashCommand{Name: "push"})
	m = updated.(Model)
	if len(m.history) != 1 || !strings.HasPrefix(m.history[0].AsText(), "[from Researcher-1] ") {
		t.Errorf("/push should behave like Ctrl+G: %+v", m.history)
	}
}

func TestHelpTextMentionsAgentCommands(t *testing.T) {
	help := HelpText()
	for _, want := range []string{"/agent", "/push", "Ctrl+A", "Ctrl+G"} {
		if !strings.Contains(help, want) {
			t.Errorf("HelpText missing %q:\n%s", want, help)
		}
	}
}

// --- Carry-over A: a main-run user_input request must not clobber a side
// thread's in-progress input. ---

// TestUserInputRequestDoesNotClobberSideThreadDraft proves the request's
// arrival handler no longer touches the shared textarea unless main is the
// thread on screen — otherwise a question the visible thread cannot answer
// silently overwrites whatever the user is mid-typing there.
func TestUserInputRequestDoesNotClobberSideThreadDraft(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")
	m.textarea.SetValue("draft reply for Reviewer")
	m.textarea.Placeholder = "side thread placeholder"

	updated, _ := m.Update(UserInputRequestMsg{Question: "continue?", Default: "yes"})
	m = updated.(Model)

	if !m.waitingForInput {
		t.Error("waitingForInput must still be set even though main isn't visible")
	}
	if got := m.textarea.Value(); got != "draft reply for Reviewer" {
		t.Errorf("side thread draft was clobbered by the pending question, got %q", got)
	}
	if m.textarea.Placeholder != "side thread placeholder" {
		t.Errorf("side thread placeholder was clobbered, got %q", m.textarea.Placeholder)
	}

	main := m.threads[""]
	if len(main.blocks) != 1 || !strings.Contains(main.blocks[0].Text, "continue?") {
		t.Fatalf("the question should still be recorded on the main transcript: %+v", main.blocks)
	}
}

// TestUserInputRequestStillPrefillsWhenMainIsVisible is the byte-identical
// regression check: arriving while main IS on screen must still prefill the
// default and swap the placeholder, exactly as before.
func TestUserInputRequestStillPrefillsWhenMainIsVisible(t *testing.T) {
	m := threadModel(t)

	updated, _ := m.Update(UserInputRequestMsg{Question: "continue?", Default: "yes"})
	m = updated.(Model)

	if got := m.textarea.Value(); got != "yes" {
		t.Errorf("textarea = %q, want the default prefilled", got)
	}
	if !strings.Contains(m.textarea.Placeholder, "answer") {
		t.Errorf("placeholder should prompt for an answer, got %q", m.textarea.Placeholder)
	}
}

// TestUserInputRequestPickedUpOnReturnToMain proves the pending request is
// still surfaced once the user comes back to main — it isn't lost just
// because it arrived while a side thread was on screen.
func TestUserInputRequestPickedUpOnReturnToMain(t *testing.T) {
	m := threadModel(t)
	m = m.switchThread("Reviewer")

	updated, _ := m.Update(UserInputRequestMsg{Question: "continue?", Default: "yes"})
	m = updated.(Model)

	m = m.switchThread("")
	if got := m.textarea.Value(); got != "yes" {
		t.Errorf("returning to main should apply the pending default, got %q", got)
	}
	if !strings.Contains(m.textarea.Placeholder, "answer") {
		t.Errorf("returning to main should restore the waiting-for-answer placeholder, got %q", m.textarea.Placeholder)
	}
}

// --- Carry-over B: Ctrl+C must not quit while any thread — visible or
// not — is still generating. ---

// TestCtrlCDoesNotQuitWhileMainGeneratesInBackground covers the reported
// scenario: an idle side thread is on screen while the main run is still
// going in the background.
func TestCtrlCDoesNotQuitWhileMainGeneratesInBackground(t *testing.T) {
	m := threadModel(t)
	m.generating = true // main generating, still on screen
	m = m.switchThread("Reviewer")
	if m.generating {
		t.Fatal("test setup: Reviewer should start idle")
	}

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if isQuitCmd(cmd) {
		t.Fatal("Ctrl+C must not quit while the main run is still generating in the background")
	}
	if main := m.threads[""]; !main.generating {
		t.Error("Ctrl+C from an idle side thread must not cancel the main run's generation")
	}
}

// TestCtrlCDoesNotQuitWhileASideThreadGeneratesInBackground is the mirror
// case: the user is back on main (idle) while a side thread they left is
// still generating.
func TestCtrlCDoesNotQuitWhileASideThreadGeneratesInBackground(t *testing.T) {
	m := threadModel(t)
	tr := m.threads["Reviewer"]
	tr.generating = true
	m.threads["Reviewer"] = tr

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if isQuitCmd(cmd) {
		t.Fatal("Ctrl+C must not quit while a side thread is still generating in the background")
	}
	if rv := m.threads["Reviewer"]; !rv.generating {
		t.Error("Ctrl+C on main must not reach in and cancel a side thread's generation")
	}
}

// TestCtrlCStillQuitsWhenNothingIsGenerating is the regression guard for the
// existing contract: with nothing generating anywhere, Ctrl+C quits.
func TestCtrlCStillQuitsWhenNothingIsGenerating(t *testing.T) {
	m := threadModel(t)
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuitCmd(cmd) {
		t.Error("Ctrl+C should quit when nothing anywhere is generating")
	}
}

// TestCtrlCStillCancelsTheVisibleThreadEvenWhileMainAlsoGenerates proves the
// existing cancel behaviour is untouched: Ctrl+C always deals with the
// thread you're looking at first, even when another thread is also live.
func TestCtrlCStillCancelsTheVisibleThreadEvenWhileMainAlsoGenerates(t *testing.T) {
	m := threadModel(t)
	m.generating = true // main generating
	m = m.switchThread("Reviewer")
	m.generating = true // Reviewer also generating
	cancelled := false
	m.cancelGen = func() { cancelled = true }

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)

	if !cancelled {
		t.Error("Ctrl+C should cancel the visible thread's generation")
	}
	if m.generating {
		t.Error("the visible thread's generating flag should clear")
	}
	if isQuitCmd(cmd) {
		t.Error("cancelling a generation must not also quit")
	}
	if main := m.threads[""]; !main.generating {
		t.Error("main's background generation must be untouched by cancelling Reviewer")
	}
}
