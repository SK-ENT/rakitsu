package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

func wakeTestCfg(dir string, enabled bool) *config.Config {
	w := config.WakeConfig{
		Enabled:               enabled,
		IntervalSeconds:       60,
		BackoffFactor:         1.5,
		MinIntervalSeconds:    30,
		MaxIntervalSeconds:    600,
		MaxTurnsPerHour:       6,
		TurnTimeoutSeconds:    1,
		KillSwitchFile:        filepath.Join(dir, "STOP"),
		HeartbeatStaleSeconds: 180,
		ConsecutiveAlarms:     1,
		Checks:                []config.WakeCheck{{Name: "f", Type: "file_contains", Path: "x", Contains: "BAD", TimeoutSeconds: 1}},
	}
	return &config.Config{Name: "t", Agents: []config.AgentDefinition{{Name: "a1"}}, Settings: config.Settings{Wake: w}}
}

// manualAfter returns an After func plus a fire() that releases the pending wait.
func manualAfter() (func(time.Duration) <-chan time.Time, func()) {
	ch := make(chan time.Time, 16)
	return func(time.Duration) <-chan time.Time { return ch }, func() { ch <- time.Time{} }
}

// tickSync coordinates tick completion using a channel.
type tickSync struct {
	done chan struct{}
}

func newTickSync() *tickSync {
	return &tickSync{done: make(chan struct{}, 100)}
}

func (ts *tickSync) wait(numTicks int) bool {
	for i := 0; i < numTicks; i++ {
		select {
		case <-ts.done:
			// tick completed, continue to next
		case <-time.After(5 * time.Second):
			// timeout waiting for tick
			return false
		}
	}
	return true
}

// waitForStop waits for the wake loop to actually stop (including defer cleanup)
func waitForLoopStop(sess *ChatSession, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if !sess.WakeRunning() {
			return true
		}
	}
	return false
}

// testAfterAdapter wraps a FakeClock and returns an After func. Tests advance time
// via clock.Advance to drive pending waits.
func startWakeSession(t *testing.T, cfg *config.Config, bf ChatBuildFunc) *ChatSession {
	t.Helper()
	sess, err := StartChatSession(context.Background(), ChatSessionOptions{ConfigID: "t", Cfg: cfg, BuildFunc: bf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestSubmitWakeDoesNotInterruptAndReportsBusy(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var runs atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		runs.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "ok", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(t.TempDir(), false), bf)
	if _, err := sess.SubmitWake("first", "r"); err != nil { // not gated by session_msg.enabled
		t.Fatalf("SubmitWake: %v", err)
	}
	// Wait for the turn to start
	<-started
	if _, err := sess.SubmitWake("second", "r"); err != nil { // fills the 1-slot queue
		t.Fatalf("second queued: %v", err)
	}
	if _, err := sess.SubmitWake("third", "r"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("want ErrSessionBusy, got %v", err)
	}
	if runs.Load() != 1 {
		t.Fatal("a wake submission must never cancel or restart the running turn")
	}
	close(release)
}

func TestWakeDisabledStartsNoLoop(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, false), makeFakeChatBuildFunc("a1", nil, nil))
	if err := sess.StartWake(WakeOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if sess.WakeRunning() {
		t.Fatal("disabled wake must not run")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("disabled wake must write no files: %v", ents)
	}
}

func TestQuietTicksMakeZeroModelCalls(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
		runs.Add(1)
		return "ok", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Fire 10 ticks manually
	for i := 0; i < 10; i++ {
		fire()
	}
	// Wait for all ticks to complete
	if !ts.wait(10) {
		t.Fatal("ticks did not complete in time")
	}
	// Check audit log
	b, _ := os.ReadFile(filepath.Join(dir, sess.ID+".audit.jsonl"))
	if len(b) == 0 || countLines(b) < 10 {
		t.Fatalf("ticks did not run: %d lines in audit", countLines(b))
	}
	if runs.Load() != 0 {
		t.Fatalf("quiet ticks made %d model calls", runs.Load())
	}
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestChangeInjectsOneWakeTurnWithWakeKind(t *testing.T) {
	dir := t.TempDir()
	var gotQuery atomic.Value
	var runs atomic.Int32
	ran := make(chan struct{}, 4)
	bf := makeFakeChatBuildFunc("a1", func(_ context.Context, q string, _ *telemetry.EventBus) (string, error) {
		gotQuery.Store(q)
		runs.Add(1)
		ran <- struct{}{}
		return "seen", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	col := newCollector(sess)
	after, fire := manualAfter()
	ts := newTickSync()
	var bad atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: bad.Load()}
	}
	_ = sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }})
	// First tick: no alarm
	fire()
	ts.wait(1)
	// Set alarm
	bad.Store(true)
	// Second tick: alarm detected
	fire()
	ts.wait(1)
	// The tick only queues the turn; the session goroutine runs it later.
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("alarm did not inject a turn in time")
	}
	if runs.Load() != 1 {
		t.Fatalf("alarm must inject exactly one turn, got %d", runs.Load())
	}
	if q, _ := gotQuery.Load().(string); q == "" || len(q) < 20 || q[:20] != "[TIMER-SOURCED TURN," {
		t.Fatalf("turn text: %q", q)
	}
	if !col.waitFor(func(ms []serverMsg) bool {
		for _, m := range ms {
			for _, e := range m.Transcript {
				if e.Kind == "wake" {
					return true
				}
			}
		}
		return false
	}, 500*time.Millisecond) {
		t.Fatal("transcript must contain a Kind:\"wake\" entry")
	}
}

