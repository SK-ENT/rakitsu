package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// stickyHeaderHeight is the number of viewport rows reserved above the
// scrolling content for the sticky user-prompt header. Always reserved so
// block line spans stay stable across scroll-state transitions.
const stickyHeaderHeight = 1

// Config holds the parameters needed to create a chat Model.
type Config struct {
	Runner          agent.Runner // root Runner — *Agent or *Orchestrator
	SingleAgent     *agent.Agent // optional: when set, enables RunWithHistory
	EventBus        *telemetry.EventBus
	AgentName       string
	ModelName       string
	Version         string                        // "" hides the version line in the startup banner
	InitialQuery    string                        // optional: auto-submitted as first turn
	UserInputReqCh  <-chan userinput.InputRequest // receives questions from user_input tool
	UserInputRespCh chan<- string                 // sends answers back to user_input tool

	// BuildLLM is the factory closure used by the `/model` slash command to
	// instantiate a new LLM client for a swap. Captures cfg + ctx from the
	// caller so the chat package doesn't need to re-import config-parsing
	// machinery. Nil disables `/model` (command reports "model swap not
	// supported in this build").
	BuildLLM func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)

	// KnownProviders lists the YAML provider keys (e.g. "litellm",
	// "litellm-gemini-flash") for `/model <agent> <model>@<providerKey>`
	// validation and for showing available providers in error messages.
	// Empty means provider-validation is skipped.
	KnownProviders []string

	// InnerRunner is the real root runner that lives BEHIND the ChatHost
	// overlay in interactive_overlay:true mode. Without this, /model can
	// only see ChatHost itself — the orchestrator and its workers are
	// reachable through ChatHost's invoke_config tool, not as direct
	// children of the runner. Leave nil when there's no overlay (Runner
	// already IS the inner one).
	InnerRunner agent.Runner

	// ConvMem enables conversation memory (settings.memory.conversation):
	// each turn the model sees a rolling summary + recent verbatim turns
	// instead of the full transcript. Only effective with SingleAgent set
	// (the RunWithHistory path); nil disables.
	ConvMem *memory.ConversationMemory

	// AgentChat is the directed-agent-chat roster. Nil disables the feature
	// in this session — Ctrl+A then explains rather than opening an empty
	// popup.
	AgentChat *agentchat.Roster

	// InFlight lets the caller cancel and wait out directed sends before it
	// tears down the tool registries they hold. bubbletea returns from Run
	// without waiting on Cmd goroutines, so without this a side chat can
	// still be mid-tool-call when cleanup closes its MCP subprocess. Nil is
	// safe and tracks nothing.
	InFlight *InFlight

	// InboundCh receives cross-session messages injected via the hub
	// (settings.session_msg). Nil disables the feature for this session.
	InboundCh <-chan InboundSessionMsg

	// PostMessageResult, when non-nil, is called (off the UI goroutine, via
	// a tea.Cmd) after an inbound external turn completes IF that turn
	// carried a WaitToken — reporting the outcome back to the hub so a
	// synchronous sender can be unblocked. Wired from cmd/rakitsu/interactive.go
	// using the same HubClient InboundCh came from. Nil disables reporting
	// (no hub / remote wait unsupported), matching InboundCh's own
	// nil-disables convention.
	PostMessageResult func(waitToken, final string, interrupted bool, errText string)

	// TurnTimeout bounds each chat turn (--timeout /
	// settings.execution.timeout_seconds). Zero means no deadline.
	TurnTimeout time.Duration
}

// blockSpan is the line range a block occupies in the rendered viewport
// content (0-indexed, half-open [start, start+count)). Used to translate a
// mouse click's Y coordinate back to the block under it.
type blockSpan struct {
	start int
	count int
}

// agentUsageSnapshot is the running per-agent total shown by /usage and
// /context. See Model.agentUsage for accumulation semantics.
type agentUsageSnapshot struct {
	Model           string
	Provider        string
	Tokens          int
	MaxTokens       int
	Cost            float64
	MaxCost         float64
	PricingKnown    bool
	TotalIterations int
	Turns           int
	Status          string
}

// Model is the bubbletea model for the chat TUI.
type Model struct {
	// UI components
	textarea textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	// Runner: either a single Agent (with rich history via RunWithHistory)
	// or an Orchestrator (stateless per turn, history prepended into query).
	runner      agent.Runner
	singleAgent *agent.Agent // nil iff orchestrator mode
	eventBus    *telemetry.EventBus
	agentName   string
	modelName   string
	version     string // shown in the startup banner; see renderBanner

	// Initial query — auto-submitted once when ready
	initialQuery     string
	initialSubmitted bool

	// Conversation state
	history    []llm.Message
	blocks     []ContentBlock
	generating bool
	cancelGen  context.CancelFunc
	lastQuery  string

	// Fluid chat: generation token discriminates stale AgentDoneMsg
	// from the live run after an interrupt-and-restart.
	genToken int

	// Token tracking
	totalTokens int

	// agentUsage accumulates per-agent token/cost/budget for the /usage and
	// /context popups. Keyed by agent name. Tokens/Cost/MaxTokens/MaxCost/
	// PricingKnown/Status/Model/Provider are overwritten on each AgentEndMsg
	// (TokenGuard is already cumulative for the life of the agent instance);
	// TotalIterations and Turns are summed/incremented since Iterations is
	// per-call, not cumulative.
	agentUsage map[string]*agentUsageSnapshot

	// Floating popup state (/usage, /context). See usage_popup.go.
	activePopup popupKind
	popupDetail bool

	// Cross-session messaging: inboundCh delivers messages from other live
	// sessions; pendingInbound queues them while the main conversation is
	// generating or a side thread is on screen. Drained on arrival (when
	// idle) and after each AgentDoneMsg.
	inboundCh      <-chan InboundSessionMsg
	pendingInbound []InboundSessionMsg
	// postMessageResult, pendingWaitToken/pendingWaitGen track a synchronous
	// remote wait on the CURRENTLY dispatched external turn (submitExternal
	// sets them; the AgentDoneMsg case reports and clears them). Only one
	// external turn is ever in flight at a time (dispatchNextInbound only
	// fires when idle), so a single pending slot — not a map — is enough.
	postMessageResult func(waitToken, final string, interrupted bool, errText string)
	pendingWaitToken  string
	pendingWaitGen    int

	// User input tool coordination. userInputDefault is the pending
	// request's default answer, kept even while a side thread is on screen
	// (where the shared textarea is left untouched — see the
	// UserInputRequestMsg handler) so switchThread can reapply it once the
	// user returns to main.
	userInputReqCh   <-chan userinput.InputRequest
	userInputRespCh  chan<- string
	waitingForInput  bool
	userInputDefault string

	// Event bridge. bridgeStop is closed by Close() (called by the caller
	// once tea.Program.Run() returns) to end StartBridge's goroutine — see
	// StartBridge's own doc comment for why this can't be left unclosed.
	bridgeCh   <-chan tea.Msg
	bridgeStop chan struct{}

	// Glamour renderer
	renderer *glamour.TermRenderer

	// Layout
	width  int
	height int
	ready  bool

	// Debounce rendering
	lastRender  time.Time
	renderDirty bool

	// reasoningSealed marks the current reasoning segment as closed (a tool
	// call ended it); the next reasoning chunk starts a fresh BlockReasoning.
	reasoningSealed bool

	// spans is the line range each block occupies in the rendered viewport
	// content — rebuilt every updateViewport, used for mouse click hit-testing.
	spans []blockSpan

	// Input history recall. inputHistory holds submitted queries; historyIdx
	// is the navigation cursor (-1 = not navigating); historyDraft is the
	// unsent input saved when navigation begins so Down can restore it.
	inputHistory []string
	historyIdx   int
	historyDraft string

	// notice is a transient status-bar message (e.g. "copied reply to
	// clipboard"); cleared by clearNoticeMsg after a short delay.
	notice string

	// copySel is the copy cursor: a 0-based position among assistant
	// replies that Ctrl+P steps through, or -1 when no reply is explicitly
	// selected (Ctrl+Y then copies the latest). Reset on each new turn.
	copySel int

	// buildLLM and knownProviders back the /model slash command — see
	// Config.BuildLLM / Config.KnownProviders. buildLLM is nil when the
	// caller didn't wire it (e.g. in unit tests); the command then reports
	// "model swap not supported in this build". innerRunner is the
	// real root behind the ChatHost overlay — see Config.InnerRunner.
	buildLLM       func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)
	knownProviders []string
	innerRunner    agent.Runner

	// convMem backs conversation memory — see Config.ConvMem.
	convMem *memory.ConversationMemory

	// spawnParents maps a runtime-spawned child's instance name to its
	// spawning agent's name, recorded from AGENT_START's parent link
	// (telemetry.AgentStartPayload.ParentAgent). Drives real nesting depth
	// for spawned agents' tool/reasoning blocks and their own status rows;
	// empty/nil for configs that never use spawn_agent.
	spawnParents map[string]string

	// Directed agent chat. roster is nil when settings.agent_chat is off or
	// the caller didn't wire it — every entry point checks for that and
	// says so rather than showing an empty popup.
	roster *agentchat.Roster
	// pickerRows is the roster snapshot the picker is currently showing,
	// captured on open so arrow keys move over a stable list even while
	// subagents finish in the background.
	pickerRows []agentchat.Entry
	pickerSel  int
	// thread is the agent whose conversation is on screen; "" is the main
	// conversation. threads holds every thread's view state, including the
	// main one under the "" key, so switching is one symmetric swap.
	thread  string
	threads map[string]agentThread
	// inFlight tracks running directed sends so shutdown can cancel and wait
	// them out before tool registries are closed — see InFlight.
	inFlight *InFlight
	// turnTimeout bounds each turn; zero = none. See turnContext.
	turnTimeout time.Duration
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("6")) // cyan

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")) // gray

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8"))

	stickyHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Underline(true).
				Foreground(lipgloss.Color("6")) // cyan, matches title
)

