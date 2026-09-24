package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/llm/openai"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
	"gopkg.in/yaml.v3"
)

// logProviderWarning appends an operator-facing provider diagnostic to
// ~/.rakitsu/provider-warnings.log instead of the terminal — see
// runInteractive's openai.SetWarnWriter call for why. Best-effort: a home
// directory or filesystem problem here just drops the diagnostic, the same
// way the original os.Stderr write would have been lost if stderr were
// redirected to /dev/null.
func logProviderWarning(s string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".rakitsu")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "provider-warnings.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s", time.Now().Format(time.RFC3339), s)
}

// sendDrainTimeout bounds how long exit waits for a cancelled directed agent
// chat to unwind before tearing down its tools anyway. A send that honours
// its context returns as soon as the in-flight HTTP call aborts, well inside
// this; the bound exists so one that does not can never leave the user
// staring at a dead terminal.
const sendDrainTimeout = 5 * time.Second

// shutdownChat tears a chat session down in the only safe order: cancel every
// in-flight directed send and wait for it to unwind, THEN run cleanup.
//
// The order is the whole fix. bubbletea launches Cmds detached and never waits
// on them, so p.Run() can return with a Roster.Send still executing, and
// cleanup closes the very tool instances (MCP subprocesses included) that the
// live side-thread agent is holding. This lived as two separate defers relying
// on LIFO registration order — correct, but one reorder away from silently
// restoring the race with every test still green. It is one function so a test
// can hold it to the order.
//
// The wait is bounded: a send that ignores its context must never hang the
// terminal on exit, and the user is told when that happens rather than left to
// guess. A nil tracker waits for nothing, which is what a session that never
// opened a side chat wants.
func shutdownChat(sends *chat.InFlight, cleanup func()) {
	sends.CancelAll()
	if !sends.Wait(sendDrainTimeout) {
		fmt.Fprintf(os.Stderr, "Warning: a directed agent chat was still running after %s — shutting down anyway\n", sendDrainTimeout)
	}
	if cleanup != nil {
		cleanup()
	}
}

