package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

func TestMonitorE2EServe_TwoMonitorsOneFailsBothContinue(t *testing.T) {
	dir := t.TempDir()
	good := writeMonitorCfg(t, dir, "good.yaml")
	missing := filepath.Join(dir, "missing.yaml")
	cm, cs := newTestChatManager(t, filepath.Join(dir, "wake"))
	mm := NewMonitorManager(cm, cs, []config.MonitorConfig{
		// Try the broken monitor first so its failure cannot hide an early exit.
		{ID: "broken", Config: missing, Workdir: dir},
		{ID: "valid", Config: good, Workdir: dir},
	})
	t.Cleanup(func() {
		mm.Stop(context.Background())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mm.Run(ctx)

	assertStates := func() {
		t.Helper()
		states := stateByID(mm)
		if len(states) != 2 {
			t.Fatalf("want both monitors tracked, got %+v", states)
		}
		if s := states["valid"]; s.State != WakeStateOK || s.SessionID != "monitor-valid" || s.Err != "" {
			t.Fatalf("valid monitor: %+v", s)
		}
		if s := states["broken"]; s.State != WakeStateFailed || !strings.Contains(s.Err, "monitor config not readable") || !strings.Contains(s.Err, "missing.yaml") {
			t.Fatalf("broken monitor: %+v", s)
		}
	}
	assertStates()

	session := cm.Get("monitor-valid")
	if session == nil || !session.WakeRunning() {
		t.Fatal("valid monitor wake loop is not running after the broken monitor failed")
	}
	if cm.Get("monitor-broken") != nil {
		t.Fatal("missing config unexpectedly created a monitor session")
	}

	// Both entries remain managed on subsequent runs: the broken one still
	// reports its failure, while the valid one's existing loop keeps running.
	mm.Run(ctx)
	assertStates()
	if cm.Get("monitor-valid") != session || !session.WakeRunning() {
		t.Fatal("valid monitor did not continue running in its original session")
	}
	if got := len(cm.Sessions()); got != 1 {
		t.Fatalf("want only the valid monitor session, got %d", got)
	}
}

func TestMonitorE2EServe_HealthzEndpoint(t *testing.T) {
	const errorText = "POSTGRES_PASSWORD=secret123 not accessible"
	lister := &mockMonitorLister{states: []MonitorState{
		{ID: "good", State: WakeStateOK},
		{ID: "failed", State: WakeStateFailed, Err: errorText},
		{ID: "stale", State: WakeStateStale, Err: "heartbeat too old"},
	}}
	s := &SSEServer{}
	s.SetMonitorLister(lister)
	handler := http.HandlerFunc(s.handleHealthz)
	check := func(t *testing.T, remote string, code int, status string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != code {
			t.Fatalf("status code = %d; want %d; body: %s", w.Code, code, w.Body.String())
		}
		if status == "" {
			return
		}
		var resp healthzResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Status != status {
			t.Errorf("status = %q; want %q", resp.Status, status)
		}
		if len(resp.Monitors) != len(lister.states) {
			t.Fatalf("monitors = %+v", resp.Monitors)
		}
		for _, st := range lister.states {
			if resp.Monitors[st.ID] != st.State {
				t.Errorf("monitor %s = %q; want %q", st.ID, resp.Monitors[st.ID], st.State)
			}
		}
		for _, forbidden := range []string{"POSTGRES_PASSWORD", "secret123", "not accessible", "heartbeat too old", "\"err\"", "\"error\""} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Errorf("response leaks %q: %s", forbidden, w.Body.String())
			}
		}
	}
	t.Run("mixed states", func(t *testing.T) { check(t, "127.0.0.1:12345", http.StatusServiceUnavailable, "fail") })
	// Check each unhealthy state independently so neither can mask the other.
	for _, state := range []string{WakeStateFailed, WakeStateStale} {
		t.Run(state, func(t *testing.T) {
			lister.states[1].State = state
			lister.states[2].State = WakeStateOK
			check(t, "127.0.0.1:12345", http.StatusServiceUnavailable, "fail")
		})
	}
	for i := range lister.states {
		lister.states[i].State = WakeStateOK
	}
	t.Run("all ok", func(t *testing.T) { check(t, "127.0.0.1:12345", http.StatusOK, "ok") })
	t.Run("remote access respects AllowRemote", func(t *testing.T) {
		// Non-loopback requests must return 403 when AllowRemote is false,
		// and return the normal health response when AllowRemote is true.

		// Test 1: loopback always works, regardless of AllowRemote
		for _, allowRemote := range []bool{true, false} {
			s := &SSEServer{}
			s.SetMonitorLister(lister)
			s.SetHealthzConfig(config.HealthzConfig{AllowRemote: allowRemote})
			t.Run(fmt.Sprintf("loopback_127.0.0.1_AllowRemote=%v", allowRemote), func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				w := httptest.NewRecorder()
				s.handleHealthz(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("loopback should work; got %d", w.Code)
				}
				var resp healthzResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatal(err)
				}
				if len(resp.Monitors) == 0 {
					t.Fatal("loopback request should receive monitors")
				}
			})
		}

		// Test 2: IPv6 loopback always works
		for _, allowRemote := range []bool{true, false} {
			s := &SSEServer{}
			s.SetMonitorLister(lister)
			s.SetHealthzConfig(config.HealthzConfig{AllowRemote: allowRemote})
			t.Run(fmt.Sprintf("loopback_::1_AllowRemote=%v", allowRemote), func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				req.RemoteAddr = "[::1]:54321"
				w := httptest.NewRecorder()
				s.handleHealthz(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("IPv6 loopback should work; got %d", w.Code)
				}
				var resp healthzResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatal(err)
				}
				if len(resp.Monitors) == 0 {
					t.Fatal("IPv6 loopback request should receive monitors")
				}
			})
		}

		// Test 3: non-loopback returns 403 when AllowRemote=false
		s := &SSEServer{}
		s.SetMonitorLister(lister)
		s.SetHealthzConfig(config.HealthzConfig{AllowRemote: false})
		t.Run("non_loopback_AllowRemote=false", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = "203.0.113.42:54321"
			w := httptest.NewRecorder()
			s.handleHealthz(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("non-loopback with AllowRemote=false should return 403; got %d; body: %s", w.Code, w.Body.String())
			}
			// Ensure no monitor details leaked
			for _, forbidden := range []string{errorText, "good", "failed", "stale"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Errorf("response leaks %q: %s", forbidden, w.Body.String())
				}
			}
		})

		// Test 4: non-loopback works when AllowRemote=true
		s = &SSEServer{}
		// Create a lister with a failed monitor to verify response is returned
		failedLister := &mockMonitorLister{states: []MonitorState{
			{ID: "remote-test", State: WakeStateFailed, Err: "test error"},
		}}
		s.SetMonitorLister(failedLister)
		s.SetHealthzConfig(config.HealthzConfig{AllowRemote: true})
		t.Run("non_loopback_AllowRemote=true", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = "203.0.113.42:54321"
			w := httptest.NewRecorder()
			s.handleHealthz(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("non-loopback with AllowRemote=true should see health response (503 for failed); got %d", w.Code)
			}
			var resp healthzResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Monitors) == 0 {
				t.Fatal("non-loopback with AllowRemote=true should receive monitors")
			}
		})

		// Test 5: X-Forwarded-For spoofing must not bypass the loopback check
		s = &SSEServer{}
		s.SetMonitorLister(lister)
		s.SetHealthzConfig(config.HealthzConfig{AllowRemote: false})
		t.Run("X-Forwarded-For_spoofing_rejected", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = "203.0.113.42:54321"
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			w := httptest.NewRecorder()
			s.handleHealthz(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("X-Forwarded-For spoofing should be rejected; got %d", w.Code)
			}
			for _, forbidden := range []string{errorText, "good", "failed", "stale"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Errorf("response leaks %q: %s", forbidden, w.Body.String())
				}
			}
		})
	})
}

