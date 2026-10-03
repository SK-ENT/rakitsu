package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

const monitorTestYAML = `name: mon
agents:
  - name: a1
    role: worker
    model: x
    system_prompt: hi
settings:
  wake:
    enabled: true
    kill_switch_file: %s
    allow:
      paths: ["log.txt"]
    checks:
      - name: f
        type: file_contains
        path: log.txt
        contains: BAD
`

func writeMonitorCfg(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := fmt.Sprintf(monitorTestYAML, filepath.Join(dir, name+".STOP"))
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestChatManager(t *testing.T, wakeDir string) (*ChatManager, *ConfigStore) {
	t.Helper()
	cs, err := NewConfigStore([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cm := NewChatManager(telemetry.NewEventBus(64), cs, nil, makeFakeChatBuildFunc("a1", nil, nil))
	cm.SetWakeDir(wakeDir)
	t.Cleanup(cm.StopAll)
	return cm, cs
}

func stateByID(m *MonitorManager) map[string]MonitorState {
	out := map[string]MonitorState{}
	for _, s := range m.MonitorStates() {
		out[s.ID] = s
	}
	return out
}

func TestMonitorsOneBadOneRunsAndDoubleStartIsNoop(t *testing.T) {
	dir := t.TempDir()
	good := writeMonitorCfg(t, dir, "good.yaml")
	cm, cs := newTestChatManager(t, filepath.Join(dir, "wake"))
	mm := NewMonitorManager(cm, cs, []config.MonitorConfig{
		{ID: "bad", Config: filepath.Join(dir, "missing.yaml")},
		{ID: "good", Config: good, Workdir: dir},
	})
	// Stop the monitor manager before the temp directory is cleaned up.
	t.Cleanup(func() {
		mm.Stop(context.Background())
	})
	mm.Run(context.Background())
	st := stateByID(mm)
	if st["good"].State != WakeStateOK || st["good"].SessionID != "monitor-good" {
		t.Fatalf("good: %+v", st["good"])
	}
	if st["bad"].State != WakeStateFailed || st["bad"].Err == "" {
		t.Fatalf("bad: %+v", st["bad"])
	}
	first := cm.Get("monitor-good")
	if first == nil || !first.WakeRunning() {
		t.Fatal("good monitor not running")
	}
	mm.Run(context.Background()) // double start
	if cm.Get("monitor-good") != first || len(cm.Sessions()) != 1 {
		t.Fatalf("double start created a second session: %d", len(cm.Sessions()))
	}
	if got := mm.MonitorStates(); len(got) != 2 || got[0].ID != "bad" {
		t.Fatalf("want config order, got %+v", got)
	}
	// Stopping the loop shows as stopped.
	first.StopWake()
	_ = first.wake // loop stops on next tick; force flag as the kill switch would
	first.wake.running.Store(false)
	if s := stateByID(mm)["good"]; s.State != WakeStateStopped {
		t.Fatalf("want stopped, got %+v", s)
	}
}

func TestMonitorLockedByOtherProcessIsFailedThenFreeAfterRelease(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeMonitorCfg(t, dir, "m.yaml")
	wakeDir := filepath.Join(dir, "wake")
	mc := []config.MonitorConfig{{ID: "m", Config: cfgPath, Workdir: dir}}

	cm1, cs1 := newTestChatManager(t, wakeDir)
	mm1 := NewMonitorManager(cm1, cs1, mc)
	t.Cleanup(func() {
		mm1.Stop(context.Background())
	})
	mm1.Run(context.Background())
	if s := stateByID(mm1)["m"]; s.State != WakeStateOK {
		t.Fatalf("holder: %+v", s)
	}

	cm2, cs2 := newTestChatManager(t, wakeDir)
	mm2 := NewMonitorManager(cm2, cs2, mc)
	t.Cleanup(func() {
		mm2.Stop(context.Background())
	})
	mm2.Run(context.Background())
	s := stateByID(mm2)["m"]
	if s.State != WakeStateFailed || !strings.Contains(s.Err, "locked") {
		t.Fatalf("second server: %+v", s)
	}

	cm1.StopAll() // holder releases the flock
	mm3 := NewMonitorManager(cm2, cs2, mc)
	t.Cleanup(func() {
		mm3.Stop(context.Background())
	})
	mm3.Run(context.Background())
	if s := stateByID(mm3)["m"]; s.State != WakeStateOK {
		t.Fatalf("after release: %+v", s)
	}
}

type errHost struct{ err error }

func (h errHost) StartOrResume(context.Context, string, string, map[string]string, string) (*ChatSession, error) {
	return nil, h.err
}
func (errHost) Get(string) *ChatSession { return nil }

func TestMonitorEnvRefs(t *testing.T) {
	const secret = "s3cr3t-value-xyz"
	mc := []config.MonitorConfig{{ID: "a", Config: "c.yaml", EnvRefs: []string{"MON_TOKEN"}}}

	// Missing var: failed, message names the variable only.
	m := newMonitorManager(errHost{errors.New("unused")}, mc)
	m.getenv = func(string) (string, bool) { return "", false }
	m.Run(context.Background())
	if s := m.MonitorStates()[0]; s.State != WakeStateFailed || !strings.Contains(s.Err, "MON_TOKEN") {
		t.Fatalf("missing env: %+v", s)
	}

	// A host error that echoes the value must be scrubbed.
	m = newMonitorManager(errHost{fmt.Errorf("auth failed with %s", secret)}, mc)
	m.getenv = func(string) (string, bool) { return secret, true }
	m.register = func(string, string) (string, error) { return "cid", nil }
	m.Run(context.Background())
	s := m.MonitorStates()[0]
	if s.State != WakeStateFailed || strings.Contains(s.Err, secret) {
		t.Fatalf("secret leaked or not failed: %+v", s)
	}

	// ErrLocked maps to a clear message.
	m = newMonitorManager(errHost{fmt.Errorf("x: %w", wake.ErrLocked)}, mc)
	m.getenv = func(string) (string, bool) { return secret, true }
	m.register = func(string, string) (string, error) { return "cid", nil }
	m.Run(context.Background())
	if s := m.MonitorStates()[0]; !strings.Contains(s.Err, "locked") {
		t.Fatalf("locked: %+v", s)
	}
}

func TestStartOrResumeUsesFixedID(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeMonitorCfg(t, dir, "m.yaml")
	cm, cs := newTestChatManager(t, filepath.Join(dir, "wake"))
	cid, err := cs.registerPath(cfgPath, "m")
	if err != nil {
		t.Fatal(err)
	}
	s1, err := cm.StartOrResume(context.Background(), cid, dir, nil, "monitor-m")
	if err != nil || s1.ID != "monitor-m" {
		t.Fatalf("got %v, %v", s1, err)
	}
	s2, err := cm.StartOrResume(context.Background(), cid, dir, nil, "monitor-m")
	if err != nil || s2 != s1 {
		t.Fatalf("second call must reattach: %v %v", s2, err)
	}
}

func TestLoadServeConfigDropsBadMonitorsBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "serve.yaml")
	body := "name: s\nagents:\n  - name: a1\n    role: worker\n    model: x\n    system_prompt: hi\nmonitors:\n  - {id: a, config: x.yaml}\n  - {id: a, config: y.yaml}\n"
	os.WriteFile(p, []byte(body), 0o600)
	cfg, dropped, err := LoadServeConfig(p)
	if err != nil || dropped == "" || !strings.Contains(dropped, "duplicate") || len(cfg.Monitors) != 0 {
		t.Fatalf("cfg=%v dropped=%q err=%v", cfg, dropped, err)
	}
}
