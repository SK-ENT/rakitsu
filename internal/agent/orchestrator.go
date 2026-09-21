// Package agent provides the agent execution engine with ReAct loop.
package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// Orchestrator manages multiple agents and routes tasks between them
type Orchestrator struct {
	name           string
	strategy       string
	model          string
	systemPrompt   string
	agentNames     []string
	routingConfig  *config.RoutingConfig
	handoffConfig  *config.HandoffConfig
	pipelineConfig *config.PipelineConfig

	llmProvider llm.LLMProvider
	eventBus    *telemetry.EventBus
	agents      map[string]Runner
	debugCtrl   *debug.DebugController
	// steering, when non-nil, is forwarded to the synthetic supervisor Agent
	// built per Run — the orchestrator itself has no ReAct loop, only the
	// supervisor does.
	steering func() []llm.Message

	// Checkpoint support (pipeline mode only)
	checkpoint       *CheckpointData      // non-nil = resume mode; steps here are skipped
	checkpointWriter func(CheckpointData) // called after each successful step; nil = no-op

	// Budget enforcement
	rootGuard    *CompositeGuard // global CompositeGuard shared with all worker agents; nil = no enforcement
	pricing      config.PricingConfig
	pricingKnown bool
}

// workerUnproductiveCap is the maximum number of delegations to the same
// worker allowed AFTER that worker has returned unproductively (salvaged,
// max_iter+empty, or success+empty) in the current supervisor turn.
// 1 = one retry is allowed, the next is blocked.
const workerUnproductiveCap = 1

// SalvageReporter is implemented by Runner-typed workers (currently *Agent)
// that can report whether their most recent Run terminated in a salvaged-
// no-progress state. Strict subset of OutcomeReporter — the orchestrator
// prefers OutcomeReporter when available and falls back to SalvageReporter
// for the salvage-only signal.
type SalvageReporter interface {
	LastRunSalvaged() bool
}

// OutcomeReporter is implemented by Runner-typed workers (currently *Agent)
// that can report whether their most recent Run terminated unproductively —
// salvaged_no_progress, max_iterations+empty, or success+empty. Strict
// superset of SalvageReporter's signal; DelegationTool checks this first
// and falls back to SalvageReporter otherwise.
type OutcomeReporter interface {
	LastRunUnproductive() bool
}

// ToolCallReporter is implemented by Runner-typed workers (currently *Agent)
// that can report every tool call made during a specific Run invocation,
// identified by the run ID a caller attached via NewToolCallRunContext
// before calling Run. The Pipeline strategy's require_tool_call step gate
// uses this to mechanically verify a step's agent actually invoked the tool
// it was told to, instead of trusting the agent's own self-reported final
// answer. Keyed by run ID rather than "the last Run" so this stays correct
// even if the same worker is reached concurrently through independent
// delegation paths — see *Agent's toolCallsByRun field doc for why a
// single shared "last run" slot isn't safe here.
type ToolCallReporter interface {
	ToolCallsForRun(runID uint64) []llm.ToolCall
}

// ToolOutputReporter is implemented by Runner-typed workers (currently
// *Agent) that can report every tool call's actual output made during a
// specific Run invocation. The Pipeline strategy's require_tool_call step
// gate uses this — when configured with output_json_path — to check a
// tool's real JSON response against value bounds, mechanically, instead of
// trusting the agent's own self-reported summary of what the tool returned.
// Same run-ID-keying rationale as ToolCallReporter.
type ToolOutputReporter interface {
	ToolOutputsForRun(runID uint64) []ToolOutput
}

