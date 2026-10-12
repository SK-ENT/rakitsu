package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
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

func TestAutoStartedMonitorMessageWaitTimeoutPersistsReply(t *testing.T) {
	dir := t.TempDir()
	path := writeMonitorCfg(t, dir, "steer.yaml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("  session_msg:\n    enabled: true\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rawCfg, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rawCfg = bytes.Replace(rawCfg, []byte("  wake:\n"), []byte("  wake:\n    interval_seconds: 10\n    min_interval_seconds: 10\n    max_interval_seconds: 10\n"), 1)
	if err := os.WriteFile(path, rawCfg, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cs, err := NewConfigStore([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := store.NewSessionStoreAt(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	releaseWake := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWake()
	cm := NewChatManager(telemetry.NewEventBus(64), cs, ss, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, query string, _ *telemetry.EventBus) (string, error) {
			if calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
					return "wake-done", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if !strings.Contains(query, "STEER_OK") {
				return "unexpected query", nil
			}
			return "STEER_OK", nil
		}, nil))
	cm.SetWakeDir(filepath.Join(dir, "wake"))
	mm := NewMonitorManager(cm, cs, []config.MonitorConfig{{ID: "steer", Config: path, Workdir: dir}})
	t.Cleanup(func() { mm.Stop(context.Background()); cm.StopAll() })
	mm.Run(context.Background())
	sess := cm.Get("monitor-steer")
	if sess == nil || !sess.WakeRunning() || !sess.cfg.Settings.SessionMsg.Enabled {
		t.Fatalf("auto-started monitor cannot accept messages: state=%+v", stateByID(mm)["steer"])
	}
	// Let the initial quiet tick finish, then change the real file check.
	// The next real timer tick must enqueue the wake; no SubmitWake shortcut.
	deadline := time.Now().Add(2 * time.Second)
	for sess.WakeStatus().LastTick == nil {
		if time.Now().After(deadline) {
			t.Fatal("initial quiet tick did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), []byte("BAD"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("natural timer wake did not start")
	}
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.chatManager = cm
	s.SetSessionStore(ss)
	body, _ := json.Marshal(SessionMessageRequest{Text: "reply STEER_OK", Wait: true, TimeoutMs: 20})
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 3 * time.Second
	request := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodPost {
			req.Header.Set("Idempotency-Key", "monitor-steering-1")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	messagePath := "/api/sessions/" + sess.ID + "/message"
	code, response := request(http.MethodPost, messagePath, body)
	if code != http.StatusOK {
		t.Fatalf("message status = %d: %s", code, response)
	}
	retryCode, retry := request(http.MethodPost, messagePath, body)
	if retryCode != code || !bytes.Equal(retry, response) {
		t.Fatalf("retry = %d %s; first = %d %s", retryCode, retry, code, response)
	}
	var reply map[string]interface{}
	if err := json.Unmarshal(response, &reply); err != nil {
		t.Fatal(err)
	}
	if reply["status"] != "delivered" || reply["waited"] != false {
		t.Fatalf("timeout response = %+v", reply)
	}
	if note, _ := reply["note"].(string); !strings.Contains(note, "/api/chat/{id}/tree") {
		t.Fatalf("timeout response has no retrieval path: %+v", reply)
	}
	if len(sess.SnapshotTree().Nodes) != 1 {
		t.Fatalf("steering ran before active wake ended: %+v", sess.SnapshotTree())
	}
	releaseWake()
	// Poll through HTTP while the session is still live.
	treePath := "/api/chat/" + sess.ID + "/tree"
	var tree treeSnapshot
	deadline = time.Now().Add(2 * time.Second)
	for {
		code, data := request(http.MethodGet, treePath, nil)
		if code != http.StatusOK {
			t.Fatalf("GET live tree = %d: %s", code, data)
		}
		if err := json.Unmarshal(data, &tree); err != nil {
			t.Fatal(err)
		}
		complete := false
		for _, node := range tree.Nodes {
			if len(tree.ActivePath) == 2 && node.ID == tree.ActivePath[1] && node.Status == turntree.StatusComplete {
				complete = true
			}
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live tree did not complete: %+v", tree)
		}
		time.Sleep(10 * time.Millisecond)
	}
	turnPath := "/api/chat/" + sess.ID + "/turn/" + tree.ActivePath[1]
	code, turnData := request(http.MethodGet, turnPath, nil)
	if code != http.StatusOK {
		t.Fatalf("GET live turn = %d: %s", code, turnData)
	}
	var turn struct {
		UserText string            `json:"user_text"`
		Entries  []transcriptEntry `json:"entries"`
		Status   turntree.Status   `json:"status"`
	}
	if err := json.Unmarshal(turnData, &turn); err != nil {
		t.Fatal(err)
	}
	if turn.UserText != "reply STEER_OK" || turn.Status != turntree.StatusComplete || len(turn.Entries) == 0 || turn.Entries[len(turn.Entries)-1].Text != "STEER_OK" {
		t.Fatalf("GET turn did not expose persisted reply: %+v", turn)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		raw, err := ss.LoadChatTree(sess.ID)
		if err == nil {
			file, err := LoadChatTreeFile(raw)
			if err != nil {
				t.Fatal(err)
			}
			path := file.Tree.ActivePath()
			if len(path) == 2 && path[1].Status == turntree.StatusComplete {
				if path[0].Status != turntree.StatusComplete || path[1].UserText != "reply STEER_OK" || len(path[1].Entries) == 0 || path[1].Entries[len(path[1].Entries)-1].Text != "STEER_OK" {
					t.Fatalf("persisted monitor turns = %+v", path)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("steering reply not persisted after wake ended: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("runner calls = %d, want one wake and one steering turn", got)
	}
	retryCode, retry = request(http.MethodPost, messagePath, body)
	if retryCode != http.StatusOK || !bytes.Equal(retry, response) || calls.Load() != 2 {
		t.Fatalf("completed retry duplicated delivery: %d %s calls=%d", retryCode, retry, calls.Load())
	}
	if !cm.Stop(sess.ID) {
		t.Fatal("failed to stop monitor")
	}
	code, persisted := request(http.MethodGet, turnPath, nil)
	if code != http.StatusOK || !bytes.Equal(persisted, turnData) {
		t.Fatalf("persisted reply differs: %d %s", code, persisted)
	}

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
