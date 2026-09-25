package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	memtool "github.com/SK-ENT/rakitsu/internal/tools/memory"
	"github.com/SK-ENT/rakitsu/internal/tools/sessionmsg"
	"github.com/SK-ENT/rakitsu/internal/tools/spawn"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// BuildOptions controls runtime construction for a config.
type BuildOptions struct {
	DebugCtrl       *debug.DebugController
	MaxCostOverride float64 // 0 = use cfg

	// PreferredAgentName selects a specific agent in single-agent configs
	// (when no orchestrator is defined). Empty = first agent.
	PreferredAgentName string

	// When non-nil, a user_input tool is auto-registered in EVERY agent's
	// tool registry. Chat mode sets these; one-shot runs leave them nil.
	UserInputReqCh  chan userinput.InputRequest
	UserInputRespCh chan string

	// OnAgentsReady fires after agents are built, before sub-orchestrators.
	// Preserves ordering needed by `rakitsu run` for late debug attach.
	OnAgentsReady func([]*agent.Agent)

	// MemorySessionScope binds the memory tools' "session" scope shorthand to
	// a concrete "session:<id>" scope. Chat surfaces set it; one-shot runs
	// leave it empty (the shorthand then falls back to the project scope).
	MemorySessionScope string

	// SessionID + HubURL enable the send_message / list_sessions tool pair
	// (settings.session_msg): SessionID is this session's own id (sender
	// attribution + reply address), HubURL the server owning /api/sessions/*.
	// Chat surfaces set both; a steerable one-shot run (settings.session_msg
	// enabled + hub-connected) also sets both, since it can be steered mid-run
	// and reply. Plain one-shot runs leave them empty (they exit before any
	// fire-and-forget reply could arrive, so they are neither a sender nor a
	// target).
	SessionID string
	HubURL    string
}

// BuildResult is returned from BuildRunner.
type BuildResult struct {
	Runner   agent.Runner            // top-level entity to Run
	Agents   map[string]*agent.Agent // all agents by name
	Runners  map[string]agent.Runner // agents + sub-orchestrators
	RootOrch *agent.Orchestrator     // nil for single-agent configs
	Cleanup  func()                  // closes MCP subprocesses, etc.

	// SpawnToolFor builds a spawn_agent tool bound to the given parent, or
	// returns nil above max_depth. The field itself is nil when settings.spawn
	// is disabled. Used by the ChatHost, whose registry is built outside the
	// runtime builder.
	SpawnToolFor func(parentName string, depth int) tools.Tool

	// AgentChat is the directed-agent-chat roster: who the user can address
	// directly, and the retained transcripts that make those conversations
	// pick up where the agent left off. Nil when the feature is disabled.
	AgentChat *agentchat.Roster
}

