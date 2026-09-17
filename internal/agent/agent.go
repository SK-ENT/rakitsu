// Package agent provides the agent execution engine with ReAct loop.
package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// AgentRole defines the role of an agent
type AgentRole string

const (
	RoleWorker     AgentRole = "worker"
	RoleSupervisor AgentRole = "supervisor"
)

const defaultReflectionPrompt = `Review your last action and its result:
1. Did the tool output match your expectations?
2. Is your reasoning still sound given this new information?
3. What should you do next and why?
Be concise.`

const defaultGroundCheckPrompt = `Validate your reasoning and proposed answer:
1. Is it consistent with all tool outputs received?
2. Are there contradictions in your reasoning chain?
3. Did you address the original query completely?
4. Rate your confidence from 0.0 to 1.0.

Respond in this exact format:
CONFIDENCE: <number>
VALID: <true/false>
ISSUES: <comma-separated list or "none">
ASSESSMENT: <brief explanation>`

// Agent represents an AI agent that can execute tasks
type Agent struct {
	name         string
	parentAgent  string // non-empty only for runtime-spawned children (spawn_agent)
	role         AgentRole
	systemPrompt string
	tools        []string
	skills       []string

	// llmMu guards llmProvider, providerName, and model — they are read on
	// every LLM call and written by SetLLMProvider, which is called from a
	// different goroutine (the chat input loop) when the user issues
	// `/model <agent> <new-model>`. All access must go through
	// LLMProvider() / Model() / ProviderName() getters.
	llmMu        sync.RWMutex
	llmProvider  llm.LLMProvider
	providerName string // e.g. "litellm-gemini-flash"; informational only
	model        string

	toolRegistry *tools.ToolRegistry
	eventBus     *telemetry.EventBus
	debugCtrl    *debug.DebugController
	// steering, when non-nil, is drained at the top of every ReAct
	// iteration; returned messages (already fenced by the caller — see
	// chat.WrapSessionMessage) are appended to history. Set only on the
	// top-level runner of a steerable one-shot run.
	steering         func() []llm.Message
	maxIterations    int
	reflection       config.ReflectionConfig
	groundCheck      config.GroundCheckConfig
	contextMon       *ContextMonitor
	retriever        ContextRetriever // per-session BM25/Ollama retrieval (nil = disabled)
	noStreamTools    bool             // disable streaming when tools are present (vLLM/Qwen3 workaround)
	thinkingOffload  bool             // strip thinking from history and store externally
	thinkingStore    *ThinkingStore
	maxContextTokens int // model context window size for pressure calculation; 0 = unknown
	guard            *CompositeGuard
	tokenGuard       *TokenGuard
	pricing          config.PricingConfig
	pricingKnown     bool
	retryConfig      RetryConfig
	rateLimiter      *RateLimiter
	cachedToolDefs   []llm.ToolDefinition
	toolDefsOnce     sync.Once

	// Per-Run salvage state, observable by an outer orchestrator so it can
	// apply a post-salvage re-delegation cap. Set when the agent terminates
	// with status="salvaged_no_progress" (iterations exhausted while
	// Result.Salvaged still true). Reset at the top of each Run. Read via
	// LastRunSalvaged().
	lastRunSalvaged atomic.Bool

	// Per-Run unproductive state — a strict superset of salvaged. Set when
	// the agent terminates without producing a usable answer:
	// salvaged_no_progress, max_iterations+empty lastResponse,
	// truncated_empty (success+empty finalAnswer with finish_reason=length,
	// i.e. reasoning-budget exhaustion), success+empty finalAnswer for any
	// other finish_reason, or iteration-level budget_exceeded+empty
	// lastResponse. "Empty" uses strings.TrimSpace(...) == "" throughout —
	// see LastRunUnproductive docstring for the rationale. Reset at the top
	// of each Run. Read via LastRunUnproductive().
	lastRunUnproductive atomic.Bool

	// toolCallsByRun records tool calls made during a Run, keyed by the
	// run-scoped ID a caller attaches to ctx via NewToolCallRunContext —
	// NOT a single shared "last run" field. *Agent instances are not safe
	// for concurrent Run() calls in general (see pipeline.go's
	// duplicateAgentInGroup error), but a worker can still be reached
	// concurrently through independent delegation paths (the same failure
	// mode orchestratorRunState exists to fix for *Orchestrator, above) —
	// a single shared slice reset at Run start would let one Run's reset
	// or recorded calls corrupt another concurrent Run's view. Keying by a
	// caller-supplied run ID makes each caller's view isolated regardless.
	// Only populated when a caller opts in via NewToolCallRunContext (most
	// Run() callers never touch this ctx key, so no calls are recorded and
	// this map stays empty for them — no unbounded growth). Read via
	// ToolCallsForRun(id), which also deletes the entry once read.
	toolCallsByRunMu sync.Mutex
	toolCallsByRun   map[uint64][]llm.ToolCall

	// rollback holds the resolved per-agent runtime self-correction config.
	// When Enabled, a dead-end iteration is rewound out of history mid-Run.
	rollback config.RollbackConfig
	// configReloadPath, when set via SetConfigReloadPath, is the YAML file the
	// agent re-reads its RollbackConfig from at the start of every Run, so a
	// config edit takes effect on the next turn without a restart.
	configReloadPath string
	// turnGuard holds the optional interim post-turn truthfulness checks
	// (no-delegation / fabricated-store). Nil = disabled, zero overhead.
	// Set via SetTurnGuard. See turnguard.go.
	turnGuard *TurnGuard
	// memoryRecaller, when set, fetches the top-k native-memory entries
	// relevant to the Run's query and injects them (fenced) into the LLM
	// context at Run start (Phase-3 auto-recall). Nil = disabled. Set via
	// SetMemoryRecaller.
	memoryRecaller MemoryRecaller
	// transcriptSink, when set, receives the agent's full message history
	// and the run's own error
	// when a run finishes. Directed agent chat uses it to retain a
	// subagent's context after its tool registry is closed — the agent is
	// destroyed on schedule, the conversation is not. Nil = no capture,
	// zero overhead. Set via SetTranscriptSink.
	transcriptSink func(history []llm.Message, runErr error)
}

// MemoryRecaller fetches a formatted, ranked index of the memories most
// relevant to a query, for Run-start auto-recall injection. The concrete
// implementation lives in internal/tools/memory (Recaller) and satisfies this
// interface structurally, so internal/agent need not import it — same pattern
// as ContextRetriever. block is the unfenced index text; ids are the matched
// node IDs. ("", nil) means nothing to inject.
type MemoryRecaller interface {
	Recall(query string) (block string, ids []string)
}

// NewAgent creates a new agent from configuration.
// ep is an optional EmbeddingProvider for context retrieval; nil uses BM25.
func NewAgent(
	def *config.AgentDefinition,
	llmProvider llm.LLMProvider,
	toolRegistry *tools.ToolRegistry,
	eventBus *telemetry.EventBus,
	ep llm.EmbeddingProvider,
) *Agent {
	maxIter := 10
	var reflection config.ReflectionConfig
	var groundCheck config.GroundCheckConfig

	if def.Settings != nil {
		if def.Settings.MaxIterations > 0 {
			maxIter = def.Settings.MaxIterations
		}
		reflection = def.Settings.Reflection
		groundCheck = def.Settings.GroundCheck
	}

	// Apply defaults
	if reflection.Mode == "" {
		reflection.Mode = "after_tool"
	}
	if reflection.Frequency == "" {
		reflection.Frequency = "always"
	}
	if groundCheck.ConfidenceThreshold == 0 {
		groundCheck.ConfidenceThreshold = 0.7
	}
	if groundCheck.MaxRetries == 0 {
		groundCheck.MaxRetries = 1
	}

	// Runtime self-correction (RollbackConfig) — resolve per-agent settings.
	var rollback config.RollbackConfig
	if def.Settings != nil {
		rollback = def.Settings.Rollback
	}
	if rollback.MaxRollbacks <= 0 {
		rollback.MaxRollbacks = 3
	}
	if len(rollback.Triggers) == 0 {
		rollback.Triggers = []string{"tool_error"}
	}

	// Create context monitor from settings (zero-value → "full" strategy, no overhead)
	var ctxConfig config.ContextConfig
	if def.Settings != nil {
		ctxConfig = def.Settings.Context
	}
	contextMon := NewContextMonitor(ctxConfig)

	// Wire per-session retriever if retrieval is enabled
	var retriever ContextRetriever
	if ctxConfig.Retrieval.Enabled {
		retriever = NewRetriever(ctxConfig.Retrieval, ep)
		contextMon.SetRetriever(retriever, ctxConfig.Retrieval.TopK)
	}

	// Check for model_config flags
	noStreamTools := false
	thinkingOffload := false
	maxCtxTokens := 0
	if def.ModelConfig != nil {
		noStreamTools = def.ModelConfig.NoStreamTools
		thinkingOffload = def.ModelConfig.ThinkingOffload
		maxCtxTokens = def.ModelConfig.MaxTokens
	}

	return &Agent{
		name:             def.Name,
		role:             AgentRole(def.Role),
		model:            def.Model,
		providerName:     def.Provider, // YAML provider key; "" means default-provider resolved at LLM build time
		systemPrompt:     def.SystemPrompt,
		tools:            def.Tools,
		skills:           def.Skills,
		llmProvider:      llmProvider,
		toolRegistry:     toolRegistry,
		eventBus:         eventBus,
		maxIterations:    maxIter,
		reflection:       reflection,
		groundCheck:      groundCheck,
		contextMon:       contextMon,
		retriever:        retriever,
		noStreamTools:    noStreamTools,
		thinkingOffload:  thinkingOffload,
		maxContextTokens: maxCtxTokens,
		rollback:         rollback,
	}
}

// LLMProvider returns the agent's current LLM provider. Thread-safe — callers
// MUST go through this getter rather than reading the private field directly,
// because SetLLMProvider can swap the provider at any time (e.g. from a
// `/model` slash command in the chat).
func (a *Agent) LLMProvider() llm.LLMProvider {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.llmProvider
}

// Model returns the agent's current model name. Thread-safe.
func (a *Agent) Model() string {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.model
}

// ProviderName returns the agent's current provider name (the YAML key under
// settings.providers, e.g. "litellm-gemini-flash"). Empty when the agent was
// constructed without provider-name plumbing. Informational only — used by
// /model status output. Thread-safe.
func (a *Agent) ProviderName() string {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.providerName
}

