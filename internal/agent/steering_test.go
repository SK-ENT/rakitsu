package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// steeringRecorder records every history it is handed and answers final
// immediately — a no-tool-call response ends the ReAct loop (agent.go:852),
// and the steering drain happens at the TOP of iteration 0, before this
// first Generate, so one iteration is all the test needs.
type steeringRecorder struct {
	mu        sync.Mutex
	histories [][]llm.Message
}

func (p *steeringRecorder) Generate(ctx context.Context, systemPrompt string, history []llm.Message, toolDefs []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]llm.Message, len(history))
	copy(cp, history)
	p.histories = append(p.histories, cp)
	return &llm.GenerateResult{Response: "final answer"}, nil
}
func (p *steeringRecorder) GetName() string  { return "steering-fake" }
func (p *steeringRecorder) GetModel() string { return "fake" }

func TestSteeringHookInjectsAtIterationBoundary(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	reg := tools.NewToolRegistry()
	def := &config.AgentDefinition{Name: "w", Role: "worker"}
	p := &steeringRecorder{}
	a := agent.NewAgent(def, p, reg, bus, nil)

	calls := 0
	a.SetSteering(func() []llm.Message {
		calls++
		if calls == 1 {
			return []llm.Message{llm.NewTextMessage("user", "<session_message>steer-now</session_message>")}
		}
		return nil
	})

	if _, err := a.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) == 0 {
		t.Fatal("provider never called")
	}
	found := false
	for _, m := range p.histories[0] {
		if m.Role == "user" && strings.Contains(m.AsText(), "steer-now") {
			found = true
		}
	}
	if !found {
		t.Fatalf("steering message missing from first LLM call history: %+v", p.histories[0])
	}
	if calls < 1 {
		t.Fatal("steering hook never called")
	}
}

func TestNilSteeringIsNoOp(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	reg := tools.NewToolRegistry()
	def := &config.AgentDefinition{Name: "w", Role: "worker"}
	p := &steeringRecorder{}
	a := agent.NewAgent(def, p, reg, bus, nil)
	if _, err := a.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run with nil steering: %v", err)
	}
}