// orchestratorRunState holds the fallback/salvage-tracking state scoped to a
// single runReAct() invocation. This used to live directly on the long-lived
// *Orchestrator (guarded by lastWorkerMu/salvageMu), which was only correct
// for one Run at a time — the same *Orchestrator can be Run concurrently
// (e.g. a nested orchestrator reachable through two independent delegation
// paths). Two concurrent Runs sharing that state would corrupt each other:
// one Run's start-of-turn reset could wipe another in-flight Run's worker
// results and unproductive counts. Allocating a fresh orchestratorRunState
// per runReAct() call and threading it through registerDelegationTools and
// DelegationTool instead keeps each Run's bookkeeping isolated.
type orchestratorRunState struct {
	lastWorkerMu     sync.RWMutex
	lastWorkerResult string

	// Per-Run, per-worker unproductive-outcome tracker. When a worker
	// returns unproductively (LastRunUnproductive()=true —
	// salvaged_no_progress, max_iter+empty, or success+empty), the entry
	// in unproductiveWorkers flips. Subsequent delegations to the same
	// worker in this Run increment postUnproductiveRedelegations[w]; once
	// that count reaches workerUnproductiveCap (default 1), the
	// DelegationTool returns a [DELEGATION BLOCKED] message instead of
	// calling the worker and emits WORKER_REDELEGATION_BLOCKED. Covers both
	// the salvage-only subset and the broader max_iter+empty / success+empty
	// supersets.
	salvageMu                     sync.Mutex
	unproductiveWorkers           map[string]bool
	postUnproductiveRedelegations map[string]int
}

// recordWorkerResult is called by DelegationTool when a worker returns a
// non-empty result. The most recent value is used as a fallback if the
// supervisor's own final answer is empty.
func (s *orchestratorRunState) recordWorkerResult(result string) {
	if strings.TrimSpace(result) == "" {
		return
	}
	s.lastWorkerMu.Lock()
	s.lastWorkerResult = result
	s.lastWorkerMu.Unlock()
}

func (s *orchestratorRunState) getLastWorkerResult() string {
	s.lastWorkerMu.RLock()
	defer s.lastWorkerMu.RUnlock()
	return s.lastWorkerResult
}

// isRedelegationBlocked reports whether a further delegation to the named
// worker should be refused in this Run. Returns true only when the worker
// has previously returned unproductively AND postUnproductiveRedelegations[w]
// has reached workerUnproductiveCap. "Unproductive" covers the strict
// superset of salvaged plus max_iter+empty and success+empty.
func (s *orchestratorRunState) isRedelegationBlocked(worker string) bool {
	s.salvageMu.Lock()
	defer s.salvageMu.Unlock()
	if !s.unproductiveWorkers[worker] {
		return false
	}
	return s.postUnproductiveRedelegations[worker] >= workerUnproductiveCap
}

// postUnproductiveCount returns how many post-unproductive delegations have
// already happened to the named worker in this Run. Used for telemetry
// payloads (the legacy JSON field `post_salvage_count` carries this value
// since salvaged is the original / strict-subset shape).
func (s *orchestratorRunState) postUnproductiveCount(worker string) int {
	s.salvageMu.Lock()
	defer s.salvageMu.Unlock()
	return s.postUnproductiveRedelegations[worker]
}

// recordWorkerOutcome updates the per-worker unproductive-outcome tracker
// AFTER a successful delegate-to-worker call. unproductive indicates whether
// the worker terminated without a usable answer on this call (salvaged,
// max_iter+empty, or success+empty). The bookkeeping:
//   - if unproductiveWorkers[w] was already true: this call was a post-
//     unproductive re-delegation, so increment postUnproductiveRedelegations[w].
//   - if unproductive: mark unproductiveWorkers[w] = true for future calls.
//
// The order (read before, set after) ensures the FIRST unproductive return
// from a worker doesn't count itself as a post-unproductive attempt.
func (s *orchestratorRunState) recordWorkerOutcome(worker string, unproductive bool) {
	s.salvageMu.Lock()
	defer s.salvageMu.Unlock()
	if s.unproductiveWorkers == nil {
		s.unproductiveWorkers = make(map[string]bool)
		s.postUnproductiveRedelegations = make(map[string]int)
	}
	if s.unproductiveWorkers[worker] {
		s.postUnproductiveRedelegations[worker]++
	}
	if unproductive {
		s.unproductiveWorkers[worker] = true
	}
}

// SetDebugController attaches a debug controller for pipeline-level breakpoints.
func (o *Orchestrator) SetDebugController(dc *debug.DebugController) {
	o.debugCtrl = dc
}

// SetSteering forwards the steering drain hook to the synthetic supervisor
// built per Run — the run is steered at its top-level loop only; workers
// never drain (spec §6).
func (o *Orchestrator) SetSteering(f func() []llm.Message) { o.steering = f }