// NewModel creates a new chat model from the given config.
func NewModel(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Enter to send, Ctrl+J or Alt+Enter for newline, /help for commands)"
	ta.Focus()
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // unlimited
	// Rebind InsertNewline so plain Enter is free for "submit".
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "alt+enter"),
		key.WithHelp("ctrl+j", "insert newline"),
	)

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	r, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(0), // we handle wrapping via viewport width
	)

	bridgeStop := make(chan struct{})

	return Model{
		textarea:          ta,
		spinner:           sp,
		runner:            cfg.Runner,
		singleAgent:       cfg.SingleAgent,
		eventBus:          cfg.EventBus,
		agentName:         cfg.AgentName,
		modelName:         cfg.ModelName,
		version:           cfg.Version,
		initialQuery:      cfg.InitialQuery,
		renderer:          r,
		userInputReqCh:    cfg.UserInputReqCh,
		userInputRespCh:   cfg.UserInputRespCh,
		bridgeStop:        bridgeStop,
		bridgeCh:          StartBridge(cfg.EventBus, bridgeStop),
		historyIdx:        -1, // not navigating input history
		copySel:           -1, // no reply selected for copy
		buildLLM:          cfg.BuildLLM,
		knownProviders:    cfg.KnownProviders,
		innerRunner:       cfg.InnerRunner,
		convMem:           cfg.ConvMem,
		spawnParents:      make(map[string]string),
		agentUsage:        make(map[string]*agentUsageSnapshot),
		roster:            cfg.AgentChat,
		threads:           map[string]agentThread{"": {}},
		inFlight:          cfg.InFlight,
		inboundCh:         cfg.InboundCh,
		postMessageResult: cfg.PostMessageResult,
		turnTimeout:       cfg.TurnTimeout,
	}
}

// turnContext returns the context for one chat turn: cancellable (Ctrl+C),
// with a deadline only when a turn timeout is configured.
func (m Model) turnContext() (context.Context, context.CancelFunc) {
	if m.turnTimeout > 0 {
		return context.WithTimeout(context.Background(), m.turnTimeout)
	}
	return context.WithCancel(context.Background())
}

// Close ends the model's background event bridge. The caller (the process
// running tea.Program) must call this exactly once, after Run() returns —
// bridgeStop is a channel reference shared with the StartBridge goroutine
// created in NewModel, so closing it here reaches that same goroutine
// regardless of how many Model values were copied through Update() calls in
// between.
func (m Model) Close() {
	close(m.bridgeStop)
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		m.spinner.Tick,
		waitForEvent(m.bridgeCh),
	}
	if m.userInputReqCh != nil {
		cmds = append(cmds, m.waitForUserInputRequest())
	}
	if m.inboundCh != nil {
		cmds = append(cmds, m.waitForInboundMessage())
	}
	return tea.Batch(cmds...)
}

// waitForInboundMessage returns a Cmd that blocks until another session
// injects a message via the hub. One-shot, like waitForUserInputRequest —
// re-armed after each receive.
func (m Model) waitForInboundMessage() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.inboundCh
		if !ok {
			return nil
		}
		return msg
	}
}

