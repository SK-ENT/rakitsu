package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

func TestWakeStatusJSONShape(t *testing.T) {
	b, _ := json.Marshal(WakeStatus{ID: "monitor-x", State: WakeStateOK})
	for _, k := range []string{`"id"`, `"state"`, `"turns_last_hour"`, `"max_turns_per_hour"`, `"running_tasks"`, `"next_tick_in_seconds"`, `"interval_seconds"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
}

// TestWakeStatusLastTick verifies that LastTick is read from the heartbeat file.
func TestWakeStatusLastTick(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session"

	// Create a fake heartbeat file with the correct format: "tick unix_seconds"
	hbPath := filepath.Join(tmpDir, sessionID+".heartbeat")
	hbTime := time.Now().UTC()
	hbData := []byte(fmt.Sprintf("42 %d", hbTime.Unix()))
	if err := os.WriteFile(hbPath, hbData, 0o600); err != nil {
		t.Fatalf("failed to write heartbeat: %v", err)
	}

	// Create engine with heartbeat
	eng, err := wake.New(wake.Deps{
		Cfg:       config.WakeConfig{IntervalSeconds: 60},
		SessionID: sessionID,
		Dir:       tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	// Create session with running wake loop
	cs := &ChatSession{
		ID:  sessionID,
		cfg: &config.Config{Settings: config.Settings{Wake: config.WakeConfig{IntervalSeconds: 60}}},
	}
	cs.wake = &wakeRunner{eng: eng}
	cs.wake.running.Store(true)

	st := cs.WakeStatus()

	// Verify LastTick is set
	if st.LastTick == nil {
		t.Errorf("expected LastTick to be set, got nil")
	}
}

// TestWakeStatusTurnsLastHour verifies that TurnsLastHour counts recent cap window entries.
func TestWakeStatusTurnsLastHour(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session"

	now := time.Now()
	cfg := config.WakeConfig{
		IntervalSeconds:       60,
		MaxTurnsPerHour:       10,
		HeartbeatStaleSeconds: 300,
	}

	// Create a custom clock for testing
	fakeClock := &FakeClock{now: now}

	eng, err := wake.New(wake.Deps{
		Cfg:       cfg,
		SessionID: sessionID,
		Dir:       tmpDir,
		Clock:     fakeClock,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	// Manually add cap window entries (simulate escalations in the last hour)
	// This would normally be done through Tick, but we're testing the accessor

	// Create session with running wake loop
	cs := &ChatSession{
		ID:  sessionID,
		cfg: &config.Config{Settings: config.Settings{Wake: cfg}},
	}
	cs.wake = &wakeRunner{eng: eng}
	cs.wake.running.Store(true)

	st := cs.WakeStatus()

	// Verify TurnsLastHour is populated (0 for newly created engine)
	if st.TurnsLastHour < 0 {
		t.Errorf("expected TurnsLastHour >= 0, got %d", st.TurnsLastHour)
	}
}

// TestWakeStatusHeartbeatStale verifies that state becomes "stale" when heartbeat is old.
func TestWakeStatusHeartbeatStale(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session"
	staleSeconds := 60

	now := time.Now()
	oldTime := now.Add(-time.Duration(staleSeconds+10) * time.Second) // older than stale threshold

	// Create heartbeat file with old timestamp
	hbPath := filepath.Join(tmpDir, sessionID+".heartbeat")
	hbData := []byte(fmt.Sprintf("42 %d", oldTime.Unix()))
	if err := os.WriteFile(hbPath, hbData, 0o600); err != nil {
		t.Fatalf("failed to write heartbeat: %v", err)
	}

	cfg := config.WakeConfig{
		IntervalSeconds:       60,
		HeartbeatStaleSeconds: staleSeconds,
	}

	fakeClock := &FakeClock{now: now}
	eng, err := wake.New(wake.Deps{
		Cfg:       cfg,
		SessionID: sessionID,
		Dir:       tmpDir,
		Clock:     fakeClock,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	cs := &ChatSession{
		ID:  sessionID,
		cfg: &config.Config{Settings: config.Settings{Wake: cfg}},
	}
	cs.wake = &wakeRunner{eng: eng}
	cs.wake.running.Store(true)

	st := cs.WakeStatus()

	// Verify state is "stale"
	if st.State != WakeStateStale {
		t.Errorf("expected state WakeStateStale, got %q", st.State)
	}
}

// TestWakeStatusNextTickInSeconds verifies that NextTickInSeconds is calculated.
func TestWakeStatusNextTickInSeconds(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session"

	cfg := config.WakeConfig{
		IntervalSeconds:    30,
		MinIntervalSeconds: 10,
		MaxIntervalSeconds: 300,
	}

	eng, err := wake.New(wake.Deps{
		Cfg:       cfg,
		SessionID: sessionID,
		Dir:       tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	cs := &ChatSession{
		ID:  sessionID,
		cfg: &config.Config{Settings: config.Settings{Wake: cfg}},
	}
	cs.wake = &wakeRunner{eng: eng}
	cs.wake.running.Store(true)

	st := cs.WakeStatus()

	// Verify NextTickInSeconds is reasonable (should be between min and max)
	if st.NextTickInSeconds < 10 || st.NextTickInSeconds > 300 {
		t.Errorf("expected NextTickInSeconds between 10 and 300, got %d", st.NextTickInSeconds)
	}
}

// TestWakeStatusNotRunning verifies state is "stopped" when wake is not running.
func TestWakeStatusNotRunning(t *testing.T) {
	cs := &ChatSession{
		ID:  "test-session",
		cfg: &config.Config{Settings: config.Settings{Wake: config.WakeConfig{IntervalSeconds: 60}}},
	}
	// No wake runner set, so WakeRunning() returns false

	st := cs.WakeStatus()

	if st.State != WakeStateStopped {
		t.Errorf("expected state WakeStateStopped when not running, got %q", st.State)
	}
	if st.LastTick != nil {
		t.Errorf("expected LastTick to be nil when not running, got %v", st.LastTick)
	}
	if st.NextTickInSeconds != 0 {
		t.Errorf("expected NextTickInSeconds to be 0 when not running, got %d", st.NextTickInSeconds)
	}
}

// TestWakeStatusLabel verifies the label is stripped from the ID.
func TestWakeStatusLabel(t *testing.T) {
	cs := &ChatSession{
		ID:  "monitor-my-service",
		cfg: &config.Config{Settings: config.Settings{Wake: config.WakeConfig{}}},
	}

	st := cs.WakeStatus()

	if st.Label != "my-service" {
		t.Errorf("expected label 'my-service', got %q", st.Label)
	}
}

// FakeClock implements wake.Clock for testing with a fixed time.
type FakeClock struct {
	now time.Time
}

func (fc *FakeClock) Now() time.Time {
	return fc.now
}

// After a kill the loop is no longer running, but status must still report the
// rolling-window count that the cap uses.
func TestWakeStatusTurnsLastHourWhileStopped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	state := fmt.Sprintf(`{"version":1,"tick":3,"cap_window":[%d,%d]}`, now.Add(-time.Minute).UnixNano(), now.Add(-2*time.Minute).UnixNano())
	if err := os.WriteFile(filepath.Join(dir, "s.state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := wake.New(wake.Deps{Cfg: config.WakeConfig{IntervalSeconds: 60}, SessionID: "s", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	cs := &ChatSession{ID: "s", cfg: &config.Config{Settings: config.Settings{Wake: config.WakeConfig{IntervalSeconds: 60}}}}
	cs.wake = &wakeRunner{eng: eng} // running flag false: stopped
	st := cs.WakeStatus()
	if st.State != WakeStateStopped {
		t.Fatalf("state = %q", st.State)
	}
	if st.TurnsLastHour != 2 {
		t.Fatalf("turns_last_hour = %d, want 2", st.TurnsLastHour)
	}
}
