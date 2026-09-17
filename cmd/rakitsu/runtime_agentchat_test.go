package main

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/agentchat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/internal/tools/spawn"
)

// This file covers the directed-agent-chat wiring in cmd/rakitsu, which the
// plan left untested on the stated grounds that "cmd/rakitsu is package main
// and has no test package". That is false — it has five package main test
// files, and runtime_spawn_test.go already constructs a runtimeBuilder
// directly. This is the seam the TUI-level and agentchat-level suites each
// only see one side of.

func agentChatBuilderFor(t *testing.T, cfg *config.Config, roster *agentchat.Roster) *runtimeBuilder {
	t.Helper()
	return &runtimeBuilder{
		ctx:        context.Background(),
		cfg:        cfg,
		eventBus:   telemetry.NewEventBus(64),
		agents:     make(map[string]*agent.Agent),
		runners:    make(map[string]agent.Runner),
		spawnState: spawn.NewRunState(cfg.Settings.Spawn.EffectiveMaxConcurrent(), agentTemplateNames(cfg)),
		agentChat:  roster,
	}
}

func agentHasTool(a *agent.Agent, name string) bool {
	for _, d := range a.GetEffectiveToolDefs() {
		if d.Name == name {
			return true
		}
	}
	return false
}

// TestBuildRunnerRegistersConfigAgentsInTheRoster pins roster construction:
// every config-declared agent must be addressable from the start, whether or
// not it ever runs.
func TestBuildRunnerRegistersConfigAgentsInTheRoster(t *testing.T) {
	cfg := spawnTestConfig(true)
	br, err := BuildRunner(context.Background(), cfg, telemetry.NewEventBus(64), BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	defer br.Cleanup()

	if br.AgentChat == nil {
		t.Fatal("no roster built — settings.agent_chat defaults to on")
	}
	byName := map[string]agentchat.Entry{}
	for _, e := range br.AgentChat.List() {
		byName[e.Name] = e
	}
	for _, name := range []string{"Coordinator", "Researcher"} {
		e, ok := byName[name]
		if !ok {
			t.Fatalf("%s not addressable; roster = %+v", name, br.AgentChat.List())
		}
		if e.Kind != agentchat.KindConfig || e.Status != agentchat.StatusIdle {
			t.Errorf("%s = kind %q status %q, want config/idle", name, e.Kind, e.Status)
		}
	}
}

// TestBuildRunnerSkipsRosterWhenDisabled: settings.agent_chat.enabled false
// must leave the feature entirely unwired, not built-but-hidden.
func TestBuildRunnerSkipsRosterWhenDisabled(t *testing.T) {
	cfg := spawnTestConfig(true)
	off := false
	cfg.Settings.AgentChat.Enabled = &off

	br, err := BuildRunner(context.Background(), cfg, telemetry.NewEventBus(64), BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	defer br.Cleanup()

	if br.AgentChat != nil {
		t.Error("roster built while settings.agent_chat.enabled is false")
	}
}

// TestAgentChatBuilderRemovesSpawnTool pins the RemoveTool("spawn_agent")
// call: a side conversation is a place to ask an agent about its work, not a
// second fan-out surface. The default max_depth of 1 hides this — a depth-1
// rebuild never gets the tool anyway — so the test raises max_depth to 2,
// where the removal is the only thing standing between a side chat and
// spawning children.
func TestAgentChatBuilderRemovesSpawnTool(t *testing.T) {
	cfg := spawnTestConfig(true)
	cfg.Settings.Spawn.MaxDepth = 2
	b := agentChatBuilderFor(t, cfg, nil)

	// Precondition: at max_depth 2 a depth-1 agent DOES normally get the tool,
	// so a passing assertion below cannot be a false positive.
	reg := tools.NewToolRegistry()
	b.registerSpawnTool(reg, "Researcher", 1)
	if reg.GetTool("spawn_agent") == nil {
		t.Fatal("precondition failed: depth 1 under max_depth 2 should get spawn_agent")
	}

	runner, cleanup, err := b.agentChatBuilder()(context.Background(), "Researcher", "direct-Researcher-1")
	if err != nil {
		t.Fatalf("agentChatBuilder: %v", err)
	}
	defer cleanup()

	ag, ok := runner.(*agent.Agent)
	if !ok {
		t.Fatalf("rebuilt runner is %T, want *agent.Agent", runner)
	}
	if agentHasTool(ag, "spawn_agent") {
		t.Error("a rebuilt side-chat agent can spawn children — spawn_agent was not removed")
	}
}

// TestAgentChatBuilderRebuildsASpawnedChildByName: a finished child's
// definition outlives the child, which is what makes talking to it later
// possible at all.
func TestAgentChatBuilderRebuildsASpawnedChildByName(t *testing.T) {
	cfg := spawnTestConfig(true)
	b := agentChatBuilderFor(t, cfg, nil)

	_, cleanup, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", TemplateAgent: "Researcher", Name: "Researcher-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cleanup() // the child dies on the normal schedule; only its definition survives

	runner, cleanup2, err := b.agentChatBuilder()(context.Background(), "Researcher-1", "direct-Researcher-1-1")
	if err != nil {
		t.Fatalf("rebuild of a finished child: %v", err)
	}
	defer cleanup2()
	if runner.GetName() != "Researcher-1" {
		t.Errorf("rebuilt agent name = %q, want Researcher-1", runner.GetName())
	}
}

// TestAgentChatBuilderRejectsUnknownNameWithTemplates: an unknown name must
// say what IS addressable rather than failing blank.
func TestAgentChatBuilderRejectsUnknownName(t *testing.T) {
	b := agentChatBuilderFor(t, spawnTestConfig(true), nil)
	_, _, err := b.agentChatBuilder()(context.Background(), "Nobody", "direct-Nobody-1")
	if err == nil {
		t.Fatal("rebuilding an unknown agent must fail")
	}
	for _, want := range []string{"Coordinator", "Researcher"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name the valid templates", err)
		}
	}
}

// TestSpawnFactoryMarksChildRunningInTheRoster pins half of the sink-install
// block: a running child appears in the picker — marked, and not sendable —
// rather than being invisible until it finishes.
func TestSpawnFactoryMarksChildRunningInTheRoster(t *testing.T) {
	cfg := spawnTestConfig(true)
	roster := agentchat.New(cfg.Settings.AgentChat, nil, nil)
	b := agentChatBuilderFor(t, cfg, roster)

	_, cleanup, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", TemplateAgent: "Researcher", Name: "Researcher-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer cleanup()

	list := roster.List()
	if len(list) != 1 || list[0].Name != "Researcher-1" {
		t.Fatalf("roster = %+v, want a Researcher-1 entry", list)
	}
	if list[0].Status != agentchat.StatusRunning || list[0].Kind != agentchat.KindSpawned {
		t.Errorf("entry = %+v, want running/spawned", list[0])
	}
}

// TestAgentChatBuilderTagsEventsWithTheTurnsOrigin is the seam that makes the
// chat surface able to tell a side chat's events from the main run's at all.
// The rebuilt agent runs on the same bus as the main run and under the same
// name the main run may be delegating to at that moment, so the only honest
// discriminator is a tag applied here, where the source is known.
//
// The agent is run on an already-cancelled context — the cheapest real path
// that still emits, no provider stub needed.
func TestAgentChatBuilderTagsEventsWithTheTurnsOrigin(t *testing.T) {
	cfg := spawnTestConfig(true)
	b := agentChatBuilderFor(t, cfg, nil)
	events := b.eventBus.Subscribe()
	defer b.eventBus.Unsubscribe(events)

	runner, cleanup, err := b.agentChatBuilder()(context.Background(), "Researcher", "direct-Researcher-7")
	if err != nil {
		t.Fatalf("agentChatBuilder: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = runner.RunWithHistory(ctx, "why?", nil)

	var seen int
	for {
		select {
		case ev := <-events:
			seen++
			if ev.Origin != "direct-Researcher-7" {
				t.Fatalf("%s carried Origin %q, want direct-Researcher-7 — an untagged event is indistinguishable from the main run's",
					ev.EventType, ev.Origin)
			}
			continue
		default:
		}
		break
	}
	if seen == 0 {
		t.Fatal("the rebuilt agent emitted nothing — the tag assertion above proved nothing")
	}
}

// TestSpawnFactorySinkRecordsAFailedRunAsFailed is the other half, and the
// end-to-end proof of I1: the sink fires on EVERY exit path, so a child whose
// run errors must reach the roster labelled failed. Before the fix it landed
// as StatusDone and the picker offered it as "done — N turns".
//
// The child is run with an already-cancelled context, which is the cheapest
// real error path (and the same shape as settings.spawn.timeout_seconds
// firing) — no network, no provider stub needed.
func TestSpawnFactorySinkRecordsAFailedRunAsFailed(t *testing.T) {
	cfg := spawnTestConfig(true)
	roster := agentchat.New(cfg.Settings.AgentChat, nil, nil)
	b := agentChatBuilderFor(t, cfg, roster)

	child, cleanup, err := b.spawnFactory()(context.Background(), spawn.Spec{
		Task: "x", TemplateAgent: "Researcher", Name: "Researcher-1", Parent: "Coordinator", Depth: 1,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := child.Run(ctx, "do the thing"); err == nil {
		t.Fatal("a run on a cancelled context should not succeed")
	}

	list := roster.List()
	if len(list) != 1 {
		t.Fatalf("roster = %+v, want one entry", list)
	}
	if list[0].Status != agentchat.StatusFailed {
		t.Errorf("Status = %q, want %q — a child that failed must not be offered as done",
			list[0].Status, agentchat.StatusFailed)
	}
}