// waitForUserInputRequest returns a Cmd that blocks until the user_input tool sends a request.
func (m Model) waitForUserInputRequest() tea.Cmd {
	return func() tea.Msg {
		req, ok := <-m.userInputReqCh
		if !ok {
			return nil
		}
		return UserInputRequestMsg{Question: req.Question, Default: req.Default}
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		resized := m.handleResize()
		// Auto-submit initial query once, after the viewport is ready.
		if !resized.initialSubmitted && resized.initialQuery != "" {
			resized.initialSubmitted = true
			q := resized.initialQuery
			resized.initialQuery = ""
			next, cmd := resized.submitQuery(q)
			return next, cmd
		}
		return resized, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case clearNoticeMsg:
		m.notice = ""
		return m, nil

	case TokenChunkMsg:
		cmds = append(cmds, waitForEvent(m.bridgeCh))
		// A rebuilt side-thread agent streams on the same bus as the main
		// run; those chunks belong to neither transcript on screen, because
		// Send settles rather than streams. Suppress them by origin.
		if msg.Directed {
			return m, tea.Batch(cmds...)
		}
		// Drop trailing chunks from a finished run (race: the agent
		// returned AgentDoneMsg before the bridge channel drained). Chunks
		// always belong to the main conversation, so this must check its
		// generating state, not the mirrored one for whatever thread is on
		// screen.
		if !m.mainGenerating() {
			return m, tea.Batch(cmds...)
		}
		visible := m.mainBlocks(func() { m.appendToCurrentAssistant(msg.Text) })
		if visible {
			m.renderDirty = true
			// Debounce viewport updates to avoid glamour re-render on every token
			if time.Since(m.lastRender) > 50*time.Millisecond {
				m.updateViewport()
				m.lastRender = time.Now()
				m.renderDirty = false
			}
		}
		return m, tea.Batch(cmds...)

	case ReasoningChunkMsg:
		cmds = append(cmds, waitForEvent(m.bridgeCh))
		if msg.Directed {
			return m, tea.Batch(cmds...)
		}
		// Drop trailing reasoning chunks from a finished run.
		if !m.mainGenerating() {
			return m, tea.Batch(cmds...)
		}
		visible := m.mainBlocks(func() { m.appendReasoning(msg.Text, msg.AgentName) })
		if visible {
			m.renderDirty = true
			// Debounce viewport updates — reasoning streams as fast as tokens.
			if time.Since(m.lastRender) > 50*time.Millisecond {
				m.updateViewport()
				m.lastRender = time.Now()
				m.renderDirty = false
			}
		}
		return m, tea.Batch(cmds...)

	case ToolCallStartMsg:
		// A side chat calling a tool is the headline use case, and its
		// TOOL_CALL_START rides the same bus as the main run's. Without this
		// a tool block for a call main never made was inserted into the main
		// transcript while main sat idle.
		if msg.Directed {
			return m, waitForEvent(m.bridgeCh)
		}
		// Insert before the trailing assistant block so the streamed answer
		// always renders below the tools it depended on. Always the main
		// conversation's — see mainBlocks.
		visible := m.mainBlocks(func() {
			m.insertBeforeAssistant(ContentBlock{
				Type:      BlockTool,
				ToolID:    msg.ID,
				ToolName:  msg.Name,
				ToolArgs:  msg.Args,
				AgentName: msg.AgentName,
				Depth:     m.agentDepth(msg.AgentName),
			})
			// A tool call closes the current reasoning segment — seal it and
			// collapse it so only the live segment stays expanded.
			m.reasoningSealed = true
			m.collapseOpenReasoning()
		})
		if visible {
			m.updateViewport()
		}
		return m, waitForEvent(m.bridgeCh)

	case ToolCallEndMsg:
		// Tool-call IDs are not globally unique — gemini synthesizes
		// call_<name>_<index> and the inline tool-call format synthesizes
		// content_tc_<i>, so two agents' first fs_read share an id. Without
		// this guard a side chat's TOOL_CALL_END completes the main run's
		// open block of the same id, with the side chat's output.
		if msg.Directed {
			return m, waitForEvent(m.bridgeCh)
		}
		visible := m.mainBlocks(func() { m.completeToolBlock(msg) })
		if visible {
			m.updateViewport()
		}
		return m, waitForEvent(m.bridgeCh)

	case AgentStartMsg:
		m.recordAgentUsageStart(msg)
		// Same split as AgentEndMsg: usage is accounted either way, only the
		// block write is origin-filtered. A side chat's agent has spawn_agent
		// removed (see agentChatBuilder) so today it emits no parented
		// AGENT_START, but the transcript must not depend on that.
		if msg.Directed {
			return m, waitForEvent(m.bridgeCh)
		}
		// The root agent's own AGENT_START fires every turn — pure noise in
		// chat (the debugger already shows it). Only spawned children render.
		if msg.Parent == "" {
			return m, waitForEvent(m.bridgeCh)
		}
		m.spawnParents[msg.Name] = msg.Parent
		visible := m.mainBlocks(func() {
			m.insertBeforeAssistant(ContentBlock{
				Type:      BlockSubagent,
				AgentName: msg.Name,
				SubModel:  msg.Model,
				Depth:     m.agentDepth(msg.Name),
			})
		})
		if visible {
			m.updateViewport()
		}
		return m, waitForEvent(m.bridgeCh)

	case AgentEndMsg:
		// Usage is accumulated either way — a side chat spends real tokens
		// and /usage must say so. Only the block write is source-filtered,
		// so a directed run can never close a main-run subagent row that
		// happens to share its name.
		m, _ = m.updateAgentUsageEnd(msg)
		if msg.Directed {
			return m, waitForEvent(m.bridgeCh)
		}
		visible := m.mainBlocks(func() { m.completeSubagentBlock(msg) })
		if visible {
			m.updateViewport()
		}
		return m, waitForEvent(m.bridgeCh)

	case AgentDoneMsg:
		// Cross-session wait-token reporting MUST be checked before the
		// stale-token early return below: a user-typed message interrupting
		// this in-flight external turn (Submit calls Interrupt() first)
		// bumps the gen token, so THIS turn's own AgentDoneMsg arrives
		// "stale" from the main-conversation's point of view and would
		// otherwise be skipped entirely — silently orphaning a synchronous
		// remote waiter until it times out instead of reporting
		// interrupted:true right away.
		var postCmd tea.Cmd
		if m.pendingWaitToken != "" && msg.Token == m.pendingWaitGen {
			waitToken := m.pendingWaitToken
			m.pendingWaitToken = ""
			errText := ""
			if msg.Err != nil {
				errText = msg.Err.Error()
			}
			if fn := m.postMessageResult; fn != nil {
				postCmd = func() tea.Msg {
					fn(waitToken, msg.Response, msg.Err != nil, errText)
					return nil
				}
			}
		}
		// AgentDoneMsg is always the main conversation's completion — its
		// token, blocks, and generating/cancel state live in m.threads[""]
		// whenever a side thread is on screen (see mainGenToken/mainBlocks),
		// not in the mirrored m.genToken/m.blocks/m.generating. A stale
		// (interrupted-run) message still re-arms the bridge pump below;
		// only the mutation is skipped.
		if msg.Token != m.mainGenToken() {
			if postCmd != nil {
				return m, tea.Batch(waitForEvent(m.bridgeCh), postCmd)
			}
			return m, waitForEvent(m.bridgeCh)
		}
		if m.thread == "" {
			m.generating = false
			m.cancelGen = nil
		} else {
			t := m.threads[""]
			t.generating = false
			t.cancel = nil
			m.threads[""] = t
		}
		var lastText string
		visible := m.mainBlocks(func() {
			// A turn ending in error or interrupt can leave a spawned child's
			// AGENT_END unfired — close any still-running subagent row so it
			// doesn't show "working…" forever.
			m.closeRunningSubagents("interrupted")
			if msg.Err != nil {
				m.blocks = append(m.blocks, ContentBlock{
					Type: BlockSystem,
					Text: fmt.Sprintf("Error: %v", msg.Err),
				})
			} else if msg.Response != "" {
				// The agent's final Response is authoritative — overwrite the
				// streamed assistant block. This handles providers that emit
				// token chunks AND a final surface chunk (duplicate text) and
				// providers that don't stream at all (empty block).
				m.setLastAssistantText(msg.Response)
			}
			// The turn is done — collapse every reasoning block so the finished
			// conversation is tidy (one-line thought summaries, answer visible).
			m.collapseAllReasoning()
			lastText = m.lastAssistantText()
		})
		if visible {
			// Flush viewport (always — streaming debounce may have skipped last chunk)
			m.updateViewport()
			m.renderDirty = false
		}
		// A turn that errored before producing any response leaves lastText
		// empty — but submitQuery already unconditionally appended this
		// turn's "user" entry to m.history. Without a matching reply here,
		// the next submitQuery's "user" entry lands right after it with no
		// "assistant" in between, and providers that enforce strict
		// alternation (Anthropic) reject the next turn outright. The
		// placeholder is history-only — the UI's own error display is the
		// "Error: ..." BlockSystem entry appended above.
		if msg.Err != nil && lastText == "" {
			lastText = fmt.Sprintf("[error: %v]", msg.Err)
		}
		// Add assistant message to history — the main conversation's,
		// tracked even while a side thread is on screen.
		if lastText != "" {
			m.history = append(m.history, llm.NewTextMessage("assistant", lastText))
		}
		// Conversation memory: fold turns that just left the verbatim
		// window into the rolling summary. Async — summarizer latency must
		// not block the UI; on failure nothing is folded and the next turn
		// degrades to a fuller history (never data loss).
		if m.convMem != nil && m.singleAgent != nil {
			convMem, ag := m.convMem, m.singleAgent
			full := make([]llm.Message, len(m.history))
			copy(full, m.history)
			go func() {
				sumCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				// Re-read the provider per call so /model swaps are honored.
				_ = convMem.Update(sumCtx, full, func(c context.Context, prompt string) (string, error) {
					return memory.ProviderSummarize(c, ag.LLMProvider(), prompt)
				})
			}()
		}
		// Re-arm waitForEvent so any trailing chunks that sat in the
		// bridge channel after the agent returned get drained (and
		// discarded — generating is now false). If cross-session messages
		// queued up while this turn ran, dispatch the next one now.
		if m.thread == "" && len(m.pendingInbound) > 0 {
			next, cmd := m.dispatchNextInbound()
			cmds := []tea.Cmd{waitForEvent(m.bridgeCh), cmd}
			if postCmd != nil {
				cmds = append(cmds, postCmd)
			}
			return next, tea.Batch(cmds...)
		}
		if postCmd != nil {
			return m, tea.Batch(waitForEvent(m.bridgeCh), postCmd)
		}
		return m, waitForEvent(m.bridgeCh)

	case AgentChatDoneMsg:
		return m.handleAgentChatDone(msg)

	case InboundSessionMsg:
		m.pendingInbound = append(append([]InboundSessionMsg(nil), m.pendingInbound...), msg)
		rearm := m.waitForInboundMessage()
		if m.thread == "" && !m.mainGenerating() {
			next, cmd := m.dispatchNextInbound()
			return next, tea.Batch(rearm, cmd)
		}
		return m, rearm

	case UserInputRequestMsg:
		m.waitingForInput = true
		m.userInputDefault = msg.Default
		// user_input is only ever wired to the main run (a side thread's
		// agent never gets the tool — see agentChatBuilder), so the
		// question always belongs on the main conversation's transcript.
		visible := m.mainBlocks(func() {
			m.blocks = append(m.blocks, ContentBlock{
				Type: BlockSystem,
				Text: fmt.Sprintf("Agent asks: %s", msg.Question),
			})
		})
		// Only touch the shared textarea while main is the thread on
		// screen — otherwise this silently overwrites whatever the user is
		// mid-typing in a side thread with an answer to a question that
		// thread cannot answer. switchThread reapplies this once the user
		// returns to main, so the request is never lost, just deferred.
		if visible {
			if msg.Default != "" {
				m.textarea.SetValue(msg.Default)
			}
			m.textarea.Placeholder = "Type your answer... (Enter to send)"
			m.updateViewport()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case TokenUsageMsg:
		// Accumulate per-call token usage into the visible counter. Re-arm
		// waitForEvent so the next bridge message (chunk, tool, agent_done)
		// still drains.
		m, _ = m.applyTokenUsage(msg)
		return m, waitForEvent(m.bridgeCh)
	}

	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	if m.activePopup == popupUsage {
		return clampToHeight(m.renderPopupBox("Usage", m.usagePopup()), m.height)
	}
	if m.activePopup == popupContext {
		return clampToHeight(m.renderPopupBox("Context", m.contextPopup()), m.height)
	}
	if m.activePopup == popupAgents {
		return clampToHeight(m.renderPopupBox("Agents", m.agentPicker()), m.height)
	}

	// Title bar. Truncated to m.width before styling — same technique as
	// renderStickyHeader below — because clampToHeight only counts logical
	// lines: a line the terminal has to physically wrap counts as 1 line to
	// clampToHeight but 2 rows on screen, which desyncs bubbletea's
	// alt-screen redraw and leaves stale frames behind. A long LiteLLM
	// model alias in m.modelName is exactly what triggers it.
	title := titleStyle.Render(clampVisibleWidth(fmt.Sprintf(" rakitsu chat · %s · %s ", m.agentName, m.modelName), m.width))

	// Sticky user-prompt header — kept in view while scrolling through the
	// current turn's response, so the question being answered is always
	// visible. Blank-padded when nothing should pin so layout stays stable.
	sticky := m.renderStickyHeader()

	// Status bar
	status := m.statusBar()

	// Directed agent chat: prefix the input row with the current thread's
	// name so it is always visible which conversation Enter will submit to.
	promptPrefix := m.threadLabel()
	// The prefix sits outside ta.SetWidth(m.width) (set on resize), so
	// without narrowing here the input row overruns m.width and the
	// terminal wraps its first line into an extra row — which
	// clampToHeight, counting logical lines only, then eats out of the
	// status bar below. threadLabel already bounds the agent-name portion;
	// this bounds the total row width. m has a value receiver here, so this
	// mutation is local to this render and never persists.
	if pw := lipgloss.Width(promptPrefix); pw > 0 {
		if w := m.width - pw; w > 0 {
			m.textarea.SetWidth(w)
		}
	}
	inputRow := promptPrefix + m.textarea.View()

	// Main layout
	out := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		title,
		sticky,
		m.viewport.View(),
		inputRow,
		status,
	)

	// Clamp to exact terminal height. Without this, any line-count drift
	// in the subcomponents (viewport after glamour re-render, textarea focus
	// state, multi-cell spinner glyphs on width-miscounting terminals) can
	// leak stale frames into alt-screen scrollback — symptom: multiple
	// "tokens: N / generating..." status lines stacked at the bottom and
	// textarea input appearing unresponsive because the live prompt sits
	// under stale frames.
	return clampToHeight(out, m.height)
}

