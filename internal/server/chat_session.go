package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
	"github.com/SK-ENT/rakitsu/internal/turntree"
	"gopkg.in/yaml.v3"
)

// ChatBuildFunc constructs a Runner for a chat session.
//
// The implementation (injected from cmd/rakitsu) is responsible for:
//   - calling BuildRunner with the given user-input channels
//   - applying the ChatHost overlay when appropriate (same logic as
//     cmd/rakitsu/interactive.go)
//
// Returns:
//   - runner       — top-level Runner served to clients. When the ChatHost
//     overlay is active, this is the ChatHost agent that wraps the real root.
//   - innerRunner  — the REAL root behind any overlay (the orchestrator whose
//     sub-agents the user can address by name via /model). When there's no
//     overlay this equals runner.
//   - cleanup      — releases resources (MCP subprocesses etc.) when the
//     session ends.
//   - agentName, modelLabel — used for session metadata + UI.
//
// sessionID is the chat session's id (minted before the build so the
// send_message tool can carry its own reply address); selfURL is the base
// URL of the serve process owning /api/sessions/* ("" disables the
// cross-session messaging tools).
type ChatBuildFunc func(
	ctx context.Context,
	cfg *config.Config,
	eventBus *telemetry.EventBus,
	userInputReqCh chan userinput.InputRequest,
	userInputRespCh chan string,
	sessionID string,
	selfURL string,
) (runner agent.Runner, innerRunner agent.Runner, cleanup func(), agentName, modelLabel string, err error)

// serverMsg is the envelope sent from the server to connected WS clients.
// The Type field discriminates the concrete payload:
//
//	token                 → Text, GenToken
//	turn_done             → Final, Err, GenToken
//	turn_started          → GenToken
//	tool_start            → ToolCallID, ToolName, ToolArgs, AgentName, Depth, GenToken
//	tool_end              → ToolCallID, Output, Err, Duration, GenToken
//	reasoning             → Text, AgentName, Depth, GenToken
//	user_input_request    → RequestID, Question, Default
//	event                 → Event (raw telemetry.AgentEvent)
//	error                 → Message
//	closed                → (session ended)
//	transcript_resync     → Transcript (flat derived list after branch op)
//	tree_snapshot         → Tree (summary tree shape for the Viewer)
type serverMsg struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Final      string `json:"final,omitempty"`
	Err        string `json:"error,omitempty"`
	GenToken   int64  `json:"gen_token,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Question   string `json:"question,omitempty"`
	Default    string `json:"default,omitempty"`
	Message    string `json:"message,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	Output     string `json:"output,omitempty"`
	Duration   int64  `json:"duration,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	Depth      int    `json:"depth,omitempty"`
	// ParentAgent, Model, Status, Tokens back "agent_start"/"agent_end" —
	// a runtime-spawned child's lifecycle (see AgentStartPayload.ParentAgent
	// and AgentEndPayload). Model/ParentAgent are set on agent_start;
	// Status/Tokens on agent_end.
	ParentAgent string                 `json:"parent_agent,omitempty"`
	Model       string                 `json:"model,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Tokens      int                    `json:"tokens,omitempty"`
	Event       *telemetry.AgentEvent  `json:"event,omitempty"`
	Meta        map[string]interface{} `json:"meta,omitempty"`
	Transcript  []transcriptEntry      `json:"transcript,omitempty"`
	Tree        *treeSnapshot          `json:"tree,omitempty"`
	// TurnID carries the server-assigned turn id on turn_started so the
	// client can patch its optimistically-pushed user block. Without this,
	// new turns never get a turn_id (only the initial attached transcript
	// does), so the inline ✎ edit / ⟳ regenerate affordances stay hidden.
	TurnID string `json:"turn_id,omitempty"`
}

// transcriptEntry is a single rendered block that a reconnecting client can
// use to rebuild the visible conversation without replaying the full event
// stream. Ring-buffered on ChatSession; sent with the `attached` message.
type transcriptEntry struct {
	Kind        string `json:"kind"`                  // "user" | "assistant" | "system" | "user_input_request" | "tool" | "reasoning"
	Text        string `json:"text,omitempty"`        // user turn text, assistant final, or reasoning content
	Interrupted bool   `json:"interrupted,omitempty"` // assistant turn ended in error/cancel
	Err         string `json:"error,omitempty"`       // assistant turn error text (Kind == "assistant", Interrupted == true)
	RequestID   string `json:"request_id,omitempty"`
	Question    string `json:"question,omitempty"`
	Default     string `json:"default,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	Timestamp   string `json:"timestamp"`

	// Tool-block fields (Kind == "tool").
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	Output     string `json:"output,omitempty"`
	ToolErr    string `json:"tool_err,omitempty"`
	Duration   int64  `json:"duration,omitempty"`

	// Subagent-block fields (Kind == "subagent") — a runtime-spawned child's
	// finished lifecycle, recorded for reconnect replay. AgentName above
	// carries the child's instance name; Status/Tokens/Duration mirror
	// AgentEndPayload. Only completed children are recorded (no "running"
	// row survives a reconnect — the live WS frame owns that state).
	Status string `json:"status,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
	// Depth is the nesting indent: 0 for the session's bound agent, 1+ for
	// delegated sub-agents. Used by tool and reasoning entries.
	Depth int `json:"depth,omitempty"`

	// PR D — branch metadata. Populated by snapshotTranscript when
	// deriving from the active path. Persisted entries (under Node.Entries)
	// leave these blank — they're decoration for the wire only.
	TurnID      string `json:"turn_id,omitempty"`
	BranchIndex int    `json:"branch_index,omitempty"` // 0-indexed; only set on "user" entries
	BranchCount int    `json:"branch_count,omitempty"` // ≥1; only set on "user" entries
}