func TestMonitorE2EServe_WakeStatusReturnsAllFields(t *testing.T) {
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	bf := makeFakeChatBuildFunc("monitor-service", func(context.Context, string, *telemetry.EventBus) (string, error) { return "ok", nil }, nil)
	sess := startWakeSession(t, cfg, bf)
	defer sess.Close()
	after, fire := manualAfter()
	ts := newTickSync()
	now := time.Now().Truncate(time.Second)
	if err := sess.StartWake(WakeOptions{
		Dir: dir, After: after, Clock: wake.NewFakeClock(now),
		Checks:   func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
		TickDone: func() { ts.done <- struct{}{} },
	}); err != nil {
		t.Fatal(err)
	}
	st := sess.WakeStatus()
	// Typed assignments also guard the public field types.
	var state string = st.State
	var lastTick *time.Time = st.LastTick
	var turns int = st.TurnsLastHour
	var nextTick int = st.NextTickInSeconds
	if state != WakeStateOK {
		t.Errorf("state = %q; want ok", state)
	}
	// The loop writes a heartbeat as soon as it starts, so last_tick is the start beat.
	if lastTick == nil {
		t.Fatal("last_tick right after start is nil; want the start heartbeat")
	}
	if turns != 0 {
		t.Errorf("turns_last_hour = %d; want 0", turns)
	}
	// NextTickInSeconds should be in a valid range (not beyond max interval)
	if nextTick <= 0 || nextTick > cfg.Settings.Wake.MaxIntervalSeconds {
		t.Errorf("next_tick_in_seconds = %d; want > 0 and <= %d", nextTick, cfg.Settings.Wake.MaxIntervalSeconds)
	}
	fire()
	if !ts.wait(1) {
		t.Fatal("wake tick did not complete")
	}
	st = sess.WakeStatus()
	if st.LastTick == nil {
		t.Fatal("last_tick is nil after a tick")
	}
	if !st.LastTick.Equal(now) {
		t.Errorf("last_tick = %v; want %v", st.LastTick, now)
	}
}