// clampToHeight pads or truncates s so it has exactly height lines. Returns
// s unchanged if height is non-positive (e.g. before the first
// tea.WindowSizeMsg arrives).
func clampToHeight(s string, height int) string {
	if height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) == height {
		return s
	}
	if len(lines) > height {
		return strings.Join(lines[:height], "\n")
	}
	return s + strings.Repeat("\n", height-len(lines))
}

// clampVisibleWidth truncates raw (unstyled) text to at most width visible
// columns, appending an ellipsis when it had to cut. Must be called on raw
// text BEFORE a lipgloss style is applied — truncating an already-styled
// (ANSI-escaped) string with runewidth would risk cutting mid-escape-sequence
// or miscounting width from the invisible escape bytes. Same technique
// renderStickyHeader already uses below.
//
// The result is NEVER wider than width, including at width 1-3: an earlier
// version floored any width < 4 up to 4 "so the ellipsis always has room",
// which meant a caller trying to fit a genuinely narrow budget (e.g.
// statusBar() splitting m.width between two fields) got back something
// WIDER than it asked for — reintroducing the exact overflow this helper
// exists to prevent whenever two independently-floored calls could combine
// past m.width (see statusBar()). runewidth.Truncate itself already
// degrades correctly at width >= 1: with width=1 and a 1-column ellipsis,
// it returns just "…" (still exactly 1 column) rather than overflowing —
// it only misbehaves at width <= 0, where callers must not ask for
// anything shown at all, which the check below handles by returning "".
//
// width <= 0 returns "" rather than s unchanged (a prior version's
// behavior) — a genuinely zero/negative budget (e.g. before the first
// tea.WindowSizeMsg, or a caller with nothing left to give this field
// after clamping another) can only be rendered safely as nothing.
func clampVisibleWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.Truncate(s, width, "…")
}

// --- Key handling ---

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.activePopup == popupAgents {
		return m.handleAgentPickerKey(msg)
	}
	if m.activePopup != popupNone {
		return m.handlePopupKey(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.generating {
			// Cancel current generation
			if m.cancelGen != nil {
				m.cancelGen()
			}
			m.generating = false
			m.blocks = append(m.blocks, ContentBlock{
				Type: BlockSystem,
				Text: "(cancelled)",
			})
			m.updateViewport()
			return m, nil
		}
		return m.requestQuit()

	case tea.KeyCtrlD:
		return m.requestQuit()

	case tea.KeyCtrlY:
		// Copy the selected assistant reply to the clipboard. Consumed
		// here so the textarea never sees Ctrl+Y as input.
		return m.copySelectedReply()

	case tea.KeyCtrlP:
		// Step the copy cursor to an older reply; Ctrl+Y then copies it.
		return m.stepCopySelection()

	case tea.KeyCtrlU:
		m.activePopup = popupUsage
		m.popupDetail = false
		return m, nil

	case tea.KeyCtrlX:
		m.activePopup = popupContext
		m.popupDetail = false
		return m, nil

	case tea.KeyCtrlA:
		// Consumed here so the textarea never sees Ctrl+A. That costs the
		// terminal's default "jump to line start"; the picker is worth it,
		// and Home still works.
		return m.openAgentPicker()

	case tea.KeyCtrlG:
		// Push the current thread's last reply into the main conversation.
		return m.pushThreadReply()

	case tea.KeyCtrlO:
		// Scroll the transcript a page up — the chat TUI runs without a
		// mouse ProgramOption (see cmd/rakitsu/interactive.go — removed so
		// native click-drag-select works), so the mouse wheel no longer
		// scrolls the viewport. Two prior choices for the keyboard
		// replacement didn't survive contact with a real Mac: PgUp isn't a
		// real key without Fn (some terminals don't even forward Fn+Up as
		// PgUp), and Alt+Up (Option+Up) turned out not to reach the app as
		// an Alt-modified key on this terminal either — confirmed with
		// `cat -v` capturing nothing usable. A plain Ctrl+<letter> is a
		// raw control byte (1-26), not an escape sequence a terminal has
		// to choose to send, so it can't have the same failure mode —
		// Ctrl+Y/Ctrl+P/Ctrl+U etc. already prove that class works here.
		m.viewport.PageUp()
		return m, nil

	case tea.KeyCtrlL:
		m.viewport.PageDown()
		return m, nil

	case tea.KeyTab:
		// Context-sensitive: complete a half-typed agent name, else cycle
		// threads. Never blocks — a Tab that means nothing here falls
		// through to the textarea.
		if updated, done := m.completeAgentName(); done {
			return updated, nil
		}
		if strings.TrimSpace(m.textarea.Value()) == "" {
			return m.cycleThread(1), nil
		}

	case tea.KeyShiftTab:
		if strings.TrimSpace(m.textarea.Value()) == "" {
			return m.cycleThread(-1), nil
		}

	case tea.KeyEsc:
		// In a thread, Esc returns to the main conversation. On the main
		// conversation it falls through to the textarea unchanged.
		if m.thread != "" {
			return m.switchThread(""), nil
		}

	case tea.KeyUp:
		// At the top line of the input → recall the previous message.
		// Otherwise fall through to normal cursor movement in the textarea.
		if m.textarea.Line() == 0 && len(m.inputHistory) > 0 {
			m.recallPrev()
			return m, nil
		}

	case tea.KeyDown:
		// At the bottom line of the input → recall the next message, but
		// only while navigating history. Otherwise normal cursor movement.
		if m.textarea.Line() == m.textarea.LineCount()-1 && m.historyIdx != -1 {
			m.recallNext()
			return m, nil
		}

	case tea.KeyEnter:
		// Alt+Enter → insert newline in textarea
		if msg.Alt {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}

		// A pending user_input request always belongs to the main
		// conversation (a side thread's agent never has the user_input
		// tool — see agentChatBuilder), so it can only be answered from
		// there. In a side thread, Enter always falls through to submit to
		// that thread's agent instead, below, even while the main run is
		// blocked waiting on this answer — waitingForInput is Model-level,
		// not per-thread, so the request just stays queued until the user
		// returns to main (Esc) and answers it there.
		if m.waitingForInput && m.thread == "" {
			// Send response to user_input tool
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}
			m.textarea.Reset()
			m.textarea.Placeholder = "Type a message... (Enter to send, Alt+Enter for newline, /help for commands)"
			m.waitingForInput = false
			m.blocks = append(m.blocks, ContentBlock{Type: BlockUser, Text: input})
			m.updateViewport()
			m.userInputRespCh <- input
			return m, m.waitForUserInputRequest()
		}
		input := strings.TrimSpace(m.textarea.Value())
		if input == "" {
			return m, nil
		}
		m.textarea.Reset()

		// Fluid chat: if generating, interrupt the current run, freeze the
		// partial assistant block, then submit the new query immediately.
		if m.generating {
			if m.cancelGen != nil {
				m.cancelGen()
			}
			m.markLastAssistantInterrupted()
			// The stale AgentDoneMsg from the interrupted run will be
			// ignored via the genToken check.
		}

		// Check for slash command
		if cmd := ParseSlashCommand(input); cmd != nil {
			return m.handleSlashCommand(cmd)
		}

		// In an agent thread the message goes to that agent, not the host.
		if m.thread != "" {
			return m.submitToAgent(m.thread, input)
		}
		return m.submitQuery(input)
	}

	// Pass typing to textarea at any time (fluid chat — user can type
	// while agent is generating).
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// assistantReplyIndices returns the block indices of every assistant reply
// that has text, oldest-first. Empty assistant blocks (a turn still
// streaming, or one that produced no answer) are skipped so the copy cursor
// only ever lands on a real reply.
func (m Model) assistantReplyIndices() []int {
	var out []int
	for i := range m.blocks {
		if m.blocks[i].Type == BlockAssistant && strings.TrimSpace(m.blocks[i].Text) != "" {
			out = append(out, i)
		}
	}
	return out
}