// SetLLMProvider swaps the agent's LLM provider + model name at runtime. The
// new provider applies to subsequent LLMProvider() reads — an in-flight LLM
// call that has already captured the previous pointer keeps using it (no
// mid-RPC swap). Used by the `/model` slash command for mid-session model
// switching.
//
// providerName is the YAML key from settings.providers (e.g. "litellm-gemini-flash");
// it is stored alongside the provider for /model status output and emitted in
// the MODEL_CHANGED telemetry event. Pass "" if unknown.
//
// Emits a MODEL_CHANGED telemetry event with the before/after model and
// provider names so session JSONLs retain a clean swap audit trail.
func (a *Agent) SetLLMProvider(p llm.LLMProvider, providerName, model string) {
	a.llmMu.Lock()
	oldModel := a.model
	oldProvider := a.providerName
	a.llmProvider = p
	a.providerName = providerName
	a.model = model
	a.llmMu.Unlock()

	if a.eventBus != nil {
		a.eventBus.Emit(a.name, telemetry.EventModelChanged, telemetry.ModelChangedPayload{
			AgentName:   a.name,
			OldModel:    oldModel,
			OldProvider: oldProvider,
			NewModel:    model,
			NewProvider: providerName,
		})
	}
}

// SetThinkingStore attaches a ThinkingStore for persisting thinking content to disk.
func (a *Agent) SetThinkingStore(ts *ThinkingStore) {
	a.thinkingStore = ts
}

// SetGuard attaches a composite guard for budget enforcement.
// SetParent marks this agent as runtime-spawned by parent, so AGENT_START /
// AGENT_END carry an explicit parent link (AgentEvent.ParentID + the
// AgentStartPayload.ParentAgent field) instead of relying on event order.
func (a *Agent) SetParent(parent string) { a.parentAgent = parent }

// emitLifecycle routes AGENT_START / AGENT_END through EmitWithParent for
// spawned agents and plain Emit otherwise.
func (a *Agent) emitLifecycle(evType telemetry.EventType, payload interface{}) {
	if a.parentAgent != "" {
		a.eventBus.EmitWithParent(a.name, evType, a.parentAgent, payload)
		return
	}
	a.eventBus.Emit(a.name, evType, payload)
}

func (a *Agent) SetGuard(guard *CompositeGuard) {
	a.guard = guard
}

// SetTokenGuard attaches a token guard and registers it with the composite guard.
func (a *Agent) SetTokenGuard(tg *TokenGuard) {
	a.tokenGuard = tg
}

// SetPricing sets the per-token pricing for cost tracking, and whether that
// pricing was actually configured (vs. defaulted to zero because the model
// is unmapped).
func (a *Agent) SetPricing(p config.PricingConfig, known bool) {
	a.pricing = p
	a.pricingKnown = known
}

// addTokenUsage records token usage through the full guard chain (per-agent + root).
// When a CompositeGuard is present, AddTokens propagates to the root TokenGuard.
// When only a local TokenGuard is present (no composite guard), it is updated directly.
func (a *Agent) addTokenUsage(in, out int) {
	if a.guard != nil {
		a.guard.AddTokens(in, out, a.pricing.Input, a.pricing.Output)
	} else if a.tokenGuard != nil {
		a.tokenGuard.Add(in, out, a.pricing.Input, a.pricing.Output)
	}
}

// SetRetryConfig overrides the default LLM call retry behaviour.
func (a *Agent) SetRetryConfig(rc RetryConfig) {
	a.retryConfig = rc
}

// SetRateLimiter attaches a provider-scoped rate limiter.
func (a *Agent) SetRateLimiter(rl *RateLimiter) {
	a.rateLimiter = rl
}

// SetConfigReloadPath enables runtime hot-reload of this agent's RollbackConfig.
// When set, the agent re-reads its rollback settings from the given YAML config
// file at the start of every Run, so an edit takes effect on the next turn
// without restarting. Pass "" to disable (the default).
func (a *Agent) SetConfigReloadPath(path string) {
	a.configReloadPath = path
}

// SetDebugController attaches a debug controller for breakpoints and param overrides.
// When nil (default), all debug checks are no-ops with zero overhead.
func (a *Agent) SetDebugController(dc *debug.DebugController) {
	a.debugCtrl = dc
}

// SetSteering installs the mid-run steering drain hook. Nil disables it.
func (a *Agent) SetSteering(f func() []llm.Message) { a.steering = f }

// SetTurnGuard attaches the interim post-turn truthfulness guard.
// When nil (default), the guard is a no-op with zero overhead. See turnguard.go.
func (a *Agent) SetTurnGuard(g *TurnGuard) {
	a.turnGuard = g
}

// SetMemoryRecaller enables Run-start auto-recall injection (Phase-3). When nil
// (default), no recall is performed and there is zero overhead.
func (a *Agent) SetMemoryRecaller(r MemoryRecaller) {
	a.memoryRecaller = r
}

// SetEventBus replaces the bus this agent emits on. The intended use is
// telemetry.EventBus.WithOrigin: a directed agent chat rebuilds an agent and
// runs it alongside the main run, and its events have to be tellable apart
// from the main run's for an agent of the same name.
//
// Call it between construction and the first Run — the bus is read from every
// goroutine a run uses, so swapping it mid-run is a data race.
func (a *Agent) SetEventBus(bus *telemetry.EventBus) {
	a.eventBus = bus
}

// maxIterationsStop is the sink-only outcome for a run that exhausted
// max_iterations. It reports IncompleteRun so a consumer can tell it apart
// from a real failure without importing this package — the same one-way
// dependency the directed-chat roster keeps by declaring its own runner
// interface.
type maxIterationsStop struct{}

func (maxIterationsStop) Error() string       { return "run stopped at max_iterations" }
func (maxIterationsStop) IncompleteRun() bool { return true }

// ErrMaxIterations reports a run that stopped because it used up its iteration
// budget. It is never returned from Run or RunWithHistory — those still hand
// back the partial answer with a nil error, which is the graceful degradation
// every caller relies on. It reaches the transcript sink only, so a consumer
// recording the run does not record "done".
var ErrMaxIterations error = maxIterationsStop{}

// SetTranscriptSink registers a callback invoked with the agent's full
// message history when a run finishes. Used by directed agent chat to
// retain a subagent's context after its tool registry is closed. When nil
// (default), nothing is captured and there is zero overhead. The slice
// handed to the callback may alias the agent's own backing array, so a
// consumer that retains it beyond the callback must copy it first.
//
// The sink fires on EVERY exit path, so runErr carries the run's own outcome:
// nil only when the run actually finished, the run's error when it failed, and
// ErrMaxIterations when it stopped on its iteration budget (which Run itself
// still reports as a graceful success). A consumer that labels the captured
// transcript must read it: without it, a run killed by a timeout, a provider
// error, or its own budget is indistinguishable from one that finished.
func (a *Agent) SetTranscriptSink(fn func(history []llm.Message, runErr error)) {
	a.transcriptSink = fn
}

// debugCheck calls the debug controller's Check if present. Returns nil when no controller.
func (a *Agent) debugCheck(ctx context.Context, checkpoint string, iteration int, cpCtx *debug.CheckpointContext) error {
	if a.debugCtrl == nil {
		return nil
	}
	return a.debugCtrl.Check(ctx, checkpoint, a.name, iteration, cpCtx)
}

// endPayload builds an AgentEndPayload with budget info if available.
func (a *Agent) endPayload(status, finalAnswer string, iterations, totalTokens int) telemetry.AgentEndPayload {
	p := telemetry.AgentEndPayload{
		Status:       status,
		FinalAnswer:  finalAnswer,
		Iterations:   iterations,
		TotalTokens:  totalTokens,
		PricingKnown: a.pricingKnown,
	}
	if a.tokenGuard != nil {
		p.TotalCost = a.tokenGuard.Cost()
		p.MaxTokens = a.tokenGuard.MaxTokens()
		p.MaxCost = a.tokenGuard.MaxCost()
	}
	return p
}

// Run executes the agent with the given query using the ReAct loop
func (a *Agent) Run(ctx context.Context, query string) (string, error) {
	return a.RunWithAttachments(ctx, query, nil, nil)
}

// RunWithHistory executes the agent with existing conversation history
func (a *Agent) RunWithHistory(ctx context.Context, query string, history []llm.Message) (string, error) {
	return a.RunWithAttachments(ctx, query, history, nil)
}