// BuildRunner assembles a top-level agent.Runner from a config.
// Handles single-agent, single-orchestrator, nested orchestrators, and
// Pipeline/ReAct/Hierarchical strategies uniformly.
func BuildRunner(
	ctx context.Context,
	cfg *config.Config,
	eventBus *telemetry.EventBus,
	opts BuildOptions,
) (*BuildResult, error) {
	b := &runtimeBuilder{
		ctx:      ctx,
		cfg:      cfg,
		eventBus: eventBus,
		opts:     opts,
		agents:   make(map[string]*agent.Agent),
		runners:  make(map[string]agent.Runner),
		memStore: openMemoryStore(cfg),
	}
	if cfg.Settings.Spawn.Enabled {
		b.spawnState = spawn.NewRunState(cfg.Settings.Spawn.EffectiveMaxConcurrent(), agentTemplateNames(cfg))
	}

	b.buildRootGuard()

	if err := b.buildAgents(); err != nil {
		b.cleanupAll()
		return nil, err
	}

	if opts.OnAgentsReady != nil {
		agList := make([]*agent.Agent, 0, len(b.agents))
		for _, ag := range b.agents {
			agList = append(agList, ag)
		}
		opts.OnAgentsReady(agList)
	}

	b.buildRunnersMap()

	// Directed agent chat: every config-declared agent is addressable from
	// the start, whether or not it ever runs. Spawned children add
	// themselves as they appear (see spawnFactory).
	if cfg.Settings.AgentChat.EffectiveEnabled() {
		b.agentChat = agentchat.New(cfg.Settings.AgentChat, b.agentChatBuilder(), eventBus)
		for i := range cfg.Agents {
			a := &cfg.Agents[i]
			b.agentChat.AddConfig(a.Name, a.Model, a.Provider)
		}
	}

	result := &BuildResult{
		Agents:    b.agents,
		Runners:   b.runners,
		Cleanup:   b.cleanupAll,
		AgentChat: b.agentChat,
	}
	if b.spawnState != nil {
		result.SpawnToolFor = func(parentName string, depth int) tools.Tool {
			if depth >= b.cfg.Settings.Spawn.EffectiveMaxDepth() {
				return nil
			}
			return spawn.NewTool(b.spawnFactory(), b.cfg.Settings.Spawn, parentName, depth, b.spawnState, b.eventBus, agentTemplateNames(b.cfg))
		}
	}

	// Single-agent config short-circuit
	if cfg.Orchestrator == nil || cfg.Orchestrator.Name == "" {
		ag, err := b.selectSingleAgent()
		if err != nil {
			b.cleanupAll()
			return nil, err
		}
		result.Runner = ag
		return result, nil
	}

	// Orchestrator path
	if err := b.buildSubOrchestrators(); err != nil {
		b.cleanupAll()
		return nil, err
	}
	b.autoIncludeOrphans()
	orch, err := b.buildMainOrchestrator()
	if err != nil {
		b.cleanupAll()
		return nil, err
	}
	result.Runner = orch
	result.RootOrch = orch
	return result, nil
}

// --- internal builder ---

type runtimeBuilder struct {
	ctx      context.Context
	cfg      *config.Config
	eventBus *telemetry.EventBus
	opts     BuildOptions

	rootGuard    *agent.CompositeGuard
	rateLimiters map[string]*agent.RateLimiter
	agents       map[string]*agent.Agent
	runners      map[string]agent.Runner
	agentRegs    []*tools.ToolRegistry // for cleanup
	memStore     *memory.Store         // nil unless settings.memory.enabled

	// globalToolReg is the shared registry backing every agent's global `tools:`
	// lookups (built lazily, once per run — see getGlobalToolRegistry). Rebuilding
	// it per agent would re-init a fresh MCP subprocess per mcp_server global tool
	// and never close the throwaway registry holding it, leaking one subprocess
	// set per agent built — including per spawned child.
	globalToolReg     *tools.ToolRegistry
	globalToolRegOnce sync.Once

	spawnState   *spawn.RunState // nil unless settings.spawn.enabled
	spawnBuildMu sync.Mutex      // serializes child construction from parallel spawn calls

	// spawnedDefs records the resolved AgentDefinition of every spawned child by
	// name, keyed under spawnBuildMu. cfg.GetAgent only resolves config-declared
	// (depth-0) agents, so once max_depth > 1 an ad-hoc child spawning its own
	// ad-hoc grandchild needs this to inherit provider/model/tools instead of
	// silently getting none.
	spawnedDefs map[string]*config.AgentDefinition

	// agentChat is the directed-agent-chat roster, nil when
	// settings.agent_chat.enabled is false. Spawned children register
	// themselves here and hand it their transcript on the way out.
	agentChat *agentchat.Roster
}

func (b *runtimeBuilder) buildRootGuard() {
	effectiveMaxCost := b.cfg.Settings.Execution.MaxCost
	if b.opts.MaxCostOverride > 0 {
		effectiveMaxCost = b.opts.MaxCostOverride
	}
	if b.cfg.Settings.Execution.MaxTotalTokens > 0 || effectiveMaxCost > 0 {
		rootTG := agent.NewTokenGuard(b.cfg.Settings.Execution.MaxTotalTokens, effectiveMaxCost)
		b.rootGuard = agent.NewCompositeGuard(nil, rootTG)
	}
	b.rateLimiters = buildRateLimiters(b.cfg)
}