func TestEngineStallTimeoutInterruptsWakeTurn(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{}, 1)
	var cancelled atomic.Bool
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		started <- struct{}{}
		<-ctx.Done() // a wedged model: returns only when interrupted
		cancelled.Store(true)
		return "", ctx.Err()
	}, nil)
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.TurnTimeoutSeconds = 1
	sess := startWakeSession(t, cfg, bf)
	after, tickFire := manualAfter()
	turnAfter, turnFire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: true}
	}
	_ = sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, TurnAfter: turnAfter, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }})
	// Trigger first tick -> alarm -> start turn
	tickFire()
	ts.wait(1)
	<-started // Wait for turn to start
	// Fire turn timeout (simulates 1+ second passing)
	turnFire()
	// Wait for cancellation to take effect
	deadline := time.After(5 * time.Second)
	for !cancelled.Load() {
		select {
		case <-deadline:
			t.Fatal("wake turn must be interrupted after turn_timeout_seconds")
		default:
		}
	}
}

func containsStr(b []byte, s string) bool {
	return len(b) >= len(s) && (string(b) != "" && indexOf(string(b), s) >= 0)
}
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestUserSubmitInterruptsWakeTurn(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{}, 1)
	var interrupted atomic.Bool
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		if q != "hello from user" {
			started <- struct{}{}
			<-ctx.Done()
			interrupted.Store(true)
			return "", ctx.Err()
		}
		return "hi", nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, false), bf)
	_, _ = sess.SubmitWake("[TIMER-SOURCED TURN, not from a user] x", "r")
	<-started
	sess.Submit("hello from user")
	// Wait for interrupt to take effect using a channel with timeout
	done := time.After(5 * time.Second)
	for !interrupted.Load() {
		select {
		case <-done:
			t.Fatal("a user Submit must interrupt a running wake turn")
		default:
		}
	}
}

func TestKillSwitchStopsLoopAndResumeNeedsFileRemoved(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	_ = sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }})
	stop := filepath.Join(dir, "STOP")
	_ = os.WriteFile(stop, nil, 0o600)
	fire()
	ts.wait(1)
	// Wait for the loop to actually exit
	if !waitForLoopStop(sess, 5*time.Second) {
		t.Fatal("kill file must stop the loop")
	}
	if err := sess.ResumeWake(); err == nil {
		t.Fatal("resume must fail while the kill file exists")
	}
	_ = os.Remove(stop)
	if err := sess.ResumeWake(); err != nil || !sess.WakeRunning() {
		t.Fatalf("resume after removal: %v running=%v", err, sess.WakeRunning())
	}
}

func TestWakeStopsWhenSessionClosed(t *testing.T) {
	dir := t.TempDir()
	sess, _ := StartChatSession(context.Background(), ChatSessionOptions{ConfigID: "t", Cfg: wakeTestCfg(dir, true), BuildFunc: makeFakeChatBuildFunc("a1", nil, nil)})
	after, fire := manualAfter()
	ts := newTickSync()
	_ = sess.StartWake(WakeOptions{Dir: dir, After: after, Checks: func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }, TickDone: func() { ts.done <- struct{}{} }})
	sess.Close()
	// Fire the after channel to let the loop process the close
	fire()
	ts.wait(1)
	if sess.WakeRunning() {
		t.Fatal("wake loop must stop when the session closes")
	}
}

