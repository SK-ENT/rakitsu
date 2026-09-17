package spawn

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

type fakeRunner struct {
	name    string
	out     string
	err     error
	delay   time.Duration
	running *atomic.Int32
	peak    *atomic.Int32
}

func (f *fakeRunner) Run(ctx context.Context, q string) (string, error) {
	if f.running != nil {
		cur := f.running.Add(1)
		for {
			p := f.peak.Load()
			if cur <= p || f.peak.CompareAndSwap(p, cur) {
				break
			}
		}
		defer f.running.Add(-1)
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return f.out, f.err
}
func (f *fakeRunner) GetName() string                           { return f.name }
func (f *fakeRunner) GetRole() agent.AgentRole                  { return agent.RoleWorker }
func (f *fakeRunner) GetTools() []string                        { return nil }
func (f *fakeRunner) SetDebugController(*debug.DebugController) {}

func newTool(f Factory, cfg config.SpawnConfig, state *RunState) (*Tool, *telemetry.EventBus) {
	bus := telemetry.NewEventBus(64)
	if state == nil {
		state = NewRunState(cfg.EffectiveMaxConcurrent(), []string{"Researcher"})
	}
	return NewTool(f, cfg, "Coordinator", 0, state, bus, []string{"Researcher"}), bus
}

func TestSpawnReturnsChildResult(t *testing.T) {
	var got Spec
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		got = spec
		return &fakeRunner{name: spec.Name, out: "child answer"}, func() {}, nil
	}
	tool, _ := newTool(f, config.SpawnConfig{}, nil)
	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "do X", "name": "helper"})
	if err != nil || out != "child answer" {
		t.Fatalf("Execute = (%q, %v), want (child answer, nil)", out, err)
	}
	if got.Parent != "Coordinator" || got.Depth != 1 || got.Name != "helper-1" {
		t.Errorf("spec = %+v", got)
	}
}

func TestSpawnDescriptionDiscouragesConfirmation(t *testing.T) {
	tool, _ := newTool(nil, config.SpawnConfig{}, nil)
	desc := tool.GetDescription()
	if !strings.Contains(desc, "Do not ask the user for confirmation") {
		t.Errorf("GetDescription() = %q, want it to discourage confirmation-seeking", desc)
	}
}

func TestSpawnEmptyTask(t *testing.T) {
	tool, _ := newTool(nil, config.SpawnConfig{}, nil)
	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "  "})
	if err == nil || !strings.Contains(err.Error(), "non-empty task") {
		t.Fatalf("Execute = (%q, %v), want a real error so the registry can count repeated calls", out, err)
	}
}

func TestSpawnFactoryErrorIsToolResult(t *testing.T) {
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		return nil, nil, context.DeadlineExceeded
	}
	tool, _ := newTool(f, config.SpawnConfig{}, nil)
	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if err == nil || !strings.Contains(err.Error(), "spawn_agent failed") {
		t.Fatalf("Execute = (%q, %v), want a real error", out, err)
	}
	if !strings.Contains(out, "spawn_agent failed") {
		t.Errorf("out = %q, want descriptive message preserved for history", out)
	}
}

func TestSpawnTimeout(t *testing.T) {
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		return &fakeRunner{name: spec.Name, delay: 3 * time.Second}, func() {}, nil
	}
	tool, _ := newTool(f, config.SpawnConfig{TimeoutSeconds: 1}, nil)
	start := time.Now()
	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "slow"})
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("Execute = (%q, %v), want a real error so repeated timeouts trigger the rollback guard", out, err)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Errorf("out = %q, want descriptive message preserved for history", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout not enforced, took %v", time.Since(start))
	}
}