func (b *runtimeBuilder) buildAgents() error {
	for i := range b.cfg.Agents {
		def := &b.cfg.Agents[i]
		ag, reg, err := b.buildAgent(def)
		if err != nil {
			return err
		}
		b.agents[def.Name] = ag
		b.agentRegs = append(b.agentRegs, reg)
	}
	return nil
}

func (b *runtimeBuilder) buildAgent(def *config.AgentDefinition) (*agent.Agent, *tools.ToolRegistry, error) {
	return b.buildAgentDepth(def, 0)
}

// buildAgentDepth builds one agent at the given spawn depth. Depth 0 is a
// config-declared agent; depth >= 1 is a runtime-spawned child (no user_input
// tool, spawn tool only while below settings.spawn.max_depth).
func (b *runtimeBuilder) buildAgentDepth(def *config.AgentDefinition, depth int) (*agent.Agent, *tools.ToolRegistry, error) {
	reg := b.buildAgentToolRegistryDepth(def, depth)

	provider, err := createAgentProvider(b.ctx, b.cfg, def)
	if err != nil {
		// reg may already hold live tools_inline MCP subprocesses (started
		// above in buildAgentToolRegistryDepth). The caller never sees reg on
		// this path — b.agentRegs.append (buildAgents) and the roster's
		// per-Send cleanup (agentChatBuilder) both happen only after a nil
		// error — so nothing else will ever close it. Close it here instead
		// of leaking it until process exit, which agentChatBuilder can hit
		// repeatedly over a long-running `serve` process.
		reg.CloseAll()
		return nil, nil, fmt.Errorf("cannot create provider for agent %q: %w", def.Name, err)
	}

	ep := createEmbeddingProvider(b.ctx, b.cfg, agentRetrievalConfig(def))
	ag := agent.NewAgent(def, provider, reg, b.eventBus, ep)
	if b.opts.DebugCtrl != nil {
		ag.SetDebugController(b.opts.DebugCtrl)
	}

	// Token guard
	var maxTok int
	var maxCost float64
	if def.Settings != nil {
		maxTok = def.Settings.MaxTotalTokens
		maxCost = def.Settings.MaxCost
	}
	if maxTok > 0 || maxCost > 0 || b.cfg.Settings.Execution.MaxTotalTokens > 0 || b.cfg.Settings.Execution.MaxCost > 0 {
		tg := agent.NewTokenGuard(maxTok, maxCost)
		ag.SetTokenGuard(tg)
		ag.SetGuard(agent.NewCompositeGuard(b.rootGuard, tg))
		pricing, pricingKnown := agent.ResolvePricing(provider.GetModel(), b.cfg.Settings.Pricing)
		ag.SetPricing(pricing, pricingKnown)
	}
	wireAgentRetryAndRateLimit(ag, b.cfg, def, b.rateLimiters)
	wireStorageClaimGuard(ag, b.memStore)
	wireAutoRecall(ag, b.cfg, b.memStore, b.opts.MemorySessionScope, def.Name, b.eventBus)

	return ag, reg, nil
}

// buildAgentToolRegistry creates a per-agent tool registry with:
//   - global tools the agent names in its `tools:` list
//   - inline tools defined in `tools_inline:`
//   - user_input tool (only if chat mode: UserInputReqCh set)
func (b *runtimeBuilder) buildAgentToolRegistry(def *config.AgentDefinition) *tools.ToolRegistry {
	return b.buildAgentToolRegistryDepth(def, 0)
}