func TestSummaryLenFromConvMem(t *testing.T) {
	sess := startWakeSession(t, wakeTestCfg(t.TempDir(), false), makeFakeChatBuildFunc("a1", nil, nil))
	if c, cp := sess.SummaryLen(); c != 0 || cp != 0 {
		t.Fatalf("no conversation memory => (0,0), got (%d,%d)", c, cp)
	}
}

// ---- resume reattach (prerequisite bug, spec 10) ----

func newWakeManager(t *testing.T) *ChatManager {
	t.Helper()
	m := NewChatManager(telemetry.NewEventBus(1024), nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	m.SetWakeDir(t.TempDir())
	return m
}

func TestStartResumeReattachesLiveSession(t *testing.T) {
	m := newWakeManager(t)
	live := startWakeSession(t, wakeTestCfg(t.TempDir(), false), makeFakeChatBuildFunc("a1", nil, nil))
	m.mu.Lock()
	m.sessions[live.ID] = live
	m.mu.Unlock()
	got, err := m.Start(context.Background(), "t", "", nil, live.ID)
	if err != nil {
		t.Fatalf("resume of a live session must reattach, got %v", err)
	}
	if got != live {
		t.Fatal("must return the same live session object")
	}
}

func TestConcurrentResumeStillRejected(t *testing.T) {
	m := newWakeManager(t)
	m.mu.Lock()
	m.resuming["sid-1"] = struct{}{}
	m.mu.Unlock()
	if _, err := m.Start(context.Background(), "t", "", nil, "sid-1"); err == nil {
		t.Fatal("a concurrent resume reservation must still be rejected")
	}
}

func TestStartFailsWhenSecretEnvMissing(t *testing.T) {
	cfg := wakeTestCfg(t.TempDir(), true)
	cfg.Settings.Wake.SecretEnv = []string{"WAKE_TEST_TOKEN_NOT_SET"}
	dir := t.TempDir()
	sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
	err := sess.StartWake(WakeOptions{Dir: dir, Getenv: func(string) string { return "" }})
	if !errors.Is(err, wake.ErrMissingSecret) {
		t.Fatalf("want ErrMissingSecret, got %v", err)
	}
	if sess.WakeRunning() {
		t.Fatal("must stay stopped")
	}
}

// ---- StopWake tests ----

func TestStopWakeDuringQuietTick(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// Fire a quiet tick
	fire()
	ts.wait(1)
	// Now stop the wake loop
	if err := sess.StopWake(); err != nil {
		t.Fatalf("StopWake: %v", err)
	}
	// Fire another tick to trigger kill file detection
	fire()
	ts.wait(1)
	// Wait for the loop to actually exit
	if !waitForLoopStop(sess, 5*time.Second) {
		t.Fatal("wake loop must stop after StopWake")
	}
}

func TestStopWakeDuringInflightWakeTurnInterruptsIt(t *testing.T) {
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
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	after, fire := manualAfter()
	ts := newTickSync()
	var alarmSet atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarmSet.Load()}
	}
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	// First tick: no alarm
	fire()
	ts.wait(1)
	// Set alarm to trigger wake turn
	alarmSet.Store(true)
	// Second tick: alarm detected, starts wake turn
	fire()
	ts.wait(1)
	<-started // Wait for wake turn to start
	// Now stop the wake loop - this should interrupt the ongoing turn
	if err := sess.StopWake(); err != nil {
		t.Fatalf("StopWake: %v", err)
	}
	// Wait for interruption to take effect
	done := time.After(5 * time.Second)
	for !interrupted.Load() {
		select {
		case <-done:
			t.Fatal("wake turn must be interrupted when wake loop is stopped")
		default:
		}
	}
}

func TestStopWakeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)
	// Stop twice
	if err := sess.StopWake(); err != nil {
		t.Fatalf("first StopWake: %v", err)
	}
	fire()
	ts.wait(1)
	// Wait for the loop to exit
	for i := 0; i < 100; i++ {
		if !sess.WakeRunning() {
			break
		}
	}
	// Second stop should be no-op (idempotent)
	if err := sess.StopWake(); err != nil {
		t.Fatalf("second StopWake (idempotent): %v", err)
	}
}