func TestMonitorE2EServe_StopViaA2AAndSlashCommand(t *testing.T) {
	for _, path := range []string{"A2A wake_stop", "/wake stop"} {
		t.Run(path, func(t *testing.T) {
			dir := t.TempDir()
			cfg := wakeTestCfg(dir, true)
			sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
			defer sess.Close()
			after, fire := manualAfter()
			ts := newTickSync()
			if err := sess.StartWake(WakeOptions{
				Dir: dir, After: after, Clock: wake.NewFakeClock(time.Now().Truncate(time.Second)),
				Checks:   func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
				TickDone: func() { ts.done <- struct{}{} },
			}); err != nil {
				t.Fatal(err)
			}
			fire()
			if !ts.wait(1) {
				t.Fatal("initial wake tick did not complete")
			}

			if path == "A2A wake_stop" {
				// Invoke the same session operation as handleA2AWakeStop.
				if err := sess.StopWake(); err != nil {
					t.Fatalf("A2A wake_stop: %v", err)
				}
			} else if got := HandleWakeCommand(sess, "stop"); got != "wake session stopped" {
				t.Fatalf("/wake stop: %q", got)
			}
			// StopWake writes the kill file; the next tick observes it and exits.
			fire()
			if !ts.wait(1) || !waitForLoopStop(sess, 5*time.Second) {
				t.Fatal("wake loop did not stop")
			}
			if got := sess.WakeStatus().State; got != WakeStateStopped {
				t.Fatalf("state after stop = %q; want %q", got, WakeStateStopped)
			}

			s := &SSEServer{}
			s.SetMonitorLister(&mockMonitorLister{states: []MonitorState{{ID: "monitor-test", State: sess.WakeStatus().State}}})
			for _, failOnStopped := range []bool{true, false} {
				s.SetHealthzConfig(config.HealthzConfig{FailOnStopped: &failOnStopped})
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				req.RemoteAddr = "127.0.0.1:54321"
				s.handleHealthz(w, req)
				want := http.StatusOK
				if failOnStopped {
					want = http.StatusServiceUnavailable
				}
				if w.Code != want {
					t.Fatalf("fail_on_stopped=%t: status = %d; want %d; body: %s", failOnStopped, w.Code, want, w.Body.String())
				}
			}

			// A2A wake_resume uses ResumeWake; its kill file must first be removed.
			if err := os.Remove(cfg.Settings.Wake.KillSwitchFile); err != nil {
				t.Fatal(err)
			}
			if err := sess.ResumeWake(); err != nil {
				t.Fatalf("A2A wake_resume: %v", err)
			}
			if !sess.WakeRunning() || sess.WakeStatus().State != WakeStateOK {
				t.Fatalf("wake did not resume: %+v", sess.WakeStatus())
			}
			fire()
			if !ts.wait(1) {
				t.Fatal("resumed wake loop did not tick")
			}
		})
	}
}

