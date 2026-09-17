package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// stubRunFunc returns a RunFunc that succeeds immediately with the given result.
func stubRunFunc(result string) RunFunc {
	return func(_ context.Context, _ *config.Config, _ *telemetry.EventBus, _ *debug.DebugController, _ string, _ func([]debug.Attachable)) (string, error) {
		return result, nil
	}
}

// panicRunFunc returns a RunFunc that panics.
func panicRunFunc(msg string) RunFunc {
	return func(_ context.Context, _ *config.Config, _ *telemetry.EventBus, _ *debug.DebugController, _ string, _ func([]debug.Attachable)) (string, error) {
		panic(msg)
	}
}

func newTestRunner(fn RunFunc) *AgentRunner {
	bus := telemetry.NewEventBus(64)
	sse := NewSSEServer(bus, "localhost", 0)
	return NewAgentRunner(bus, nil, nil, sse, fn)
}

// ============================================================
// Stale-run reaping and panic-recovery regression tests
// ============================================================

func TestRunner_ReapStaleRun(t *testing.T) {
	runner := newTestRunner(stubRunFunc("ok"))

	// Simulate a stale run: status=running, startTime far in the past, timeout short
	runner.mu.Lock()
	runner.status = "running"
	runner.sessionID = "stale-session"
	runner.startTime = time.Now().Add(-10 * time.Minute)
	runner.runTimeout = 1 * time.Second
	runner.mu.Unlock()

	// reapStaleRun should clear it
	runner.mu.Lock()
	reaped := runner.reapStaleRun()
	runner.mu.Unlock()

	if !reaped {
		t.Error("expected stale run to be reaped")
	}

	runner.mu.Lock()
	status := runner.status
	runner.mu.Unlock()

	if status != "idle" {
		t.Errorf("expected status 'idle' after reap, got %q", status)
	}
}

func TestRunner_ReapStaleRun_NotStaleYet(t *testing.T) {
	runner := newTestRunner(stubRunFunc("ok"))

	// Simulate a run that just started (not stale)
	runner.mu.Lock()
	runner.status = "running"
	runner.sessionID = "fresh-session"
	runner.startTime = time.Now()
	runner.runTimeout = 5 * time.Minute
	runner.mu.Unlock()

	runner.mu.Lock()
	reaped := runner.reapStaleRun()
	runner.mu.Unlock()

	if reaped {
		t.Error("expected fresh run to NOT be reaped")
	}
}

func TestRunner_ReapStaleRun_NotRunning(t *testing.T) {
	runner := newTestRunner(stubRunFunc("ok"))

	runner.mu.Lock()
	reaped := runner.reapStaleRun()
	runner.mu.Unlock()

	if reaped {
		t.Error("expected no reap when status is idle")
	}
}

func TestRunner_PanicRecovery_SetsErrorStatus(t *testing.T) {
	runner := newTestRunner(panicRunFunc("kaboom"))

	// Manually invoke the goroutine logic to test panic recovery.
	// We can't use StartFromPath (needs real config file), so simulate.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner.mu.Lock()
	runner.status = "running"
	runner.sessionID = "panic-test"
	runner.startTime = time.Now()
	runner.runTimeout = 5 * time.Second
	runner.cancel = cancel
	runner.mu.Unlock()

	// Run the panicking function with recovery, like the goroutine does
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				runner.mu.Lock()
				runner.status = "error"
				runner.runError = fmt.Sprintf("agent panic: %v", p)
				runner.mu.Unlock()
			}
		}()
		runner.runFunc(ctx, &config.Config{}, runner.eventBus, nil, "", nil)
	}()

	<-done

	runner.mu.Lock()
	status := runner.status
	errMsg := runner.runError
	runner.mu.Unlock()

	if status != "error" {
		t.Errorf("expected status 'error' after panic, got %q", status)
	}
	if errMsg != "agent panic: kaboom" {
		t.Errorf("expected panic message in error, got %q", errMsg)
	}
}

