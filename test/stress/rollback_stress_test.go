//go:build stress

package stress

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/test/stress/mock"
	"github.com/SK-ENT/rakitsu/test/stress/mocktools"
)

// TestRollbackConcurrentSelfCorrection runs N agents concurrently, each forced
// into a dead-end loop — a tool that always fails — with runtime
// self-correction enabled. It asserts every agent terminates (no deadlock /
// runaway loop), no agent exceeds its MaxRollbacks cap, and ROLLBACK events
// actually fire. Run with -race to catch data races in the checkpoint path.
func TestRollbackConcurrentSelfCorrection(t *testing.T) {
	const (
		numAgents    = 50
		maxRollbacks = 3
		maxIter      = 8
		timeout      = 60 * time.Second
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	var completed int64
	var totalRollbacks int64
	violations := make(chan string, numAgents)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			agentName := fmt.Sprintf("StressWorker-%d", id)
			bus := telemetry.NewEventBus(256)

			// Count this agent's ROLLBACK events.
			ch := bus.Subscribe()
			var rollbacks int64
			drained := make(chan struct{})
			go func() {
				for ev := range ch {
					if ev.EventType == telemetry.EventRollback {
						atomic.AddInt64(&rollbacks, 1)
					}
				}
				close(drained)
			}()

			// LoopingToolCall always calls "flaky"; FailingTool always fails —
			// so every iteration dead-ends with a tool_error.
			provider := mock.NewProvider(mock.Config{Scenario: mock.LoopingToolCall("flaky")})
			registry := tools.NewToolRegistry()
			registry.RegisterTool(&mocktools.FailingTool{Name: "flaky", FailureRate: 1.0})

			def := &config.AgentDefinition{
				Name:         agentName,
				Role:         "worker",
				SystemPrompt: "stress",
				Tools:        []string{"flaky"},
				Settings: &config.AgentSettings{
					MaxIterations: maxIter,
					Rollback: config.RollbackConfig{
						Enabled:      true,
						MaxRollbacks: maxRollbacks,
					},
				},
			}
			a := agent.NewAgent(def, provider, registry, bus, nil)

			_, err := a.Run(ctx, "retrieve the flaky value")

			bus.Unsubscribe(ch)
			<-drained

			if err != nil {
				// max_iterations is a graceful (nil-err) exit; a real error fails the test.
				t.Errorf("%s: Run returned error: %v", agentName, err)
				return
			}
			n := atomic.LoadInt64(&rollbacks)
			if n > maxRollbacks {
				violations <- fmt.Sprintf("%s: %d rollbacks exceeds cap %d", agentName, n, maxRollbacks)
			}
			atomic.AddInt64(&totalRollbacks, n)
			atomic.AddInt64(&completed, 1)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timeout: only %d/%d agents completed — possible deadlock or runaway loop",
			atomic.LoadInt64(&completed), numAgents)
	}
	close(violations)

	for v := range violations {
		t.Error(v)
	}
	if c := atomic.LoadInt64(&completed); c != numAgents {
		t.Errorf("completed %d/%d agents", c, numAgents)
	}
	if atomic.LoadInt64(&totalRollbacks) == 0 {
		t.Error("expected ROLLBACK events under a forced dead-end loop, got none")
	}
	t.Logf("%d agents, %d total rollbacks (cap %d each), all terminated cleanly",
		numAgents, atomic.LoadInt64(&totalRollbacks), maxRollbacks)
}
