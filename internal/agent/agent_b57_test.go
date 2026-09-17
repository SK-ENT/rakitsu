package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// turnResettableTool is a Tool that also implements tools.TurnResetter,
// counting how many times the agent loop reset it.
type turnResettableTool struct {
	mu     sync.Mutex
	resets int
}

func (t *turnResettableTool) GetName() string        { return "noop" }
func (t *turnResettableTool) GetDescription() string { return "noop tool" }

func (t *turnResettableTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func (t *turnResettableTool) Execute(context.Context, map[string]interface{}) (string, error) {
	return "", nil
}

func (t *turnResettableTool) ResetTurn() {
	t.mu.Lock()
	t.resets++
	t.mu.Unlock()
}

func (t *turnResettableTool) resetCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resets
}

// TestAgentRun_ResetsTurnResetterToolsEachTurn pins the per-turn reset
// wiring: RunWithHistory must call ResetTurn on every registered
// TurnResetter tool at the start of each turn. For the ChatHost agent
// each Run is exactly one user turn, which is how the InvokeTool's
// per-turn loop guard gets cleared.
func TestAgentRun_ResetsTurnResetterToolsEachTurn(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	rt := &turnResettableTool{}
	registry := tools.NewToolRegistry()
	registry.RegisterTool(rt)
	def := &config.AgentDefinition{Name: "ChatHostLike", SystemPrompt: "x", Tools: []string{"noop"}}
	provider := newSequenceProvider(stopResponse("turn 1"), stopResponse("turn 2"))
	a := NewAgent(def, provider, registry, bus, nil)

	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if got := rt.resetCount(); got != 1 {
		t.Errorf("after turn 1: ResetTurn called %d times, want 1", got)
	}

	if _, err := a.Run(context.Background(), "second"); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := rt.resetCount(); got != 2 {
		t.Errorf("after turn 2: ResetTurn called %d times, want 2", got)
	}
}