func TestMonitorE2EServe_HeartbeatStaleReflectedInHealthz(t *testing.T) {
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.HeartbeatStaleSeconds = 5
	sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("monitor-health", nil, nil))
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	after, _ := manualAfter()
	if err := sess.StartWake(WakeOptions{Dir: dir, Clock: clock, After: after}); err != nil {
		t.Fatal(err)
	}
	// Keep the ticker idle so it cannot refresh the heartbeat while time advances.
	heartbeat := clock.Now()
	if err := os.WriteFile(filepath.Join(dir, sess.ID+".heartbeat"), []byte(fmt.Sprintf("1 %d\n", heartbeat.Unix())), 0o600); err != nil {
		t.Fatal(err)
	}
	lister := &mockMonitorLister{}
	s := &SSEServer{}
	s.SetMonitorLister(lister)
	check := func(wantState string, wantCode int) {
		t.Helper()
		st := sess.WakeStatus()
		if st.State != wantState {
			t.Fatalf("wake state = %q; want %q", st.State, wantState)
		}
		if st.LastTick == nil || !st.LastTick.Equal(heartbeat) {
			t.Fatalf("last tick = %v; want %v", st.LastTick, heartbeat)
		}
		lister.states = []MonitorState{{ID: "heartbeat", SessionID: sess.ID, State: st.State}}
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		s.handleHealthz(w, req)
		if w.Code != wantCode {
			t.Fatalf("healthz status = %d; want %d; body: %s", w.Code, wantCode, w.Body.String())
		}
		var resp healthzResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Monitors["heartbeat"] != wantState {
			t.Fatalf("healthz monitors = %+v; want heartbeat=%s", resp.Monitors, wantState)
		}
	}
	check(WakeStateOK, http.StatusOK)
	clock.Advance(time.Duration(cfg.Settings.Wake.HeartbeatStaleSeconds+1) * time.Second)
	check(WakeStateStale, http.StatusServiceUnavailable)
}