// clientMsg is the envelope received from a WS client.
//
// PR C branch ops added: edit_turn / regenerate / switch_branch /
// set_active_path / request_tree. Each carries TurnID identifying the
// target node; switch_branch additionally carries Dir (±1) or TargetIndex.
type clientMsg struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	Dir         int    `json:"dir,omitempty"`
	TargetIndex int    `json:"target_index,omitempty"`
}

// chatTurn is queued internally when a client submits a new turn.
//
// skipAppend supports the edit_turn / regenerate auto-generate flow (PR D-2):
// EditTurn / RegenerateTurn already mutated the tree to put a new sibling at
// the active leaf, so the executor must fill that node's Entries WITHOUT
// appending a fresh child turn underneath it.
type chatTurn struct {
	text       string
	skipAppend bool
	// fromSessionID/fromName mark a cross-session message injected via
	// SubmitExternal. Non-empty fromSessionID switches executeTurn into
	// external mode: system note + LLM-only <session_message> wrapping.
	// Both are sender-claimed and unverified.
	fromSessionID string
	fromName      string
	// done, when non-nil, is how SubmitExternalWait learns this specific
	// turn's outcome. nil means fire-and-forget (SubmitExternal). Buffered
	// (cap 1) so executeTurn's send never blocks even if the waiter already
	// gave up (ctx timeout).
	done chan turnOutcome
}

// turnOutcome is how a completed (or errored) turn is reported back to a
// synchronous waiter — locally via SubmitExternalWait's done channel, or
// remotely via the hub message-result relay (session_message.go/hub.go).
type turnOutcome struct {
	TurnID      string
	Final       string
	Interrupted bool
	Err         string
}

// ChatSession runs a long-lived interactive conversation with a config.
//
// Architecture:
//   - Per-session event bus; isolated from the main hub bus.
//   - One background run goroutine per turn (cancellable).
//   - Fan-out to attached WS clients via per-client send channels.
//   - Session persistence matches the CLI interactive path.
//
// Thread safety: the Session is shared across client goroutines. `mu` guards
// the client set and the current-gen cancel func; gen token uses atomic.
type ChatSession struct {
	ID          string
	cfg         *config.Config
	configID    string
	workdir     string
	interactive bool // cfg.Interactive — informational, not used to gate UI
	agentName   string
	modelLabel  string
	created     time.Time

	eventBus     *telemetry.EventBus
	forwardBus   *telemetry.EventBus
	sessionStore *store.SessionStore
	runner       agent.Runner
	innerRunner  agent.Runner // real root behind any ChatHost overlay; == runner when no overlay
	cleanup      func()

	// Slash-command dispatch context (populated when ChatSessionOptions
	// includes BuildLLM). nil disables /model swap.
	buildLLM       func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)
	knownProviders []string

	userInputReqCh  chan userinput.InputRequest
	userInputRespCh chan string

	// convMem, when non-nil, enables conversation memory
	// (settings.memory.conversation): turns are composed as rolling summary
	// + recent verbatim turns instead of the full transcript. Only effective
	// on the RunWithHistory path (single agent / ChatHost overlay).
	convMem *memory.ConversationMemory

	turnCh   chan chatTurn
	stopCh   chan struct{}
	stopOnce sync.Once
	closed   atomic.Bool

	genToken atomic.Int64

	// persistSeq/persistMu/persistGen guard the on-disk .chat.json against
	// two independent snapshot-then-write callers (persistTree and
	// broadcastTreeMutationLocked) racing each other. persistSeq is bumped
	// under mu at snapshot time so its order matches mutation order;
	// persistMu+persistGen (checked in persistTreeRaw) then make sure a
	// write only lands if its generation is still the newest one seen,
	// regardless of which caller's disk I/O happens to finish first.
	persistSeq uint64
	persistMu  sync.Mutex
	persistGen uint64

	// tree is the authoritative branching turn history. The active
	// conversation is the unique root→leaf path; the flat transcript that
	// the wire still exposes is derived from this on demand. A linear
	// session (no edits/forks) has a single chain — behavior is identical
	// to the previous flat slice in that case. Guarded by mu.
	tree *turntree.Tree[transcriptEntry]

	mu         sync.Mutex
	cancelGen  context.CancelFunc
	generating bool
	clients    map[*chatClient]struct{}

	// pendingInput tracks the currently-outstanding user_input request so
	// that an incoming user_input_response from any client gets forwarded
	// even if the original request arrived before the client connected.
	pendingInputID string
}

// ChatSessionOptions is the input to StartChatSession.
type ChatSessionOptions struct {
	ConfigID     string
	ConfigPath   string // resolved path from ConfigStore — persisted so "rerun" can re-open the YAML
	Cfg          *config.Config
	Workdir      string
	SessionStore *store.SessionStore // may be nil (persistence disabled)
	BuildFunc    ChatBuildFunc
	// ForwardBus, when non-nil, receives a copy of every event emitted on
	// the session's private bus — with the session id tagged so debugger
	// SSE streams can filter for them. This is how USER_INPUT_PENDING
	// bubbles up from a chat-mode run into the main debugger UI.
	ForwardBus *telemetry.EventBus

	// BuildLLM, when non-nil, enables the `/model` slash command to swap an
	// agent's LLM mid-session via a freshly built provider client. Captures
	// the same cfg + ctx machinery as the regular run path. ChatManager
	// supplies a closure that wraps cmd/rakitsu's createLLMProvider.
	BuildLLM func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)

	// KnownProviders lists the YAML provider keys (e.g. "litellm",
	// "litellm-gemini-flash") used by the /model command for `@provider`
	// validation. Empty disables provider-name validation.
	KnownProviders []string

	// SelfURL is the base URL of the serve process owning /api/sessions/*,
	// passed through to BuildFunc so the send_message / list_sessions tools
	// can reach the messaging endpoints. "" disables those tools.
	SelfURL string

	// ResumeID, when non-empty, seeds ChatSession.ID with this exact value
	// instead of generating a fresh "chat-<uuid>". Paired with ResumeTree to
	// rehydrate a previously persisted session after a server restart.
	ResumeID string
	// ResumeTree, when non-nil, seeds the session's branching turn history
	// with this tree. Must already be hydrated (see turntree.LoadFromJSON).
	ResumeTree *turntree.Tree[transcriptEntry]
	// ResumeCreated, when non-zero, preserves the session's original
	// creation timestamp so a resumed session doesn't look freshly-started
	// to clients. Zero means time.Now().
	ResumeCreated time.Time
}

