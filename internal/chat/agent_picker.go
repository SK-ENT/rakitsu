package chat

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// agentChatDisabledNote is shown when Ctrl+A is pressed in a session with no
// roster. An empty popup would read as "no agents"; this says why.
const agentChatDisabledNote = "Directed agent chat is off for this session. " +
	"Enable it with settings.agent_chat.enabled: true in your config."

// agentThread is one conversation's view state. The main conversation lives
// under the "" key, so entering and leaving a thread is the same swap in both
// directions. The conversation itself (the llm.Message history) is owned by
// the roster for agent threads and by Model.history for the main one — this
// struct holds only what is on screen.
type agentThread struct {
	blocks     []ContentBlock
	genToken   int
	generating bool
	cancel     context.CancelFunc
	lastReply  string
	// lastQuery is this thread's own last submitted message, so /retry
	// re-runs the thread the user is looking at rather than the main
	// conversation's last turn.
	lastQuery string
}

// openAgentPicker snapshots the roster and opens the picker popup.
func (m Model) openAgentPicker() (tea.Model, tea.Cmd) {
	if m.roster == nil || !m.roster.Enabled() {
		m.blocks = append(m.blocks, ContentBlock{Type: BlockSystem, Text: agentChatDisabledNote})
		m.updateViewport()
		return m, nil
	}
	m.pickerRows = m.roster.List()
	m.pickerSel = 0
	m.activePopup = popupAgents
	return m, nil
}

// movePickerSelection steps the selection by delta, wrapping at both ends so
// a keyboard user never hits an invisible wall.
func (m Model) movePickerSelection(delta int) Model {
	n := len(m.pickerRows)
	if n == 0 {
		m.pickerSel = 0
		return m
	}
	m.pickerSel = ((m.pickerSel+delta)%n + n) % n
	return m
}

// agentPicker renders the picker body: one row per addressable agent, with
// the selection marker, plus a footer of key hints. When the hard entry cap
// dropped older agents, the footer names how many — a truncated list must
// never read as a complete one.
func (m Model) agentPicker() string {
	var b strings.Builder
	if len(m.pickerRows) == 0 {
		b.WriteString("No addressable agents in this session.\n\n")
	}
	cols := m.pickerColumns()
	for i, e := range m.pickerRows {
		marker := "  "
		if i == m.pickerSel {
			marker = "> "
		}
		b.WriteString(marker)
		b.WriteString(padCell(e.Name, cols.name))
		b.WriteString(" " + padCell(pickerKindLabel(e), cols.kind))
		b.WriteString(" " + padCell(pickerDetail(e), cols.detail))
		b.WriteString(" " + padCellRight(m.pickerTokens(e), cols.tokens))
		if cols.model > 0 {
			// Unpadded: the last column on the row needs no trailing run of
			// spaces, and lipgloss would keep them when it wraps.
			b.WriteString(" " + truncCell(e.Model, cols.model))
		}
		b.WriteString("\n")
	}
	if dropped := m.roster.Dropped(); dropped > 0 {
		fmt.Fprintf(&b, "\n(%d older agents dropped — the session outgrew the roster's entry cap)\n", dropped)
	}
	b.WriteString("\nSpawning is off inside a direct chat.\n")
	b.WriteString("\n↑↓ select   ⏎ open   tab next   esc cancel")
	return b.String()
}

// Picker column caps, in display cells. These are ceilings, not fixed
// widths: pickerColumns shrinks each one to the rows actually on screen so a
// roster of short names doesn't spend the popup on padding.
const (
	pickerNameMaxW   = 20
	pickerKindMaxW   = 11
	pickerDetailMaxW = 21
	pickerTokensMaxW = 7
	// pickerModelMinW is the narrowest model column worth printing. Below
	// this the column is dropped whole, because two characters and an
	// ellipsis name no model — it just costs the row width.
	pickerModelMinW = 8
	// popupBoxPadding is renderPopupBox's Padding(1, 2), left plus right.
	popupBoxPadding = 4
)

// pickerCols is the width of each picker column in display cells. A model
// width of 0 means the terminal is too narrow for that column at all.
type pickerCols struct{ name, kind, detail, tokens, model int }