// RunWithAttachments executes the agent with existing history plus extra
// ContentBlocks (e.g. images loaded via CLI --attach) appended to this
// turn's user message alongside the text query. attachments may be nil —
// Run and RunWithHistory are thin wrappers around this with attachments
// always nil, so this is the one real implementation both funnel through.
func (a *Agent) RunWithAttachments(ctx context.Context, query string, history []llm.Message, attachments []llm.ContentBlock) (_ string, runErr error) {
	runStart := time.Now()

	// Reset per-Run salvage state so a prior Run that terminated salvaged
	// doesn't poison the orchestrator's view of this fresh invocation.
	a.lastRunSalvaged.Store(false)
	// Reset broader unproductive flag for the same reason.
	a.lastRunUnproductive.Store(false)
	// Tool-call tracking (toolCallsByRun) is NOT reset here — unlike the two
	// flags above, it's keyed by a caller-supplied run ID from ctx (see
	// NewToolCallRunContext), not a single "last run" slot, specifically so
	// this Run doesn't clobber another concurrent Run's recorded calls on
	// the same *Agent. See the toolCallsByRun field doc for why.
	runToolCallID, trackToolCalls := toolCallRunIDFromContext(ctx)

	// Hot-reload: re-read this agent's RollbackConfig from its source YAML so
	// a config edit takes effect on this turn. No-op unless SetConfigReloadPath
	// was called.
	a.reloadRollback()

	// Clear per-user-turn tool state. For the ChatHost agent
	// each RunWithHistory is exactly one user turn, so this resets the
	// InvokeTool's per-turn loop guard. Non-ChatHost agents hold no
	// TurnResetter tools, making this a no-op for them.
	if a.toolRegistry != nil {
		for _, tl := range a.toolRegistry.GetAllTools() {
			if tr, ok := tl.(tools.TurnResetter); ok {
				tr.ResetTurn()
			}
		}
	}

	// Emit agent start event — use resolved values from provider when agent-level is empty.
	// Snapshot the provider via the getter so a mid-Run /model swap doesn't tear this event.
	currentLLM := a.LLMProvider()
	agentModel := a.Model()
	if agentModel == "" {
		agentModel = currentLLM.GetModel()
	}
	a.emitLifecycle(telemetry.EventAgentStart, telemetry.AgentStartPayload{
		Role:         string(a.role),
		Provider:     currentLLM.GetName(),
		Model:        agentModel,
		SystemPrompt: a.systemPrompt,
		Tools:        a.tools,
		Skills:       a.skills,
		ParentAgent:  a.parentAgent,
	})

	// Guarantee AGENT_END + EXECUTION_COMPLETE fire on every exit path —
	// success, max_iterations, budget, ctx timeout/cancel, and unhandled LLM
	// errors. Success paths set endEmitted=true to skip this defer. Anything
	// else (ctx.Done, LLM error, debug abort) falls through with a status
	// derived from the context state.
	endEmitted := false
	iterationsReached := 0
	var lastResponse string
	var totalTokens int
	defer func() {
		if endEmitted {
			return
		}
		status := "aborted"
		switch ctx.Err() {
		case context.DeadlineExceeded:
			status = "timeout"
		case context.Canceled:
			status = "cancelled"
		}
		a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload(status, lastResponse, iterationsReached, totalTokens))
		a.eventBus.EmitWithDuration(a.name, telemetry.EventExecutionComplete, a.endPayload(status, lastResponse, iterationsReached, totalTokens), time.Since(runStart).Milliseconds())
	}()

	// Initialize conversation history if not provided
	if history == nil {
		history = []llm.Message{}
	}

	// Add the query as a user message, folding in any attachments (e.g.
	// --attach images) alongside the text. Text block goes first.
	if len(attachments) == 0 {
		history = append(history, llm.NewTextMessage("user", query))
	} else {
		blocks := append([]llm.ContentBlock{{Type: llm.ContentTypeText, Text: query}}, attachments...)
		history = append(history, llm.Message{Role: "user", Content: blocks})
	}

	// Capture the settled transcript on every exit path. A deferred closure
	// reads `history` at return time, so it sees whatever the ReAct loop,
	// rollback, and reflection left behind — no per-return-site plumbing.
	// Registered after the seed so a run that fails before this point
	// records nothing rather than a misleading empty transcript.
	//
	// stopReason carries an outcome the caller is deliberately NOT given:
	// max_iterations degrades gracefully to (partial answer, nil) for every
	// caller, but a consumer that labels the stored transcript must not read
	// that nil as "finished".
	var stopReason error
	if a.transcriptSink != nil {
		defer func() {
			outcome := runErr
			if outcome == nil {
				outcome = stopReason
			}
			a.transcriptSink(history, outcome)
		}()
	}

	// Get tool definitions for the LLM
	toolDefs := a.getToolDefinitions()

	// Initialize step log for context management (only when needed)
	var stepLog *StepLog
	if a.contextMon.NeedsStepLog() {
		stepLog = NewStepLog(query)
	}

	// Phase-3 auto-recall: query the native memory store ONCE for this turn and
	// cache a fenced block to inject into the ephemeral LLM history each
	// iteration (never into the persisted history). recallMessage stays empty
	// when no recaller is set or nothing matches, making this a no-op.
	var recallMessage string
	if a.memoryRecaller != nil {
		if block, _ := a.memoryRecaller.Recall(query); block != "" {
			if warnings := a.contextMon.DetectInjection(block); len(warnings) > 0 {
				a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
					ErrorType:   "injection_warning",
					Message:     "auto-recall block: " + strings.Join(warnings, "; "),
					Recoverable: true,
				})
			}
			recallMessage = "<recalled_memory>\n" + block + "</recalled_memory>"
		}
	}

	// Debug breakpoint: before agent starts
	if err := a.debugCheck(ctx, "pre_agent", 0, nil); err != nil {
		return "", err
	}

	// ReAct loop — local copies for debug-overrideable values.
	// lastResponse and totalTokens are declared above so the AGENT_END
	// defer above can read them on abort paths.
	effectivePrompt := a.systemPrompt
	effectiveMaxIter := a.maxIterations
	effectiveToolDefs := toolDefs
	var totalTokensIn, totalTokensOut int
	var lastInputTokens int // tracks most recent prompt token count for CONTEXT_COMPRESSED payload
	retryCount := 0
	// rollbacksUsed counts runtime self-corrections this Run; capped by
	// a.rollback.MaxRollbacks so a persistently-failing path cannot loop.
	rollbacksUsed := 0
	// Track consecutive identical tool failures so the agent doesn't
	// loop on the same broken call forever. Key = "toolName:errorMsg",
	// value = consecutive failure count. Reset to 0 when the tool succeeds.
	toolFailCounts := map[string]int{}
	// TurnGuard per-Run signals: accumulated across all iterations of
	// this turn (the no-delegation case answers on iteration 0 with zero
	// tool calls, so these must be Run-scoped, not per-iteration). Reset
	// only here, at the top of the Run. guardRetried bounds the guard to a
	// single corrective retry so a stubborn model cannot loop.
	var invokeConfigCalled bool
	var spawnCalled bool
	var memoryWriteCount int
	guardRetried := false
	// Track whether we've emitted EventFormatSelected yet — fire once per
	// agent run, on the first iteration where the provider reports a
	// non-pending FormatInfo. Sniffing may need a couple of deltas to commit,
	// so the first iteration is the earliest we can report a final adapter.
	formatSelectedEmitted := false
	// One-time convergence nudge when the iteration budget is nearly
	// spent, so an exploring model starts synthesizing instead of burning its
	// whole budget on tool calls and exiting with nothing.
	convergenceNudged := false
	for i := 0; i < effectiveMaxIter; i++ {
		iterationsReached = i
		// Steerable one-shot (spec §6): fold any queued cross-session
		// steering messages into history at this iteration boundary. The
		// hook itself emits SESSION_MSG_RECEIVED for each drained message.
		if a.steering != nil {
			if msgs := a.steering(); len(msgs) > 0 {
				history = append(history, msgs...)
			}
		}
		// With 2 iterations left, tell the model to converge — it has
		// one tool-call slot and one answer slot remaining. Injected before
		// the checkpoint snapshot so a rollback cannot prune it, and skipped
		// for budgets too small to explore at all (the nudge would land on
		// the very first iteration). effectiveMaxIter here carries the most
		// recently resolved value: a sticky debug override lowering it makes
		// the nudge fire correspondingly earlier via the >= comparison.
		if !convergenceNudged && effectiveMaxIter >= 3 && i >= effectiveMaxIter-2 {
			convergenceNudged = true
			nudge := fmt.Sprintf(
				"[SYSTEM] Iteration budget: %d of %d tool-call iterations used, only %d remain. "+
					"Stop exploring and converge now — synthesize what you have already gathered into your final answer. "+
					"Only call another tool if it is essential to answering; otherwise reply with your final answer in plain text and no tool calls.",
				i, effectiveMaxIter, effectiveMaxIter-i)
			history = append(history, llm.NewTextMessage("system", nudge))
			a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
				ErrorType:   "iteration_budget_nudge",
				Message:     nudge,
				Recoverable: true,
			})
		}
		// Snapshot conversation state so a dead-end iteration can be rewound
		// by runtime self-correction. History grows append-only within a Run,
		// so the checkpoint is just a length (see historyCheckpoint).
		checkpoint := historyCheckpoint{iteration: i, historyLen: len(history), lastResponse: lastResponse}
		// Check for context cancellation — the defer above emits AGENT_END.
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		// Debug breakpoint: before thought
		preThoughtCtx := &debug.CheckpointContext{
			HistoryLength:  len(history),
			TotalTokensIn:  totalTokensIn,
			TotalTokensOut: totalTokensOut,
			MaxTokens:      a.maxIterations, // will be overridden if provider exposes it
			MaxIterations:  a.maxIterations,
		}
		// Extract last thought from history for iterations > 0
		if i > 0 {
			for j := len(history) - 1; j >= 0; j-- {
				if history[j].Role == "assistant" && history[j].AsText() != "" {
					preThoughtCtx.LastThought = history[j].AsText()
					break
				}
			}
		}
		if err := a.debugCheck(ctx, "pre_thought", i, preThoughtCtx); err != nil {
			return "", err
		}

		// Emit thought start event
		a.eventBus.Emit(a.name, telemetry.EventThoughtStart, telemetry.ThoughtStartPayload{
			Iteration:      i,
			HistoryLength:  len(history),
			AvailableTools: a.tools,
		})

		// Resolve provider — apply debug overrides if present.
		// Reset effective values to defaults each iteration so non-sticky
		// overrides revert after one use. Re-load the provider via the
		// getter every iteration so a /model swap mid-Run takes effect on
		// the next iteration without disrupting the in-flight one.
		provider := a.LLMProvider()
		effectivePrompt = a.systemPrompt
		effectiveMaxIter = a.maxIterations
		effectiveToolDefs = toolDefs
		if a.debugCtrl != nil {
			if ovr := a.debugCtrl.GetOverrides(a.name); ovr != nil {
				provider = llm.WrapWithOverrides(provider, llm.OverrideConfig{
					Temperature: ovr.Temperature,
					MaxTokens:   ovr.MaxTokens,
					TopP:        ovr.TopP,
					Model:       ovr.Model,
				})
				if ovr.SystemPrompt != nil {
					effectivePrompt = *ovr.SystemPrompt
				}
				if ovr.MaxIterations != nil {
					effectiveMaxIter = *ovr.MaxIterations
				}
				if len(ovr.Tools) > 0 {
					effectiveToolDefs = a.getToolDefinitionsFor(ovr.Tools)
				}
			}
		}

		// Build context-managed history for the LLM call (detect auto-strategy escalation)
		prevStrategy := a.contextMon.AppliedStrategy()
		llmHistory, retrievedSegments := a.contextMon.BuildHistory(stepLog, history)
		if newStrat := a.contextMon.AppliedStrategy(); newStrat != prevStrategy && prevStrategy != "" {
			a.eventBus.Emit(a.name, telemetry.EventContextCompressed, telemetry.ContextCompressedPayload{
				FromStrategy:   prevStrategy,
				ToStrategy:     newStrat,
				MessagesBefore: len(history),
				MessagesAfter:  len(llmHistory),
				TokensBefore:   lastInputTokens,
				Pressure:       a.contextMon.Pressure(),
				Iteration:      i,
			})
		}
		if retrievedSegments > 0 && a.retriever != nil {
			a.eventBus.Emit(a.name, telemetry.EventRetrievalInjected, telemetry.RetrievalInjectedPayload{
				Agent:     a.name,
				Iteration: i,
				Segments:  retrievedSegments,
				Backend:   a.retriever.Backend(),
			})
		}

		// Phase-3 auto-recall: splice the cached recall block into the
		// ephemeral llmHistory right after the user query (index 1, before any
		// step-retrieval block). Build a FRESH slice — for the "full" strategy
		// BuildHistory returns history by reference, so an in-place insert would
		// leak the block into the persisted transcript + summarizer. Same
		// copy-on-write shape as the retrieval-block injection in context.go.
		if recallMessage != "" {
			recalled := make([]llm.Message, 0, len(llmHistory)+1)
			if len(llmHistory) > 0 {
				recalled = append(recalled, llmHistory[0])
			}
			recalled = append(recalled, llm.NewTextMessage("user", recallMessage))
			if len(llmHistory) > 1 {
				recalled = append(recalled, llmHistory[1:]...)
			}
			llmHistory = recalled
		}

		// Call LLM with retry — use streaming when available for real-time output
		startTime := time.Now()
		result, err := a.generateWithRetry(ctx, provider, effectivePrompt, llmHistory, effectiveToolDefs, i)
		duration := time.Since(startTime).Milliseconds()

		if err != nil {
			return "", err
		}
		if result == nil {
			return "", fmt.Errorf("LLM returned nil result for agent %q", a.name)
		}

		// Track token usage
		if result.TokenUsage != nil {
			totalTokens += result.TokenUsage.TotalTokens
			totalTokensIn += result.TokenUsage.InputTokens
			totalTokensOut += result.TokenUsage.OutputTokens
			a.eventBus.EmitWithTokenUsage(a.name, telemetry.EventTokenUsage, struct{}{}, telemetry.TokenUsage{
				InputTokens:  result.TokenUsage.InputTokens,
				OutputTokens: result.TokenUsage.OutputTokens,
				TotalTokens:  result.TokenUsage.TotalTokens,
			})
		}

		// Record usage in token guard and check budget
		if result.TokenUsage != nil {
			a.addTokenUsage(result.TokenUsage.InputTokens, result.TokenUsage.OutputTokens)
			lastInputTokens = result.TokenUsage.InputTokens
			if a.maxContextTokens > 0 {
				a.contextMon.SetTokenPressure(result.TokenUsage.InputTokens, a.maxContextTokens)
			}
		}
		if a.guard != nil {
			if err := a.guard.Check(); err != nil {
				a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload("budget_exceeded", lastResponse, i+1, totalTokens))
				endEmitted = true
				// budget_exceeded is unproductive when lastResponse is empty
				// OR whitespace-only — the worker
				// burned its token/cost budget at the iteration-level guard
				// check without committing any usable content. TrimSpace
				// matches the max_iterations exit at the bottom of this
				// function; some reasoning models exit with lastResponse =
				// "\n" rather than literal "" and would otherwise mint a
				// non-empty "[budget exceeded — partial result]\n\n\n"
				// marker that the supervisor mistakes for real output.
				if strings.TrimSpace(lastResponse) != "" {
					return fmt.Sprintf("[budget exceeded — partial result]\n\n%s", lastResponse), nil
				}
				a.lastRunUnproductive.Store(true)
				return fmt.Sprintf("[budget exceeded: %s]", err.Error()), nil
			}
		}

		// Track last response for graceful max_iterations fallback
		if result.Response != "" {
			lastResponse = result.Response
		}

		// Emit thought end event with structured reasoning
		a.eventBus.EmitWithDuration(a.name, telemetry.EventThoughtEnd, telemetry.ThoughtEndPayload{
			StructuredThought: telemetry.StructuredThought{
				Reasoning:         result.Response,
				IntendedToolCalls: a.convertToolCalls(result.ToolCalls),
			},
			Iteration:    i,
			Model:        a.Model(),
			FinishReason: result.FinishReason,
		}, duration)

		// Emit format selection once per run, as soon as the provider reports
		// a committed adapter. Providers that don't use the format system leave
		// FormatInfo nil; we only emit when it's populated.
		if !formatSelectedEmitted && result.FormatInfo != nil {
			a.eventBus.Emit(a.name, telemetry.EventFormatSelected, telemetry.FormatSelectedPayload{
				Agent:    a.name,
				Model:    agentModel,
				Adapter:  result.FormatInfo.Adapter,
				Reason:   result.FormatInfo.Reason,
				Resolved: result.FormatInfo.Resolved,
			})
			formatSelectedEmitted = true
		}

		// Debug breakpoint: after thought
		postThoughtCtx := &debug.CheckpointContext{
			LastThought:    result.Response,
			HistoryLength:  len(history),
			TotalTokensIn:  totalTokensIn,
			TotalTokensOut: totalTokensOut,
			MaxIterations:  a.maxIterations,
		}
		for _, tc := range result.ToolCalls {
			postThoughtCtx.PendingTools = append(postThoughtCtx.PendingTools, debug.PendingToolInfo{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		if err := a.debugCheck(ctx, "post_thought", i, postThoughtCtx); err != nil {
			return "", err
		}

		// Decision branch: no tool calls means final answer
		if len(result.ToolCalls) == 0 {
			// When the format adapter promoted reasoning-only text as
			// content (Result.Salvaged), the model did NOT actually commit
			// to an answer. Treating the [REASONING-ONLY OUTPUT — …]
			// <reasoning> blob as a successful final answer is exactly the
			// silent-failure observed in a dogfood run: agent returned
			// status=success with zero files written.
			//
			// If we have iterations left, inject a recovery directive
			// and try again — the model gets a chance to call a tool
			// or commit. If we don't have iterations left, fall through
			// to the normal termination path but flag the status as
			// "salvaged_no_progress" so consumers can distinguish
			// genuine success from silently-broken runs.
			if result.Salvaged {
				preview := result.Response
				if len(preview) > 200 {
					preview = preview[:200]
				}
				if i+1 < effectiveMaxIter {
					a.eventBus.Emit(a.name, telemetry.EventSalvagedOutput, telemetry.SalvagedOutputPayload{
						Agent:          a.name,
						Iteration:      i + 1,
						Recovery:       true,
						ContentPreview: preview,
						ReasoningChars: len(result.ThinkingContent),
					})
					history = append(history, llm.NewTextMessage("assistant", result.Response))
					history = append(history, llm.NewTextMessage("user",
						"Your previous response did not call any tool and did not commit to a final answer — the model produced internal reasoning only. "+
							"To make progress, EITHER call one of the available tools to advance the task, OR produce a committed final answer in plain text that does NOT start with the [REASONING-ONLY OUTPUT] marker. "+
							"Do not echo your previous reasoning; act now."))
					continue
				}
				// Out of iterations — emit terminal salvaged event and
				// fall through to the normal end with the salvaged
				// status applied below.
				a.eventBus.Emit(a.name, telemetry.EventSalvagedOutput, telemetry.SalvagedOutputPayload{
					Agent:          a.name,
					Iteration:      i + 1,
					Recovery:       false,
					ContentPreview: preview,
					ReasoningChars: len(result.ThinkingContent),
				})
			}

			finalAnswer := result.Response

			// Some reasoning models (notably nemotron with the
			// reasoning_content_field adapter) emit their final answer
			// into the `reasoning_content` channel instead of `content`.
			// The tokens stream correctly to the UI as TOKEN_CHUNK
			// events, but result.Response stays empty, so finalAnswer
			// would be empty and the agent terminates without producing
			// a visible result for the chat host or supervisor. Fall
			// back to ThinkingContent when Response is empty — the
			// alternative is silent truncation of a model that did
			// produce output, just on the wrong channel.
			if strings.TrimSpace(finalAnswer) == "" && strings.TrimSpace(result.ThinkingContent) != "" {
				finalAnswer = result.ThinkingContent
			}

			// Ground-check before returning final answer
			if a.groundCheck.Enabled {
				valid, _, gcTokIn, gcTokOut, gcErr := a.doGroundCheck(ctx, history, i)
				totalTokens += gcTokIn + gcTokOut
				a.addTokenUsage(gcTokIn, gcTokOut)
				if a.guard != nil {
					if budgetErr := a.guard.Check(); budgetErr != nil {
						a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload("budget_exceeded", finalAnswer, i+1, totalTokens))
						endEmitted = true
						return finalAnswer, budgetErr
					}
				}
				if gcErr != nil {
					a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
						ErrorType:   "ground_check_error",
						Message:     gcErr.Error(),
						Recoverable: true,
					})
				} else if !valid && retryCount < a.groundCheck.MaxRetries {
					// Low confidence — retry by adding feedback and continuing the loop
					retryCount++
					history = append(history, llm.NewTextMessage("assistant", result.Response))
					history = append(history, llm.NewTextMessage("user",
						"Your answer did not pass the ground-check validation. Please review your reasoning, address any issues, and provide an improved answer."))
					continue
				}
			}

			// Reflection before final answer
			if a.reflection.Enabled && (a.reflection.Mode == "before_answer" || a.reflection.Mode == "both") {
				// Add the proposed answer to history for reflection context
				reflectHistory := append(history, llm.NewTextMessage("assistant", result.Response))
				reflectedHistory, rTokIn, rTokOut, reflectErr := a.doReflect(ctx, reflectHistory, i, "before_answer")
				totalTokens += rTokIn + rTokOut
				a.addTokenUsage(rTokIn, rTokOut)
				if a.guard != nil {
					if budgetErr := a.guard.Check(); budgetErr != nil {
						a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload("budget_exceeded", finalAnswer, i+1, totalTokens))
						endEmitted = true
						return finalAnswer, budgetErr
					}
				}
				if reflectErr == nil {
					// Use the last assistant message as the refined answer
					for j := len(reflectedHistory) - 1; j >= 0; j-- {
						if reflectedHistory[j].Role == "assistant" {
							finalAnswer = reflectedHistory[j].AsText()
							break
						}
					}
				}
			}

			// Surface salvaged-no-progress in the terminal status so chat
			// hosts, supervisors, and downstream log consumers can
			// distinguish genuine completion from "ran out of iterations
			// while the worker was salvaged."
			finalStatus := "success"
			if result.Salvaged {
				finalStatus = "salvaged_no_progress"
				// Surface the salvaged-terminal state to any outer
				// orchestrator via LastRunSalvaged() so it can apply a
				// per-worker re-delegation cap.
				a.lastRunSalvaged.Store(true)
				// Salvaged is always unproductive.
				a.lastRunUnproductive.Store(true)
			} else if strings.TrimSpace(finalAnswer) == "" {
				// No-tool-calls exit with empty answer is unproductive even
				// when status=success (a reasoning model burned its budget
				// on ThinkingContent, produced no Response; agent terminates
				// "successfully" with nothing to return). Supervisor must
				// not loop on it.
				a.lastRunUnproductive.Store(true)
				if result.FinishReason == "length" {
					// The model exhausted its output-token budget before
					// producing any visible content (common with reasoning
					// models such as gpt-5-nano, whose hidden reasoning
					// tokens share the same budget as the answer). Leaving
					// finalAnswer=="" with status="success" here renders as
					// total silence in chat hosts — AgentDoneMsg{Response:
					// "", Err: nil} hits neither the error branch nor the
					// msg.Response != "" branch in internal/chat/model.go,
					// so the pre-existing empty BlockAssistant placeholder
					// never gets text and prints nothing. Same synthesized-
					// marker idiom as the budget_exceeded / max_iterations
					// exits elsewhere in this function.
					finalStatus = "truncated_empty"
					finalAnswer = "[truncated: model ran out of output tokens before answering (finish_reason=length) — raise --max-tokens or settings.defaults.max_tokens]"
				}
			}

			// TurnGuard: cross-check the settled final answer against
			// this turn's hard execution signals (did invoke_config fire? did
			// a memory write happen?). Runs after ground-check + reflection so
			// it sees the final text. On a rejection, retry exactly once
			// (bounded by guardRetried and by iterations remaining, so the
			// marker path below can never be silently skipped on the last
			// iteration); if it still fails, prepend a visible marker and set a
			// plain interim status, then fall through to the normal exit.
			if marker, reason := a.turnGuard.Check(finalAnswer, turnSignals{
				invokeConfigCalled: invokeConfigCalled,
				memoryWriteCount:   memoryWriteCount,
				spawnCalled:        spawnCalled,
			}); marker != "" {
				if !guardRetried && i+1 < effectiveMaxIter {
					guardRetried = true
					a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
						ErrorType:   "turn_guard_retry",
						Message:     marker + " " + reason,
						Recoverable: true,
					})
					history = append(history, llm.NewTextMessage("assistant", result.Response))
					history = append(history, llm.NewTextMessage("user", turnGuardDirective(marker)))
					continue
				}
				// Retry exhausted (or no iterations left): surface the breach.
				a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
					ErrorType:   "turn_guard_unrecovered",
					Message:     marker + " " + reason,
					Recoverable: true,
				})
				finalAnswer = marker + " " + finalAnswer
				finalStatus = statusFor(marker)
				a.lastRunUnproductive.Store(true)
			}
			a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload(finalStatus, finalAnswer, i+1, totalTokens))
			a.eventBus.EmitWithDuration(a.name, telemetry.EventExecutionComplete, a.endPayload(finalStatus, finalAnswer, i+1, totalTokens), time.Since(runStart).Milliseconds())
			endEmitted = true
			// The loop's own `history = append(history, assistantMsg)` sits
			// below this return, so without this the transcript sink would
			// see history ending on the user/tool message that preceded the
			// agent's answer rather than the answer itself. Gated on the
			// sink being set so a run with no sink keeps its current
			// allocation profile exactly.
			if a.transcriptSink != nil {
				history = append(history, llm.NewTextMessage("assistant", finalAnswer))
			}
			return finalAnswer, nil
		}

		// Handle thinking content: offload to disk and inject placeholder
		if result.ThinkingContent != "" && a.thinkingOffload {
			_ = a.thinkingStore.Save(a.name, i, result.ThinkingContent)
			history = append(history, llm.NewTextMessage("user", ThinkingPlaceholder(i, result.ThinkingContent)))
			// 1D: index thinking content for retrieval so future iterations can recall reasoning
			if a.retriever != nil {
				a.retriever.Index(fmt.Sprintf("thinking-%d", i), result.ThinkingContent, SegmentMeta{
					Iteration: i,
					Type:      "thinking",
				})
			}
		}

		// Add assistant message to history (Response is already clean — thinking stripped by provider)
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   []llm.ContentBlock{{Type: llm.ContentTypeText, Text: result.Response}},
			ToolCalls: result.ToolCalls,
		}
		if result.ProviderMetadata != nil {
			assistantMsg.Metadata = result.ProviderMetadata
		}
		history = append(history, assistantMsg)

		// Execute tool calls in parallel
		type toolResult struct {
			call   llm.ToolCall
			output string
			err    error
			dur    int64
		}

		results := make([]toolResult, len(result.ToolCalls))
		var wg sync.WaitGroup

		// Debug breakpoint: before tool execution
		preToolCtx := &debug.CheckpointContext{
			LastThought:    result.Response,
			HistoryLength:  len(history),
			TotalTokensIn:  totalTokensIn,
			TotalTokensOut: totalTokensOut,
			MaxIterations:  a.maxIterations,
		}
		for _, tc := range result.ToolCalls {
			preToolCtx.PendingTools = append(preToolCtx.PendingTools, debug.PendingToolInfo{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		if err := a.debugCheck(ctx, "pre_tool", i, preToolCtx); err != nil {
			return "", err
		}

		for idx, call := range result.ToolCalls {
			// Emit tool call start
			a.eventBus.Emit(a.name, telemetry.EventToolCallStart, telemetry.ToolCallStartPayload{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Arguments:  call.Arguments,
			})

			wg.Add(1)
			go func(i int, c llm.ToolCall) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						results[i] = toolResult{
							call:   c,
							output: "",
							err:    fmt.Errorf("tool panic: %v", r),
							dur:    0,
						}
					}
				}()
				start := time.Now()
				output, err := a.executeTool(ctx, c)
				results[i] = toolResult{c, output, err, time.Since(start).Milliseconds()}
			}(idx, call)
		}
		wg.Wait()

		// Collect results in order and emit events
		var anyToolError bool
		var stepToolResults []ToolOutput
		var repeatedFailTools []string // tools that hit the retry ceiling this iteration
		for _, r := range results {
			var errMsg string
			if r.err != nil {
				errMsg = r.err.Error()
				anyToolError = true
			}

			// Record every attempted call (regardless of success) so a
			// pipeline-level require_tool_call gate can mechanically verify
			// the agent actually invoked the tool it was asked to use. Only
			// when this Run opted in via NewToolCallRunContext — see
			// toolCallsByRun's field doc for why this is keyed by run ID
			// instead of a single shared slot.
			if trackToolCalls {
				a.toolCallsByRunMu.Lock()
				if a.toolCallsByRun == nil {
					a.toolCallsByRun = make(map[uint64][]llm.ToolCall)
				}
				a.toolCallsByRun[runToolCallID] = append(a.toolCallsByRun[runToolCallID], r.call)
				a.toolCallsByRunMu.Unlock()
			}

			// Track consecutive identical failures per tool+error pair.
			failKey := r.call.Name + ":" + errMsg
			if r.err != nil {
				toolFailCounts[failKey]++
				if toolFailCounts[failKey] >= 3 {
					repeatedFailTools = append(repeatedFailTools, r.call.Name)
				}
			} else {
				// Success — reset all failure counters for this tool.
				for k := range toolFailCounts {
					if strings.HasPrefix(k, r.call.Name+":") {
						delete(toolFailCounts, k)
					}
				}
				// TurnGuard signals: record ground-truth side effects so
				// the post-turn guard can cross-check the final answer. Keyed on
				// tool name to keep internal/agent free of any chathost/memory
				// import; r.err == nil matches exactly when the memory tools
				// emit EventMemoryWrite.
				switch r.call.Name {
				case "invoke_config":
					invokeConfigCalled = true
				case "spawn_agent":
					spawnCalled = true
				case "memory_add", "memory_link", "memory_retire":
					memoryWriteCount++
				}
			}

			// Emit event with raw output (telemetry always sees full data)
			a.eventBus.EmitWithDuration(a.name, telemetry.EventToolCallEnd, telemetry.ToolCallEndPayload{
				ToolCallID: r.call.ID,
				ToolName:   r.call.Name,
				Output:     r.output,
				Error:      errMsg,
			}, r.dur)

			// Injection detection — emit warnings as telemetry events
			if warnings := a.contextMon.DetectInjection(r.output); len(warnings) > 0 {
				a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
					ErrorType:   "injection_warning",
					Message:     fmt.Sprintf("tool %q output: %s", r.call.Name, strings.Join(warnings, "; ")),
					Recoverable: true,
				})
			}

			// Record in step log (raw output preserved)
			stepToolResults = append(stepToolResults, ToolOutput{
				CallID:    r.call.ID,
				ToolName:  r.call.Name,
				RawOutput: r.output,
				Error:     errMsg,
				Duration:  r.dur,
				Metadata:  r.call.Metadata,
			})

			// Sanitize output before adding to LLM history.
			// When the tool errored, prepend an unambiguous
			// [TOOL ERROR: ...] marker so the LLM's next turn can tell
			// this call failed. Before this, the history entry looked
			// identical to a successful call (just fenced content), and
			// smaller models (e.g. gemini-flash-lite) treated stderr text
			// like "fatal: bad revision" as data instead of as an error
			// signal, causing nonsense retry loops.
			rawForHistory := r.output
			if r.err != nil {
				rawForHistory = "[TOOL ERROR: " + r.err.Error() + "]\n" + r.output
			}
			sanitized := a.contextMon.SanitizeOutput(r.call.Name, r.call.ID, rawForHistory)

			history = append(history, llm.Message{
				Role:       "tool",
				Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: sanitized}},
				ToolCallID: r.call.ID,
				Name:       r.call.Name,
				Metadata:   r.call.Metadata,
			})
		}

		// When any tool has failed 3+ times with the same error,
		// inject a system-level hint so the LLM changes strategy instead
		// of endlessly retrying the same broken call.
		if len(repeatedFailTools) > 0 {
			hint := fmt.Sprintf(
				"[SYSTEM] The following tools have failed 3+ times with the same error: %s. "+
					"Do NOT retry them with the same arguments. Either try a completely different "+
					"approach, use different arguments, or give your best answer with the information you already have.",
				strings.Join(repeatedFailTools, ", "))
			history = append(history, llm.NewTextMessage("system", hint))
			a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
				ErrorType:   "tool_retry_budget",
				Message:     hint,
				Recoverable: true,
			})
		}

		// Runtime self-correction: if this iteration dead-ended, rewind
		// history to before it so the failed attempt's tool errors don't
		// pollute the LLM context, then retry. Deterministic triggers fire
		// first; the opt-in LLM self-judge is consulted only as a fallback
		// hint. Capped by MaxRollbacks; once exhausted the loop proceeds
		// normally (the retry-budget hint / max_iterations / unproductive cap remain the backstop).
		if a.rollback.Enabled && rollbacksUsed < a.rollback.MaxRollbacks && i+1 < effectiveMaxIter {
			allToolsFailed := len(results) > 0
			for _, r := range results {
				if r.err == nil {
					allToolsFailed = false
					break
				}
			}
			trigger, reason, hit := a.rollbackTrigger(allToolsFailed, repeatedFailTools)
			if !hit && a.rollback.LLMSelfJudge {
				deadEnd, judgeReason, jTokIn, jTokOut := a.judgeDeadEnd(ctx, history, i)
				totalTokens += jTokIn + jTokOut
				a.addTokenUsage(jTokIn, jTokOut)
				if deadEnd {
					trigger, reason, hit = "llm_judge", judgeReason, true
				}
			}
			if hit {
				rollbacksUsed++
				pruned := len(history) - checkpoint.historyLen
				a.eventBus.Emit(a.name, telemetry.EventRollback, telemetry.RollbackPayload{
					Agent:          a.name,
					Iteration:      i,
					Trigger:        trigger,
					Reason:         reason,
					RollbackNum:    rollbacksUsed,
					MaxRollbacks:   a.rollback.MaxRollbacks,
					MessagesPruned: pruned,
				})
				history = history[:checkpoint.historyLen]
				lastResponse = checkpoint.lastResponse
				history = append(history, llm.NewTextMessage("system", fmt.Sprintf(
					"[SELF-CORRECTION] Your previous attempt was discarded: %s. "+
						"That failed attempt has been removed from this conversation to keep your "+
						"context clean. Take a different approach now — do not repeat the discarded "+
						"tool calls with the same arguments.", reason)))
				continue
			}
		}

		// Reflection after tool execution (respects frequency setting)
		var reflectionText string
		if a.shouldReflect(i, anyToolError) {
			reflectedHistory, rTokIn, rTokOut, reflectErr := a.doReflect(ctx, history, i, "after_tool")
			totalTokens += rTokIn + rTokOut
			a.addTokenUsage(rTokIn, rTokOut)
			if a.guard != nil {
				if budgetErr := a.guard.Check(); budgetErr != nil {
					a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload("budget_exceeded", lastResponse, i+1, totalTokens))
					endEmitted = true
					return lastResponse, budgetErr
				}
			}
			if reflectErr == nil {
				history = reflectedHistory
				// Extract the reflection text for step log
				if last := reflectedHistory[len(reflectedHistory)-1]; last.Role == "assistant" {
					reflectionText = last.AsText()
				}
			}
		}

		// Record completed step in step log
		if stepLog != nil {
			step := Step{
				Iteration:   i,
				Thought:     result.Response,
				ToolCalls:   result.ToolCalls,
				ToolResults: stepToolResults,
				Reflection:  reflectionText,
			}
			if result.TokenUsage != nil {
				step.TokensIn = result.TokenUsage.InputTokens
				step.TokensOut = result.TokenUsage.OutputTokens
			}
			stepLog.AddStep(step)
			// Index step for per-session retrieval
			if a.retriever != nil {
				a.retriever.Index(fmt.Sprintf("step-%d", i), FormatStepForIndex(&step), SegmentMeta{
					Iteration: i,
					Type:      "step",
					IsError:   StepHasErrors(&step),
				})
			}
		}
	}

	// Max iterations reached — return last response gracefully instead of
	// erroring. When the loop never committed any response text (every
	// iteration was a bare tool call), first run one forced no-tools synthesis
	// pass over the gathered context, so the run returns an answer grounded in
	// the work already done instead of nothing. The spend is bounded to one
	// call and only happens on runs that would otherwise be a total loss, so
	// it is deliberately not re-gated on the token guard.
	var maxIterAnswer string
	if strings.TrimSpace(lastResponse) == "" {
		synth, sTokIn, sTokOut := a.forceSynthesis(ctx, stepLog, history)
		totalTokens += sTokIn + sTokOut
		a.addTokenUsage(sTokIn, sTokOut)
		if strings.TrimSpace(synth) != "" {
			maxIterAnswer = fmt.Sprintf("[max_iterations reached — synthesized answer]\n\n%s", synth)
		}
	}

	// max_iterations is unproductive when lastResponse is empty OR
	// whitespace-only and synthesis rescued nothing — the worker
	// burned its entire iteration budget without committing any usable
	// content. The trimmed check matters because some reasoning models exit
	// with `lastResponse = "\n"` (a stray newline) rather than the literal
	// empty string, which the supervisor sees as a non-empty 44-char
	// "[max_iterations reached — partial result]\n\n\n" marker and
	// re-delegates against. Outer orchestrator sees this via
	// LastRunUnproductive() and will block further re-delegations to this
	// worker after the cap. A synthesized answer is real output, so it does
	// NOT set the flag.
	if maxIterAnswer == "" {
		if strings.TrimSpace(lastResponse) == "" {
			a.lastRunUnproductive.Store(true)
			maxIterAnswer = "[max_iterations reached — no response produced]"
		} else {
			maxIterAnswer = fmt.Sprintf("[max_iterations reached — partial result]\n\n%s", lastResponse)
		}
	}

	// Emitted after the answer settles so the AGENT_END payload carries what
	// the caller actually receives (synthesized or marker-wrapped), not the
	// raw lastResponse.
	a.emitLifecycle(telemetry.EventAgentEnd, a.endPayload("max_iterations", maxIterAnswer, a.maxIterations, totalTokens))
	a.eventBus.EmitWithDuration(a.name, telemetry.EventExecutionComplete, a.endPayload("max_iterations", maxIterAnswer, a.maxIterations, totalTokens), time.Since(runStart).Milliseconds())
	endEmitted = true
	// Same rationale as the success-branch return above: the loop's own
	// history append happens per-iteration inside the loop, never after it
	// exits, so a run that exhausted its iterations would otherwise leave
	// the transcript ending mid-turn (e.g. on a role:"tool" message).
	if a.transcriptSink != nil {
		history = append(history, llm.NewTextMessage("assistant", maxIterAnswer))
	}
	// Report the stop to the sink only. The return stays (answer, nil) — every
	// other caller treats max_iterations as graceful degradation and that is
	// unchanged — but a consumer recording this run must not label it done.
	stopReason = ErrMaxIterations
	return maxIterAnswer, nil
}