func (b *runtimeBuilder) buildAgentToolRegistryDepth(def *config.AgentDefinition, depth int) *tools.ToolRegistry {
	reg := tools.NewToolRegistry()

	// Global tools by name
	globalReg := b.getGlobalToolRegistry()
	for _, toolName := range def.Tools {
		if t := globalReg.GetTool(toolName); t != nil {
			reg.RegisterTool(t)
		}
	}

	// Inline tools — shared implementation in run.go's registerToolDefs,
	// see its comment for why this is one function, not another hand-rolled
	// copy.
	registerToolDefs(b.ctx, reg, b.cfg.Settings.AllowedCommands, def.ToolsInline)

	// user_input tool — only in chat mode, and never for spawned children
	// (a subagent prompting the human would collide with the parent's turn).
	if depth == 0 && b.opts.UserInputReqCh != nil && b.opts.UserInputRespCh != nil {
		reg.RegisterTool(userinput.NewTool(b.opts.UserInputReqCh, b.opts.UserInputRespCh, def.Name, b.eventBus))
	}

	// memory_* tools — only when settings.memory.enabled
	registerMemoryTools(reg, b.cfg, b.memStore, b.opts.MemorySessionScope, def.Name, b.eventBus)

	// spawn_agent — only when settings.spawn.enabled and below max_depth
	b.registerSpawnTool(reg, def.Name, depth)

	// send_message / list_sessions — top-level only (depth 0), when
	// settings.session_msg.enabled. Chat sessions AND steerable one-shot runs
	// get the pair; NewTools no-ops when no HubURL was provided, which keeps
	// standalone runs (--no-hub, failed registration) tool-free.
	if depth == 0 && b.cfg.Settings.SessionMsg.Enabled {
		for _, t := range sessionmsg.NewTools(sessionmsg.Deps{
			HubURL:    b.opts.HubURL,
			SessionID: b.opts.SessionID,
			AgentName: def.Name,
		}) {
			reg.RegisterTool(t)
		}
	}

	return reg
}

// agentTemplateNames lists config-declared agents usable as spawn templates.
func agentTemplateNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
	}
	return names
}

// registerSpawnTool adds spawn_agent when settings.spawn is enabled and the
// agent sits below max_depth. No-op otherwise (tool never visible to the LLM).
func (b *runtimeBuilder) registerSpawnTool(reg *tools.ToolRegistry, parentName string, depth int) {
	sc := b.cfg.Settings.Spawn
	if b.spawnState == nil || depth >= sc.EffectiveMaxDepth() {
		return
	}
	reg.RegisterTool(spawn.NewTool(b.spawnFactory(), sc, parentName, depth, b.spawnState, b.eventBus, agentTemplateNames(b.cfg)))
}

// newSpawnRuntime builds a minimal runtimeBuilder for run.go's standalone
// agent-construction paths (runAgent single-agent, executeConfig), which
// assemble registries outside BuildRunner — the same trap the skills feature
// hit. Returns nil when settings.spawn is disabled. One instance per run:
// the concurrency semaphore and name counter are global to that run.
//
// rootGuard and rateLimiters must be the SAME instances the caller wired onto
// its own top-level agent(s) — spawned children share the parent's token/cost
// budget and provider rate limits rather than getting fresh, disconnected
// ones. debugCtrl (may be nil) is likewise forwarded so spawned children stay
// visible to an attached debugger.
func newSpawnRuntime(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, memStore *memory.Store, rootGuard *agent.CompositeGuard, rateLimiters map[string]*agent.RateLimiter, debugCtrl *debug.DebugController) *runtimeBuilder {
	if !cfg.Settings.Spawn.Enabled {
		return nil
	}
	b := &runtimeBuilder{
		ctx:          ctx,
		cfg:          cfg,
		eventBus:     eventBus,
		opts:         BuildOptions{DebugCtrl: debugCtrl},
		agents:       make(map[string]*agent.Agent),
		runners:      make(map[string]agent.Runner),
		memStore:     memStore,
		rootGuard:    rootGuard,
		rateLimiters: rateLimiters,
	}
	b.spawnState = spawn.NewRunState(cfg.Settings.Spawn.EffectiveMaxConcurrent(), agentTemplateNames(cfg))
	return b
}

// adhocSpawnPrompt is the system prompt for template-less children.
const adhocSpawnPrompt = "You are a focused subagent. Complete the assigned task independently and end with a clear, complete result. Do not ask the user questions — make reasonable assumptions and state them in your answer."