// SetCheckpoint loads a prior checkpoint for resume. Steps present in the
// checkpoint will be skipped and their saved outputs injected into context.
func (o *Orchestrator) SetCheckpoint(cp *CheckpointData) {
	o.checkpoint = cp
}

// SetCheckpointWriter registers a callback invoked after each successful
// pipeline step. The callback receives updated CheckpointData to persist.
func (o *Orchestrator) SetCheckpointWriter(fn func(CheckpointData)) {
	o.checkpointWriter = fn
}

// SetRootGuard attaches the shared budget guard from the caller so the
// supervisor agent's token usage is counted against the global budget.
func (o *Orchestrator) SetRootGuard(g *CompositeGuard, pricing config.PricingConfig, known bool) {
	o.rootGuard = g
	o.pricing = pricing
	o.pricingKnown = known
}

// NewOrchestrator creates a new orchestrator from configuration
// GetName returns the orchestrator's name.
func (o *Orchestrator) GetName() string { return o.name }

// GetRole returns RoleSupervisor for orchestrators.
func (o *Orchestrator) GetRole() AgentRole { return RoleSupervisor }

// GetTools returns the delegation tool names.
func (o *Orchestrator) GetTools() []string { return o.getDelegationToolNames() }

// NewOrchestrator creates a new orchestrator from configuration.
// The agents map may contain both *Agent and *Orchestrator values (nested orchestration).
func NewOrchestrator(
	orchConfig *config.OrchestratorConfig,
	llmProvider llm.LLMProvider,
	eventBus *telemetry.EventBus,
	agents map[string]Runner,
) *Orchestrator {
	return &Orchestrator{
		name:           orchConfig.Name,
		strategy:       orchConfig.Strategy,
		model:          orchConfig.Model,
		systemPrompt:   orchConfig.SystemPrompt,
		agentNames:     orchConfig.Agents,
		routingConfig:  orchConfig.Routing,
		handoffConfig:  orchConfig.Handoff,
		pipelineConfig: orchConfig.Pipeline,
		llmProvider:    llmProvider,
		eventBus:       eventBus,
		agents:         agents,
	}
}

// AgentNames returns the names of sub-agents this orchestrator delegates to,
// in declaration order. Used by the chat layer's `/model` command to enumerate
// swappable workers. Excludes the orchestrator itself.
func (o *Orchestrator) AgentNames() []string {
	out := make([]string, 0, len(o.agentNames))
	out = append(out, o.agentNames...)
	return out
}

// GetAgent returns the sub-agent runner by name (case-sensitive) and a bool
// indicating presence. Used by the chat layer's `/model` command to reach a
// specific worker for runtime model swap. Returns (nil, false) for unknown
// names; returns the orchestrator itself if name matches o.name (so callers
// can target the supervisor with the same API — though Orchestrator's own LLM
// is NOT swappable in this release; type-assert to *Agent first).
func (o *Orchestrator) GetAgent(name string) (Runner, bool) {
	if r, ok := o.agents[name]; ok {
		return r, true
	}
	return nil, false
}

// Run executes the orchestrator with the given query.
// Routes to the appropriate strategy: Pipeline (deterministic) or ReAct (LLM-driven).
func (o *Orchestrator) Run(ctx context.Context, query string) (string, error) {
	switch o.strategy {
	case "Pipeline":
		return o.runPipeline(ctx, query)
	default: // "ReAct", "PlanAndExecute", "Hierarchical"
		return o.runReAct(ctx, query)
	}
}