// rollbackTrigger evaluates the configured deterministic rollback triggers
// against one iteration's outcome. It returns the matched trigger name, a
// human-readable reason, and whether a runtime self-correction should fire.
// Unrecognized trigger names are ignored.
func (a *Agent) rollbackTrigger(allToolsFailed bool, repeatedFails []string) (trigger, reason string, hit bool) {
	for _, t := range a.rollback.Triggers {
		switch t {
		case "tool_error":
			if allToolsFailed {
				return "tool_error", "every tool call in the iteration failed", true
			}
		case "repeated_tool_failure":
			if len(repeatedFails) > 0 {
				return "repeated_tool_failure",
					fmt.Sprintf("tool(s) %s failed repeatedly with the same error", strings.Join(repeatedFails, ", ")),
					true
			}
		}
	}
	return "", "", false
}

// judgeDeadEnd asks the LLM whether the just-completed iteration made real
// progress or hit a dead end. It is the opt-in (RollbackConfig.LLMSelfJudge)
// secondary rollback trigger — a hint only, consulted after the deterministic
// triggers. Returns whether it judged a dead end, a short reason, and the
// tokens spent. A failed or ambiguous judge call returns false (fail-safe: do
// not rewind on uncertainty).
func (a *Agent) judgeDeadEnd(ctx context.Context, history []llm.Message, iteration int) (deadEnd bool, reason string, tokIn, tokOut int) {
	judgeHistory := make([]llm.Message, len(history))
	copy(judgeHistory, history)
	judgeHistory = append(judgeHistory, llm.NewTextMessage("user",
		"Review ONLY the most recent step you just took. Did it make real progress "+
			"toward the task, or did it hit a dead end (an error, a wrong approach, or no "+
			"useful result)?\n\nRespond with EXACTLY one line in this format:\n"+
			"VERDICT: PROGRESS\nor\nVERDICT: DEAD_END — <short reason>"))

	result, err := a.LLMProvider().Generate(ctx, a.systemPrompt, judgeHistory, nil)
	if err != nil || result == nil {
		return false, "", 0, 0
	}
	if result.TokenUsage != nil {
		tokIn = result.TokenUsage.InputTokens
		tokOut = result.TokenUsage.OutputTokens
	}
	verdict := result.Response
	if strings.TrimSpace(verdict) == "" {
		verdict = result.ThinkingContent
	}
	// Match and slice directly on verdict (never on a strings.ToUpper copy):
	// ToUpper can change a string's byte length (e.g. 'ɐ' U+0250 -> 'Ɑ'
	// U+2C6D grows by a byte), so an index found in the uppercased copy can
	// land past the end of — or mid-rune inside — the original, byte-for-byte
	// different verdict string. A case-insensitive regex on verdict itself
	// finds "DEAD_END" and slices from a position that's always valid there.
	deadEndRe := regexp.MustCompile(`(?i)DEAD_END`)
	loc := deadEndRe.FindStringIndex(verdict)
	if loc == nil {
		return false, "", tokIn, tokOut
	}
	reason = "the LLM self-judge flagged the step as a dead end"
	tail := strings.TrimLeft(strings.TrimSpace(verdict[loc[1]:]), "—-: ")
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 {
		tail = tail[:nl]
	}
	if tail = strings.TrimSpace(tail); tail != "" {
		reason = "LLM self-judge: " + tail
	}
	return true, reason, tokIn, tokOut
}