// Regression: a child that runs to completion but returns a genuine error
// (not a timeout) must also surface as a real error — matching invoke_config's
// `return result, err` — so repeated identical child failures are countable.
func TestSpawnChildRunError(t *testing.T) {
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		return &fakeRunner{name: spec.Name, err: fmt.Errorf("provider unreachable")}, func() {}, nil
	}
	tool, _ := newTool(f, config.SpawnConfig{}, nil)
	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if err == nil || !strings.Contains(err.Error(), "provider unreachable") {
		t.Fatalf("Execute = (%q, %v), want a real error wrapping the child's failure", out, err)
	}
	if !strings.Contains(out, "provider unreachable") {
		t.Errorf("out = %q, want descriptive message preserved for history", out)
	}
}

func TestSpawnConcurrencyCap(t *testing.T) {
	var running, peak atomic.Int32
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		return &fakeRunner{name: spec.Name, out: "ok", delay: 50 * time.Millisecond, running: &running, peak: &peak}, func() {}, nil
	}
	state := NewRunState(2, nil)
	tool, _ := newTool(f, config.SpawnConfig{MaxConcurrent: 2}, state)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tool.Execute(context.Background(), map[string]interface{}{"task": "x"}) //nolint:errcheck
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Errorf("peak concurrency %d, want <= 2", p)
	}
}

func TestSpawnUniqueNameSkipsReservedCollision(t *testing.T) {
	// "Researcher-1" is a real configured agent name here, not a spawn-issued
	// one — uniqueName must skip past it rather than returning it verbatim.
	state := NewRunState(4, []string{"Researcher-1"})
	got := state.uniqueName("Researcher")
	if got != "Researcher-2" {
		t.Errorf("uniqueName(%q) = %q, want %q (Researcher-1 collides with a configured agent name)", "Researcher", got, "Researcher-2")
	}
}

func TestSpawnFactoryCtxHasTimeoutDeadline(t *testing.T) {
	var gotDeadline time.Time
	var hadDeadline bool
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		gotDeadline, hadDeadline = ctx.Deadline()
		return &fakeRunner{name: spec.Name, out: "ok"}, func() {}, nil
	}
	tool, _ := newTool(f, config.SpawnConfig{TimeoutSeconds: 5}, nil)
	start := time.Now()
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"task": "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !hadDeadline {
		t.Fatal("factory's context has no deadline — the configured per-child timeout does not bound the factory call, only child.Run")
	}
	want := start.Add(5 * time.Second)
	if diff := gotDeadline.Sub(want); diff < -time.Second || diff > time.Second {
		t.Errorf("factory context deadline = %v, want close to %v", gotDeadline, want)
	}
}

func TestSpawnUniqueNames(t *testing.T) {
	var mu sync.Mutex
	var names []string
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		mu.Lock()
		names = append(names, spec.Name)
		mu.Unlock()
		return &fakeRunner{name: spec.Name, out: "ok"}, func() {}, nil
	}
	tool, _ := newTool(f, config.SpawnConfig{}, nil)
	tool.Execute(context.Background(), map[string]interface{}{"task": "a", "agent": "Researcher"}) //nolint:errcheck
	tool.Execute(context.Background(), map[string]interface{}{"task": "b", "agent": "Researcher"}) //nolint:errcheck
	if len(names) != 2 || names[0] != "Researcher-1" || names[1] != "Researcher-2" {
		t.Errorf("names = %v", names)
	}
}

func TestSpawnEmitsHandoff(t *testing.T) {
	f := func(ctx context.Context, spec Spec) (agent.Runner, func(), error) {
		return &fakeRunner{name: spec.Name, out: "ok"}, func() {}, nil
	}
	tool, bus := newTool(f, config.SpawnConfig{}, nil)
	ch := bus.Subscribe()
	tool.Execute(context.Background(), map[string]interface{}{"task": "x"}) //nolint:errcheck
	select {
	case ev := <-ch:
		if ev.EventType != telemetry.EventAgentHandoff {
			t.Errorf("first event = %s, want AGENT_HANDOFF", ev.EventType)
		}
	case <-time.After(time.Second):
		t.Fatal("no handoff event")
	}
}