// runReAct executes the orchestrator using LLM-driven delegation via tool calls.
func (o *Orchestrator) runReAct(ctx context.Context, query string) (string, error) {
	// Run-scoped fallback/salvage-tracking state, fresh per call so
	// concurrent Runs of the same Orchestrator (e.g. a nested orchestrator
	// reachable through two independent delegation paths) don't corrupt
	// each other's worker-result fallback or unproductive-redelegation
	// counts. See orchestratorRunState's doc comment.
	state := &orchestratorRunState{}

	// Delegation tools are registered into a fresh registry per call too,
	// not just a fresh state: a shared, Orchestrator-lifetime registry would
	// let a second concurrent Run's registerDelegationTools overwrite the
	// first Run's delegate_to_X tool object (RegisterTool replaces same-name
	// entries), so the first Run's supervisor would end up executing the
	// second Run's tool — recording outcomes into the wrong run's state even
	// though the state objects themselves are correctly isolated.
	registry := tools.NewToolRegistry()
	o.registerDelegationTools(registry, state)

	// Create a supervisor agent that uses delegation tools
	supervisor := &Agent{
		name:          o.name,
		role:          RoleSupervisor,
		model:         o.model,
		systemPrompt:  o.buildSystemPrompt(),
		tools:         o.getDelegationToolNames(),
		llmProvider:   o.llmProvider,
		toolRegistry:  registry,
		eventBus:      o.eventBus,
		maxIterations: 15, // Higher limit for orchestrator
		contextMon:    NewContextMonitor(config.ContextConfig{}),
		steering:      o.steering,
	}

	// Wire budget guard: supervisor counts against the global budget
	if o.rootGuard != nil {
		supTG := NewTokenGuard(0, 0) // no per-supervisor cap; root enforces global limit
		supervisor.SetTokenGuard(supTG)
		supervisor.SetGuard(NewCompositeGuard(o.rootGuard, supTG))
		supervisor.SetPricing(o.pricing, o.pricingKnown)
	}

	// Run the supervisor
	result, err := supervisor.Run(ctx, query)
	if err != nil {
		return "", err
	}

	// Fallback: synthetic Hierarchical supervisors have only
	// delegate_to_X tools — no path to emit text directly. When the
	// supervisor finishes without producing a final answer (e.g. it
	// delegated successfully and then returned empty thinking), the
	// orchestrator would return "" and the upstream chat agent would
	// retry invoke_config indefinitely. Falling back to the most recent
	// worker output is strictly better than empty: the user gets the
	// answer the worker produced, even if synthesis was missed.
	if strings.TrimSpace(result) == "" {
		if last := state.getLastWorkerResult(); last != "" {
			return last, nil
		}
	}
	return result, nil
}

// registerDelegationTools creates virtual tools for delegating to worker
// agents, wiring each one to the given run-scoped state and registering it
// into the given run-scoped registry. Both must be fresh per runReAct()
// call — a registry shared across concurrent Runs would let one Run's
// registration overwrite another's tool object under the same name.
func (o *Orchestrator) registerDelegationTools(registry *tools.ToolRegistry, state *orchestratorRunState) {
	toolNames := o.resolvedToolNames()
	for _, agentName := range o.agentNames {
		agent, exists := o.agents[agentName]
		if !exists {
			continue
		}

		// Create a delegation tool for this agent
		tool := &DelegationTool{
			agent:      agent,
			eventBus:   o.eventBus,
			handoffCfg: o.handoffConfig,
			fromAgent:  o.name,
			debugCtrl:  o.debugCtrl,
			runState:   state,
			toolName:   toolNames[agentName],
		}

		registry.RegisterTool(tool)
	}
}

// getDelegationToolNames returns the names of all delegation tools. Skips
// any agentNames entry missing from o.agents, matching the existence check
// registerDelegationTools and buildSystemPrompt already apply — otherwise a
// name present in one but not the other would advertise a tool that was
// never actually registered.
func (o *Orchestrator) getDelegationToolNames() []string {
	toolNames := o.resolvedToolNames()
	names := make([]string, 0, len(o.agentNames))
	for _, agentName := range o.agentNames {
		if _, exists := o.agents[agentName]; !exists {
			continue
		}
		names = append(names, "delegate_to_"+toolNames[agentName])
	}
	return names
}