// forceSynthesis makes one final no-tools LLM call over the gathered
// conversation so a run that exhausted max_iterations without committing any
// response text still returns an answer grounded in the tool results it
// already collected. Emits a THOUGHT_START/THOUGHT_END pair (iteration
// = maxIterations) so the pass is visible in the trace. Returns "" when the
// call fails or the model still produces nothing; the caller then falls back
// to the no-response marker.
func (a *Agent) forceSynthesis(ctx context.Context, stepLog *StepLog, history []llm.Message) (answer string, tokIn, tokOut int) {
	llmHistory, _ := a.contextMon.BuildHistory(stepLog, history)
	// Copy-on-write: BuildHistory may return history by reference (the "full"
	// strategy), and the directive must not leak into the persisted transcript.
	synthHistory := make([]llm.Message, len(llmHistory), len(llmHistory)+1)
	copy(synthHistory, llmHistory)
	synthHistory = append(synthHistory, llm.NewTextMessage("user",
		"You have used your entire tool-call budget for this task. Do not request any more tool calls. "+
			"Using ONLY the information already gathered above, write your best final answer to the original query now. "+
			"If the gathered information is incomplete, say so and answer with what you have."))

	a.eventBus.Emit(a.name, telemetry.EventThoughtStart, telemetry.ThoughtStartPayload{
		Iteration:     a.maxIterations,
		HistoryLength: len(synthHistory),
	})
	start := time.Now()
	result, err := a.LLMProvider().Generate(ctx, a.systemPrompt, synthHistory, nil)
	duration := time.Since(start).Milliseconds()

	if err != nil || result == nil {
		msg := "nil result"
		if err != nil {
			msg = err.Error()
		}
		a.eventBus.EmitWithDuration(a.name, telemetry.EventThoughtEnd, telemetry.ThoughtEndPayload{
			Iteration:    a.maxIterations,
			Model:        a.Model(),
			FinishReason: "error",
		}, duration)
		a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
			ErrorType:   "forced_synthesis_error",
			Message:     "forced synthesis at max_iterations failed: " + msg,
			Recoverable: true,
		})
		return "", 0, 0
	}
	if result.TokenUsage != nil {
		tokIn = result.TokenUsage.InputTokens
		tokOut = result.TokenUsage.OutputTokens
	}
	answer = result.Response
	// Reasoning models may emit the answer on the reasoning channel only.
	if strings.TrimSpace(answer) == "" && strings.TrimSpace(result.ThinkingContent) != "" {
		answer = result.ThinkingContent
	}
	a.eventBus.EmitWithDuration(a.name, telemetry.EventThoughtEnd, telemetry.ThoughtEndPayload{
		StructuredThought: telemetry.StructuredThought{Reasoning: result.Response},
		Iteration:         a.maxIterations,
		Model:             a.Model(),
		FinishReason:      result.FinishReason,
	}, duration)
	return answer, tokIn, tokOut
}