// pickerColumns sizes the columns to the rows on screen, then gives what is
// left to the model column. The model is last and first to go because it is
// the least load-bearing: the name says which thread, the state column says
// whether it can be talked to, and the model is context. Dropping it beats
// letting lipgloss wrap every row in half on an 80-column terminal.
func (m Model) pickerColumns() pickerCols {
	var c pickerCols
	for _, e := range m.pickerRows {
		c.name = cellWidth(c.name, e.Name, pickerNameMaxW)
		c.kind = cellWidth(c.kind, pickerKindLabel(e), pickerKindMaxW)
		c.detail = cellWidth(c.detail, pickerDetail(e), pickerDetailMaxW)
		c.tokens = cellWidth(c.tokens, m.pickerTokens(e), pickerTokensMaxW)
	}
	// 2 for the selection marker, then one space before each later column.
	used := 2 + c.name + 1 + c.kind + 1 + c.detail + 1 + c.tokens
	if free := m.popupBoxWidth() - popupBoxPadding - used - 1; free >= pickerModelMinW {
		c.model = free
	}
	return c
}

// cellWidth grows w to fit s, never past max.
func cellWidth(w int, s string, max int) int {
	if n := lipgloss.Width(s); n > w {
		w = n
	}
	if w > max {
		w = max
	}
	return w
}