// resolvedToolNames returns each agent's sanitized, collision-free
// delegation-tool-name suffix (without the "delegate_to_" prefix), keyed by
// agent name. Computed fresh from o.agentNames on every call so
// registerDelegationTools, getDelegationToolNames, and buildSystemPrompt —
// which must all agree on the exact same name for a given agent — derive it
// from one shared pass instead of each calling sanitizeToolName
// independently and risking disagreement once a collision is disambiguated.
//
// sanitizeToolName alone isn't collision-free: distinct agent names like
// "foo.bar", "foo_bar", "foo bar", and "Foo.Bar" all sanitize to the same
// "foo_bar". Config validation only rejects exact-duplicate agent names, so
// two such agents pass validation as distinct, then both register under the
// same tool name — ToolRegistry.RegisterTool silently lets the second
// overwrite the first (a log line, no error), leaving the first agent
// permanently unreachable via delegation. A repeated sanitized name here
// instead gets a numeric suffix (_2, _3, ...) so every agent keeps a
// distinct, working tool name — and callers see the collision happen (it
// shows up as an unexpected tool name in the system prompt), rather than an
// agent silently vanishing with no error anywhere.
func (o *Orchestrator) resolvedToolNames() map[string]string {
	resolved := make(map[string]string, len(o.agentNames))
	assigned := make(map[string]bool, len(o.agentNames))
	for _, agentName := range o.agentNames {
		if _, exists := o.agents[agentName]; !exists {
			continue
		}
		base := sanitizeToolName(agentName)
		name := base
		for n := 2; assigned[name]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		assigned[name] = true
		resolved[agentName] = name
	}
	return resolved
}

// buildSystemPrompt builds the system prompt for the supervisor
func (o *Orchestrator) buildSystemPrompt() string {
	var sb strings.Builder

	sb.WriteString(o.systemPrompt)
	sb.WriteString("\n\n## Available Agents\n\n")

	for _, agentName := range o.agentNames {
		agent, exists := o.agents[agentName]
		if !exists {
			continue
		}

		sb.WriteString(fmt.Sprintf("- **%s**: ", agentName))
		sb.WriteString(fmt.Sprintf("Role: %s, Tools: %v\n", agent.GetRole(), agent.GetTools()))
	}

	sb.WriteString("\n## Delegation Tools\n\n")
	sb.WriteString("You have access to delegation tools to assign tasks to worker agents:\n")

	toolNames := o.resolvedToolNames()
	for _, agentName := range o.agentNames {
		if _, exists := o.agents[agentName]; !exists {
			continue
		}
		toolName := "delegate_to_" + toolNames[agentName]
		sb.WriteString(fmt.Sprintf("- `%s`: Delegate a task to %s\n", toolName, agentName))
	}

	sb.WriteString("\n## Instructions\n\n")
	sb.WriteString("1. Analyze the user's request\n")
	sb.WriteString("2. Break down complex tasks into subtasks\n")
	sb.WriteString("3. Delegate subtasks to the most appropriate agent\n")
	sb.WriteString("4. Wait for agent responses before proceeding\n")
	sb.WriteString("5. Synthesize results and provide a comprehensive answer\n")

	return sb.String()
}

// toolNameInvalidChars matches anything outside the character set most LLM
// function-calling APIs (OpenAI, Anthropic, Gemini) allow in a tool name.
var toolNameInvalidChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// sanitizeToolName converts an agent name to a valid tool name. Agent names
// come directly from user-authored YAML with no character-set restriction,
// so beyond lowercasing and swapping spaces for underscores this also
// strips anything else a provider is likely to reject outright (parens,
// dots, unicode, ...), which would otherwise break tool-calling for the
// whole orchestrator turn.
func sanitizeToolName(name string) string {
	lowered := strings.ToLower(strings.ReplaceAll(name, " ", "_"))
	return toolNameInvalidChars.ReplaceAllString(lowered, "_")
}

// ============================================================
// DELEGATION TOOL
// ============================================================

// DelegationTool is a virtual tool that delegates tasks to worker agents or sub-orchestrators
type DelegationTool struct {
	agent      Runner
	eventBus   *telemetry.EventBus
	handoffCfg *config.HandoffConfig
	fromAgent  string
	debugCtrl  *debug.DebugController
	runState   *orchestratorRunState // owning Run's fallback/salvage state; may be nil
	// toolName is the pre-resolved, collision-free suffix (from
	// Orchestrator.resolvedToolNames) to use after "delegate_to_". Left
	// empty by call sites that build a DelegationTool directly rather than
	// through registerDelegationTools (e.g. tests) — GetName falls back to
	// sanitizing the agent's own name for those, same as before this field
	// existed.
	toolName string
}

// GetName returns the tool name
func (t *DelegationTool) GetName() string {
	if t.toolName != "" {
		return "delegate_to_" + t.toolName
	}
	return "delegate_to_" + sanitizeToolName(t.agent.GetName())
}