// reloadRollback re-reads this agent's RollbackConfig from its source YAML.
// No-op unless SetConfigReloadPath was called. A missing, invalid, or
// mid-write config file is ignored — the agent keeps its current config
// rather than failing the Run.
func (a *Agent) reloadRollback() {
	if a.configReloadPath == "" {
		return
	}
	cfg, err := config.Load(a.configReloadPath)
	if err != nil || cfg == nil {
		return
	}
	def := cfg.GetAgent(a.name)
	if def == nil || def.Settings == nil {
		return
	}
	rb := def.Settings.Rollback
	if rb.MaxRollbacks <= 0 {
		rb.MaxRollbacks = 3
	}
	if len(rb.Triggers) == 0 {
		rb.Triggers = []string{"tool_error"}
	}
	a.rollback = rb
}

// doReflect performs a self-reflection step by making an LLM call without tools.
// Returns updated history, input tokens, output tokens, and any error.
func (a *Agent) doReflect(ctx context.Context, history []llm.Message, iteration int, mode string) ([]llm.Message, int, int, error) {
	if err := a.debugCheck(ctx, "pre_reflection", iteration, nil); err != nil {
		return history, 0, 0, err
	}
	a.eventBus.Emit(a.name, telemetry.EventReflectionStart, telemetry.ReflectionStartPayload{
		Iteration: iteration,
		Mode:      mode,
	})

	prompt := a.reflection.Prompt
	if prompt == "" {
		prompt = defaultReflectionPrompt
	}

	// Build reflection history: existing history + reflection prompt as user message
	reflectHistory := make([]llm.Message, len(history))
	copy(reflectHistory, history)
	reflectHistory = append(reflectHistory, llm.NewTextMessage("user", prompt))

	startTime := time.Now()
	result, err := a.LLMProvider().Generate(ctx, a.systemPrompt, reflectHistory, nil)
	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return history, 0, 0, fmt.Errorf("reflection LLM error: %w", err)
	}

	var tokIn, tokOut int
	if result.TokenUsage != nil {
		tokIn = result.TokenUsage.InputTokens
		tokOut = result.TokenUsage.OutputTokens
	}

	a.eventBus.EmitWithDuration(a.name, telemetry.EventReflectionEnd, telemetry.ReflectionEndPayload{
		Iteration:  iteration,
		Mode:       mode,
		Reflection: result.Response,
	}, duration)

	// Append only the reflection response to the original history (not the injected prompt)
	updatedHistory := append(history, llm.NewTextMessage("assistant", fmt.Sprintf("[Reflection] %s", result.Response)))

	return updatedHistory, tokIn, tokOut, nil
}