// stepCopySelection moves the copy cursor one reply older (Ctrl+P), wrapping
// from the oldest reply back to the latest. The first step selects the
// latest reply. The viewport scrolls so the selection is visible.
func (m Model) stepCopySelection() (tea.Model, tea.Cmd) {
	idxs := m.assistantReplyIndices()
	if len(idxs) == 0 {
		m.notice = "no replies to copy yet"
		return m, clearNoticeAfter(2 * time.Second)
	}
	switch {
	case m.copySel < 0:
		m.copySel = len(idxs) - 1 // first step selects the latest reply
	case m.copySel == 0:
		m.copySel = len(idxs) - 1 // wrap past the oldest back to the latest
	default:
		m.copySel--
	}
	if m.copySel >= len(idxs) {
		m.copySel = len(idxs) - 1 // clamp if blocks changed under us
	}
	if block := idxs[m.copySel]; block < len(m.spans) {
		m.viewport.SetYOffset(m.spans[block].start)
	}
	m.notice = fmt.Sprintf("reply %d/%d selected · Ctrl+Y to copy", m.copySel+1, len(idxs))
	return m, clearNoticeAfter(4 * time.Second)
}

// copySelectedReply copies the agent reply under the copy cursor to the
// clipboard via OSC 52. With no explicit selection (copySel < 0) it copies
// the most recent reply. Shared by the Ctrl+Y key and the /copy command.
func (m Model) copySelectedReply() (tea.Model, tea.Cmd) {
	idxs := m.assistantReplyIndices()
	if len(idxs) == 0 {
		m.notice = "nothing to copy yet"
		return m, clearNoticeAfter(2 * time.Second)
	}
	pos := m.copySel
	if pos < 0 || pos >= len(idxs) {
		pos = len(idxs) - 1 // default: the latest reply
	}
	text := m.blocks[idxs[pos]].Text
	m.notice = fmt.Sprintf("copied reply %d/%d to clipboard", pos+1, len(idxs))
	return m, tea.Batch(copyToClipboard(text), clearNoticeAfter(2*time.Second))
}

// handlePopupKey handles input while a popup (/usage, /context) is active.
// Only Esc, d, e, c are recognized; everything else is swallowed so it never
// reaches the textarea underneath.
func (m Model) handlePopupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.activePopup = popupNone
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "d":
			m.popupDetail = !m.popupDetail
			return m, nil
		case "e":
			return m.exportActivePopup("md")
		case "c":
			return m.exportActivePopup("csv")
		}
		return m, nil
	}
	return m, nil
}

// exportActivePopup writes the currently-open popup's data to
// ~/.rakitsu/reports and sets a transient notice with the resulting path.
// Handles both popupUsage and popupContext.
func (m Model) exportActivePopup(format string) (tea.Model, tea.Cmd) {
	var kind, body string
	switch m.activePopup {
	case popupUsage:
		kind = "usage"
		if format == "csv" {
			body = m.usageReportCSV()
		} else {
			body = m.usageReportMarkdown()
		}
	case popupContext:
		kind = "context"
		if format == "csv" {
			body = m.contextReportCSV()
		} else {
			body = m.contextReportMarkdown()
		}
	default:
		return m, nil
	}
	path, err := writeReport(kind, format, body)
	if err != nil {
		m.notice = fmt.Sprintf("export failed: %v", err)
	} else {
		m.notice = fmt.Sprintf("exported to %s", path)
	}
	return m, clearNoticeAfter(3 * time.Second)
}

// exportActivePopupOrKind exports the given kind's report without requiring
// the popup to already be open — used by "/usage export <fmt>" typed
// directly, as opposed to pressing e/c inside an already-open popup.
func (m Model) exportActivePopupOrKind(kind popupKind, format string) (tea.Model, tea.Cmd) {
	saved := m.activePopup
	m.activePopup = kind
	updated, tcmd := m.exportActivePopup(format)
	m2 := updated.(Model)
	m2.activePopup = saved
	return m2, tcmd
}

// requestQuit is the single gate every quit path goes through: Ctrl+C (after
// it has dealt with the visible thread), Ctrl+D, /exit, /quit and /q.
//
// Quitting while a thread is generating tears down the run out from under the
// user, and for a directed send it is worse than rude: bubbletea returns from
// Run without waiting on Cmd goroutines, so the caller's cleanup then closes
// the tool registry — MCP subprocesses included — that the live side-thread
// agent is still holding. Ctrl+C already refused; Ctrl+D and the slash
// commands walked straight past. Switching threads never cancels a background
// thread, so neither does this: it explains and waits.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	if m.anyThreadGenerating() {
		m.notice = "a thread is still generating — switch to it and press Ctrl+C to cancel, or wait for it to finish"
		return m, clearNoticeAfter(4 * time.Second)
	}
	return m, tea.Quit
}

// --- Slash commands ---

func (m Model) handleSlashCommand(cmd *SlashCommand) (tea.Model, tea.Cmd) {
	switch cmd.Name {
	case "exit", "quit", "q":
		return m.requestQuit()

	case "copy":
		return m.copySelectedReply()

	case "clear":
		return m.handleClearCommand()

	case "help":
		m.blocks = append(m.blocks, ContentBlock{
			Type: BlockSystem,
			Text: HelpText(),
		})
		m.updateViewport()
		return m, nil

	case "usage":
		parts := strings.Fields(cmd.Args)
		if len(parts) == 2 && parts[0] == "export" && (parts[1] == "md" || parts[1] == "csv") {
			updated, tcmd := m.exportActivePopupOrKind(popupUsage, parts[1])
			return updated, tcmd
		}
		m.activePopup = popupUsage
		m.popupDetail = false
		return m, nil

	case "context":
		parts := strings.Fields(cmd.Args)
		if len(parts) == 2 && parts[0] == "export" && (parts[1] == "md" || parts[1] == "csv") {
			updated, tcmd := m.exportActivePopupOrKind(popupContext, parts[1])
			return updated, tcmd
		}
		m.activePopup = popupContext
		m.popupDetail = false
		return m, nil

	case "retry":
		return m.handleRetryCommand()

	case "model":
		return m.handleModelCommand(cmd.Args)

	case "agent":
		return m.handleAgentCommand(cmd.Args)

	case "push":
		return m.pushThreadReply()

	default:
		m.blocks = append(m.blocks, ContentBlock{
			Type: BlockSystem,
			Text: fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", cmd.Name),
		})
		m.updateViewport()
		return m, nil
	}
}