// GetDescription returns the tool description
func (t *DelegationTool) GetDescription() string {
	return fmt.Sprintf("Delegate a task to the %s agent. Use this when the task requires %s's expertise.",
		t.agent.GetName(), t.agent.GetName())
}

// GetParametersSchema returns the JSON Schema for parameters
func (t *DelegationTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task to delegate to the agent",
			},
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Additional context to help the agent understand the task",
			},
		},
		"required": []string{"task"},
	}
}

// Execute delegates the task to the worker agent
func (t *DelegationTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	task, ok := args["task"].(string)
	if !ok {
		return "", fmt.Errorf("task argument required")
	}

	// Get optional context
	var additionalContext string
	if ctxVal, ok := args["context"].(string); ok {
		additionalContext = ctxVal
	}

	// Build the full task with context
	fullTask := task
	if additionalContext != "" {
		fullTask = fmt.Sprintf("%s\n\nContext: %s", task, additionalContext)
	}

	workerName := t.agent.GetName()

	// Refuse the delegation when the worker has already
	// returned unproductively (salvaged, max_iter+empty, or success+empty)
	// in this supervisor turn AND the post-unproductive cap has been
	// reached. Returns a [DELEGATION BLOCKED] tool output (not an error)
	// so the supervisor's ReAct loop continues normally and the LLM is
	// informed about why this delegation didn't happen — letting it
	// choose a different worker or commit a final answer instead.
	if t.runState != nil && t.runState.isRedelegationBlocked(workerName) {
		count := t.runState.postUnproductiveCount(workerName)
		t.eventBus.Emit(t.fromAgent, telemetry.EventWorkerRedelegationBlocked, telemetry.WorkerRedelegationBlockedPayload{
			FromAgent:        t.fromAgent,
			BlockedWorker:    workerName,
			Reason:           "post-unproductive cap reached",
			PostSalvageCount: count,
		})
		return fmt.Sprintf(
			"[DELEGATION BLOCKED] %s previously returned an unproductive result (salvaged, max_iterations with empty output, or success with empty output) in this turn and has been re-tried %d time(s). Further delegations to %s are blocked. Pick a different worker, or stop and respond to the user directly with what you already know.",
			workerName, count, workerName,
		), nil
	}

	// Debug breakpoint: before agent delegation (ReAct mode)
	if t.debugCtrl != nil {
		if err := t.debugCtrl.Check(ctx, "pre_agent", workerName, 0, nil); err != nil {
			return "", err
		}
	}

	// Emit handoff event
	t.eventBus.Emit(t.fromAgent, telemetry.EventAgentHandoff, telemetry.AgentHandoffPayload{
		FromAgent:   t.fromAgent,
		ToAgent:     workerName,
		Task:        task,
		ContextSize: len(fullTask),
	})

	// Execute the worker agent
	result, err := t.agent.Run(ctx, fullTask)
	if err != nil {
		return "", fmt.Errorf("agent %s failed: %w", workerName, err)
	}

	// Capture this worker's result on the owning Run's state so it can
	// fall back to the latest worker output if the supervisor's own final
	// answer is empty.
	if t.runState != nil {
		t.runState.recordWorkerResult(result)

		// Read the worker's outcome (via OutcomeReporter,
		// the broader signal — falling back to SalvageReporter for the
		// salvage-only subset on workers that don't implement
		// OutcomeReporter yet) and update the per-worker tracker. This is
		// what makes the NEXT delegate_to_<worker> call in this turn
		// either pass through or hit the gate above.
		unproductive := false
		if r, ok := t.agent.(OutcomeReporter); ok {
			unproductive = r.LastRunUnproductive()
		} else if r, ok := t.agent.(SalvageReporter); ok {
			unproductive = r.LastRunSalvaged()
		}
		t.runState.recordWorkerOutcome(workerName, unproductive)
	}

	// Emit message event with the result
	t.eventBus.Emit(workerName, telemetry.EventAgentMessage, telemetry.AgentMessagePayload{
		FromAgent: workerName,
		ToAgent:   t.fromAgent,
		Message:   result,
		Type:      "result",
	})

	return result, nil
}