// StartChatSession builds the runner, starts the session loop, and returns
// a session that is ready to accept client connections and turns.
func StartChatSession(ctx context.Context, opts ChatSessionOptions) (*ChatSession, error) {
	if opts.Cfg == nil {
		return nil, fmt.Errorf("cfg is required")
	}
	if opts.BuildFunc == nil {
		return nil, fmt.Errorf("build func is required")
	}

	eventBus := telemetry.NewEventBus(1024)
	userReqCh := make(chan userinput.InputRequest, 1)
	userRespCh := make(chan string, 1)

	// Session id is minted BEFORE the build so the send_message tool inside
	// the runner knows its own reply address.
	sessID := "chat-" + uuid.New().String()
	if opts.ResumeID != "" {
		sessID = opts.ResumeID
	}

	// Build runner via injected callback (BuildRunner + optional ChatHost overlay).
	// innerRunner is the orchestrator behind any ChatHost overlay — needed by
	// the slash-command dispatch path so /model can reach sub-agents.
	runner, innerRunner, cleanup, agentName, modelLabel, err := opts.BuildFunc(ctx, opts.Cfg, eventBus, userReqCh, userRespCh, sessID, opts.SelfURL)
	if err != nil {
		return nil, err
	}
	tree := opts.ResumeTree
	if tree == nil {
		tree = turntree.New[transcriptEntry]()
	}
	created := time.Now()
	if !opts.ResumeCreated.IsZero() {
		created = opts.ResumeCreated
	}

	s := &ChatSession{
		ID:              sessID,
		cfg:             opts.Cfg,
		configID:        opts.ConfigID,
		workdir:         opts.Workdir,
		interactive:     opts.Cfg.Interactive,
		agentName:       agentName,
		modelLabel:      modelLabel,
		created:         created,
		eventBus:        eventBus,
		forwardBus:      opts.ForwardBus,
		sessionStore:    opts.SessionStore,
		runner:          runner,
		innerRunner:     innerRunner,
		cleanup:         cleanup,
		buildLLM:        opts.BuildLLM,
		knownProviders:  opts.KnownProviders,
		userInputReqCh:  userReqCh,
		userInputRespCh: userRespCh,
		turnCh:          make(chan chatTurn, 1),
		stopCh:          make(chan struct{}),
		clients:         make(map[*chatClient]struct{}),
		tree:            tree,
	}

	// Conversation memory (settings.memory.conversation): summarized context
	// instead of full-history re-feed. Engages only on the RunWithHistory
	// path; the rolling summary lives in the "session:<id>" scope, so a
	// resumed session (ResumeID) reloads it automatically.
	if opts.Cfg.Settings.Memory.Enabled && opts.Cfg.Settings.Memory.Conversation.Enabled {
		if _, ok := runner.(*agent.Agent); ok {
			if ms, err := memory.OpenShared(opts.Cfg.Settings.Memory.Dir); err == nil {
				s.convMem = memory.NewConversationMemory(ms, sessID, memory.ConversationOptions{
					KeepRecentTurns: opts.Cfg.Settings.Memory.Conversation.KeepRecentTurns,
					SummaryMaxChars: opts.Cfg.Settings.Memory.Conversation.SummaryMaxChars,
					DisableSummary:  opts.Cfg.Settings.Memory.Conversation.DisableSummary,
				})
			} else {
				fmt.Printf("warn: chat %s: memory store init failed: %v (conversation memory disabled)\n", sessID, err)
			}
		}
	}

	// Begin session recording (matches interactive.go). Persist ConfigPath +
	// ConfigYAML so `GET /api/sessions/<id>/config` and the rerun endpoint
	// can resurrect this chat as a one-shot (Phase 3 will add true chat
	// resume with transcript replay).
	if s.sessionStore != nil {
		agentNames := make([]string, len(opts.Cfg.Agents))
		for i, a := range opts.Cfg.Agents {
			agentNames[i] = a.Name
		}
		configYAML := ""
		if yamlBytes, err := yaml.Marshal(config.Redacted(opts.Cfg)); err == nil {
			configYAML = string(yamlBytes)
		}
		_ = s.sessionStore.StartSession(store.SessionMeta{
			Name:       opts.Cfg.Name,
			Query:      "(interactive session)",
			ConfigPath: opts.ConfigPath,
			ConfigYAML: configYAML,
			Workdir:    opts.Workdir,
			Agents:     agentNames,
		})
	}

	go s.recordLoop()
	go s.userInputBridgeLoop()
	go s.forwardLoop()
	go s.runLoop()

	return s, nil
}