// handleClearCommand implements /clear.
//
// On the main conversation it clears the transcript, the LLM history and the
// token counter, unchanged. Inside a side thread it clears THAT thread: its
// blocks and the roster's retained transcript for that agent.
//
// m.history is the main conversation's and is Model-level, not per-thread, so
// clearing it from a side thread wiped the host's entire memory while main's
// transcript stayed fully on screen — an invisible, unrecoverable loss.
// Clearing the roster's copy matters just as much in the other direction: the
// blocks are only the rendering, and the conversation the agent actually
// replays lives in the roster, so without it the next Send replayed
// everything the user just asked to clear.
func (m Model) handleClearCommand() (tea.Model, tea.Cmd) {
	if m.thread != "" {
		m.blocks = nil
		t := m.threads[m.thread]
		t.lastReply = ""
		t.lastQuery = ""
		m.threads[m.thread] = t
		if m.roster != nil {
			m.roster.ClearTranscript(m.thread)
		}
		m.updateViewport()
		return m, nil
	}
	m.blocks = nil
	m.history = nil
	m.totalTokens = 0
	m.updateViewport()
	return m, nil
}

// handleRetryCommand implements /retry against whichever conversation is on
// screen.
//
// Inside a side thread it re-runs that thread's own last message through the
// roster. Dispatching to the main runner instead set generating and bumped
// the genToken on the visible side thread while the run went to main, so the
// resulting AgentDoneMsg carried a token that could never match main's: the
// answer was discarded, the side thread stayed generating forever,
// anyThreadGenerating was then permanently true so Ctrl+C could never quit,
// and main's history was left with a user turn that has no reply.
func (m Model) handleRetryCommand() (tea.Model, tea.Cmd) {
	if m.thread != "" {
		last := m.threads[m.thread].lastQuery
		if last == "" {
			m.blocks = append(m.blocks, ContentBlock{
				Type: BlockSystem,
				Text: "Nothing to retry in this thread yet",
			})
			m.updateViewport()
			return m, nil
		}
		return m.submitToAgent(m.thread, last)
	}
	if m.lastQuery == "" {
		m.blocks = append(m.blocks, ContentBlock{
			Type: BlockSystem,
			Text: "Nothing to retry",
		})
		m.updateViewport()
		return m, nil
	}
	return m.submitQuery(m.lastQuery)
}

// --- Agent interaction ---

func (m Model) submitQuery(query string) (tea.Model, tea.Cmd) {
	m.lastQuery = query
	m.generating = true
	m.genToken++
	m.reasoningSealed = false
	token := m.genToken

	// Record the query for Up/Down history recall (skip consecutive dupes,
	// e.g. /retry) and exit any in-progress history navigation.
	if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != query {
		m.inputHistory = append(m.inputHistory, query)
	}
	m.historyIdx = -1
	m.historyDraft = ""
	m.copySel = -1 // a new turn resets the copy cursor to "latest"

	// Add user block
	m.blocks = append(m.blocks, ContentBlock{Type: BlockUser, Text: query})
	// Add empty assistant block for streaming. Tool and reasoning blocks
	// insert *before* this block so the answer renders last.
	m.blocks = append(m.blocks, ContentBlock{Type: BlockAssistant})
	m.updateViewport()
	// A new turn always snaps to the bottom, even if the user had scrolled up.
	m.viewport.GotoBottom()

	// Add to history
	m.history = append(m.history, llm.NewTextMessage("user", query))

	// Make a copy of history for the goroutine
	historyCopy := make([]llm.Message, len(m.history))
	copy(historyCopy, m.history)
	priorHistory := historyCopy[:len(historyCopy)-1]

	ctx, cancel := m.turnContext()
	m.cancelGen = cancel

	// Dispatch: single-agent uses RunWithHistory (rich structured history);
	// orchestrator uses Run with history prepended into the query text.
	if m.singleAgent != nil {
		ag := m.singleAgent
		if m.convMem != nil {
			// Conversation memory: rolling summary + recent verbatim turns
			// instead of the full transcript. Falls back to priorHistory
			// unchanged until turns have been folded into the summary.
			priorHistory = m.convMem.ComposeHistory(priorHistory)
		}
		return m, func() tea.Msg {
			result, err := ag.RunWithHistory(ctx, query, priorHistory)
			return AgentDoneMsg{Token: token, Response: result, Err: err}
		}
	}
	runner := m.runner
	composed := buildQueryWithHistory(priorHistory, query)
	return m, func() tea.Msg {
		result, err := runner.Run(ctx, composed)
		return AgentDoneMsg{Token: token, Response: result, Err: err}
	}
}

// dispatchNextInbound pops the oldest queued cross-session message and runs
// it as a main-conversation turn. Caller must ensure the main conversation is
// on screen and idle. The pop copies the slice — Model is a value type, and
// two divergent copies resharing one backing array is how appends clobber.
func (m Model) dispatchNextInbound() (tea.Model, tea.Cmd) {
	if len(m.pendingInbound) == 0 {
		return m, nil
	}
	msg := m.pendingInbound[0]
	m.pendingInbound = append([]InboundSessionMsg(nil), m.pendingInbound[1:]...)
	return m.submitExternal(msg)
}

// submitExternal renders and dispatches a cross-session message as a new
// main-conversation turn. Mirrors submitQuery minus everything that belongs
// to typed input: the textarea, input history, history navigation, and
// /retry's lastQuery are untouched, so an unsent draft survives an injected
// turn. The clean text goes to the transcript and history; only the LLM call
// sees the <session_message> wrapping.
func (m Model) submitExternal(msg InboundSessionMsg) (tea.Model, tea.Cmd) {
	m.generating = true
	m.genToken++
	m.reasoningSealed = false
	token := m.genToken
	m.copySel = -1
	m.pendingWaitToken = msg.WaitToken
	m.pendingWaitGen = token

	m.blocks = append(m.blocks, ContentBlock{Type: BlockSystem, Text: SessionMsgNote(msg.FromSessionID, msg.FromName)})
	m.blocks = append(m.blocks, ContentBlock{Type: BlockUser, Text: msg.Text})
	m.blocks = append(m.blocks, ContentBlock{Type: BlockAssistant})
	m.updateViewport()
	m.viewport.GotoBottom()

	m.history = append(m.history, llm.NewTextMessage("user", msg.Text))

	if m.eventBus != nil {
		m.eventBus.Emit(m.agentName, telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
			FromSessionID: msg.FromSessionID,
			FromName:      msg.FromName,
			Text:          TruncateSessionMsg(msg.Text),
		})
	}

	historyCopy := make([]llm.Message, len(m.history))
	copy(historyCopy, m.history)
	priorHistory := historyCopy[:len(historyCopy)-1]

	ctx, cancel := m.turnContext()
	m.cancelGen = cancel

	llmText := WrapSessionMessage(msg.Text, msg.FromSessionID, msg.FromName)
	if m.singleAgent != nil {
		ag := m.singleAgent
		if m.convMem != nil {
			priorHistory = m.convMem.ComposeHistory(priorHistory)
		}
		return m, func() tea.Msg {
			result, err := ag.RunWithHistory(ctx, llmText, priorHistory)
			return AgentDoneMsg{Token: token, Response: result, Err: err}
		}
	}
	runner := m.runner
	composed := buildQueryWithHistory(priorHistory, llmText)
	return m, func() tea.Msg {
		result, err := runner.Run(ctx, composed)
		return AgentDoneMsg{Token: token, Response: result, Err: err}
	}
}

// --- Content management ---

func (m *Model) appendToCurrentAssistant(text string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockAssistant {
			m.blocks[i].Text += text
			return
		}
	}
}

func (m *Model) lastAssistantText() string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockAssistant {
			return m.blocks[i].Text
		}
	}
	return ""
}

func (m *Model) setLastAssistantText(text string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockAssistant {
			m.blocks[i].Text = text
			return
		}
	}
	// No assistant block yet — append one
	m.blocks = append(m.blocks, ContentBlock{Type: BlockAssistant, Text: text})
}

// markLastAssistantInterrupted appends an "(interrupted)" marker to the
// last assistant block so the user can see the run was cut off.
func (m *Model) markLastAssistantInterrupted() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockAssistant {
			if m.blocks[i].Text == "" {
				m.blocks[i].Text = "*(interrupted)*"
			} else {
				m.blocks[i].Text += "\n\n*(interrupted)*"
			}
			return
		}
	}
}

