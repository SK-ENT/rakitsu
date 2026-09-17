package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// pickerModel builds a Model with a roster holding a host, a config agent, a
// running child, a finished child, and an evicted child.
func pickerModel(t *testing.T) Model {
	t.Helper()
	r := agentchat.New(config.AgentChatConfig{MaxRetained: 1}, nil, nil)
	r.AddHost("Coordinator", "gemini-3.5-flash-lite", "litellm")
	r.AddConfig("Reviewer", "gpt-4o-mini", "openai")
	r.MarkRunning("BM25-Summary-1", agentchat.KindSpawned, "gpt-4o-mini", "openai")
	r.MarkRunning("HNSW-Summary-1", agentchat.KindSpawned, "gpt-4o-mini", "openai")
	r.RecordTranscript("HNSW-Summary-1", []llm.Message{llm.NewTextMessage("user", "q"), llm.NewTextMessage("assistant", "a")}, nil)
	r.MarkRunning("Researcher-1", agentchat.KindSpawned, "gpt-4o-mini", "openai")
	r.RecordTranscript("Researcher-1", []llm.Message{llm.NewTextMessage("user", "q"), llm.NewTextMessage("assistant", "a")}, nil)

	return Model{
		width: 80, height: 24, ready: true,
		spinner:    spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{},
		roster:     r,
		threads:    map[string]agentThread{"": {}},
	}
}

func TestCtrlA_OpensAgentPicker(t *testing.T) {
	m := pickerModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := updated.(Model)
	if m2.activePopup != popupAgents {
		t.Fatalf("activePopup = %v, want popupAgents", m2.activePopup)
	}
	if len(m2.pickerRows) != 5 {
		t.Errorf("pickerRows = %d, want 5", len(m2.pickerRows))
	}
	if m2.pickerSel != 0 {
		t.Errorf("pickerSel = %d, want 0", m2.pickerSel)
	}
}

func TestCtrlA_WithNoRosterExplains(t *testing.T) {
	m := Model{width: 80, height: 24, ready: true, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{}, threads: map[string]agentThread{"": {}}}
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := updated.(Model)
	if m2.activePopup != popupNone {
		t.Errorf("activePopup = %v, want popupNone when no roster", m2.activePopup)
	}
	if len(m2.blocks) != 1 || m2.blocks[0].Type != BlockSystem {
		t.Fatalf("want one system block explaining the feature is off, got %+v", m2.blocks)
	}
	if !strings.Contains(m2.blocks[0].Text, "agent_chat") {
		t.Errorf("explanation should name the setting, got %q", m2.blocks[0].Text)
	}
}