func TestResumeAfterStop(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)
	// Stop the wake loop
	if err := sess.StopWake(); err != nil {
		t.Fatalf("StopWake: %v", err)
	}
	fire()
	ts.wait(1)
	// Wait for the loop to actually exit
	if !waitForLoopStop(sess, 5*time.Second) {
		t.Fatal("loop should be stopped")
	}
	// Remove the kill file
	killFile := filepath.Join(dir, "STOP")
	if err := os.Remove(killFile); err != nil {
		t.Fatalf("remove kill file: %v", err)
	}
	// Resume the wake loop
	if err := sess.ResumeWake(); err != nil {
		t.Fatalf("ResumeWake: %v", err)
	}
	if !sess.WakeRunning() {
		t.Fatal("loop must be running after resume")
	}
}

// ---- HTTP handler tests ----

func TestWakeStopHTTPHandler(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)

	// Create SSE server and chat manager
	bus := telemetry.NewEventBus(1024)
	server := NewSSEServer(bus, "localhost", 9100)
	manager := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	manager.mu.Lock()
	manager.sessions[sess.ID] = sess
	manager.mu.Unlock()
	server.chatManager = manager

	// POST to /api/chat/{id}/wake/stop
	req := httptest.NewRequest("POST", "/api/chat/"+sess.ID+"/wake/stop", nil)
	w := httptest.NewRecorder()
	server.handleChatWakeStop(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "stopped" {
		t.Fatalf("expected status=stopped, got %s", resp["status"])
	}

	// Verify the loop stops on the next tick
	fire()
	ts.wait(1)
	// Wait for exit
	if !waitForLoopStop(sess, 5*time.Second) {
		t.Fatal("wake loop must be stopped")
	}
}

func TestWakeResumeHTTPHandler(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)

	// Stop via kill file
	killFile := filepath.Join(dir, "STOP")
	if err := os.WriteFile(killFile, nil, 0o600); err != nil {
		t.Fatalf("create kill file: %v", err)
	}
	fire()
	ts.wait(1)
	// Wait for loop to exit
	for i := 0; i < 100; i++ {
		if !sess.WakeRunning() {
			break
		}
	}

	// Create SSE server and chat manager
	bus := telemetry.NewEventBus(1024)
	server := NewSSEServer(bus, "localhost", 9100)
	manager := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	manager.mu.Lock()
	manager.sessions[sess.ID] = sess
	manager.mu.Unlock()
	server.chatManager = manager

	// Remove kill file first
	if err := os.Remove(killFile); err != nil {
		t.Fatalf("remove kill file: %v", err)
	}

	// POST to /api/chat/{id}/wake/resume
	req := httptest.NewRequest("POST", "/api/chat/"+sess.ID+"/wake/resume", nil)
	w := httptest.NewRecorder()
	server.handleChatWakeResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "resumed" {
		t.Fatalf("expected status=resumed, got %s", resp["status"])
	}

	// Verify the loop resumed
	if !sess.WakeRunning() {
		t.Fatal("wake loop must be running after resume")
	}
}

func TestWakeResumeHTTPHandlerKillFilePresentIs409(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)
	if err := os.WriteFile(filepath.Join(dir, "STOP"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)

	bus := telemetry.NewEventBus(1024)
	server := NewSSEServer(bus, "localhost", 9100)
	manager := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	manager.mu.Lock()
	manager.sessions[sess.ID] = sess
	manager.mu.Unlock()
	server.chatManager = manager

	req := httptest.NewRequest("POST", "/api/chat/"+sess.ID+"/wake/resume", nil)
	w := httptest.NewRecorder()
	server.handleChatWakeResume(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "kill-switch file") {
		t.Fatalf("message must name the kill-switch file: %s", w.Body.String())
	}
}