// TestRunner_StopUnregistersStuckAdapter covers the case where the LLM call
// ignores ctx cancellation so the goroutine never exits — Stop() must still
// remove the adapter from the registry synchronously so the picker updates.
func TestRunner_StopUnregistersStuckAdapter(t *testing.T) {
	runner := newTestRunner(stubRunFunc("ok"))
	reg := session.NewRegistry()
	runner.SetSessionRegistry(reg)

	// Simulate an in-flight run with a registered adapter (don't actually
	// start the goroutine — we're testing Stop's synchronous behavior).
	adapter := &oneShotSessionAdapter{
		id:        "stuck-1",
		configID:  "cfg",
		name:      "Stuck",
		startedAt: time.Now(),
	}
	runner.mu.Lock()
	runner.status = "running"
	runner.sessionID = "stuck-1"
	runner.startTime = time.Now()
	runner.runTimeout = 5 * time.Minute
	runner.currentAdapter = adapter
	runner.mu.Unlock()
	reg.Register(adapter)

	if reg.Len() != 1 {
		t.Fatalf("pre-stop: expected 1 session in registry, got %d", reg.Len())
	}

	runner.Stop()

	if reg.Len() != 0 {
		t.Fatalf("Stop() must unregister adapter synchronously, registry still has %d", reg.Len())
	}
	if _, err := reg.Get("stuck-1"); err == nil {
		t.Fatal("expected ErrNotFound after Stop()")
	}
	runner.mu.Lock()
	status := runner.status
	cur := runner.currentAdapter
	runner.mu.Unlock()
	if status != "idle" {
		t.Errorf("expected status idle after Stop, got %q", status)
	}
	if cur != nil {
		t.Error("expected currentAdapter to be nil after Stop")
	}
}

// TestRunner_ReapStaleRunUnregistersAdapter covers the panic/hang path where
// reapStaleRun is invoked via the next Start(): the stale adapter must not
// linger in the registry.
func TestRunner_ReapStaleRunUnregistersAdapter(t *testing.T) {
	runner := newTestRunner(stubRunFunc("ok"))
	reg := session.NewRegistry()
	runner.SetSessionRegistry(reg)

	adapter := &oneShotSessionAdapter{
		id:        "stale-1",
		configID:  "cfg",
		name:      "Stale",
		startedAt: time.Now().Add(-10 * time.Minute),
	}
	runner.mu.Lock()
	runner.status = "running"
	runner.sessionID = "stale-1"
	runner.startTime = time.Now().Add(-10 * time.Minute)
	runner.runTimeout = 1 * time.Second
	runner.currentAdapter = adapter
	runner.mu.Unlock()
	reg.Register(adapter)

	runner.mu.Lock()
	reaped := runner.reapStaleRun()
	runner.mu.Unlock()

	if !reaped {
		t.Fatal("expected stale run to be reaped")
	}
	if reg.Len() != 0 {
		t.Fatalf("reapStaleRun must unregister adapter, registry still has %d", reg.Len())
	}
}

// TestRunner_StartFromPath_WiresDebugControllerOntoRunner regression-guards:
// Start/StartFromPath built a debugCtrl and passed it to runFunc (and wired
// it into the SSE server), but never assigned it to r.debugCtrl. Every method
// that reads r.debugCtrl — Stop() (resume-on-stop), CurrentDebugController(),
// AttachDebugger* — saw nil during an actively-debugging run: Stop() skipped
// resuming a paused breakpoint (leaving that goroutine blocked forever), and
// an attach call would construct a second, disconnected controller instead
// of returning the one actually driving the run.
func TestRunner_StartFromPath_WiresDebugControllerOntoRunner(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := "name: Test\nversion: \"1.0\"\nagents:\n  - name: Assistant\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	seenCtrl := make(chan *debug.DebugController, 1)
	fn := func(_ context.Context, _ *config.Config, _ *telemetry.EventBus, dc *debug.DebugController, _ string, _ func([]debug.Attachable)) (string, error) {
		seenCtrl <- dc
		return "ok", nil
	}
	runner := newTestRunner(fn)

	if _, err := runner.StartFromPath(context.Background(), cfgPath, "hello", 5, "", true); err != nil {
		t.Fatalf("StartFromPath: %v", err)
	}

	got := runner.CurrentDebugController()
	if got == nil {
		t.Fatal("CurrentDebugController() = nil right after StartFromPath(..., enableDebug=true); r.debugCtrl was never assigned")
	}

	select {
	case passed := <-seenCtrl:
		if passed != got {
			t.Errorf("runFunc was given a different *DebugController than CurrentDebugController() returns — attach/resume would target the wrong instance")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runFunc never invoked")
	}
}