// doGroundCheck performs a ground-check validation by making an LLM call.
// Returns validity, confidence, input tokens, output tokens, and any error.
func (a *Agent) doGroundCheck(ctx context.Context, history []llm.Message, iteration int) (bool, float64, int, int, error) {
	if err := a.debugCheck(ctx, "pre_ground_check", iteration, nil); err != nil {
		return false, 0, 0, 0, err
	}
	a.eventBus.Emit(a.name, telemetry.EventGroundCheckStart, telemetry.GroundCheckStartPayload{
		Iteration: iteration,
	})

	prompt := a.groundCheck.Prompt
	if prompt == "" {
		prompt = defaultGroundCheckPrompt
	}

	// Build check history
	checkHistory := make([]llm.Message, len(history))
	copy(checkHistory, history)
	checkHistory = append(checkHistory, llm.NewTextMessage("user", prompt))

	startTime := time.Now()
	result, err := a.LLMProvider().Generate(ctx, a.systemPrompt, checkHistory, nil)
	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return false, 0, 0, 0, fmt.Errorf("ground-check LLM error: %w", err)
	}

	var tokIn, tokOut int
	if result.TokenUsage != nil {
		tokIn = result.TokenUsage.InputTokens
		tokOut = result.TokenUsage.OutputTokens
	}

	// Parse the structured response
	confidence, valid, issues, assessment := parseGroundCheckResponse(result.Response)

	action := "proceed"
	if !valid {
		action = "retry"
	}

	a.eventBus.EmitWithDuration(a.name, telemetry.EventGroundCheckEnd, telemetry.GroundCheckEndPayload{
		Iteration:  iteration,
		IsValid:    valid,
		Confidence: confidence,
		Issues:     issues,
		Action:     action,
		Assessment: assessment,
	}, duration)

	return valid && confidence >= a.groundCheck.ConfidenceThreshold, confidence, tokIn, tokOut, nil
}

// parseGroundCheckResponse extracts structured fields from the ground-check LLM response.
func parseGroundCheckResponse(response string) (confidence float64, valid bool, issues []string, assessment string) {
	confidence = 0.5
	valid = true

	// Parse CONFIDENCE: <number>
	confRe := regexp.MustCompile(`(?i)CONFIDENCE:\s*([\d.]+)`)
	if matches := confRe.FindStringSubmatch(response); len(matches) > 1 {
		if v, err := strconv.ParseFloat(matches[1], 64); err == nil {
			confidence = v
		}
	}

	// Parse VALID: <true/false>
	validRe := regexp.MustCompile(`(?i)VALID:\s*(true|false)`)
	if matches := validRe.FindStringSubmatch(response); len(matches) > 1 {
		valid = strings.EqualFold(matches[1], "true")
	}

	// Parse ISSUES: <list>
	issuesRe := regexp.MustCompile(`(?i)ISSUES:\s*(.+)`)
	if matches := issuesRe.FindStringSubmatch(response); len(matches) > 1 {
		issueStr := strings.TrimSpace(matches[1])
		if !strings.EqualFold(issueStr, "none") {
			for _, issue := range strings.Split(issueStr, ",") {
				if trimmed := strings.TrimSpace(issue); trimmed != "" {
					issues = append(issues, trimmed)
				}
			}
		}
	}

	// Parse ASSESSMENT: <text>
	assessRe := regexp.MustCompile(`(?i)ASSESSMENT:\s*(.+)`)
	if matches := assessRe.FindStringSubmatch(response); len(matches) > 1 {
		assessment = strings.TrimSpace(matches[1])
	}

	return
}

// shouldReflect checks whether reflection should run based on the frequency setting.
func (a *Agent) shouldReflect(iteration int, hadToolError bool) bool {
	if !a.reflection.Enabled {
		return false
	}
	if a.reflection.Mode != "after_tool" && a.reflection.Mode != "both" {
		return false
	}
	switch a.reflection.Frequency {
	case "on_error":
		return hadToolError
	case "every_n":
		if a.reflection.EveryN <= 0 {
			return true
		}
		return (iteration+1)%a.reflection.EveryN == 0
	default: // "always"
		return true
	}
}

// getToolDefinitions returns tool definitions for the LLM (cached after first call).
func (a *Agent) getToolDefinitions() []llm.ToolDefinition {
	a.toolDefsOnce.Do(func() {
		registryDefs := a.toolRegistry.GetToolDefinitions()
		a.cachedToolDefs = make([]llm.ToolDefinition, len(registryDefs))
		for i, def := range registryDefs {
			a.cachedToolDefs[i] = llm.ToolDefinition{
				Name:        def.Function.Name,
				Description: def.Function.Description,
				Parameters:  def.Function.Parameters,
			}
		}
	})
	return a.cachedToolDefs
}