// agentChatBuilder returns the Builder closure the directed-agent-chat roster
// uses to rebuild an agent on demand. Names resolve against the config first,
// then against spawnedDefs — a finished child's definition outlives the child
// itself, which is what makes talking to it later possible.
//
// Built at depth 1 for two reasons: it skips the user_input tool (a side-chat
// agent prompting the human would collide with the main turn), and it matches
// the depth a spawned child actually ran at. spawn_agent is then removed
// outright — a side conversation is a place to ask an agent about its work,
// not a second fan-out surface.
func (b *runtimeBuilder) agentChatBuilder() agentchat.Builder {
	return func(ctx context.Context, name, origin string) (agentchat.HistoryRunner, func(), error) {
		b.spawnBuildMu.Lock()
		defer b.spawnBuildMu.Unlock()

		def := b.cfg.GetAgent(name)
		if def == nil {
			def = b.spawnedDefs[name]
		}
		if def == nil {
			return nil, nil, fmt.Errorf("no config agent and no recorded definition named %q — valid templates: %s",
				name, strings.Join(agentTemplateNames(b.cfg), ", "))
		}
		d := *def
		d.Name = name

		ag, reg, err := b.buildAgentDepth(&d, 1)
		if err != nil {
			return nil, nil, err
		}
		reg.RemoveTool("spawn_agent")
		// Tag everything this rebuild emits with the turn's origin. Without
		// it a side chat's stream and tool calls are indistinguishable from
		// the main run's for an agent of the same name, and the chat surface
		// has to guess — which it did, by name, and blinded the main
		// transcript whenever the main run was delegating to that agent.
		ag.SetEventBus(b.eventBus.WithOrigin(origin))
		return ag, func() { reg.CloseAll() }, nil
	}
}

// spawnFactory returns the Factory closure the spawn tool uses to build
// children at runtime. Construction is serialized (spawnBuildMu) because the
// ReAct loop calls tools from parallel goroutines; execution stays parallel.
func (b *runtimeBuilder) spawnFactory() spawn.Factory {
	return func(ctx context.Context, spec spawn.Spec) (agent.Runner, func(), error) {
		b.spawnBuildMu.Lock()
		defer b.spawnBuildMu.Unlock()

		var def config.AgentDefinition
		if spec.TemplateAgent != "" {
			tpl := b.cfg.GetAgent(spec.TemplateAgent)
			if tpl == nil {
				return nil, nil, fmt.Errorf("no agent named %q in this config — valid templates: %s",
					spec.TemplateAgent, strings.Join(agentTemplateNames(b.cfg), ", "))
			}
			def = *tpl
		} else {
			def = config.AgentDefinition{Role: "worker", SystemPrompt: adhocSpawnPrompt}
			// spec.Parent is either a config-declared agent (depth 0) or a
			// previously spawned child (depth >= 1) — cfg.GetAgent only covers
			// the former, so fall back to the recorded def for the latter.
			parent := b.cfg.GetAgent(spec.Parent)
			if parent == nil {
				parent = b.spawnedDefs[spec.Parent]
			}
			if parent != nil {
				def.Provider = parent.Provider
				def.Model = parent.Model
				def.Tools = append([]string(nil), parent.Tools...)
				def.Settings = parent.Settings
			}
		}
		def.Name = spec.Name
		// Note: ad-hoc children inherit the parent's GLOBAL tool names only;
		// tools_inline is deliberately not copied (re-initializing per-child
		// MCP subprocesses would be expensive and surprising). Template
		// children keep their own full YAML tool set, inline included.

		ag, reg, err := b.buildAgentDepth(&def, spec.Depth)
		if err != nil {
			return nil, nil, err
		}
		if b.spawnedDefs == nil {
			b.spawnedDefs = make(map[string]*config.AgentDefinition)
		}
		defCopy := def
		b.spawnedDefs[spec.Name] = &defCopy
		ag.SetParent(spec.Parent)

		// Directed agent chat: list the child while it runs (marked, not
		// sendable) and capture its transcript on the way out. The sink
		// fires inside RunWithHistory, before the deferred cleanup closes
		// this registry — the agent still dies on schedule, its
		// conversation does not.
		if b.agentChat != nil {
			name := spec.Name
			b.agentChat.MarkRunning(name, agentchat.KindSpawned, def.Model, def.Provider)
			roster := b.agentChat
			// runErr is forwarded, not dropped: the sink fires on every exit
			// path, so a child killed by settings.spawn.timeout_seconds or a
			// provider error would otherwise be recorded — and offered in the
			// picker — as "done".
			ag.SetTranscriptSink(func(h []llm.Message, runErr error) { roster.RecordTranscript(name, h, runErr) })
		}

		return ag, func() { reg.CloseAll() }, nil
	}
}