func TestPickerArrowKeysMoveSelection(t *testing.T) {
	m := pickerModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.pickerSel != 1 {
		t.Errorf("after Down, pickerSel = %d, want 1", m.pickerSel)
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.pickerSel != 0 {
		t.Errorf("after Up, pickerSel = %d, want 0", m.pickerSel)
	}
	// Up at the top wraps to the bottom — a keyboard user should never hit
	// an invisible wall.
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.pickerSel != 4 {
		t.Errorf("Up at top should wrap to 4, got %d", m.pickerSel)
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.pickerSel != 0 {
		t.Errorf("Down at bottom should wrap to 0, got %d", m.pickerSel)
	}
}

func TestPickerTabMovesSelection(t *testing.T) {
	m := pickerModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.pickerSel != 1 {
		t.Errorf("Tab in picker should move down; pickerSel = %d, want 1", m.pickerSel)
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(Model)
	if m.pickerSel != 0 {
		t.Errorf("Shift+Tab in picker should move up; pickerSel = %d, want 0", m.pickerSel)
	}
}

func TestPickerEscCloses(t *testing.T) {
	m := pickerModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.activePopup != popupNone {
		t.Errorf("Esc should close the picker, activePopup = %v", m.activePopup)
	}
	if m.thread != "" {
		t.Errorf("Esc should not enter a thread, thread = %q", m.thread)
	}
}

func TestAgentPickerRendersEveryStatus(t *testing.T) {
	m := pickerModel(t)
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	body := m.agentPicker()
	for _, want := range []string{
		"Coordinator", "host",
		"Reviewer", "not run yet",
		"BM25-Summary-1", "running",
		"Researcher-1", "done",
		"HNSW-Summary-1", "transcript evicted",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("picker body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "⏎ open") || !strings.Contains(body, "esc") {
		t.Errorf("picker footer missing key hints:\n%s", body)
	}
}

func TestAgentPickerNamesDroppedEntries(t *testing.T) {
	r := agentchat.New(config.AgentChatConfig{}, nil, nil)
	for i := 0; i < 205; i++ {
		r.MarkRunning("child-"+itoaTest(i), agentchat.KindSpawned, "m", "p")
	}
	m := Model{width: 80, height: 24, ready: true, spinner: spinner.New(),
		agentUsage: map[string]*agentUsageSnapshot{}, roster: r,
		threads: map[string]agentThread{"": {}}}
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	body := m.agentPicker()
	if !strings.Contains(body, "5 older agents dropped") {
		t.Errorf("picker must say how many entries were dropped:\n%s", body)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// pickerRowFor returns the single picker line naming agent, so a column
// assertion can't be satisfied by some other agent's row.
func pickerRowFor(t *testing.T, body, agent string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, agent) {
			return line
		}
	}
	t.Fatalf("no picker row for %q:\n%s", agent, body)
	return ""
}

// TestAgentPickerShowsTokensAndModel pins the two columns the spec's picker
// mock asked for: what the thread has cost so far, and which model is
// answering it. The token figure comes from the live usage tracker, the only
// place in the process that actually counts tokens — agentchat.Entry never
// carried one. The model comes from the roster, which knows it for every
// entry including those that have not emitted usage yet.
func TestAgentPickerShowsTokensAndModel(t *testing.T) {
	m := pickerModel(t)
	m.width = 140
	m.agentUsage["Researcher-1"] = &agentUsageSnapshot{Model: "gpt-4o-mini", Tokens: 1234}

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	row := pickerRowFor(t, m.agentPicker(), "Researcher-1")
	if !strings.Contains(row, "1.2K") {
		t.Errorf("Researcher-1 row missing its token total, got %q", row)
	}
	if !strings.Contains(row, "gpt-4o-mini") {
		t.Errorf("Researcher-1 row missing its model, got %q", row)
	}
}

// TestAgentPickerDropsModelColumnOnNarrowTerminal pins the trade the column
// layout makes. The model is the one column that can be spared, so a
// terminal too narrow for all five loses it whole — the alternative is
// lipgloss wrapping every row onto two lines, which costs the picker its
// one-row-per-agent shape.
func TestAgentPickerDropsModelColumnOnNarrowTerminal(t *testing.T) {
	m := pickerModel(t)
	m.width = 56
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(Model)

	body := m.agentPicker()
	if strings.Contains(body, "gpt-4o-mini") || strings.Contains(body, "gemini") {
		t.Errorf("narrow picker kept the model column instead of dropping it:\n%s", body)
	}
	row := pickerRowFor(t, body, "Researcher-1")
	if !strings.Contains(row, "done") {
		t.Errorf("narrow picker dropped the state column, which decides whether the thread is reachable: %q", row)
	}
}

// TestPickerTokensUnreportedShowsDash: an agent the usage tracker has not
// heard from has not spent 0 tokens — it has not reported. Printing 0 would
// state as fact something the picker does not know.
func TestPickerTokensUnreportedShowsDash(t *testing.T) {
	m := pickerModel(t)
	e := agentchat.Entry{Name: "Researcher-1"}

	if got := m.pickerTokens(e); got != "—" {
		t.Errorf("pickerTokens with no usage entry = %q, want a dash", got)
	}

	m.agentUsage["Researcher-1"] = &agentUsageSnapshot{Tokens: 0}
	if got := m.pickerTokens(e); got != "—" {
		t.Errorf("pickerTokens with a zero total = %q, want a dash", got)
	}
}

// TestPickerLabelsFailedAgentHonestly pins the user-facing half of I1: a
// child whose run errored must not read as "done" in the picker. Telling the
// user a child finished when it did not is exactly the "never present a guess
// as fact" violation the status enum was extended to prevent.
func TestPickerLabelsFailedAgentHonestly(t *testing.T) {
	e := agentchat.Entry{Name: "Researcher-1", Kind: agentchat.KindSpawned, Status: agentchat.StatusFailed, Turns: 3}
	if got := pickerKindLabel(e); got != "failed" {
		t.Errorf("pickerKindLabel = %q, want %q", got, "failed")
	}
	if got := pickerDetail(e); got != "3 turns" {
		t.Errorf("pickerDetail = %q, want %q — the turns actually taken before the failure", got, "3 turns")
	}
}

// TestPickerLabelsIncompleteAgentHonestly is the same rule for the third
// outcome: a child that used up max_iterations is neither done nor failed, and
// the column that decides whether you can talk to it must say which.
func TestPickerLabelsIncompleteAgentHonestly(t *testing.T) {
	e := agentchat.Entry{Name: "Researcher-1", Kind: agentchat.KindSpawned, Status: agentchat.StatusIncomplete, Turns: 3}
	if got := pickerKindLabel(e); got != "incomplete" {
		t.Errorf("pickerKindLabel = %q, want %q", got, "incomplete")
	}
	if got := pickerDetail(e); got != "3 turns" {
		t.Errorf("pickerDetail = %q, want %q — the turns actually taken before it ran out", got, "3 turns")
	}
}