func (m *Model) completeToolBlock(msg ToolCallEndMsg) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockTool && m.blocks[i].ToolID == msg.ID {
			m.blocks[i].ToolDone = true
			m.blocks[i].ToolErr = msg.Error
			m.blocks[i].Text = msg.Output
			m.blocks[i].Duration = msg.Duration
			return
		}
	}
}

// completeSubagentBlock marks the matching open subagent block done. A
// spawned child's AGENT_END always follows its AGENT_START on the shared bus
// (the spawn tool blocks on child.Run), so the block always exists by the
// time this fires.
func (m *Model) completeSubagentBlock(msg AgentEndMsg) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Type == BlockSubagent && m.blocks[i].AgentName == msg.Name && !m.blocks[i].SubDone {
			m.blocks[i].SubDone = true
			m.blocks[i].SubStatus = msg.Status
			m.blocks[i].SubTokens = msg.Tokens
			m.blocks[i].Duration = msg.Duration
			return
		}
	}
}

// closeRunningSubagents marks every still-running subagent block as done
// with the given status — called when the turn ends (interrupt or error)
// so a spawned child whose AGENT_END never arrived doesn't spin forever.
func (m *Model) closeRunningSubagents(status string) {
	for i := range m.blocks {
		if m.blocks[i].Type == BlockSubagent && !m.blocks[i].SubDone {
			m.blocks[i].SubDone = true
			m.blocks[i].SubStatus = status
		}
	}
}

// agentDepth maps an emitting agent name to a nesting indent level. Runtime-
// spawned agents (recorded in spawnParents) get their true chain depth —
// walking parent links until the root agent or an unknown name is reached —
// so a grandchild spawn nests two levels deep, not one. Everything else
// (pre-declared orchestrator delegation, or a spawn parent link the chat
// session hasn't seen yet) falls back to the original binary heuristic: 0
// for the root agent, 1 for any other name.
func (m Model) agentDepth(agentName string) int {
	if agentName == "" || agentName == m.agentName {
		return 0
	}
	if _, spawned := m.spawnParents[agentName]; spawned {
		depth := 0
		seen := map[string]bool{}
		for cur := agentName; ; {
			parent, ok := m.spawnParents[cur]
			if !ok || seen[cur] {
				break
			}
			seen[cur] = true
			depth++
			if parent == "" || parent == m.agentName {
				break
			}
			cur = parent
		}
		return depth
	}
	return 1
}

// insertBeforeAssistant inserts block just before the current turn's trailing
// assistant block, so streamed answer text always renders last. If the turn
// has no assistant block yet, the block is appended.
func (m *Model) insertBeforeAssistant(block ContentBlock) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		switch m.blocks[i].Type {
		case BlockAssistant:
			tail := make([]ContentBlock, len(m.blocks)-i)
			copy(tail, m.blocks[i:])
			m.blocks = append(append(m.blocks[:i:i], block), tail...)
			return
		case BlockUser:
			// Reached the start of the current turn without an assistant
			// block — append at the end.
			m.blocks = append(m.blocks, block)
			return
		}
	}
	m.blocks = append(m.blocks, block)
}

// appendReasoning routes a streaming reasoning delta into a BlockReasoning
// block. While a reasoning segment is open the delta is appended to it; once
// a tool call seals the segment (reasoningSealed), the next delta starts a
// fresh block so reasoning interleaves with tools in execution order.
func (m *Model) appendReasoning(text, agentName string) {
	if !m.reasoningSealed {
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].Type == BlockReasoning {
				m.blocks[i].Text += text
				return
			}
			if m.blocks[i].Type == BlockUser {
				break
			}
		}
	}
	m.insertBeforeAssistant(ContentBlock{
		Type:      BlockReasoning,
		Text:      text,
		AgentName: agentName,
		Depth:     m.agentDepth(agentName),
	})
	m.reasoningSealed = false
}

// isThinking reports whether the agent is streaming reasoning (CoT) and has
// not yet produced any visible answer text — used to distinguish a
// "thinking..." status from "generating...".
func (m Model) isThinking() bool {
	if m.lastAssistantText() != "" {
		return false
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		switch m.blocks[i].Type {
		case BlockReasoning:
			if strings.TrimSpace(m.blocks[i].Text) != "" {
				return true
			}
		case BlockUser:
			return false
		}
	}
	return false
}

// collapseOpenReasoning collapses the most recent reasoning block of the
// current turn — called when a tool call seals it, so only the live segment
// stays expanded ("collapse old, expand active").
func (m *Model) collapseOpenReasoning() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		switch m.blocks[i].Type {
		case BlockReasoning:
			m.blocks[i].Collapsed = true
			return
		case BlockUser:
			return
		}
	}
}

// collapseAllReasoning collapses every reasoning block — called at turn end
// so a finished conversation shows one-line thought summaries.
func (m *Model) collapseAllReasoning() {
	for i := range m.blocks {
		if m.blocks[i].Type == BlockReasoning {
			m.blocks[i].Collapsed = true
		}
	}
}

// --- Mouse ---

// handleMouse routes mouse events: the wheel scrolls the viewport, a left
// click on a tool/reasoning block toggles its collapsed state. Motion and
// press events are ignored (no re-render — avoids motion-event storms).
//
// Currently unreachable: cmd/rakitsu/interactive.go's tea.NewProgram(...)
// no longer passes a mouse ProgramOption (removed so native click-drag
// text selection works — see the Ctrl+O/Ctrl+L cases in handleKey for the
// keyboard scroll replacement), and bubbletea never emits tea.MouseMsg
// without one.
// Left in place, not deleted, as the starting point for ROADMAP M2.5 phase
// 2 ("Mouse interaction"), which would reintroduce a mouse ProgramOption.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionRelease {
			m.handleClick(msg.Y)
		}
		return m, nil
	}
	return m, nil
}

// handleClick toggles the collapsed state of the tool/reasoning block under
// the given screen row. Row 0 is the title bar, row 1 is the sticky user-
// prompt header; the viewport occupies the rows below them.
func (m *Model) handleClick(y int) {
	const chromeRows = 1 + stickyHeaderHeight
	rel := y - chromeRows
	if rel < 0 || rel >= m.viewport.Height {
		return // click landed on the title, sticky header, textarea, or status bar
	}
	contentLine := m.viewport.YOffset + rel
	i := m.blockAt(contentLine)
	if i < 0 {
		return
	}
	if m.blocks[i].Type == BlockReasoning || m.blocks[i].Type == BlockTool {
		m.blocks[i].Collapsed = !m.blocks[i].Collapsed
		m.updateViewport()
	}
}

// --- Input history recall ---

// recallPrev steps the input back to an older submitted message. The first
// step saves the current unsent draft so recallNext can restore it.
func (m *Model) recallPrev() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIdx == -1 {
		m.historyDraft = m.textarea.Value()
		m.historyIdx = len(m.inputHistory)
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		m.textarea.SetValue(m.inputHistory[m.historyIdx])
		m.textarea.CursorEnd()
	}
}

// recallNext steps the input forward toward newer messages; stepping past the
// newest restores the saved draft and exits navigation.
func (m *Model) recallNext() {
	if m.historyIdx == -1 {
		return // not navigating
	}
	m.historyIdx++
	if m.historyIdx >= len(m.inputHistory) {
		m.historyIdx = -1
		m.textarea.SetValue(m.historyDraft)
	} else {
		m.textarea.SetValue(m.inputHistory[m.historyIdx])
	}
	m.textarea.CursorEnd()
}

// --- Layout ---

func (m Model) handleResize() Model {
	headerHeight := 1                                                            // title
	footerHeight := 1                                                            // status
	inputHeight := 3                                                             // textarea
	chrome := headerHeight + stickyHeaderHeight + footerHeight + inputHeight + 2 // borders/padding

	vpHeight := m.height - chrome
	if vpHeight < 3 {
		vpHeight = 3
	}

	if !m.ready {
		m.viewport = viewport.New(m.width, vpHeight)
		m.viewport.YPosition = headerHeight + stickyHeaderHeight
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vpHeight
	}
	m.textarea.SetWidth(m.width)

	// Re-create glamour renderer with new width
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(m.width-4),
	)
	if err == nil {
		m.renderer = r
	}

	m.updateViewport()
	return m
}