// recordEntryOnActiveTurn appends a finalized response block to the active
// turn's Entries. The user message of a turn is held as Node.UserText, not
// as an Entry — it is set when executeTurn calls tree.AppendTurn. This
// method covers everything else: tool, reasoning, assistant, system, and
// user_input_request entries. Safe to call concurrently; guarded by mu.
func (s *ChatSession) recordEntryOnActiveTurn(e transcriptEntry) {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	leaf := s.tree.ActiveLeaf()
	if leaf == nil {
		// No active turn — drop. Should not happen in practice: response
		// blocks only arrive between AppendTurn and Finalize. Defensive.
		return
	}
	_ = s.tree.AppendEntry(leaf.ID, e)
}

// snapshotTranscript returns the flat transcript derived from the active
// root→leaf path. Each turn yields one synthetic "user" entry (from
// Node.UserText, timestamped at Node.Created) followed by that node's
// Entries in order. All entries are decorated with turn_id; user entries
// additionally carry branch_index/branch_count so the inline UX can show
// the `‹n/m›` chip without a separate tree query. Result is capped to the
// last transcriptCap entries for the `attached` replay frame; the
// underlying tree retains everything.
func (s *ChatSession) snapshotTranscript() []transcriptEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decorateActivePathLocked()
}

// historyFromTranscript reconstructs the prior conversation as an
// llm.Message list by walking the active root→leaf path: one User from
// each node's UserText, one Assistant from that node's final assistant
// Entry (if any). It is the source of multi-turn memory for the web chat:
// each turn passes this to the runner so the agent remembers the
// conversation. Reasoning, tool, and system entries are intentionally
// omitted; user/assistant pairs are the portable minimum (matching the
// terminal TUI's history handling).
func (s *ChatSession) historyFromTranscript() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return historyFromTree(s.tree)
}

// composeHistoryQuery prepends prior user/assistant turns to a new query as
// plain text, for orchestrator runners that have no structured-history entry
// point. Returns query unchanged when history is empty. Long assistant
// answers are truncated to keep the prefix bounded.
func composeHistoryQuery(history []llm.Message, query string) string {
	if len(history) == 0 {
		return query
	}
	var sb strings.Builder
	sb.WriteString("Previous conversation:\n")
	for _, m := range history {
		switch m.Role {
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.AsText())
			sb.WriteString("\n")
		case "assistant":
			c := m.AsText()
			if len(c) > 2000 {
				c = c[:2000] + "...[truncated]"
			}
			sb.WriteString("Assistant: ")
			sb.WriteString(c)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nCurrent question:\n")
	sb.WriteString(query)
	return sb.String()
}

// chatDepth maps an emitting agent name to a nesting indent level. Runtime-
// spawned agents (recorded in spawnParents, keyed by AGENT_START's parent
// link) get their true chain depth; everything else falls back to the
// original binary indent: 0 for the session's bound agent, 1 for any other
// name. Mirrors the TUI's Model.agentDepth.
func (s *ChatSession) chatDepth(agentName string, spawnParents map[string]string) int {
	if agentName == "" || agentName == s.agentName {
		return 0
	}
	if _, spawned := spawnParents[agentName]; spawned {
		depth := 0
		seen := map[string]bool{}
		for cur := agentName; ; {
			parent, ok := spawnParents[cur]
			if !ok || seen[cur] {
				break
			}
			seen[cur] = true
			depth++
			if parent == "" || parent == s.agentName {
				break
			}
			cur = parent
		}
		return depth
	}
	return 1
}

// forwardLoop mirrors every event on the private bus onto the hub bus with
// the session id tagged, so the debugger SSE stream can observe this chat
// session (for surfacing user_input waits, token counts, tool calls, etc.).
func (s *ChatSession) forwardLoop() {
	if s.forwardBus == nil {
		return
	}
	ch := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(ch)
	for {
		select {
		case <-s.stopCh:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			ev.SessionID = s.ID
			s.forwardBus.Publish(ev)
		}
	}
}

// recordLoop subscribes to the event bus and persists events. Matches the
// CLI interactive recorder.
func (s *ChatSession) recordLoop() {
	if s.sessionStore == nil {
		return
	}
	ch := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(ch)
	for {
		select {
		case <-s.stopCh:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.sessionStore.WriteEvent(ev)
		}
	}
}

// userInputBridgeLoop forwards user_input tool requests to all connected
// clients. The first client to respond unblocks the tool.
func (s *ChatSession) userInputBridgeLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		case req, ok := <-s.userInputReqCh:
			if !ok {
				return
			}
			reqID := uuid.New().String()
			s.mu.Lock()
			s.pendingInputID = reqID
			s.mu.Unlock()
			s.recordEntryOnActiveTurn(transcriptEntry{
				Kind:      "user_input_request",
				RequestID: reqID,
				Question:  req.Question,
				Default:   req.Default,
			})
			s.broadcast(serverMsg{
				Type:      "user_input_request",
				RequestID: reqID,
				Question:  req.Question,
				Default:   req.Default,
			})
		}
	}
}

// runLoop processes turns serially. Interruption is handled by cancelling the
// current gen's context before submitting the next turn.
func (s *ChatSession) runLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		case turn, ok := <-s.turnCh:
			if !ok {
				return
			}
			s.executeTurn(turn)
		}
	}
}

