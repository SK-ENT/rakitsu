package server

import (
	"context"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestSpawnedChildFramesForwarded — a runtime-spawned child's AGENT_START /
// AGENT_END (ParentAgent set) reach the client as "agent_start"/"agent_end"
// frames with the parent link and depth 1; the ROOT agent's own AGENT_START
// (no parent) is dropped as noise.
func TestSpawnedChildFramesForwarded(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			// Root's own AGENT_START — must NOT produce an agent_start frame.
			bus.Emit("a1", telemetry.EventAgentStart, telemetry.AgentStartPayload{Role: "worker", Model: "root-model"})
			bus.EmitWithParent("Researcher-1", telemetry.EventAgentStart, "a1",
				telemetry.AgentStartPayload{Role: "worker", Model: "child-model", ParentAgent: "a1"})
			bus.EmitWithParent("Researcher-1", telemetry.EventAgentEnd, "a1",
				telemetry.AgentEndPayload{Status: "success", TotalTokens: 3000})
			time.Sleep(10 * time.Millisecond) // let forwarder drain
			return "done", nil
		},
		nil,
	))
	col := newCollector(sess)
	if err := sess.Submit("go"); err != nil {
		t.Fatal(err)
	}

	ok := col.waitFor(func(msgs []serverMsg) bool {
		var start, end bool
		for _, m := range msgs {
			if m.Type == "agent_start" && m.AgentName == "Researcher-1" {
				start = true
			}
			if m.Type == "agent_end" && m.AgentName == "Researcher-1" {
				end = true
			}
		}
		return start && end
	}, 2*time.Second)
	if !ok {
		t.Fatalf("expected agent_start + agent_end for Researcher-1, got %+v", col.snapshot())
	}

	msgs := col.snapshot()
	var gotStart, gotEnd bool
	for _, m := range msgs {
		if m.Type == "agent_start" && m.AgentName == "a1" {
			t.Errorf("root agent_start frame leaked: %+v", m)
		}
		if m.Type == "agent_start" && m.AgentName == "Researcher-1" {
			if m.ParentAgent != "a1" || m.Model != "child-model" || m.Depth != 1 {
				t.Errorf("agent_start = %+v, want parent=a1 model=child-model depth=1", m)
			}
			gotStart = true
		}
		if m.Type == "agent_end" && m.AgentName == "Researcher-1" {
			if m.Status != "success" || m.Tokens != 3000 {
				t.Errorf("agent_end = %+v, want status=success tokens=3000", m)
			}
			gotEnd = true
		}
	}
	if !gotStart || !gotEnd {
		t.Fatalf("re-check failed: gotStart=%v gotEnd=%v, msgs=%+v", gotStart, gotEnd, msgs)
	}
}
