package main

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/internal/tools/spawn"
)

func spawnTestConfig(enabled bool) *config.Config {
	return &config.Config{
		Settings: config.Settings{
			DefaultProvider: "ollama",
			Providers: map[string]config.ProviderDefinition{
				"ollama": {Type: "ollama", DefaultModel: "llama3.1:8b"},
			},
			Spawn: config.SpawnConfig{Enabled: enabled},
		},
		Agents: []config.AgentDefinition{
			{Name: "Coordinator", Role: "worker", SystemPrompt: "coordinate"},
			{Name: "Researcher", Role: "worker", SystemPrompt: "research"},
		},
	}
}

func newSpawnTestBuilder(t *testing.T, enabled bool) *runtimeBuilder {
	t.Helper()
	cfg := spawnTestConfig(enabled)
	b := &runtimeBuilder{
		ctx:      context.Background(),
		cfg:      cfg,
		eventBus: telemetry.NewEventBus(64),
		agents:   make(map[string]*agent.Agent),
		runners:  make(map[string]agent.Runner),
	}
	if enabled {
		b.spawnState = spawn.NewRunState(cfg.Settings.Spawn.EffectiveMaxConcurrent(), agentTemplateNames(cfg))
	}
	return b
}

func TestRegisterSpawnToolGating(t *testing.T) {
	// disabled → no tool
	b := newSpawnTestBuilder(t, false)
	reg := tools.NewToolRegistry()
	b.registerSpawnTool(reg, "Coordinator", 0)
	if reg.GetTool("spawn_agent") != nil {
		t.Error("spawn_agent registered while disabled")
	}
	// enabled, depth 0 → tool present
	b = newSpawnTestBuilder(t, true)
	reg = tools.NewToolRegistry()
	b.registerSpawnTool(reg, "Coordinator", 0)
	if reg.GetTool("spawn_agent") == nil {
		t.Error("spawn_agent missing at depth 0")
	}
	// enabled, depth == max_depth (default 1) → no tool
	reg = tools.NewToolRegistry()
	b.registerSpawnTool(reg, "Researcher-1", 1)
	if reg.GetTool("spawn_agent") != nil {
		t.Error("spawn_agent registered at max depth")
	}
}

func TestSpawnFactoryUnknownTemplate(t *testing.T) {
	b := newSpawnTestBuilder(t, true)
	_, _, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", TemplateAgent: "Nope", Name: "Nope-1", Parent: "Coordinator", Depth: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "Coordinator, Researcher") {
		t.Fatalf("err = %v, want listing of valid templates", err)
	}
}

func TestSpawnFactoryBuildsChild(t *testing.T) {
	b := newSpawnTestBuilder(t, true)
	// template child
	child, cleanup, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", TemplateAgent: "Researcher", Name: "Researcher-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("template spawn: %v", err)
	}
	defer cleanup()
	if child.GetName() != "Researcher-1" {
		t.Errorf("child name = %q", child.GetName())
	}
	// ad-hoc child
	adhoc, cleanup2, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "y", Name: "subagent-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("ad-hoc spawn: %v", err)
	}
	defer cleanup2()
	if adhoc.GetName() != "subagent-1" {
		t.Errorf("ad-hoc name = %q", adhoc.GetName())
	}
}

