package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

type hbHost struct{ s *ChatSession }

func (h hbHost) StartOrResume(context.Context, string, string, map[string]string, string) (*ChatSession, error) {
	return h.s, nil
}
func (h hbHost) Get(string) *ChatSession { return h.s }

// hbSetup starts a quiet monitor with the default wake config (60/600/180).
// ticks fire the check loop by hand; beats fire the heartbeat timer by hand.
func hbSetup(t *testing.T) (sess *ChatSession, clock *wake.FakeClock, tick, beat func(), ticked *tickSync) {
	t.Helper()
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	sess = startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
	clock = wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	after, tickFn := manualAfter()
	hbAfter, beatFn := manualAfter()
	ticked = newTickSync()
	err := sess.StartWake(WakeOptions{
		Dir: dir, Clock: clock, After: after, HeartbeatAfter: hbAfter, TickDone: func() { ticked.done <- struct{}{} },
		Checks: func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ticked.wait(1) {
		t.Fatal("first tick did not run")
	}
	return sess, clock, tickFn, beatFn, ticked
}

func healthzCode(t *testing.T, m *MonitorManager) int {
	t.Helper()
	s := &SSEServer{}
	s.SetMonitorLister(m)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:1"
	w := httptest.NewRecorder()
	s.handleHealthz(w, req)
	return w.Code
}

func hbManager(sess *ChatSession) *MonitorManager {
	return &MonitorManager{
		host:   hbHost{sess},
		states: map[string]*MonitorState{"m": {ID: "m", SessionID: sess.ID, State: WakeStateOK}},
		order:  []string{"m"},
	}
}

// A healthy monitor that has backed off to max_interval_seconds (600) must stay ok
// under the default heartbeat_stale_seconds (180), in both status and /healthz.
func TestWakeHeartbeat_BackoffDoesNotGoStale(t *testing.T) {
	sess, clock, tick, beat, ticked := hbSetup(t)
	mgr := hbManager(sess)
	// Quiet ticks until the interval reaches 600s (60*1.5^n capped).
	for i := 0; i < 8; i++ {
		clock.Advance(60 * time.Second)
		tick()
		if !ticked.wait(1) {
			t.Fatal("tick did not run")
		}
	}
	// The next check is 600s away. Walk the clock across it in 60s steps; the
	// heartbeat timer fires each step, as it would at min(interval, stale/3)=60s.
	for i := 0; i < 10; i++ {
		clock.Advance(60 * time.Second)
		beat()
		waitHeartbeat(t, sess, clock)
		if st := sess.WakeStatus(); st.State != WakeStateOK {
			t.Fatalf("after %ds quiet: status = %q; want ok", (i+1)*60, st.State)
		}
		if code := healthzCode(t, mgr); code != http.StatusOK {
			t.Fatalf("after %ds quiet: healthz = %d; want 200", (i+1)*60, code)
		}
	}
}

// waitHeartbeat polls until the heartbeat file carries the fake clock's time.
func waitHeartbeat(t *testing.T, sess *ChatSession, clock *wake.FakeClock) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sess.wakeMu.Lock()
		eng := sess.wake.eng
		sess.wakeMu.Unlock()
		if lt := eng.LastHeartbeat(clock.Now()); lt != nil && lt.Equal(time.Unix(clock.Now().Unix(), 0)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("heartbeat was not refreshed")
}

// A truly wedged loop (no heartbeat writes) must still go stale, and healthz must agree.
func TestWakeHeartbeat_WedgedLoopGoesStaleInStatusAndHealthz(t *testing.T) {
	sess, clock, _, _, _ := hbSetup(t)
	mgr := hbManager(sess)
	if code := healthzCode(t, mgr); code != http.StatusOK {
		t.Fatalf("healthz = %d; want 200 while fresh", code)
	}
	clock.Advance(181 * time.Second) // no tick, no beat
	if st := sess.WakeStatus(); st.State != WakeStateStale {
		t.Fatalf("status = %q; want stale", st.State)
	}
	if code := healthzCode(t, mgr); code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d; want 503 (must agree with status)", code)
	}
}

func hbBrokenSetup(t *testing.T) (*ChatSession, *wake.FakeClock) {
	t.Helper()
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	after, _ := manualAfter()
	// A directory where the heartbeat file belongs: writes fail and reads fail.
	if err := os.MkdirAll(filepath.Join(dir, sess.ID+".heartbeat", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	ticked := newTickSync()
	err := sess.StartWake(WakeOptions{
		Dir: dir, Clock: clock, After: after, TickDone: func() { ticked.done <- struct{}{} },
		Checks: func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ticked.wait(1) {
		t.Fatal("first tick did not run")
	}
	return sess, clock
}

// A running loop with no readable heartbeat is starting within the grace, then stale.
func TestWakeHeartbeat_MissingHeartbeatStartingThenStale(t *testing.T) {
	sess, clock := hbBrokenSetup(t)
	mgr := hbManager(sess)
	if st := sess.WakeStatus(); st.State != WakeStateStarting {
		t.Fatalf("status = %q; want starting within grace", st.State)
	}
	if code := healthzCode(t, mgr); code != http.StatusOK {
		t.Fatalf("healthz = %d; want 200 within grace", code)
	}
	clock.Advance(181 * time.Second)
	if st := sess.WakeStatus(); st.State != WakeStateStale {
		t.Fatalf("status = %q; want stale after grace", st.State)
	}
	if code := healthzCode(t, mgr); code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d; want 503 after grace", code)
	}
}

// Normal start: never stale between StartWake and the first heartbeat.
func TestWakeHeartbeat_NoFalseStaleAtStart(t *testing.T) {
	dir := t.TempDir()
	sess := startWakeSession(t, wakeTestCfg(dir, true), makeFakeChatBuildFunc("a1", nil, nil))
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	after, _ := manualAfter()
	if err := sess.StartWake(WakeOptions{Dir: dir, Clock: clock, After: after,
		Checks: func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} }}); err != nil {
		t.Fatal(err)
	}
	if st := sess.WakeStatus(); st.State == WakeStateStale {
		t.Fatalf("status = %q right after start", st.State)
	}
	waitHeartbeat(t, sess, clock)
	if st := sess.WakeStatus(); st.State != WakeStateOK {
		t.Fatalf("status = %q; want ok once heartbeat exists", st.State)
	}
}