// openMemoryStore opens the native memory store when settings.memory.enabled.
// Returns nil when disabled or on error (with a stderr warning) — memory is
// an enhancement, never a run blocker. Instances are shared per dir process-
// wide (memory.OpenShared) so concurrent consumers never race file rewrites.
func openMemoryStore(cfg *config.Config) *memory.Store {
	if !cfg.Settings.Memory.Enabled {
		return nil
	}
	store, err := memory.OpenShared(cfg.Settings.Memory.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: memory store init failed: %v (memory tools disabled)\n", err)
		return nil
	}
	return store
}

// wireStorageClaimGuard enables an interim guard when a memory
// store is present (i.e. memory_* tools were registered for this agent), so a
// fabricated "I've stored that" with zero MEMORY_WRITE that turn is caught:
// one corrective retry, then an [UNVERIFIED CLAIM] marker. No-op when store is
// nil, so non-memory configs are unaffected.
func wireStorageClaimGuard(ag *agent.Agent, store *memory.Store) {
	if store == nil {
		return
	}
	ag.SetTurnGuard(agent.NewTurnGuard(agent.TurnGuardConfig{CheckStorageClaims: true}))
}

// registerMemoryTools adds the memory_* tool set to reg. No-op when store is
// nil. The default scope is the config's project_id ("global" when unset).
func registerMemoryTools(reg *tools.ToolRegistry, cfg *config.Config, store *memory.Store, sessionScope, agentName string, bus *telemetry.EventBus) {
	if store == nil {
		return
	}
	for _, t := range memtool.NewTools(memoryDeps(cfg, store, sessionScope, agentName, bus)) {
		reg.RegisterTool(t)
	}
}

// memoryDeps builds the shared memory tool/recaller dependencies for one agent,
// so the memory_* tools and the auto-recall Recaller search identical scopes.
func memoryDeps(cfg *config.Config, store *memory.Store, sessionScope, agentName string, bus *telemetry.EventBus) memtool.Deps {
	return memtool.Deps{
		Store:        store,
		ProjectScope: memory.ProjectScope(cfg.ProjectID),
		SessionScope: sessionScope,
		AgentName:    agentName,
		Bus:          bus,
	}
}

// wireAutoRecall enables Phase-3 Run-start auto-recall injection when memory and
// auto_recall are both enabled. No-op when the store is nil or auto_recall is
// off, so non-memory and opt-out configs are unaffected.
func wireAutoRecall(ag *agent.Agent, cfg *config.Config, store *memory.Store, sessionScope, agentName string, bus *telemetry.EventBus) {
	if store == nil || !cfg.Settings.Memory.AutoRecall.Enabled {
		return
	}
	ar := cfg.Settings.Memory.AutoRecall
	ag.SetMemoryRecaller(memtool.NewRecaller(memoryDeps(cfg, store, sessionScope, agentName, bus), ar.TopK, ar.NodeType, ar.Scopes))
}

func (b *runtimeBuilder) selectSingleAgent() (*agent.Agent, error) {
	if len(b.cfg.Agents) == 0 {
		return nil, fmt.Errorf("no agents defined in config")
	}
	if b.opts.PreferredAgentName != "" {
		if ag, ok := b.agents[b.opts.PreferredAgentName]; ok {
			return ag, nil
		}
		names := make([]string, 0, len(b.agents))
		for n := range b.agents {
			names = append(names, n)
		}
		return nil, fmt.Errorf("agent %q not found. Available: %v", b.opts.PreferredAgentName, names)
	}
	// First agent
	first := b.cfg.Agents[0].Name
	return b.agents[first], nil
}

func (b *runtimeBuilder) buildRunnersMap() {
	for name, ag := range b.agents {
		b.runners[name] = ag
	}
}