// runInteractive launches the chat TUI for any config (single agent,
// orchestrator, nested) via BuildRunner. Called from `rakitsu run` when
// `interactive: true` is set in the config (or --interactive CLI flag).
//
// initialQuery is optional; if set, it's auto-submitted as the first turn
// (same effect as the user typing it on open).
func runInteractive(ctx context.Context, cfg *config.Config, initialQuery string, turnTimeout time.Duration) (runErr error) {
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("no agents defined in config")
	}

	// This command owns the terminal via bubbletea's alt-screen renderer
	// (see the tea.NewProgram call below). A provider-level diagnostic
	// (e.g. a reasoning-tier model rejecting a non-default temperature)
	// must never write to the terminal directly — that races bubbletea's
	// own concurrent render writes and corrupts the screen, no matter how
	// atomic the write itself is (see internal/llm/openai's warnFn doc).
	// Redirect it to a log file before any provider is built below.
	openai.SetWarnWriter(logProviderWarning)

	eventBus := telemetry.NewEventBus(1024)

	// Session persistence — always-on, failure-tolerant. Each chat run is
	// one session; all events (turns, tool calls, streaming tokens) are
	// persisted to ~/.rakitsu/sessions/<id>.jsonl for later replay/inspection.
	var sessionStore *store.SessionStore
	if ss, err := store.NewSessionStore(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session persistence unavailable: %v\n", err)
	} else {
		sessionStore = ss
		agentNames := make([]string, len(cfg.Agents))
		for i, a := range cfg.Agents {
			agentNames[i] = a.Name
		}
		configYAML := ""
		if yamlBytes, err := yaml.Marshal(config.Redacted(cfg)); err == nil {
			configYAML = string(yamlBytes)
		}
		startQuery := initialQuery
		if startQuery == "" {
			startQuery = "(interactive session)"
		}
		if err := sessionStore.StartSession(store.SessionMeta{
			Name:       cfg.Name,
			Query:      startQuery,
			ConfigYAML: configYAML,
			Agents:     agentNames,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not start session recording: %v\n", err)
			sessionStore = nil
		}
	}
	if sessionStore != nil {
		eventCh := eventBus.Subscribe()
		var subWG sync.WaitGroup
		subWG.Add(1)
		go func() {
			defer subWG.Done()
			for event := range eventCh {
				sessionStore.WriteEvent(event)
			}
		}()
		defer func() {
			// See cmd/rakitsu/run.go for rationale. Drain the
			// subscriber goroutine before closing the session so trailing
			// AGENT_END events aren't dropped on a closed file.
			eventBus.Unsubscribe(eventCh)
			subWG.Wait()

			status := store.SessionSuccess
			if runErr != nil {
				status = store.SessionError
			}
			sessionStore.EndSession(status)
		}()
	}

	userInputReqCh := make(chan userinput.InputRequest, 1)
	userInputRespCh := make(chan string, 1)

	// Bind the memory tools' "session" scope shorthand to this chat session.
	var sessionID string
	if sessionStore != nil {
		sessionID = sessionStore.CurrentSessionID()
	}
	// Hub URL for the send_message/list_sessions tools; "" (with --no-hub)
	// disables them. Registration with the hub happens further down — tools
	// built before it simply error with "unavailable" if the hub is down.
	toolHubURL := ""
	if !noHub {
		toolHubURL = hubURL
	}
	buildOpts := BuildOptions{
		UserInputReqCh:  userInputReqCh,
		UserInputRespCh: userInputRespCh,
		SessionID:       sessionID,
		HubURL:          toolHubURL,
	}
	if cfg.Settings.Memory.Enabled && sessionID != "" {
		buildOpts.MemorySessionScope = memory.SessionScope(sessionID)
	}

	br, err := BuildRunner(ctx, cfg, eventBus, buildOpts)
	if err != nil {
		return err
	}
	// One defer, not two: draining in-flight directed sends before tearing
	// down the tools they hold is an ordering requirement, and shutdownChat
	// owns it. See its doc for why.
	sends := &chat.InFlight{}
	defer shutdownChat(sends, br.Cleanup)

	// Decide whether to wrap the root Runner in a ChatHost meta-agent.
	// Default: wrap non-conversational configs (those with an orchestrator).
	// Single-agent configs are already conversational — use them directly.
	// Override with `interactive_overlay: true/false` in YAML.
	overlay := br.RootOrch != nil
	if cfg.InteractiveOverlay != nil {
		overlay = *cfg.InteractiveOverlay
	}

	var singleAgent *agent.Agent
	runner := br.Runner
	innerRunner := br.Runner // the real root; preserved for chat /model when overlay wraps it
	agentName := runner.GetName()
	modelName := "interactive"
	// hostModel is a real model name for AddHost, never a display label — see
	// Entry.Model's contract. It stays "" unless we actually know the model:
	// the ChatHost overlay's own default, or nothing at all for the
	// single-agent case (AddConfig already recorded that agent's real model
	// when BuildRunner ran; AddHost fills Model/Provider only when empty, so
	// passing "" here is exactly "don't clobber that").
	hostModel := ""
	if br.RootOrch != nil {
		modelName = "orchestrator: " + cfg.Orchestrator.Strategy
	}

	if overlay {
		host, err := buildChatHostAgent(ctx, cfg, br.Runner, eventBus, userInputReqCh, userInputRespCh, br.SpawnToolFor, sessionID, toolHubURL)
		if err != nil {
			return fmt.Errorf("failed to build chat-host overlay: %w", err)
		}
		runner = host
		singleAgent = host
		agentName = host.GetName()
		modelName = "chat-host (" + cfg.Settings.Defaults.Model + ")"
		hostModel = cfg.Settings.Defaults.Model
		// innerRunner stays = br.Runner (the orchestrator behind the overlay)
		// so /model can reach Coder/Tester/Auditor.
	} else if a, ok := br.Runner.(*agent.Agent); ok {
		// Single-agent direct mode: enables RunWithHistory for rich context.
		singleAgent = a
	}

	// Conversation memory (settings.memory.conversation): summarized context
	// instead of full-history re-feed. Engages only on the RunWithHistory
	// path (single agent / ChatHost overlay) — orchestrator-direct mode
	// keeps the existing capped text-composed history. Requires a session ID
	// so the rolling summary persists for resume.
	var convMem *memory.ConversationMemory
	if cfg.Settings.Memory.Enabled && cfg.Settings.Memory.Conversation.Enabled && singleAgent != nil && sessionID != "" {
		if ms := openMemoryStore(cfg); ms != nil {
			convMem = memory.NewConversationMemory(ms, sessionID, memory.ConversationOptions{
				KeepRecentTurns: cfg.Settings.Memory.Conversation.KeepRecentTurns,
				SummaryMaxChars: cfg.Settings.Memory.Conversation.SummaryMaxChars,
				DisableSummary:  cfg.Settings.Memory.Conversation.DisableSummary,
			})
		}
	}

	// Wire the /model slash command: closure captures ctx + cfg so the chat
	// package can build a new LLM client mid-session without re-importing
	// our provider-factory machinery. Also pass the configured provider keys
	// for `/model <agent> <model>@<provider>` validation and error messages.
	buildLLM := func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error) {
		return createLLMProvider(ctx, cfg, providerName, model, mc)
	}
	providerNames := make([]string, 0, len(cfg.Settings.Providers))
	for name := range cfg.Settings.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	// The host is listed so the picker can offer "return to the main
	// conversation". Its history stays owned by the chat surface — the
	// roster never runs it.
	if br.AgentChat != nil {
		br.AgentChat.AddHost(agentName, hostModel, "")
	}

	// Hub connection (cross-session messaging + live observability): register
	// as a chat-mode session so other sessions can discover and message this
	// TUI. Best-effort — a missing hub means standalone, same as one-shot.
	// Inbound "session_message" commands are pumped into the bubbletea model
	// via inboundCh; everything else on the command channel is ignored (the
	// TUI has no debug-attach surface).
	var inboundCh chan chat.InboundSessionMsg
	var postMessageResult func(waitToken, final string, interrupted bool, errText string)
	if !noHub && sessionID != "" {
		agentNames := make([]string, len(cfg.Agents))
		for i, a := range cfg.Agents {
			agentNames[i] = a.Name
		}
		hc := telemetry.NewHubClient(hubURL, sessionID, eventBus)
		if err := hc.Register(cfg.Name, "(interactive)", "", agentNames, "chat", cfg.Settings.SessionMsg.Enabled); err != nil {
			fmt.Fprintf(os.Stderr, "Hub not available at %s, running standalone\n", hubURL)
		} else {
			fmt.Fprintf(os.Stderr, "Connected to hub at %s\nSession ID: %s\n", hubURL, sessionID)
			if cfg.Settings.SessionMsg.Enabled {
				ch := make(chan chat.InboundSessionMsg, 4)
				inboundCh = ch
				hc.OnCommand = func(action string, data map[string]interface{}) {
					if action != "session_message" {
						return
					}
					text, _ := data["text"].(string)
					if text == "" {
						return
					}
					fromID, _ := data["from_session_id"].(string)
					fromName, _ := data["from_name"].(string)
					waitToken, _ := data["wait_token"].(string)
					select {
					case ch <- chat.InboundSessionMsg{FromSessionID: fromID, FromName: fromName, Text: text, WaitToken: waitToken}:
					default:
						// TUI queue backlogged — drop rather than block the
						// 500ms poll loop. The pairwise rate limit upstream
						// keeps this from being a real data-loss path.
					}
				}
				postMessageResult = func(waitToken, final string, interrupted bool, errText string) {
					if waitToken == "" {
						return
					}
					_ = hc.PostMessageResult(waitToken, final, interrupted, errText)
				}
			}
			hc.Start()
			defer hc.Stop("completed")
		}
	}

	model := chat.NewModel(chat.Config{
		Runner:            runner,
		SingleAgent:       singleAgent,
		EventBus:          eventBus,
		AgentName:         agentName,
		ModelName:         modelName,
		Version:           versionString(),
		InitialQuery:      initialQuery,
		UserInputReqCh:    userInputReqCh,
		UserInputRespCh:   userInputRespCh,
		BuildLLM:          buildLLM,
		KnownProviders:    providerNames,
		InnerRunner:       innerRunner,
		ConvMem:           convMem,
		AgentChat:         br.AgentChat,
		InFlight:          sends,
		InboundCh:         inboundCh,
		PostMessageResult: postMessageResult,
		TurnTimeout:       turnTimeout,
	})

	// No mouse ProgramOption: the terminal keeps mouse input, so native
	// click-drag-select and Cmd+C / Ctrl+Shift+C copy work like any other
	// terminal program. handleMouse/handleClick in internal/chat/model.go
	// are unreachable as a result (bubbletea never emits tea.MouseMsg
	// without one) — kept for ROADMAP M2.5 phase 2 ("Mouse interaction"),
	// not dead code to delete. Ctrl+O/Ctrl+L replace the mouse-wheel scroll
	// this gives up — plain control bytes, not escape sequences, after
	// PgUp/PgDown (no real key on most Mac keyboards without Fn) and
	// Alt+Up/Alt+Down (didn't reach the app as Alt-modified keys on a real
	// Mac terminal either) both turned out to be terminal-dependent.
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	// Stop the background event bridge — see chat.Model.Close's doc comment
	// for why this can't be left to the goroutine to notice on its own.
	if cm, ok := finalModel.(chat.Model); ok {
		cm.Close()
	}
	if err != nil {
		return fmt.Errorf("chat TUI error: %w", err)
	}
	return nil
}