// executeTurn runs a single user turn end-to-end. It blocks until the Runner
// returns (successfully, via cancellation, or with an error).
//
// skipAppend (PR D-2 edit_turn / regenerate path): when true, the active
// leaf has already been mutated to be the new turn (sibling created by
// EditTurn / RegenerateTurn). The executor must NOT append another child
// turn; it just fills the existing leaf's Entries. The active leaf's
// UserText IS this turn's input text, so it is trimmed from priorHistory
// to avoid the agent seeing its own current input as a prior user message.
func (s *ChatSession) executeTurn(turn chatTurn) {
	text, skipAppend := turn.text, turn.skipAppend
	tok := s.genToken.Add(1)
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.cancelGen = cancel
	s.generating = true
	s.mu.Unlock()

	// Multi-turn memory: reconstruct the prior conversation from the active
	// root→leaf path. In the append path this runs BEFORE the new node is
	// appended, so the runner sees the history without the new turn yet.
	// In the skipAppend path the new node is already on the active path
	// (created by EditTurn / RegenerateTurn) — its UserText is THIS turn's
	// input, not prior context, so trim the trailing user message.
	priorHistory := s.historyFromTranscript()
	if skipAppend && len(priorHistory) > 0 && priorHistory[len(priorHistory)-1].Role == "user" {
		priorHistory = priorHistory[:len(priorHistory)-1]
	}
	// Conversation memory: compress prior history to rolling summary +
	// recent verbatim turns. Falls back to priorHistory unchanged until
	// turns have been folded into the summary.
	if s.convMem != nil {
		if _, ok := s.runner.(*agent.Agent); ok {
			priorHistory = s.convMem.ComposeHistory(priorHistory)
		}
	}

	// Append a new turn node to the tree as a child of the current active
	// leaf. The user message lives as Node.UserText; this turn's response
	// blocks will be recorded into Node.Entries by recordEntryOnActiveTurn.
	//
	// skipAppend bypasses this: the active leaf already has the right
	// UserText and an empty Entries slice, waiting to be filled.
	turnID := uuid.New().String()
	if !skipAppend {
		s.mu.Lock()
		parentID := ""
		if leaf := s.tree.ActiveLeaf(); leaf != nil {
			parentID = leaf.ID
		}
		_, _ = s.tree.AppendTurn(turnID, text, parentID)
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		if leaf := s.tree.ActiveLeaf(); leaf != nil {
			turnID = leaf.ID
		}
		s.mu.Unlock()
	}

	// Cross-session message: announce the sender in the transcript, then
	// resync attached clients — unlike a WS-submitted turn, no client pushed
	// this user block optimistically, so without the resync a live browser
	// tab would never see the injected text.
	if turn.fromSessionID != "" {
		s.recordEntryOnActiveTurn(transcriptEntry{
			Kind: "system",
			Text: chat.SessionMsgNote(turn.fromSessionID, turn.fromName),
		})
		s.broadcast(serverMsg{Type: "transcript_resync", Transcript: s.snapshotTranscript()})
	}

	s.broadcast(serverMsg{Type: "turn_started", GenToken: tok, TurnID: turnID})

	// Emit a chat-turn event so the debug tree can group this turn's events
	// under a single container node instead of a new root-level agent call
	// per turn. Uses the ChatSession.ID as AgentName so the event carries
	// the conversation identity even after the event leaves the bus.
	s.eventBus.Emit(s.ID, telemetry.EventChatTurnStart, telemetry.ChatTurnStartPayload{
		Turn: int(tok),
		Text: text,
	})

	// Per-turn forwarder — torn down before the next turn so stale chunks
	// don't leak across turns. Handles three streamed signals:
	//   - TOKEN_CHUNK     → "token" message (the visible answer)
	//   - REASONING_CHUNK → "reasoning" message (reasoning-model CoT)
	//   - TOOL_CALL_*     → "tool_start" / "tool_end" messages
	// Tool and reasoning blocks are also recorded into the transcript so a
	// reconnecting client (browser reload) rebuilds them. Reasoning is
	// buffered per segment and flushed on each tool-call start (a tool call
	// seals the open segment) and once more at turn end.
	chunkSub := s.eventBus.Subscribe()
	chunkDone := make(chan struct{})
	var reasoningBuf strings.Builder
	var reasoningAgent string
	toolStarts := make(map[string]transcriptEntry)
	// spawnParents maps a runtime-spawned child's instance name to its
	// spawning agent's name, recorded from AGENT_START's parent link. Scoped
	// to this turn — spawned children only exist within the turn that calls
	// spawn_agent.
	spawnParents := make(map[string]string)

	flushReasoning := func() {
		txt := reasoningBuf.String()
		reasoningBuf.Reset()
		if strings.TrimSpace(txt) == "" {
			reasoningAgent = ""
			return
		}
		s.recordEntryOnActiveTurn(transcriptEntry{
			Kind:      "reasoning",
			Text:      txt,
			AgentName: reasoningAgent,
			Depth:     s.chatDepth(reasoningAgent, spawnParents),
		})
		reasoningAgent = ""
	}

	handleChunkEvent := func(ev telemetry.AgentEvent) {
		switch ev.EventType {
		case telemetry.EventAgentStart:
			var p telemetry.AgentStartPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.ParentAgent == "" {
				// The root agent's own AGENT_START fires every turn — pure
				// noise in chat (the debugger already shows it). Only
				// runtime-spawned children (ParentAgent set) render.
				return
			}
			spawnParents[ev.AgentName] = p.ParentAgent
			s.broadcast(serverMsg{
				Type:        "agent_start",
				AgentName:   ev.AgentName,
				ParentAgent: p.ParentAgent,
				Model:       p.Model,
				Depth:       s.chatDepth(ev.AgentName, spawnParents),
				GenToken:    tok,
			})
		case telemetry.EventAgentEnd:
			if _, spawned := spawnParents[ev.AgentName]; !spawned {
				return
			}
			var p telemetry.AgentEndPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return
			}
			s.recordEntryOnActiveTurn(transcriptEntry{
				Kind:      "subagent",
				AgentName: ev.AgentName,
				Status:    p.Status,
				Tokens:    p.TotalTokens,
				Duration:  ev.Duration,
				Depth:     s.chatDepth(ev.AgentName, spawnParents),
			})
			s.broadcast(serverMsg{
				Type:      "agent_end",
				AgentName: ev.AgentName,
				Status:    p.Status,
				Tokens:    p.TotalTokens,
				Duration:  ev.Duration,
				GenToken:  tok,
			})
		case telemetry.EventTokenChunk:
			var p telemetry.TokenChunkPayload
			if json.Unmarshal(ev.Payload, &p) == nil {
				s.broadcast(serverMsg{Type: "token", Text: p.Text, GenToken: tok})
			}
		case telemetry.EventReasoningChunk:
			var p telemetry.ReasoningChunkPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return
			}
			reasoningBuf.WriteString(p.Text)
			if reasoningAgent == "" {
				reasoningAgent = p.AgentName
			}
			s.broadcast(serverMsg{
				Type:      "reasoning",
				Text:      p.Text,
				AgentName: p.AgentName,
				Depth:     s.chatDepth(p.AgentName, spawnParents),
				GenToken:  tok,
			})
		case telemetry.EventToolCallStart:
			var p telemetry.ToolCallStartPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return
			}
			// A tool call seals the current reasoning segment.
			flushReasoning()
			argsJSON, _ := json.Marshal(p.Arguments)
			depth := s.chatDepth(ev.AgentName, spawnParents)
			toolStarts[p.ToolCallID] = transcriptEntry{
				Kind:       "tool",
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolArgs:   string(argsJSON),
				AgentName:  ev.AgentName,
				Depth:      depth,
			}
			s.broadcast(serverMsg{
				Type:       "tool_start",
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolArgs:   string(argsJSON),
				AgentName:  ev.AgentName,
				Depth:      depth,
				GenToken:   tok,
			})
		case telemetry.EventToolCallEnd:
			var p telemetry.ToolCallEndPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				return
			}
			te, ok := toolStarts[p.ToolCallID]
			if !ok {
				te = transcriptEntry{Kind: "tool", ToolCallID: p.ToolCallID, ToolName: p.ToolName}
			}
			delete(toolStarts, p.ToolCallID)
			te.Output = p.Output
			te.ToolErr = p.Error
			te.Duration = ev.Duration
			s.recordEntryOnActiveTurn(te)
			s.broadcast(serverMsg{
				Type:       "tool_end",
				ToolCallID: p.ToolCallID,
				Output:     p.Output,
				Err:        p.Error,
				Duration:   ev.Duration,
				GenToken:   tok,
			})
		}
	}

	go func() {
		defer close(chunkDone)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-chunkSub:
				if !ok {
					return
				}
				handleChunkEvent(ev)
			}
		}
	}()

	// LLM-facing text: cross-session messages are wrapped in the
	// <session_message> fence for this one call only — `text` (clean) is what
	// went into the tree, so the fence never re-enters persisted history.
	llmText := text
	if turn.fromSessionID != "" {
		llmText = chat.WrapSessionMessage(text, turn.fromSessionID, turn.fromName)
	}

	var result string
	var runErr error
	if ag, ok := s.runner.(*agent.Agent); ok {
		// Single-agent runner (including the ChatHost overlay): thread
		// structured conversation history so the agent has multi-turn memory.
		result, runErr = ag.RunWithHistory(ctx, llmText, priorHistory)
	} else {
		// Orchestrator runner: it has no RunWithHistory, so prior turns are
		// prepended to the query as text (same approach as the terminal TUI).
		result, runErr = s.runner.Run(ctx, composeHistoryQuery(priorHistory, llmText))
	}
	cancel()
	// Drain any remaining token chunks *before* unsubscribing, so the final
	// visible text on clients matches the authoritative Final we're about to
	// send. Without this, a late chunk could arrive after turn_done on slow
	// clients and visually append past the final string.
	drainDeadline := time.NewTimer(50 * time.Millisecond)