// truncCell shortens s to at most w display cells, marking the cut with an
// ellipsis so a clipped model name never reads as a whole one.
func truncCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// padCell left-aligns s in a w-cell column. Width is measured in display
// cells, not bytes, so the em dash pickerDetail uses lines up with the text
// in the rows above and below it.
func padCell(s string, w int) string {
	s = truncCell(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// padCellRight right-aligns s in a w-cell column, for the numeric column.
func padCellRight(s string, w int) string {
	s = truncCell(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// pickerTokens is the token column: what this agent has spent so far,
// according to the live usage tracker — the only place in the process that
// counts tokens. An agent the tracker has not heard from shows a dash and
// not 0: "0 tokens" is a claim about a run, and one that has not reported
// yet has not made it.
func (m Model) pickerTokens(e agentchat.Entry) string {
	snap, ok := m.agentUsage[e.Name]
	if !ok || snap.Tokens <= 0 {
		return "—"
	}
	return fmtTok(snap.Tokens)
}

// pickerKindLabel is the second column: where the agent came from, except
// that a running or evicted agent shows its state instead, because that is
// what decides whether you can talk to it.
func pickerKindLabel(e agentchat.Entry) string {
	switch {
	case e.Status == agentchat.StatusRunning:
		return "running"
	case e.Status == agentchat.StatusEvicted:
		return "evicted"
	case e.Status == agentchat.StatusFailed:
		// A run that errored or timed out is never labelled "done" — telling
		// the user a child finished when it did not is the exact failure this
		// column exists to avoid. The turn count still shows in pickerDetail:
		// the turns it did take before failing are real.
		return "failed"
	case e.Status == agentchat.StatusIncomplete:
		// Neither "done" nor "failed": the run used up its iteration budget.
		// Both neighbouring labels would be a claim about it that is not true.
		return "incomplete"
	case e.Kind == agentchat.KindHost:
		return "host"
	case e.Status == agentchat.StatusDone:
		return "done"
	default:
		return string(e.Kind)
	}
}

// pickerDetail is the third column: what state the agent's context is in.
func pickerDetail(e agentchat.Entry) string {
	switch e.Status {
	case agentchat.StatusRunning:
		return "—"
	case agentchat.StatusEvicted:
		return "transcript evicted"
	case agentchat.StatusIdle:
		if e.Kind == agentchat.KindHost {
			return "the main conversation"
		}
		return "not run yet"
	default:
		if e.Turns == 1 {
			return "1 turn"
		}
		return fmt.Sprintf("%d turns", e.Turns)
	}
}

// handleAgentPickerKey handles keys while the picker is open. Arrows and Tab
// move, Enter opens, Esc cancels. Everything else is swallowed so it never
// reaches the textarea underneath.
func (m Model) handleAgentPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.activePopup = popupNone
		return m, nil
	case tea.KeyUp:
		return m.movePickerSelection(-1), nil
	case tea.KeyDown, tea.KeyTab:
		return m.movePickerSelection(1), nil
	case tea.KeyShiftTab:
		return m.movePickerSelection(-1), nil
	case tea.KeyEnter:
		if m.pickerSel < 0 || m.pickerSel >= len(m.pickerRows) {
			m.activePopup = popupNone
			return m, nil
		}
		sel := m.pickerRows[m.pickerSel]
		m.activePopup = popupNone
		if sel.Kind == agentchat.KindHost {
			return m.switchThread(""), nil
		}
		return m.switchThread(sel.Name), nil
	}
	return m, nil
}

// switchThread puts a different conversation on screen. The current thread's
// view state is stashed under its own key and the target's is loaded — one
// symmetric swap in both directions, with the main conversation stored under
// "". Switching never cancels anything: a thread left generating keeps going
// and its reply lands on its stored blocks.
func (m Model) switchThread(name string) Model {
	if m.threads == nil {
		m.threads = map[string]agentThread{"": {}}
	}
	if name == m.thread {
		return m
	}

	cur := m.threads[m.thread]
	cur.blocks = m.blocks
	cur.genToken = m.genToken
	cur.generating = m.generating
	cur.cancel = m.cancelGen
	m.threads[m.thread] = cur

	next := m.threads[name]
	m.blocks = next.blocks
	m.genToken = next.genToken
	m.generating = next.generating
	m.cancelGen = next.cancel
	m.thread = name
	m.threads[name] = next

	// A pending user_input request only touches the shared textarea while
	// main is on screen (see the UserInputRequestMsg handler) — reapply it
	// now if this switch just landed back on main, so the answer prompt the
	// user left main to avoid isn't silently lost.
	if name == "" && m.waitingForInput {
		if m.userInputDefault != "" {
			m.textarea.SetValue(m.userInputDefault)
		}
		m.textarea.Placeholder = "Type your answer... (Enter to send)"
	}

	m.copySel = -1
	m.updateViewport()
	m.viewport.GotoBottom()
	return m
}

// submitToAgent sends one message to the thread's agent. The roster call runs
// off the update loop; its reply comes back as AgentChatDoneMsg tagged with
// this thread's generation token.
func (m Model) submitToAgent(name, query string) (tea.Model, tea.Cmd) {
	m.generating = true
	m.genToken++
	token := m.genToken

	// Nothing is armed here to route this send's bridge events. Roster.Send
	// tags them at the emitting end (telemetry.AgentEvent.Origin) and the
	// Update handlers read that tag, so there is no window in which a filter
	// is on when the send is logically over — the case Ctrl+C used to open by
	// clearing `generating` while the cancelled Send was still unwinding.

	// Remember the query per thread so /retry in this thread re-runs THIS
	// thread's last message rather than the main conversation's.
	t := m.threads[name]
	t.lastQuery = query
	m.threads[name] = t

	// Record the query for Up/Down history recall, matching submitQuery
	// (skip consecutive dupes, exit any in-progress history navigation) —
	// otherwise a message typed in a side thread can never be recalled.
	if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != query {
		m.inputHistory = append(m.inputHistory, query)
	}
	m.historyIdx = -1
	m.historyDraft = ""

	m.blocks = append(m.blocks, ContentBlock{Type: BlockUser, Text: query})
	m.updateViewport()
	m.viewport.GotoBottom()

	// Registered with the shutdown tracker before the Cmd is handed to
	// bubbletea, which runs it detached and never waits on it: without this,
	// exit can close the tool registry this send is holding mid-call.
	ctx, cancel, done := m.inFlight.Begin(context.Background())
	m.cancelGen = cancel
	roster := m.roster
	return m, func() tea.Msg {
		defer done()
		reply, err := roster.Send(ctx, name, query)
		return AgentChatDoneMsg{Agent: name, Token: token, Reply: reply, Err: err}
	}
}

// threadOrder is the cycle order for Tab: the main conversation first, then
// every non-host agent in roster order.
func (m Model) threadOrder() []string {
	order := []string{""}
	if m.roster == nil {
		return order
	}
	for _, e := range m.roster.List() {
		if e.Kind == agentchat.KindHost {
			continue
		}
		order = append(order, e.Name)
	}
	return order
}

// cycleThread moves to the next (delta=1) or previous (delta=-1) thread,
// wrapping. With no agents in the roster it is a no-op.
func (m Model) cycleThread(delta int) Model {
	order := m.threadOrder()
	if len(order) < 2 {
		return m
	}
	at := 0
	for i, name := range order {
		if name == m.thread {
			at = i
			break
		}
	}
	next := ((at+delta)%len(order) + len(order)) % len(order)
	return m.switchThread(order[next])
}

// completeAgentName expands a partially-typed agent name after "/agent ".
// Returns false when the input is not an agent-name prefix, so Tab can fall
// through to its other meanings. Completion is restricted to threadOrder's
// set (host entries excluded) so Tab never completes to a name Tab-cycling
// itself cannot reach.
func (m Model) completeAgentName() (Model, bool) {
	const prefix = "/agent "
	value := m.textarea.Value()
	if !strings.HasPrefix(value, prefix) {
		return m, false
	}
	partial := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	if partial == "" || m.roster == nil {
		return m, false
	}
	lower := strings.ToLower(partial)
	for _, name := range m.threadOrder() {
		if name == "" {
			// threadOrder's "" entry is the main conversation, not an
			// addressable agent name.
			continue
		}
		if strings.HasPrefix(strings.ToLower(name), lower) {
			m.textarea.SetValue(prefix + name)
			m.textarea.CursorEnd()
			return m, true
		}
	}
	return m, false
}

// handleAgentChatDone folds a directed chat reply into its thread — the one
// on screen or one running in the background.
func (m Model) handleAgentChatDone(msg AgentChatDoneMsg) (tea.Model, tea.Cmd) {
	if msg.Agent == m.thread {
		if msg.Token != m.genToken {
			return m, nil
		}
		m.generating = false
		m.cancelGen = nil
		t := m.threads[msg.Agent]
		if msg.Err != nil {
			m.blocks = append(m.blocks, ContentBlock{Type: BlockSystem, Text: fmt.Sprintf("Error: %v", msg.Err)})
		} else {
			m.blocks = append(m.blocks, ContentBlock{Type: BlockAssistant, Text: msg.Reply})
			t.lastReply = msg.Reply
		}
		m.threads[msg.Agent] = t
		m.updateViewport()
		return m, nil
	}

	t, ok := m.threads[msg.Agent]
	if !ok || msg.Token != t.genToken {
		return m, nil
	}
	t.generating = false
	t.cancel = nil
	if msg.Err != nil {
		t.blocks = append(t.blocks, ContentBlock{Type: BlockSystem, Text: fmt.Sprintf("Error: %v", msg.Err)})
	} else {
		t.blocks = append(t.blocks, ContentBlock{Type: BlockAssistant, Text: msg.Reply})
		t.lastReply = msg.Reply
	}
	m.threads[msg.Agent] = t
	return m, nil
}

// threadNameMaxWidth caps the agent-name portion of the input-row prompt
// prefix, so a pathologically long agent name can't by itself overflow the
// row even before View narrows the textarea to compensate.
const threadNameMaxWidth = 24

// threadLabel is the prompt prefix naming the current thread. Empty for the
// main conversation, so the default prompt is unchanged. A long agent name
// is truncated so the prefix stays a bounded, predictable width.
func (m Model) threadLabel() string {
	if m.thread == "" {
		return ""
	}
	name := m.thread
	if r := []rune(name); len(r) > threadNameMaxWidth {
		name = string(r[:threadNameMaxWidth-1]) + "…"
	}
	return "[" + name + "] "
}

// mainGenToken returns the main conversation's current generation token,
// regardless of which thread is on screen. When the main thread is visible
// (m.thread == ""), m.genToken IS the live value; otherwise the live value
// is parked in m.threads[""] (see switchThread).
func (m Model) mainGenToken() int {
	if m.thread == "" {
		return m.genToken
	}
	return m.threads[""].genToken
}

// mainGenerating reports whether the main conversation is currently
// generating, regardless of which thread is on screen. Handlers that drop
// trailing/late events must check this, not the mirrored m.generating.
//
// Every bridge-driven event whose Directed flag is false belongs to the main
// run. That flag is load-bearing: Roster.Send runs a rebuilt agent through the
// same RunWithHistory path on the same event bus, so without it a side chat's
// chunks and tool calls were written into the main transcript — and, via
// AgentDoneMsg, into the host's LLM history. AgentChatDoneMsg remains the only
// writer of a side thread's blocks.
//
// It is a SOURCE test, decided where the event is emitted, and it must never
// be replaced by a test on the agent's name. Both directions break: a child
// the main run spawned emits under its own name and must still render in
// main (live subagent visibility), and a config agent the main run is
// delegating to can be addressed in a side thread at the same time, which a
// name test would answer by dropping the main run's own rows.
func (m Model) mainGenerating() bool {
	if m.thread == "" {
		return m.generating
	}
	return m.threads[""].generating
}

// mainBlocks runs fn with m.blocks pointed at the main conversation's
// blocks, persists any change back to m.threads[""], and restores m.blocks
// to whatever was visible before — leaving a side thread's blocks untouched
// if one is on screen. Bridge-driven events that are not Directed belong to
// the main run (see mainGenerating), so this is how
// every such handler writes into the right transcript regardless of what the
// user is looking at. Returns
// whether the main conversation is the one currently visible, so callers
// can decide whether to re-render the viewport.
func (m *Model) mainBlocks(fn func()) (visible bool) {
	if m.thread == "" {
		fn()
		return true
	}
	saved := m.blocks
	main := m.threads[""]
	m.blocks = main.blocks
	fn()
	main.blocks = m.blocks
	m.threads[""] = main
	m.blocks = saved
	return false
}

// anyThreadGenerating reports whether any conversation — main or any agent
// thread — is currently generating, regardless of which one is on screen.
// The visible thread's live state lives in m.generating, not its (possibly
// stale) entry in m.threads (see switchThread); every other thread's live
// state is exactly what's stored there. Ctrl+C uses this so its "quit only
// when nothing is generating" contract holds across threads, not just the
// one currently on screen.
func (m Model) anyThreadGenerating() bool {
	if m.generating {
		return true
	}
	for name, t := range m.threads {
		if name == m.thread {
			continue
		}
		if t.generating {
			return true
		}
	}
	return false
}

// pushThreadReply copies the current thread's last reply into the main
// conversation as a labelled user-role message.
//
// User-role is the honest framing: the user is handing the host this
// information — the host did not fetch it. Nothing already in the host's
// history is rewritten.
func (m Model) pushThreadReply() (tea.Model, tea.Cmd) {
	if m.thread == "" {
		m.blocks = append(m.blocks, ContentBlock{
			Type: BlockSystem,
			Text: "Nothing to push — you are on the main conversation. Open an agent thread with Ctrl+A first.",
		})
		m.updateViewport()
		return m, nil
	}

	t := m.threads[m.thread]
	if strings.TrimSpace(t.lastReply) == "" {
		m.blocks = append(m.blocks, ContentBlock{
			Type: BlockSystem,
			Text: fmt.Sprintf("Nothing to push — %s has no reply in this thread yet.", m.thread),
		})
		m.updateViewport()
		return m, nil
	}

	labelled := fmt.Sprintf("[from %s] %s", m.thread, t.lastReply)

	main := m.threads[""]
	main.blocks = append(main.blocks, ContentBlock{Type: BlockUser, Text: labelled})
	m.threads[""] = main
	m.history = append(m.history, llm.NewTextMessage("user", labelled))

	m.blocks = append(m.blocks, ContentBlock{
		Type: BlockSystem,
		Text: "Pushed to the main conversation.",
	})
	m.updateViewport()
	return m, nil
}

// handleAgentCommand implements /agent. Bare, it opens the picker on the main
// conversation and returns to main from inside a thread. With a name, it
// enters that agent's thread.
func (m Model) handleAgentCommand(args string) (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(args)
	if name == "" {
		if m.thread != "" {
			return m.switchThread(""), nil
		}
		return m.openAgentPicker()
	}
	if m.roster == nil || !m.roster.Enabled() {
		m.blocks = append(m.blocks, ContentBlock{Type: BlockSystem, Text: agentChatDisabledNote})
		m.updateViewport()
		return m, nil
	}

	var names []string
	for _, e := range m.roster.List() {
		if strings.EqualFold(e.Name, name) {
			if e.Kind == agentchat.KindHost {
				return m.switchThread(""), nil
			}
			return m.switchThread(e.Name), nil
		}
		names = append(names, e.Name)
	}
	m.blocks = append(m.blocks, ContentBlock{
		Type: BlockSystem,
		Text: fmt.Sprintf("No agent named %q. Addressable agents: %s", name, strings.Join(names, ", ")),
	})
	m.updateViewport()
	return m, nil
}