// Regression: an ad-hoc child (not in cfg.Agents) spawning its own ad-hoc
// grandchild must still propagate tools/provider/model — cfg.GetAgent(spec.Parent)
// only resolves config-declared agents, so without a spawnedDefs fallback the
// grandchild silently got zero tools whenever max_depth > 1.
func TestSpawnFactoryAdhocGrandchildInheritsTools(t *testing.T) {
	cfg := spawnTestConfig(true)
	cfg.Agents[0].Tools = []string{"some_tool"}
	b := &runtimeBuilder{
		ctx:        context.Background(),
		cfg:        cfg,
		eventBus:   telemetry.NewEventBus(64),
		agents:     make(map[string]*agent.Agent),
		runners:    make(map[string]agent.Runner),
		spawnState: spawn.NewRunState(cfg.Settings.Spawn.EffectiveMaxConcurrent(), agentTemplateNames(cfg)),
	}

	// depth-1 ad-hoc child spawned from the config-declared "Coordinator"
	_, cleanup, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", Name: "subagent-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("depth-1 ad-hoc spawn: %v", err)
	}
	defer cleanup()

	// depth-2 ad-hoc grandchild spawned from "subagent-1" — not in cfg.Agents
	grandchild, cleanup2, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "y", Name: "subagent-1-1", Parent: "subagent-1", Depth: 2,
	})
	if err != nil {
		t.Fatalf("depth-2 ad-hoc spawn: %v", err)
	}
	defer cleanup2()

	toolNames := grandchild.GetTools()
	if len(toolNames) != 1 || toolNames[0] != "some_tool" {
		t.Errorf("grandchild tools = %v, want [some_tool] inherited via spawnedDefs fallback", toolNames)
	}
}

// Regression: getGlobalToolRegistry must build the global tool registry at
// most once per runtimeBuilder — previously every agent build (including every
// spawned child) rebuilt a fresh, throwaway registry, re-initializing (and
// never closing) an MCP subprocess per global mcp_server tool on every call.
func TestGetGlobalToolRegistryMemoized(t *testing.T) {
	b := newSpawnTestBuilder(t, true)
	first := b.getGlobalToolRegistry()
	second := b.getGlobalToolRegistry()
	if first != second {
		t.Error("getGlobalToolRegistry returned a new registry on the second call, want the same memoized instance")
	}
}

// Regression: run.go's standalone construction paths (runAgent single-agent,
// executeConfig) must get spawn wiring too — they bypass BuildRunner.
func TestNewSpawnRuntime(t *testing.T) {
	if sb := newSpawnRuntime(context.Background(), spawnTestConfig(false), telemetry.NewEventBus(8), nil, nil, nil, nil); sb != nil {
		t.Error("newSpawnRuntime should be nil when spawn disabled")
	}
	sb := newSpawnRuntime(context.Background(), spawnTestConfig(true), telemetry.NewEventBus(8), nil, nil, nil, nil)
	if sb == nil {
		t.Fatal("newSpawnRuntime nil while enabled")
	}
	reg := tools.NewToolRegistry()
	sb.registerSpawnTool(reg, "Coordinator", 0)
	if reg.GetTool("spawn_agent") == nil {
		t.Error("spawn_agent not registered via newSpawnRuntime builder")
	}
}

// Regression: spawned children must share the caller's rootGuard, rateLimiters,
// and debugCtrl instances rather than fresh, disconnected ones — otherwise a
// run's token/cost budget and provider rate limits don't actually span the
// parent + its spawned children, and children stay invisible to an attached
// debugger.
func TestNewSpawnRuntimeSharesCallerState(t *testing.T) {
	rootGuard := agent.NewCompositeGuard(nil, agent.NewTokenGuard(1000, 0))
	rateLimiters := map[string]*agent.RateLimiter{"ollama": agent.NewRateLimiter(10)}
	debugCtrl := debug.NewDebugController(telemetry.NewEventBus(8))

	sb := newSpawnRuntime(context.Background(), spawnTestConfig(true), telemetry.NewEventBus(8), nil, rootGuard, rateLimiters, debugCtrl)
	if sb == nil {
		t.Fatal("newSpawnRuntime nil while enabled")
	}
	if sb.rootGuard != rootGuard {
		t.Error("rootGuard not shared with caller's instance — spawned children get a disconnected budget")
	}
	if len(sb.rateLimiters) != 1 || sb.rateLimiters["ollama"] != rateLimiters["ollama"] {
		t.Error("rateLimiters map not shared with caller's instance — spawned children get separate limiter state")
	}
	if sb.opts.DebugCtrl != debugCtrl {
		t.Error("debugCtrl not forwarded — spawned children are invisible to an attached debugger")
	}
}