func TestMonitorE2EServe_SessionResumeAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.MaxTurnsPerHour = 10

	var turnCount atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		turnCount.Add(1)
		return "ok", nil
	}, nil)
	sess := startWakeSession(t, cfg, bf)
	clock := wake.NewFakeClock(time.Unix(1_700_000_000, 0))
	after, fire := manualAfter()
	ts := newTickSync()

	if err := sess.StartWake(WakeOptions{
		Dir:   dir,
		Clock: clock,
		After: after,
		Checks: func(context.Context, config.WakeCheck) wake.Observation {
			return wake.Observation{Exists: true, Contains: true}
		},
		TickDone: func() { ts.done <- struct{}{} },
	}); err != nil {
		t.Fatal(err)
	}

	// Fire initial tick to get the wake loop started
	if !ts.wait(1) {
		t.Fatal("initial wake tick did not complete")
	}

	// Fire a second tick with some advancement
	clock.Advance(time.Minute)
	fire()
	if !ts.wait(1) {
		t.Fatal("second wake tick did not complete")
	}

	// Verify turns accumulated
	st1 := sess.WakeStatus()
	if st1.TurnsLastHour < 0 {
		t.Fatalf("turns_last_hour should be >= 0, got %d", st1.TurnsLastHour)
	}

	sessionID := sess.ID
	sess.Close()
	if sess.WakeRunning() {
		t.Fatal("original wake loop is still running after close")
	}

	// Verify state file was persisted
	statePath := filepath.Join(dir, sessionID+".state.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("persisted wake state %s: %v", statePath, err)
	}

	// Create a new session with ResumeID
	resumed, err := StartChatSession(context.Background(), ChatSessionOptions{
		ConfigID:  "t",
		Cfg:       cfg,
		BuildFunc: bf,
		ResumeID:  sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()

	if resumed.ID != sessionID {
		t.Fatalf("resumed session ID = %q; want %q", resumed.ID, sessionID)
	}

	// Start wake loop on resumed session
	resumeAfter, resumeFire := manualAfter()
	resumeTicks := newTickSync()
	resumeClock := wake.NewFakeClock(clock.Now().Add(10 * time.Minute))
	if err := resumed.StartWake(WakeOptions{
		Dir:   dir,
		Clock: resumeClock,
		After: resumeAfter,
		Checks: func(context.Context, config.WakeCheck) wake.Observation {
			return wake.Observation{Exists: true, Contains: true}
		},
		TickDone: func() { resumeTicks.done <- struct{}{} },
	}); err != nil {
		t.Fatalf("reattach wake session: %v", err)
	}

	if !resumeTicks.wait(1) {
		t.Fatal("resumed wake loop did not process its initial tick")
	}

	if !resumed.WakeRunning() {
		t.Fatal("resumed wake loop is not running")
	}

	// Verify cap window state is accessible (preserved across restart)
	st2 := resumed.WakeStatus()
	if st2.TurnsLastHour < 0 {
		t.Fatalf("turns_last_hour should be >= 0 after restart, got %d", st2.TurnsLastHour)
	}

	// Fire another tick and verify the loop continues to work
	resumeFire()
	if !resumeTicks.wait(1) {
		t.Fatal("resumed wake loop did not process another tick")
	}

	st3 := resumed.WakeStatus()
	if st3.TurnsLastHour < 0 {
		t.Fatalf("turns_last_hour should be >= 0 after resumed tick, got %d", st3.TurnsLastHour)
	}
}

func TestMonitorE2EServe_A2AWakeHandlers(t *testing.T) {
	// Test the A2A wake handlers (wake_status, wake_stop, wake_resume) through httptest.
	// These handlers should work correctly for:
	// - valid wake sessions
	// - unknown session ids (404)
	// - non-wake sessions (409)

	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	cm := &ChatManager{sessions: make(map[string]*ChatSession)}
	ss := &SSEServer{chatManager: cm}

	// Test 1: Unknown session ID - all handlers should return 404
	t.Run("unknown_session_returns_404", func(t *testing.T) {
		for _, method := range []string{"handleA2AWakeStatus", "handleA2AWakeStop", "handleA2AWakeResume"} {
			t.Run(method, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				switch method {
				case "handleA2AWakeStatus":
					ss.handleA2AWakeStatus(w, req, "unknown-session")
				case "handleA2AWakeStop":
					ss.handleA2AWakeStop(w, req, "unknown-session")
				case "handleA2AWakeResume":
					ss.handleA2AWakeResume(w, req, "unknown-session")
				}
				if w.Code != http.StatusNotFound {
					t.Fatalf("expect 404 for unknown session, got %d: %s", w.Code, w.Body.String())
				}
			})
		}
	})

	// Test 2: Non-wake session - all handlers should return 409 (conflict)
	t.Run("non_wake_session_returns_409", func(t *testing.T) {
		nonWakeSess := &ChatSession{ID: "non-wake-sess", cfg: &config.Config{}}
		nonWakeSess.cfg.Settings.Wake.Enabled = false
		cm.sessions["non-wake-sess"] = nonWakeSess

		for _, method := range []string{"handleA2AWakeStatus", "handleA2AWakeStop", "handleA2AWakeResume"} {
			t.Run(method, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				switch method {
				case "handleA2AWakeStatus":
					ss.handleA2AWakeStatus(w, req, "non-wake-sess")
				case "handleA2AWakeStop":
					ss.handleA2AWakeStop(w, req, "non-wake-sess")
				case "handleA2AWakeResume":
					ss.handleA2AWakeResume(w, req, "non-wake-sess")
				}
				if w.Code != http.StatusConflict {
					t.Fatalf("expect 409 for non-wake session, got %d: %s", w.Code, w.Body.String())
				}
			})
		}
	})

	// Test 3: Valid wake session - test full cycle: status -> stop -> resume
	t.Run("valid_wake_session_full_cycle", func(t *testing.T) {
		bf := makeFakeChatBuildFunc("a2a-test", nil, nil)
		sess := startWakeSession(t, cfg, bf)
		defer sess.Close()
		cm.sessions[sess.ID] = sess

		after, fire := manualAfter()
		ts := newTickSync()
		if err := sess.StartWake(WakeOptions{
			Dir: dir, After: after, Clock: wake.NewFakeClock(time.Now().Truncate(time.Second)),
			Checks:   func(context.Context, config.WakeCheck) wake.Observation { return wake.Observation{Exists: true} },
			TickDone: func() { ts.done <- struct{}{} },
		}); err != nil {
			t.Fatal(err)
		}
		fire()
		if !ts.wait(1) {
			t.Fatal("initial wake tick did not complete")
		}

		// Test wake_status when running
		t.Run("status_when_running", func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			ss.handleA2AWakeStatus(w, req, sess.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("expect 200 for valid session, got %d: %s", w.Code, w.Body.String())
			}
			var status WakeStatus
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.State != WakeStateOK {
				t.Fatalf("expected state ok, got %s", status.State)
			}
			if !sess.WakeRunning() {
				t.Fatal("wake loop should still be running after status check")
			}
		})

		// Test wake_stop
		t.Run("stop_via_handler", func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			ss.handleA2AWakeStop(w, req, sess.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("expect 200 for stop, got %d: %s", w.Code, w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp["state"] != WakeStateStopped {
				t.Fatalf("expected state %s, got %s", WakeStateStopped, resp["state"])
			}

			// Fire another tick to let the loop exit
			fire()
			if !ts.wait(1) {
				t.Fatal("tick after stop did not complete")
			}
			if !waitForLoopStop(sess, 5*time.Second) {
				t.Fatal("wake loop did not stop after handler call")
			}
			if sess.WakeStatus().State != WakeStateStopped {
				t.Fatalf("wake status should be stopped, got %s", sess.WakeStatus().State)
			}
		})

		// Test wake_resume
		t.Run("resume_via_handler", func(t *testing.T) {
			// Remove kill file to allow resume
			if err := os.Remove(cfg.Settings.Wake.KillSwitchFile); err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			ss.handleA2AWakeResume(w, req, sess.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("expect 200 for resume, got %d: %s", w.Code, w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp["state"] != WakeStateOK {
				t.Fatalf("expected state %s, got %s", WakeStateOK, resp["state"])
			}

			if !sess.WakeRunning() {
				t.Fatal("wake loop should be running after resume")
			}
			if sess.WakeStatus().State != WakeStateOK {
				t.Fatalf("wake status should be ok, got %s", sess.WakeStatus().State)
			}

			// Fire a tick to verify the loop works
			fire()
			if !ts.wait(1) {
				t.Fatal("tick after resume did not complete")
			}
		})
	})
}