// getToolDefinitionsFor returns tool definitions filtered to the given tool names.
func (a *Agent) getToolDefinitionsFor(toolNames []string) []llm.ToolDefinition {
	allDefs := a.getToolDefinitions()
	nameSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		nameSet[n] = true
	}
	filtered := make([]llm.ToolDefinition, 0, len(toolNames))
	for _, d := range allDefs {
		if nameSet[d.Name] {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// generateWithStreaming uses a StreamingProvider to get token-level output,
// emitting TOKEN_CHUNK events for each piece. Returns the final assembled result.
func (a *Agent) generateWithStreaming(
	ctx context.Context,
	sp llm.StreamingProvider,
	systemPrompt string,
	history []llm.Message,
	toolDefs []llm.ToolDefinition,
	iteration int,
) (*llm.GenerateResult, error) {
	stream, err := sp.GenerateStream(ctx, systemPrompt, history, toolDefs)
	if err != nil {
		return nil, err
	}

	// Read chunks, emitting TOKEN_CHUNK for answer deltas and
	// REASONING_CHUNK for reasoning_content deltas. Reasoning models emit
	// the bulk of their output on the reasoning channel; without the
	// peer event, the agent loop's stream is silent during the longest
	// phase of every request (Claim 1-2 in POSITIONING-AUDIT.md).
	for chunk := range stream.Chunks {
		if chunk.Done {
			break
		}
		if chunk.Text != "" {
			a.eventBus.Emit(a.name, telemetry.EventTokenChunk, telemetry.TokenChunkPayload{
				Text:      chunk.Text,
				AgentName: a.name,
				Iteration: iteration,
			})
		}
		if chunk.Reasoning != "" {
			a.eventBus.Emit(a.name, telemetry.EventReasoningChunk, telemetry.ReasoningChunkPayload{
				Text:      chunk.Reasoning,
				AgentName: a.name,
				Iteration: iteration,
			})
		}
	}

	// Get the final assembled result.
	// Only read from Final — errors are checked when Final yields nil
	// (avoids race where closed errCh returns nil and beats Final in select).
	select {
	case result := <-stream.Final:
		if result == nil {
			// Stream closed without result — check for error
			select {
			case err := <-stream.Err:
				if err != nil {
					return nil, err
				}
			default:
			}
			return nil, fmt.Errorf("stream ended without result for agent %q — the provider closed the connection before returning a response", a.name)
		}

		// Fallback: if streaming produced no tool calls and empty/whitespace content,
		// retry with non-streaming Generate(). Some providers (LiteLLM + vLLM/Qwen3)
		// strip <tool_call> XML tags during streaming but preserve them in non-streaming.
		if len(result.ToolCalls) == 0 && len(toolDefs) > 0 && strings.TrimSpace(result.Response) == "" {
			if plain, ok := sp.(llm.LLMProvider); ok {
				return plain.Generate(ctx, systemPrompt, history, toolDefs)
			}
		}

		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// generateWithRetry wraps LLM calls with exponential-backoff retry for transient errors.
func (a *Agent) generateWithRetry(
	ctx context.Context,
	provider llm.LLMProvider,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolDefinition,
	iteration int,
) (*llm.GenerateResult, error) {
	cfg := a.retryConfig
	if cfg.MaxAttempts == 0 {
		cfg = defaultRetryConfig
	}
	useStreaming := !a.noStreamTools || len(tools) == 0
	sp, canStream := provider.(llm.StreamingProvider)

	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Rate-limit: wait for the next allowed slot before calling the provider.
		if a.rateLimiter != nil {
			if err := a.rateLimiter.Wait(ctx); err != nil {
				return nil, err
			}
		}
		if attempt > 0 {
			a.eventBus.Emit(a.name, telemetry.EventRetryAttempt, telemetry.RetryAttemptPayload{
				Attempt:     attempt,
				MaxAttempts: cfg.MaxAttempts,
				Error:       lastErr.Error(),
				Iteration:   iteration,
			})
			// Rate-limit errors need a longer backoff than network blips.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay(cfg, attempt-1, isRateLimit(lastErr))):
			}
		}

		var result *llm.GenerateResult
		var err error
		if canStream && useStreaming {
			result, err = a.generateWithStreaming(ctx, sp, systemPrompt, history, tools, iteration)
		} else {
			result, err = provider.Generate(ctx, systemPrompt, history, tools)
		}

		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}

	a.eventBus.Emit(a.name, telemetry.EventError, telemetry.ErrorPayload{
		ErrorType:   "llm_error",
		Message:     lastErr.Error(),
		Recoverable: false,
	})
	return nil, fmt.Errorf("LLM error for agent %q after %d attempt(s): %w\n  hint: check API key, network connectivity, and provider rate limits", a.name, cfg.MaxAttempts, lastErr)
}

// executeTool executes a tool call
func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall) (string, error) {
	toolCall := tools.ToolCall{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	}
	return a.toolRegistry.ExecuteToolCall(ctx, toolCall)
}

// convertToolCalls converts LLM tool calls to telemetry format. Carries the
// call's ID through so a replay-reconstructed history (see
// internal/debug/replay.go) can round-trip it into the assistant message's
// llm.ToolCall — without it, the corresponding tool-role response message's
// ToolCallID has nothing matching in the preceding assistant message, which
// breaks providers (OpenAI, Anthropic) that require every tool-role
// message's tool_call_id to match an id in that assistant turn's tool_calls.
func (a *Agent) convertToolCalls(calls []llm.ToolCall) []telemetry.ToolCallSignature {
	result := make([]telemetry.ToolCallSignature, len(calls))
	for i, call := range calls {
		result[i] = telemetry.ToolCallSignature{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		}
	}
	return result
}

// GetName returns the agent's name
func (a *Agent) GetName() string {
	return a.name
}

// GetRole returns the agent's role
func (a *Agent) GetRole() AgentRole {
	return a.role
}

// GetTools returns the agent's tool names
func (a *Agent) GetTools() []string {
	return a.tools
}

// GetSystemPrompt returns the agent's configured system prompt.
func (a *Agent) GetSystemPrompt() string {
	return a.systemPrompt
}

// GetEffectiveToolDefs returns the tool definitions this agent sends to the
// LLM on a normal Run — the same unfiltered registry set built by
// getToolDefinitions(), not a hand-rolled reconstruction. (A debug-controller
// Tools override can narrow this via getToolDefinitionsFor on a live Run,
// but that is a rare debugging path, not the default this accessor reflects.)
func (a *Agent) GetEffectiveToolDefs() []llm.ToolDefinition {
	return a.getToolDefinitions()
}

// LastRunSalvaged reports whether the most recent Run terminated with
// status="salvaged_no_progress" — the format adapter promoted reasoning-only
// content and the ReAct loop exhausted iterations without committing a real
// answer. An outer orchestrator reads this (via the SalvageReporter interface
// in orchestrator.go) to gate re-delegations after a salvaged worker return.
func (a *Agent) LastRunSalvaged() bool {
	return a.lastRunSalvaged.Load()
}

// LastRunUnproductive reports whether the most recent Run terminated without
// producing a usable answer. Strict superset of LastRunSalvaged — covers:
//   - status="salvaged_no_progress" (format adapter promoted reasoning;
//     agent-side recovery loop exhausted)
//   - status="max_iterations" with empty lastResponse (iteration budget
//     burned without committing any partial response)
//   - status="truncated_empty" with empty finalAnswer (no-tool-calls exit on
//     a reasoning model that ran out of output tokens before answering,
//     finish_reason=length; the synthesized marker text is not "empty",
//     but the underlying LLM answer was)
//   - status="success" with empty finalAnswer (no-tool-calls exit on a
//     model that put everything in ThinkingContent or produced nothing for
//     any other finish_reason)
//   - status="budget_exceeded" with empty lastResponse, reached via the
//     iteration-level guard check that returns a nil error (the only
//     budget_exceeded path the orchestrator's cap can observe; the
//     reflection/ground-check budget paths return a non-nil BudgetExceededError
//     and so bail out of the cap path entirely).
//
// In every "empty" case above the check is strings.TrimSpace(...) == "" rather
// than == "" — reasoning models routinely exit with whitespace-only output
// like "\n" that would otherwise slip past a literal-empty comparison and
// mint a non-empty "[budget|max_iterations … — partial result]\n\n\n" marker
// the supervisor mistakes for real progress.
//
// Outer orchestrators read this (via OutcomeReporter in orchestrator.go) to
// apply a per-worker re-delegation cap broader than the salvage-only cap.
func (a *Agent) LastRunUnproductive() bool {
	return a.lastRunUnproductive.Load()
}

// toolCallRunIDKeyType is an unexported context-key type so no other
// package can accidentally collide with or forge this key.
type toolCallRunIDKeyType struct{}

var toolCallRunIDKey = toolCallRunIDKeyType{}

// toolCallRunIDCounter mints unique run IDs for NewToolCallRunContext.
// Package-level (not per-Agent) since a run ID must be unique across every
// *Agent instance a caller might track concurrently, not just one.
var toolCallRunIDCounter atomic.Uint64

// NewToolCallRunContext returns ctx wrapped with a fresh, unique ID for
// this specific call, so ToolCallsForRun can later retrieve exactly this
// invocation's tool calls — even if the same *Agent is (against the usual
// expectation, see toolCallsByRun's field doc) Run concurrently from
// another goroutine. A caller that doesn't need require_tool_call-style
// verification should just pass ctx through unmodified to Run(); tool
// calls are only tracked for run IDs a caller explicitly asked for.
func NewToolCallRunContext(ctx context.Context) (context.Context, uint64) {
	id := toolCallRunIDCounter.Add(1)
	return context.WithValue(ctx, toolCallRunIDKey, id), id
}

// toolCallRunIDFromContext extracts a run ID set by NewToolCallRunContext,
// if any. The second return is false when the caller never opted in — in
// that case RunWithAttachments skips tool-call tracking for this Run
// entirely, so toolCallsByRun never accumulates entries nobody will read.
func toolCallRunIDFromContext(ctx context.Context) (uint64, bool) {
	id, ok := ctx.Value(toolCallRunIDKey).(uint64)
	return id, ok
}

// ToolCallsForRun returns every tool call attempted during the specific Run
// invocation identified by runID (obtained from NewToolCallRunContext and
// passed to Run's ctx), in call order, regardless of whether each call
// succeeded. Outer orchestrators read this (via the ToolCallReporter
// interface in orchestrator.go) to mechanically verify a step actually did
// what its worker agent claims — e.g. a pipeline step's require_tool_call
// gate. Unlike a single "last run" field, keying by an explicit run ID
// keeps this correct even if the same *Agent is reached concurrently
// through independent delegation paths (see toolCallsByRun's field doc).
// The returned slice is a copy; mutating it does not affect the agent.
// The entry is deleted after being read, so toolCallsByRun doesn't grow
// unboundedly across a long-lived agent's many Runs.
func (a *Agent) ToolCallsForRun(runID uint64) []llm.ToolCall {
	a.toolCallsByRunMu.Lock()
	defer a.toolCallsByRunMu.Unlock()
	calls := a.toolCallsByRun[runID]
	delete(a.toolCallsByRun, runID)
	out := make([]llm.ToolCall, len(calls))
	copy(out, calls)
	return out
}