func (b *runtimeBuilder) buildSubOrchestrators() error {
	for i := range b.cfg.Orchestrators {
		subCfg := &b.cfg.Orchestrators[i]

		subRunners := make(map[string]agent.Runner)
		for _, name := range subCfg.Agents {
			if r, ok := b.runners[name]; ok {
				subRunners[name] = r
			}
		}

		subModelConfig := subCfg.ModelConfig
		if subModelConfig == nil {
			subModelConfig = &config.ModelConfig{Temperature: 0.3}
		}
		provider, err := createLLMProvider(b.ctx, b.cfg, subCfg.Provider, subCfg.Model, subModelConfig)
		if err != nil {
			return fmt.Errorf("cannot create provider for sub-orchestrator %q: %w", subCfg.Name, err)
		}

		subOrch := agent.NewOrchestrator(subCfg, provider, b.eventBus, subRunners)
		if b.opts.DebugCtrl != nil {
			subOrch.SetDebugController(b.opts.DebugCtrl)
		}
		if b.rootGuard != nil {
			subPricing, subPricingKnown := agent.ResolvePricing(provider.GetModel(), b.cfg.Settings.Pricing)
			subOrch.SetRootGuard(b.rootGuard, subPricing, subPricingKnown)
		}

		b.runners[subCfg.Name] = subOrch
	}
	return nil
}

// autoIncludeOrphans mutates cfg.Orchestrator.Agents to include unclaimed
// agents and sub-orchestrators. Preserves the semantics of run.go's
// runMultiAgent so ReAct delegation tools are created for all entities.
func (b *runtimeBuilder) autoIncludeOrphans() {
	if b.cfg.Orchestrator == nil {
		return
	}

	claimed := make(map[string]bool)
	for _, name := range b.cfg.Orchestrator.Agents {
		claimed[name] = true
	}
	for _, sub := range b.cfg.Orchestrators {
		for _, name := range sub.Agents {
			claimed[name] = true
		}
	}

	existing := make(map[string]bool, len(b.cfg.Orchestrator.Agents))
	for _, name := range b.cfg.Orchestrator.Agents {
		existing[name] = true
	}

	// Auto-add sub-orchestrators to main orchestrator
	for _, sub := range b.cfg.Orchestrators {
		if !existing[sub.Name] {
			b.cfg.Orchestrator.Agents = append(b.cfg.Orchestrator.Agents, sub.Name)
			existing[sub.Name] = true
		}
	}

	// Auto-add orphaned agents to main orchestrator
	for _, ag := range b.cfg.Agents {
		if !claimed[ag.Name] && !existing[ag.Name] {
			b.cfg.Orchestrator.Agents = append(b.cfg.Orchestrator.Agents, ag.Name)
			existing[ag.Name] = true
		}
	}
}

func (b *runtimeBuilder) buildMainOrchestrator() (*agent.Orchestrator, error) {
	orchCfg := b.cfg.Orchestrator
	modelConfig := orchCfg.ModelConfig
	if modelConfig == nil {
		modelConfig = &config.ModelConfig{Temperature: 0.3}
	}
	provider, err := createLLMProvider(b.ctx, b.cfg, orchCfg.Provider, orchCfg.Model, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("cannot create orchestrator provider %q: %w", orchCfg.Provider, err)
	}

	orch := agent.NewOrchestrator(orchCfg, provider, b.eventBus, b.runners)
	if b.opts.DebugCtrl != nil {
		orch.SetDebugController(b.opts.DebugCtrl)
	}
	if b.rootGuard != nil {
		orchPricing, orchPricingKnown := agent.ResolvePricing(provider.GetModel(), b.cfg.Settings.Pricing)
		orch.SetRootGuard(b.rootGuard, orchPricing, orchPricingKnown)
	}
	return orch, nil
}

func (b *runtimeBuilder) cleanupAll() {
	for _, reg := range b.agentRegs {
		reg.CloseAll()
	}
	if b.globalToolReg != nil {
		b.globalToolReg.CloseAll()
	}
}

// getGlobalToolRegistry returns the registry with global tools built from cfg,
// building it at most once per runtimeBuilder (per run) — see globalToolReg.
// Identical tool set to createToolRegistry in run.go — reused here to avoid
// requiring callers to pre-build one.
func (b *runtimeBuilder) getGlobalToolRegistry() *tools.ToolRegistry {
	b.globalToolRegOnce.Do(func() {
		b.globalToolReg = createToolRegistry(b.ctx, b.cfg)
	})
	return b.globalToolReg
}
