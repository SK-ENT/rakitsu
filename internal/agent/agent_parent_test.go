package agent

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestEmitLifecycleCarriesParent(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	ch := bus.Subscribe()

	child := NewAgent(&config.AgentDefinition{Name: "worker-1", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)
	child.SetParent("Coordinator")
	child.emitLifecycle(telemetry.EventAgentStart, telemetry.AgentStartPayload{Role: "worker", ParentAgent: "Coordinator"})
	ev := <-ch
	if ev.ParentID != "Coordinator" {
		t.Errorf("ParentID = %q, want Coordinator", ev.ParentID)
	}

	solo := NewAgent(&config.AgentDefinition{Name: "solo", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)
	solo.emitLifecycle(telemetry.EventAgentEnd, telemetry.AgentEndPayload{Status: "success"})
	ev = <-ch
	if ev.ParentID != "" {
		t.Errorf("ParentID = %q, want empty for non-spawned agent", ev.ParentID)
	}
}