draining:
	for {
		select {
		case ev, ok := <-chunkSub:
			if !ok {
				break draining
			}
			if ev.EventType == telemetry.EventTokenChunk {
				var p telemetry.TokenChunkPayload
				if err := json.Unmarshal(ev.Payload, &p); err == nil {
					s.broadcast(serverMsg{Type: "token", Text: p.Text, GenToken: tok})
				}
			}
		case <-drainDeadline.C:
			break draining
		}
	}
	drainDeadline.Stop()
	s.eventBus.Unsubscribe(chunkSub)
	<-chunkDone

	// Flush any reasoning that arrived after the last tool call so the final
	// reasoning segment is persisted in the transcript ahead of the answer.
	flushReasoning()

	s.mu.Lock()
	s.cancelGen = nil
	s.generating = false
	s.mu.Unlock()

	endPayload := telemetry.ChatTurnEndPayload{
		Turn:  int(tok),
		Final: result,
	}
	if runErr != nil {
		endPayload.Interrupted = true
		endPayload.Err = runErr.Error()
	}
	s.eventBus.Emit(s.ID, telemetry.EventChatTurnEnd, endPayload)

	msg := serverMsg{Type: "turn_done", GenToken: tok, Final: result}
	if runErr != nil {
		msg.Err = runErr.Error()
	}
	s.recordEntryOnActiveTurn(transcriptEntry{
		Kind:        "assistant",
		Text:        result,
		Interrupted: runErr != nil,
		Err:         msg.Err,
	})
	// Finalize the turn node so the tree reflects its terminal state.
	// Any error (cancel, timeout, agent failure) maps to Interrupted —
	// matches the existing `Interrupted` bool on the assistant entry. A
	// future PR can split Interrupted vs Failed if useful.
	status := turntree.StatusComplete
	if runErr != nil {
		status = turntree.StatusInterrupted
	}
	s.mu.Lock()
	_ = s.tree.Finalize(turnID, status)
	s.mu.Unlock()
	if turn.done != nil {
		select {
		case turn.done <- turnOutcome{TurnID: turnID, Final: result, Interrupted: runErr != nil, Err: msg.Err}:
		default: // waiter already gave up (ctx timeout) — buffered chan makes this unreachable in practice
		}
	}
	s.broadcast(msg)
	// Write-through persistence: every finalized turn (success or error)
	// makes the session resumable after a server restart. Branch ops will
	// also persist once PR C lands.
	s.persistTree()

	// Conversation memory: fold turns that left the verbatim window into the
	// rolling summary. Async — summarizer latency must not delay turn_done;
	// on failure nothing is folded and the next turn degrades to a fuller
	// history (never data loss). Interrupted turns are skipped; they fold
	// after the next completed turn.
	if s.convMem != nil && runErr == nil {
		if ag, ok := s.runner.(*agent.Agent); ok {
			s.startBackgroundSummarize(ag, s.historyFromTranscript())
		}
	}
}

