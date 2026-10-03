package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

const wakeTaskYAML = "name: t\nversion: \"1.0\"\nsettings:\n  default_provider: ollama\n  providers:\n    ollama:\n      type: ollama\n      base_url: http://localhost:11434/v1\n  defaults:\n    model: m\n" +
	"tools:\n  - name: sh\n    type: cli\nagents:\n  - name: a\n    role: worker\n    system_prompt: hi\n    tools: [sh]\n"

type wakeTaskEnv struct {
	sess     *ChatSession
	startT   tools.Tool
	statusT  tools.Tool
	fire     func()
	ts       *tickSync
	specs    chan wake.TaskSpec
	ctxs     chan context.Context
	timer    chan time.Time
	events   <-chan telemetry.AgentEvent
	dir      string
	modelRan chan struct{}
	alarm    *atomic.Bool
}

func newWakeTaskEnv(t *testing.T) *wakeTaskEnv {
	t.Helper()
	dir := t.TempDir()
	wd := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wd, "tasks"), 0o755)
	_ = os.WriteFile(filepath.Join(wd, "tasks", "alert.yaml"), []byte(wakeTaskYAML), 0o600)

	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.Allow.Configs = []config.WakeAllowConfig{{
		Name: "alert-handler", Path: "tasks/alert.yaml", Task: "Handle it.",
		Params: map[string]config.WakeTaskParam{"severity": {Type: "enum", Enum: []string{"low", "high"}}},
	}}
	cfg.Settings.Wake.MaxTasksPerHour, cfg.Settings.Wake.MaxConcurrentTasks, cfg.Settings.Wake.TaskTimeoutSeconds = 5, 2, 300
	cfg.Settings.Wake.TurnTimeoutSeconds = 60

	env := &wakeTaskEnv{dir: dir, specs: make(chan wake.TaskSpec, 8), ctxs: make(chan context.Context, 8),
		timer: make(chan time.Time, 8), modelRan: make(chan struct{}, 8), alarm: &atomic.Bool{}}
	inner := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
		env.modelRan <- struct{}{}
		return "ok", nil
	}, nil)
	var handle *wake.TaskHandle
	bf := func(ctx context.Context, c *config.Config, bus *telemetry.EventBus, rq chan userinput.InputRequest, rs chan string, sid, self string) (agent.Runner, agent.Runner, func(), string, string, error) {
		handle = wake.TaskHandleFrom(ctx)
		return inner(ctx, c, bus, rq, rs, sid, self)
	}
	sess := startWakeSession(t, cfg, bf)
	if handle == nil {
		t.Fatal("build ctx must carry the task handle when allow.configs is set")
	}
	env.sess = sess
	for _, tl := range handle.Tools() {
		if tl.GetName() == "start_task" {
			env.startT = tl
		} else {
			env.statusT = tl
		}
	}
	env.events = sess.eventBus.Subscribe()
	after, fire := manualAfter()
	env.fire = fire
	env.ts = newTickSync()
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: env.alarm.Load()}
	}
	err := sess.StartWake(WakeOptions{
		Dir: dir, Workdir: wd, Checks: probe, After: after, Clock: wake.NewFakeClock(time.Unix(1_700_000_000, 0)),
		TickDone:  func() { env.ts.done <- struct{}{} },
		TaskAfter: func(time.Duration) <-chan time.Time { return env.timer },
		TaskRun: func(ctx context.Context, s wake.TaskSpec) (string, error) {
			env.ctxs <- ctx
			env.specs <- s
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func (e *wakeTaskEnv) start(t *testing.T, sev string) (string, error) {
	t.Helper()
	return e.startT.Execute(context.Background(), map[string]interface{}{"config_name": "alert-handler", "arguments": map[string]interface{}{"severity": sev}})
}

func (e *wakeTaskEnv) waitEvent(t *testing.T, et telemetry.EventType) telemetry.WakeTaskPayload {
	t.Helper()
	for ev := range e.events {
		if ev.EventType == et {
			b, _ := json.Marshal(ev.Payload)
			var p telemetry.WakeTaskPayload
			_ = json.Unmarshal(b, &p)
			return p
		}
	}
	t.Fatal("event stream closed")
	return telemetry.WakeTaskPayload{}
}

func TestWakeTaskStartsIndependentRunWithEvents(t *testing.T) {
	t.Setenv("WAKE_SESSION_ENV_SECRET", "s3cret")
	e := newWakeTaskEnv(t)
	out, err := e.start(t, "high")
	if err != nil || !strings.Contains(out, `"launched"`) {
		t.Fatalf("start: %q %v", out, err)
	}
	spec := <-e.specs
	if !strings.HasSuffix(spec.ConfigPath, filepath.Join("tasks", "alert.yaml")) || strings.Contains(spec.Prompt, "s3cret") {
		t.Fatalf("spec: %+v", spec)
	}
	if p := e.waitEvent(t, telemetry.EventWakeTaskStart); p.Status != "started" || p.Args["severity"] != "high" {
		t.Fatalf("start event: %+v", p)
	}
	if _, err := e.start(t, "nuke"); err == nil {
		t.Fatal("bad arg must be refused")
	}
	if p := e.waitEvent(t, telemetry.EventWakeTaskStart); p.Status != "refused" {
		t.Fatalf("refusal must be an event: %+v", p)
	}
}

func TestWakeTaskStopRouteCancelsAndBlocksNewStarts(t *testing.T) {
	e := newWakeTaskEnv(t)
	if _, err := e.start(t, "low"); err != nil {
		t.Fatal(err)
	}
	<-e.specs
	taskCtx := <-e.ctxs

	bus := telemetry.NewEventBus(64)
	srv := NewSSEServer(bus, "localhost", 0)
	cm := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	cm.sessions[e.sess.ID] = e.sess
	srv.chatManager = cm
	w := httptest.NewRecorder()
	srv.handleChatByID(w, httptest.NewRequest("POST", "/api/chat/"+e.sess.ID+"/wake/stop", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	<-taskCtx.Done()
	if p := e.waitEvent(t, telemetry.EventWakeTaskEnd); p.Status != "cancelled" {
		t.Fatalf("end event: %+v", p)
	}
	if _, err := e.start(t, "low"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("starts must be blocked after stop: %v", err)
	}
	// The wake loop still ends cleanly on its next tick.
	e.fire()
	e.ts.wait(1)
	if !waitForLoopStop(e.sess, 5*time.Second) {
		t.Fatal("wake loop must stop")
	}
}

func TestWakeTaskCancelEndpointCancelsOnlyThatTask(t *testing.T) {
	e := newWakeTaskEnv(t)
	for i := 0; i < 2; i++ {
		if _, err := e.start(t, "low"); err != nil {
			t.Fatal(err)
		}
		<-e.specs
	}
	c1, c2 := <-e.ctxs, <-e.ctxs
	var id string
	id = e.waitEvent(t, telemetry.EventWakeTaskStart).TaskID
	bus := telemetry.NewEventBus(64)
	srv := NewSSEServer(bus, "localhost", 0)
	cm := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	cm.sessions[e.sess.ID] = e.sess
	srv.chatManager = cm

	w := httptest.NewRecorder()
	srv.handleChatByID(w, httptest.NewRequest("POST", "/api/chat/"+e.sess.ID+"/wake/tasks/"+id+"/cancel", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if (c1.Err() == nil) == (c2.Err() == nil) {
		t.Fatal("exactly one task context must be cancelled")
	}
	w = httptest.NewRecorder()
	srv.handleChatByID(w, httptest.NewRequest("POST", "/api/chat/"+e.sess.ID+"/wake/tasks/nope/cancel", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown task: %d", w.Code)
	}
}

func TestWakeTaskCancelRouteIsPostOnly(t *testing.T) {
	e := newWakeTaskEnv(t)
	if _, err := e.start(t, "low"); err != nil {
		t.Fatal(err)
	}
	<-e.specs
	taskCtx := <-e.ctxs
	id := e.waitEvent(t, telemetry.EventWakeTaskStart).TaskID
	bus := telemetry.NewEventBus(64)
	srv := NewSSEServer(bus, "localhost", 0)
	cm := NewChatManager(bus, nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	cm.sessions[e.sess.ID] = e.sess
	srv.chatManager = cm
	url := "/api/chat/" + e.sess.ID + "/wake/tasks/" + id + "/cancel"

	for _, m := range []string{"GET", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		srv.handleChatByID(w, httptest.NewRequest(m, url, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: got %d, want 405", m, w.Code)
		}
		if taskCtx.Err() != nil {
			t.Fatalf("%s must not cancel the task", m)
		}
	}
	w := httptest.NewRecorder()
	srv.handleChatByID(w, httptest.NewRequest("POST", url, nil))
	if w.Code != http.StatusOK || taskCtx.Err() == nil {
		t.Fatalf("POST must cancel: %d %s", w.Code, w.Body)
	}
}

func TestWakeTaskLoopUnaffectedByTasks(t *testing.T) {
	e := newWakeTaskEnv(t)
	if _, err := e.start(t, "low"); err != nil {
		t.Fatal(err)
	}
	<-e.specs
	e.alarm.Store(true)
	e.fire()
	e.ts.wait(1)
	// The escalated wake turn still runs the model while a task is running.
	<-e.modelRan
}

func TestWakeTaskBadAllowlistBlocksSessionStart(t *testing.T) {
	dir := t.TempDir()
	cfg := wakeTestCfg(dir, true)
	cfg.Settings.Wake.Allow.Configs = []config.WakeAllowConfig{{Name: "x", Path: "tasks/missing.yaml", Task: "t"}}
	cfg.Settings.Wake.MaxTasksPerHour, cfg.Settings.Wake.MaxConcurrentTasks, cfg.Settings.Wake.TaskTimeoutSeconds = 5, 2, 300
	sess := startWakeSession(t, cfg, makeFakeChatBuildFunc("a1", nil, nil))
	err := sess.StartWake(WakeOptions{Dir: dir, Workdir: t.TempDir(), TaskRun: func(context.Context, wake.TaskSpec) (string, error) { return "", nil }})
	if err == nil || sess.WakeRunning() {
		t.Fatalf("missing allowlisted config must block the session: %v", err)
	}
}
