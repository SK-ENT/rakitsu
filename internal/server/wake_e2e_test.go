package server

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// TestWakeE2E_ZeroModelCallsOnUnchangedTicks verifies that unchanged state
// across N ticks produces zero model calls (no agents are invoked).
func TestWakeE2E_ZeroModelCallsOnUnchangedTicks(t *testing.T) {
	dir := t.TempDir()
	var modelCalls atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
		modelCalls.Add(1)
		return "ok", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Fire 20 ticks with no state changes
	for i := 0; i < 20; i++ {
		fire()
	}
	if !ts.wait(20) {
		t.Fatal("ticks did not complete")
	}
	if modelCalls.Load() != 0 {
		t.Fatalf("unchanged ticks must make zero model calls, got %d", modelCalls.Load())
	}
}

// TestWakeE2E_AtLeastOneWakeTurnOnStateChange verifies that at least one
// injected turn is triggered when state changes.
func TestWakeE2E_AtLeastOneWakeTurnOnStateChange(t *testing.T) {
	dir := t.TempDir()
	var modelCalls atomic.Int32
	var lastQuery atomic.Value
	bf := makeFakeChatBuildFunc("a1", func(_ context.Context, q string, _ *telemetry.EventBus) (string, error) {
		modelCalls.Add(1)
		lastQuery.Store(q)
		return "response", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	after, fire := manualAfter()
	ts := newTickSync()
	var alarmState atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarmState.Load()}
	}
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// 5 quiet ticks
	for i := 0; i < 5; i++ {
		fire()
	}
	ts.wait(5)
	// Trigger alarm
	alarmState.Store(true)
	fire()
	ts.wait(1)
	// 5 more ticks with alarm active
	for i := 0; i < 5; i++ {
		fire()
	}
	ts.wait(5)
	if modelCalls.Load() < 1 {
		t.Fatalf("state change must trigger at least 1 model call, got %d", modelCalls.Load())
	}
	q, _ := lastQuery.Load().(string)
	if !containsStr([]byte(q), "[TIMER-SOURCED TURN,") {
		t.Fatalf("injected turn must have wake marker, got: %q", q)
	}
}

// TestWakeE2E_HourlyCapEnforced verifies that max 6 turns per hour (default in wakeTestCfg)
// are actually enforced across simulated time.
func TestWakeE2E_HourlyCapEnforced(t *testing.T) {
	dir := t.TempDir()
	var modelCalls atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
		modelCalls.Add(1)
		return "ok", nil
	}, nil)
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.MaxTurnsPerHour = 3 // lower for testing
	sess := startWakeSession(t, cfg, bf)
	after, fire := manualAfter()
	ts := newTickSync()
	var alarmState atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarmState.Load()}
	}
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: clock, TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Trigger alarms in succession (with no time advancement, all within first tick's minute)
	for i := 0; i < 5; i++ {
		alarmState.Store(true)
		fire()
		ts.wait(1)
		alarmState.Store(false)
		fire()
		ts.wait(1)
	}
	// Should have been capped at 3 calls despite 5 alarms
	if modelCalls.Load() > 3 {
		t.Fatalf("hourly cap of 3 must be enforced, got %d model calls", modelCalls.Load())
	}
}

// TestWakeE2E_KillSwitchStopsImmediately verifies that the kill-switch file
// stops the loop on the next tick.
func TestWakeE2E_KillSwitchStopsImmediately(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Fire a tick
	fire()
	ts.wait(1)
	if !sess.WakeRunning() {
		t.Fatal("loop must be running")
	}
	// Create kill-switch
	killFile := filepath.Join(dir, "STOP")
	if err := os.WriteFile(killFile, nil, 0o600); err != nil {
		t.Fatalf("create kill file: %v", err)
	}
	// Fire another tick - loop should detect kill file and stop
	fire()
	ts.wait(1)
	// Wait for loop to actually exit
	if waitForLoopStop(sess, 5*time.Second) {
		return
	}
	t.Fatal("kill file must stop the loop")
}

// TestWakeE2E_RestartResume verifies that after stopping with
// kill file and restarting, the loop resumes correctly.
func TestWakeE2E_RestartResume(t *testing.T) {
	dir := t.TempDir()
	var modelCalls atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
		modelCalls.Add(1)
		return "ok", nil
	}, nil)
	cfg := wakeTestCfg(dir, true)
	sess := startWakeSession(t, cfg, bf)
	after, fire := manualAfter()
	ts := newTickSync()
	var alarmState atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarmState.Load()}
	}
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: clock, TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Fire quiet tick
	fire()
	ts.wait(1)
	// Kill the loop
	killFile := filepath.Join(dir, "STOP")
	if err := os.WriteFile(killFile, nil, 0o600); err != nil {
		t.Fatalf("create kill file: %v", err)
	}
	fire()
	ts.wait(1)
	// Wait for loop to stop
	if !waitForLoopStop(sess, 5*time.Second) {
		t.Fatal("wake loop must stop")
	}
	// Remove kill file
	if err := os.Remove(killFile); err != nil {
		t.Fatalf("remove kill file: %v", err)
	}
	// Resume the loop
	if err := sess.ResumeWake(); err != nil {
		t.Fatalf("ResumeWake: %v", err)
	}
	// Verify it's running
	if !sess.WakeRunning() {
		t.Fatal("wake loop must be running after resume")
	}
}

// TestWakeE2E_StallTimeoutInterruptsAt180Seconds verifies that a wedged
// turn is interrupted after 180 seconds (3 minutes) of stall time.
func TestWakeE2E_StallTimeoutInterruptsAt180Seconds(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{}, 1)
	var interrupted atomic.Bool
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		if len(q) > 20 && q[:20] == "[TIMER-SOURCED TURN," {
			started <- struct{}{}
			<-ctx.Done()
			interrupted.Store(true)
			return "", ctx.Err()
		}
		return "ok", nil
	}, nil)
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.TurnTimeoutSeconds = 180 // 3 minutes
	sess := startWakeSession(t, cfg, bf)
	after, fire := manualAfter()
	turnAfter, turnFire := manualAfter()
	ts := newTickSync()
	var alarmState atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarmState.Load()}
	}
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, TurnAfter: turnAfter, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Trigger alarm
	alarmState.Store(true)
	fire()
	ts.wait(1)
	<-started // Wait for turn to start
	// Simulate 180+ seconds passing
	turnFire()
	// Wait for interruption
	done := time.After(5 * time.Second)
	for !interrupted.Load() {
		select {
		case <-done:
			t.Fatal("turn must be interrupted after 180s timeout")
		default:
		}
	}
}