func TestWakeStopHTTPHandlerNotFound(t *testing.T) {
	bus := telemetry.NewEventBus(1024)
	server := NewSSEServer(bus, "localhost", 9100)
	manager := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	server.chatManager = manager

	// POST to /api/chat/nonexistent/wake/stop
	req := httptest.NewRequest("POST", "/api/chat/nonexistent/wake/stop", nil)
	w := httptest.NewRecorder()
	server.handleChatWakeStop(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

var _ = agent.RoleWorker // keep the import used by helper reuse

func TestStopWakeAndWaitJoinsTurnWatcher(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, _ := manualAfter()
	if err := sess.StartWake(WakeOptions{
		Dir: dir, After: after,
		Checks: func(context.Context, config.WakeCheck) wake.Observation {
			return wake.Observation{Exists: true}
		},
	}); err != nil {
		t.Fatal(err)
	}

	r := sess.wake
	ctx, cancel := context.WithCancel(context.Background())
	// Use the same cancellation signal as the loop for a watcher whose turn
	// and timeout never complete. Shutdown must not depend on either channel.
	loopCancel := r.cancel
	r.cancel = func() { loopCancel(); cancel() }
	started := make(chan struct{})
	exited := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(exited)
		sess.watchWakeTurn(ctx, r, make(chan turnOutcome), time.Hour, func(time.Duration) <-chan time.Time {
			close(started)
			return make(chan time.Time)
		})
	}()
	<-started

	stopped := make(chan struct{})
	go func() {
		sess.StopWakeAndWait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel and join the turn watcher")
	}
	select {
	case <-exited:
	default:
		t.Fatal("shutdown returned before the turn watcher exited")
	}
	if sess.WakeRunning() {
		t.Fatal("shutdown returned before the wake loop exited")
	}
}

func TestStopWakeAndWaitBoundedOnStuckRunner(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	stopCh := make(chan time.Time, 1)
	var stopArmed atomic.Bool
	after := func(d time.Duration) <-chan time.Time {
		if d == wakeStopTimeout {
			stopArmed.Store(true)
			return stopCh
		}
		return make(chan time.Time)
	}
	if err := sess.StartWake(WakeOptions{
		Dir: dir, StopAfter: after,
		Checks: func(context.Context, config.WakeCheck) wake.Observation {
			return wake.Observation{Exists: true}
		},
	}); err != nil {
		t.Fatal(err)
	}
	// A runner that never exits.
	release := make(chan struct{})
	sess.wake.wg.Add(1)
	go func() { defer sess.wake.wg.Done(); <-release }()
	defer close(release)

	res := make(chan error, 1)
	go func() { res <- sess.StopWakeAndWait() }()
	deadline := time.Now().Add(5 * time.Second)
	for !stopArmed.Load() && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	stopCh <- time.Time{} // fire the bound by hand
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("expected timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopWakeAndWait hung on a stuck runner")
	}
	b, _ := os.ReadFile(filepath.Join(dir, sess.wake.eng.SessionID()+".audit.jsonl"))
	if !strings.Contains(string(b), "wake_stop_timeout") {
		t.Fatalf("missing wake_stop_timeout audit line: %s", b)
	}
}

// Once StopWakeAndWait has begun, ResumeWake must not start new goroutines on
// the shared WaitGroup (Add racing Wait, and an uncancelled loop after stop).
func TestResumeWakeRefusedAfterStopWakeAndWait(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	after, _ := manualAfter()
	if err := sess.StartWake(WakeOptions{
		Dir: dir, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)),
		Checks: func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.StopWakeAndWait(); err != nil {
		t.Fatal(err)
	}
	if err := sess.ResumeWake(); err == nil {
		t.Fatal("ResumeWake must be refused after StopWakeAndWait")
	}
	if sess.WakeRunning() {
		t.Fatal("no loop may run after StopWakeAndWait")
	}
}

func TestStopWakeExpandsTildeKillPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.KillSwitchFile = "~/x/KILL"
	sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
	after, fire := manualAfter()
	ts := newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }
	if err := sess.StartWake(WakeOptions{Dir: dir, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)), TickDone: func() { ts.done <- struct{}{} }}); err != nil {
		t.Fatal(err)
	}
	fire()
	ts.wait(1)
	if err := sess.StopWake(); err != nil {
		t.Fatalf("StopWake: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "x", "KILL")); err != nil {
		t.Fatalf("kill file not at expanded path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".", "~")); err == nil {
		t.Fatal("literal ~ directory must not be created")
	}
	fire()
	ts.wait(1)
	for i := 0; i < 200 && sess.WakeRunning(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.WakeRunning() {
		t.Fatal("engine did not see the kill file on next tick")
	}
}
