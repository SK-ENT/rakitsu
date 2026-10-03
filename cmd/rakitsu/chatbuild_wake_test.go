package main

import (
	"context"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// start_task is offered only to a wake session (build ctx carries the handle).
// A started task is built from a ctx without it, so it cannot start tasks.
func TestChatBuildOffersStartTaskOnlyWithHandle(t *testing.T) {
	build := func(ctx context.Context) *agent.Agent {
		runner, _, cleanup, _, _, err := buildChatRunner(ctx, spawnTestConfig(false), telemetry.NewEventBus(64), nil, nil, "s1", "")
		if err != nil {
			t.Fatalf("buildChatRunner: %v", err)
		}
		t.Cleanup(cleanup)
		ag, ok := runner.(*agent.Agent)
		if !ok {
			// orchestrated configs are wrapped; reach any config agent instead
			t.Skipf("runner is %T", runner)
		}
		return ag
	}
	with := build(wake.WithTaskHandle(context.Background(), &wake.TaskHandle{}))
	if !agentHasTool(with, "start_task") || !agentHasTool(with, "get_task_status") {
		t.Fatal("wake session agent must get start_task and get_task_status")
	}
	without := build(wake.MarkTaskContext(context.Background()))
	if agentHasTool(without, "start_task") || agentHasTool(without, "get_task_status") {
		t.Fatal("a started task must not get start_task (no recursion)")
	}
}