// startBackgroundSummarize folds turns that left the verbatim window into
// the rolling summary, off the turn-completion path so summarizer latency
// never delays turn_done. On failure nothing is folded and the next turn
// degrades to a fuller history (never data loss).
//
// Tied to s.stopCh, not just its own 60s timeout: without this, Close()
// running shortly after a turn finishes left this goroutine running for up
// to a minute past session teardown, still holding the agent and making a
// real LLM call.
func (s *ChatSession) startBackgroundSummarize(ag *agent.Agent, full []llm.Message) {
	convMem := s.convMem
	go func() {
		sumCtx, sumCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer sumCancel()
		go func() {
			select {
			case <-s.stopCh:
				sumCancel()
			case <-sumCtx.Done():
			}
		}()
		// Re-read the provider per call so /model swaps are honored.
		_ = convMem.Update(sumCtx, full, func(c context.Context, prompt string) (string, error) {
			return memory.ProviderSummarize(c, ag.LLMProvider(), prompt)
		})
	}()
}

// Submit queues a new turn. If a turn is currently executing, it is cancelled
// first (fluid chat). Returns an error if the session is closed.
func (s *ChatSession) Submit(text string) error {
	if s.closed.Load() {
		return fmt.Errorf("session closed")
	}
	s.Interrupt()
	// Wait briefly for the prior turn's runLoop iteration to accept and
	// finish the cancellation before enqueuing. The 1-slot turnCh plus
	// serial runLoop means the new turn runs after the prior Run returns.
	//
	// Selected against stopCh, not an unconditional send: if turnCh's one
	// slot is already full (a second concurrent Submit racing an in-flight
	// turn) and Close() runs before runLoop's own select happens to drain
	// it, nothing will ever receive from turnCh again — an unconditional
	// send here would block this caller's goroutine forever.
	select {
	case <-s.stopCh:
		return fmt.Errorf("session closed")
	case s.turnCh <- chatTurn{text: text}:
	}
	return nil
}

// ErrSessionBusy is returned by SubmitExternal when the session's single-slot
// turn queue is already full. The caller surfaces this as a delivery failure
// instead of blocking or interrupting the session's current work.
var ErrSessionBusy = errors.New("session busy: a turn is already queued")

// submitExternalEnqueue is the shared queue-accept logic behind SubmitExternal
// and SubmitExternalWait: closed/opt-in checks, then a non-blocking send on
// turnCh carrying done (nil for fire-and-forget). Never calls Interrupt() —
// an injected message must not cancel the target's in-flight turn.
func (s *ChatSession) submitExternalEnqueue(text, fromSessionID, fromName string, done chan turnOutcome) error {
	if s.closed.Load() {
		return fmt.Errorf("session closed")
	}
	if !s.cfg.Settings.SessionMsg.Enabled {
		return fmt.Errorf("session does not accept messages (settings.session_msg.enabled is false)")
	}
	select {
	case s.turnCh <- chatTurn{text: text, fromSessionID: fromSessionID, fromName: fromName, done: done}:
		s.eventBus.Emit(s.ID, telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
			FromSessionID: fromSessionID,
			FromName:      fromName,
			Text:          chat.TruncateSessionMsg(text),
		})
		return nil
	default:
		return ErrSessionBusy
	}
}

// SubmitExternal queues text as a turn attributed to another live session.
// Never blocks: if turnCh's one slot is taken it returns ErrSessionBusy
// immediately. Fire-and-forget: success means "queued", not "the agent has
// replied". Rejected outright unless this session's config opted in via
// settings.session_msg.enabled.
func (s *ChatSession) SubmitExternal(text, fromSessionID, fromName string) error {
	return s.submitExternalEnqueue(text, fromSessionID, fromName, nil)
}

// SubmitExternalWait is SubmitExternal's synchronous counterpart: it queues
// the turn the same way, then blocks until executeTurn reports the outcome
// or ctx expires — whichever comes first. A ctx timeout does NOT cancel the
// queued/running turn (matches SubmitExternal's non-cancelling contract); it
// keeps running in the background and its result is still persisted, just
// not delivered to this caller.
func (s *ChatSession) SubmitExternalWait(ctx context.Context, text, fromSessionID, fromName string) (turnOutcome, error) {
	done := make(chan turnOutcome, 1)
	if err := s.submitExternalEnqueue(text, fromSessionID, fromName, done); err != nil {
		return turnOutcome{}, err
	}
	select {
	case out := <-done:
		return out, nil
	case <-ctx.Done():
		return turnOutcome{}, ctx.Err()
	}
}