// updateViewport re-renders every block, rebuilds the line-span table for
// click hit-testing, and refreshes the viewport. It keeps the user pinned to
// the bottom only if they were already there — a manual scroll-up to read is
// not yanked back down by streaming content.
func (m *Model) updateViewport() {
	if !m.ready {
		return
	}
	stickToBottom := m.viewport.AtBottom()

	// Render each block on its own so we can record where it lands in the
	// final content. count = number of newlines in the block's rendered
	// output; that is exactly how many viewport lines it occupies.
	var sb strings.Builder
	line := 0
	// The startup banner is raw lipgloss (fixed box layout), not markdown —
	// it's prepended directly rather than going through renderBlock/glamour,
	// which would word-wrap and reflow its border box. It isn't a block, so
	// it gets no span entry; line starts after it so click hit-testing
	// (blockAt) still maps content lines to the right block.
	if banner := renderBanner(m.width, m.version, m.agentName, m.modelName); banner != "" {
		sb.WriteString(banner)
		line = strings.Count(banner, "\n")
	}
	m.spans = m.spans[:0]
	for i := range m.blocks {
		rendered := m.renderBlock(i)
		n := strings.Count(rendered, "\n")
		m.spans = append(m.spans, blockSpan{start: line, count: n})
		sb.WriteString(rendered)
		line += n
	}

	m.viewport.SetContent(sb.String())
	if stickToBottom {
		m.viewport.GotoBottom()
	}
}

// renderBlock renders block i to its final (glamour-styled) string, with
// spacing normalized to exactly one leading and one trailing blank line.
// Rendering blocks individually (needed for the line-span table) means
// glamour applies its per-document prefix/suffix to each — without
// normalization that stacks extra blank lines for every block.
func (m *Model) renderBlock(i int) string {
	raw := m.blocks[i].Render()
	if strings.TrimSpace(raw) == "" {
		return "" // empty block occupies no viewport lines
	}
	out := raw
	if m.renderer != nil {
		if r, err := m.renderer.Render(raw); err == nil {
			out = r
		}
	}
	out = strings.Trim(out, "\n")
	if out == "" {
		return ""
	}
	return "\n" + out + "\n"
}

// stickyUserBlockIdx returns the index of the user block currently being
// scrolled through — the most recent BlockUser whose rendered span starts at
// or before viewport.YOffset. Returns -1 when the viewport is at the top, or
// when no preceding user block exists (e.g. only system blocks rendered).
func (m *Model) stickyUserBlockIdx() int {
	if m.viewport.YOffset == 0 {
		return -1
	}
	found := -1
	for i := range m.blocks {
		if i >= len(m.spans) {
			break
		}
		if m.spans[i].start > m.viewport.YOffset {
			break
		}
		if m.blocks[i].Type == BlockUser {
			found = i
		}
	}
	return found
}

// renderStickyHeader produces the line displayed above the viewport: the
// truncated text of the currently-scrolled-through user prompt, or a blank
// line of the reserved height when nothing should pin (top of the chat,
// or no user block before the current scroll position).
func (m *Model) renderStickyHeader() string {
	idx := m.stickyUserBlockIdx()
	if idx < 0 || idx >= len(m.blocks) {
		return strings.Repeat(" ", m.width)
	}
	text := m.blocks[idx].Text
	if nl := strings.IndexByte(text, '\n'); nl >= 0 {
		text = text[:nl]
	}
	const prefix = "❯ "
	avail := m.width - runewidth.StringWidth(prefix) - 1 // -1 for the ellipsis slot
	if avail < 4 {
		avail = 4
	}
	text = runewidth.Truncate(text, avail, "…")
	return stickyHeaderStyle.Render(prefix + text)
}

// blockAt returns the index of the block occupying the given rendered-content
// line, or -1 if none (e.g. trailing blank line).
func (m *Model) blockAt(contentLine int) int {
	for i, s := range m.spans {
		if s.count > 0 && contentLine >= s.start && contentLine < s.start+s.count {
			return i
		}
	}
	return -1
}

// applyTokenUsage accumulates a per-call TokenUsageMsg into the visible
// counter. Extracted as a value-receiver method so tests can drive it
// without constructing a full bubbletea runtime. Returns the updated Model
// so tests can chain calls (the `_ tea.Cmd` slot matches Update's signature
// for callers that want it).
func (m Model) applyTokenUsage(msg TokenUsageMsg) (Model, tea.Cmd) {
	m.totalTokens += msg.Total
	return m, nil
}

// recordAgentUsageStart creates or refreshes an agent's Model/Provider from
// its AGENT_START event. Fires for every agent (root, orchestrator-declared,
// and spawned children alike) — bridge.go does not filter by parent.
func (m *Model) recordAgentUsageStart(msg AgentStartMsg) {
	if m.agentUsage == nil {
		m.agentUsage = make(map[string]*agentUsageSnapshot)
	}
	snap, ok := m.agentUsage[msg.Name]
	if !ok {
		snap = &agentUsageSnapshot{}
		m.agentUsage[msg.Name] = snap
	}
	snap.Model = msg.Model
	snap.Provider = msg.Provider
}

// updateAgentUsageEnd folds an AGENT_END event into that agent's running
// snapshot. Tokens/Cost/MaxTokens/MaxCost/PricingKnown/Status overwrite
// (TokenGuard is already cumulative per agent instance across turns);
// TotalIterations and Turns accumulate since Iterations is per-call.
func (m Model) updateAgentUsageEnd(msg AgentEndMsg) (Model, tea.Cmd) {
	if m.agentUsage == nil {
		m.agentUsage = make(map[string]*agentUsageSnapshot)
	}
	snap, ok := m.agentUsage[msg.Name]
	if !ok {
		snap = &agentUsageSnapshot{}
		m.agentUsage[msg.Name] = snap
	}
	snap.Tokens = msg.Tokens
	snap.Cost = msg.Cost
	snap.MaxTokens = msg.MaxTokens
	snap.MaxCost = msg.MaxCost
	snap.PricingKnown = msg.PricingKnown
	snap.Status = msg.Status
	snap.TotalIterations += msg.Iterations
	snap.Turns++
	return m, nil
}

func (m Model) statusBar() string {
	leftRaw := fmt.Sprintf(" tokens: %d", m.totalTokens)
	var rightRaw string
	switch {
	case m.waitingForInput:
		// Agent is paused on user_input, not generating. Show the pause
		// reason instead of the misleading "generating..." label.
		rightRaw = "⏸ waiting for your answer · Enter to send"
	case m.generating && m.isThinking():
		// Reasoning models stream CoT before any answer text — surface it
		// as progress so the UI doesn't read as frozen. spinner.View() is
		// plain text here (no .Style configured on m.spinner), so it's
		// safe to clamp below alongside the rest of rightRaw.
		rightRaw = m.spinner.View() + " thinking..."
	case m.generating:
		rightRaw = m.spinner.View() + " generating..."
	case m.notice != "":
		rightRaw = m.notice
	default:
		rightRaw = "Enter send · Ctrl+Y copy · Ctrl+P older · Ctrl+O/L scroll · Ctrl+C quit"
	}

	// Truncate the raw text before styling — same reasoning as the title
	// bar in View(): a long notice (or, now, the longer default hint) must
	// never push this line past m.width and physically wrap. leftRaw needs
	// this too, not just rightRaw: totalTokens is an unbounded running
	// count, and " tokens: <N>" alone can exceed m.width on a narrow
	// terminal over a long enough session. Clamp leftRaw first (reserving
	// up to 4 cols for the right side, but never MORE than m.width itself
	// has to give — see below) so avail is computed from leftRaw's actual
	// post-clamp width, not its unclamped one.
	//
	// Both floors here used to have their own independent minimum of 4,
	// which could both trigger on the same narrow terminal and claim 4+4=8
	// columns combined even when m.width was e.g. 5 or 6 — reintroducing
	// the exact overflow this function exists to prevent, just needing a
	// narrower terminal than the original bug. Flooring at 0 instead of 4
	// means leftMax/avail can never claim more than m.width has left to
	// give, and clampVisibleWidth no longer inflates a small-but-positive
	// width back up past what its caller actually asked for.
	leftMax := m.width - 4
	if leftMax < 0 {
		leftMax = 0
	}
	leftRaw = clampVisibleWidth(leftRaw, leftMax)

	avail := m.width - runewidth.StringWidth(leftRaw)
	if avail < 0 {
		avail = 0
	}
	rightRaw = clampVisibleWidth(rightRaw, avail)

	left := statusStyle.Render(leftRaw)
	right := statusStyle.Render(rightRaw)

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + right
}