// HandleSlashCommand parses a chat input that starts with "/" and dispatches
// the corresponding command. Returns (replyText, handled): when handled is
// true the caller should NOT also forward the text to Submit — the command
// has been processed entirely server-side and the caller should send the
// replyText back to the client as a system message.
//
// Returns ("", false) for inputs that don't start with "/" (so callers can
// uniformly call this and fall through to Submit). Returns
// ("Unknown command: /X. Type /help for available commands.", true) for
// unknown commands — keeps parity with the TUI's default branch.
//
// Supported commands: /model and its variants (see chat.HandleModelCommand).
// The other TUI commands (/help, /clear, /usage, /context, /copy, /retry,
// /exit) are inherently UI-state operations and have no meaningful
// server-side dispatch — the web ChatPanel handles them client-side, the
// same way the TUI does.
func (s *ChatSession) HandleSlashCommand(text string) (reply string, handled bool) {
	cmd := chat.ParseSlashCommand(text)
	if cmd == nil {
		return "", false
	}
	switch cmd.Name {
	case "model":
		ctx := chat.SlashCommandContext{
			Runner:         s.runner,
			InnerRunner:    s.innerRunner,
			BuildLLM:       s.buildLLM,
			KnownProviders: s.knownProviders,
		}
		// SingleAgent: the wrapping ChatHost overlay is *Agent and is also
		// addressable by name. If runner is an *Agent (overlay mode or
		// single-agent mode), expose it as SingleAgent so /model <its-name>
		// works.
		if a, ok := s.runner.(*agent.Agent); ok {
			ctx.SingleAgent = a
		}
		return chat.HandleModelCommand(ctx, cmd.Args), true
	default:
		return fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", cmd.Name), true
	}
}

// ActiveLeafUserText returns the UserText of the current active leaf, or
// "" if the tree is empty. Used by the WS dispatcher after RegenerateTurn:
// the regenerate WS message carries only the turn id, but SubmitOnActiveLeaf
// needs the text to run the agent with — read it off the freshly-active
// sibling that RegenerateTurn created.
func (s *ChatSession) ActiveLeafUserText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf := s.tree.ActiveLeaf(); leaf != nil {
		return leaf.UserText
	}
	return ""
}

// SubmitOnActiveLeaf queues an agent run that fills the active leaf's
// Entries WITHOUT appending a new turn node. Used by the WS dispatcher
// after EditTurn / RegenerateTurn (PR D-2): those branch ops create the
// new sibling and switch the active path; this method then drives the
// agent to actually respond on that leaf.
//
// Caller is responsible for ensuring the active leaf is the intended
// target — the tree mutation that made it active should have just
// completed (under s.mu) before this call.
func (s *ChatSession) SubmitOnActiveLeaf(text string) error {
	if s.closed.Load() {
		return fmt.Errorf("session closed")
	}
	s.Interrupt()
	// Same reasoning as Submit's select against stopCh — an unconditional
	// send here would block this caller forever if Close() races a full
	// turnCh before runLoop's select drains it.
	select {
	case <-s.stopCh:
		return fmt.Errorf("session closed")
	case s.turnCh <- chatTurn{text: text, skipAppend: true}:
	}
	return nil
}

// Interrupt cancels the current generation if any. Idempotent.
func (s *ChatSession) Interrupt() {
	s.mu.Lock()
	c := s.cancelGen
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// RespondUserInput forwards an answer to a pending user_input tool call.
// Non-blocking: if no tool is waiting, drops the response.
func (s *ChatSession) RespondUserInput(reqID, text string) {
	s.mu.Lock()
	expected := s.pendingInputID
	if reqID == expected {
		s.pendingInputID = ""
	}
	s.mu.Unlock()
	if reqID != "" && reqID != expected {
		// Stale response for a request that was already answered.
		return
	}
	select {
	case s.userInputRespCh <- text:
	default:
		// No tool blocking — ignore.
	}
}

// Close stops the session and releases resources. Idempotent.
func (s *ChatSession) Close() {
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		s.Interrupt()
		close(s.stopCh)
		s.broadcast(serverMsg{Type: "closed"})
		// Disconnect all clients.
		s.mu.Lock()
		for c := range s.clients {
			c.close()
		}
		s.clients = map[*chatClient]struct{}{}
		s.mu.Unlock()
		if s.cleanup != nil {
			s.cleanup()
		}
		if s.sessionStore != nil {
			s.sessionStore.EndSession(store.SessionSuccess)
		}
	})
}

// SetDebugController wires a debug controller into this chat session's
// top-level Runner. Pass nil to detach. Called from chatSessionAdapter as the
// Phase-2 per-session attach path.
func (s *ChatSession) SetDebugController(dc *debug.DebugController) {
	if s.runner != nil {
		s.runner.SetDebugController(dc)
	}
}

// Meta returns a JSON-serializable snapshot of the session.
func (s *ChatSession) Meta() map[string]interface{} {
	s.mu.Lock()
	generating := s.generating
	s.mu.Unlock()
	return map[string]interface{}{
		"id":          s.ID,
		"config_id":   s.configID,
		"name":        s.cfg.Name,
		"agent_name":  s.agentName,
		"model":       s.modelLabel,
		"interactive": s.interactive,
		"created":     s.created.Format(time.RFC3339),
		"generating":  generating,
	}
}

// attach registers a new client for broadcasts. The returned detach func must
// be called when the client disconnects.
func (s *ChatSession) attach(c *chatClient) (detach func()) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}
}

// broadcast fans out a message to all attached clients, non-blockingly.
// A slow client drops messages rather than blocking the session.
func (s *ChatSession) broadcast(msg serverMsg) {
	s.mu.Lock()
	clients := make([]*chatClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.send(msg)
	}
}
