package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// These tests drive the REAL A2A JSON-RPC handler (a2aHandlerFunc over
// httptest) while real wake-enabled chat sessions tick. The inbound A2A run is
// detached from any chat session, so the assertions are: A2A results are
// correct, and wake sessions are undisturbed by A2A traffic.

const a2aStressFailsafe = 20 * time.Second

type stressRunner struct {
	run func(ctx context.Context, q string) (string, error)
}

func (r *stressRunner) Run(ctx context.Context, q string) (string, error) { return r.run(ctx, q) }
func (r *stressRunner) GetName() string                                   { return "a1" }
func (r *stressRunner) GetRole() agent.AgentRole                          { return agent.RoleWorker }
func (r *stressRunner) GetTools() []string                                { return nil }
func (r *stressRunner) SetDebugController(*debug.DebugController)         {}

type stressWake struct {
	sess *server.ChatSession
	fire func()
	done chan struct{}
}

// startStressWake starts one wake-enabled session in lockstep mode. The first
// tick has already been requested; call awaitDone to receive it.
func startStressWake(t *testing.T, probe wake.CheckFunc, run func(context.Context, string) (string, error)) *stressWake {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Name:   "t",
		Agents: []config.AgentDefinition{{Name: "a1"}},
		Settings: config.Settings{Wake: config.WakeConfig{
			Enabled: true, IntervalSeconds: 60, BackoffFactor: 1.5, MinIntervalSeconds: 30,
			MaxIntervalSeconds: 600, MaxTurnsPerHour: 6, TurnTimeoutSeconds: 1,
			KillSwitchFile: dir + "/STOP", HeartbeatStaleSeconds: 180, ConsecutiveAlarms: 1,
			Checks: []config.WakeCheck{{Name: "f", Type: "file_contains", Path: "x", Contains: "BAD", TimeoutSeconds: 1}},
		}},
	}
	bf := func(context.Context, *config.Config, *telemetry.EventBus, chan userinput.InputRequest, chan string, string, string) (agent.Runner, agent.Runner, func(), string, string, error) {
		r := &stressRunner{run: run}
		return r, r, func() {}, "a1", "fake", nil
	}
	sess, err := server.StartChatSession(context.Background(), server.ChatSessionOptions{ConfigID: "t", Cfg: cfg, BuildFunc: bf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	ch := make(chan time.Time, 16)
	w := &stressWake{sess: sess, fire: func() { ch <- time.Time{} }, done: make(chan struct{}, 4)}
	if err := sess.StartWake(server.WakeOptions{
		Dir: dir, Checks: probe,
		After:     func(time.Duration) <-chan time.Time { return ch },
		TurnAfter: func(time.Duration) <-chan time.Time { return nil },
		Clock:     wake.NewFakeClock(time.Unix(1_700_000_000, 0)),
		TickDone:  func() { w.done <- struct{}{} },
	}); err != nil {
		t.Fatal(err)
	}
	if !sess.WakeRunning() {
		t.Fatal("wake loop not running after StartWake (cfg must enable wake)")
	}
	return w
}

func (w *stressWake) awaitDone(t *testing.T) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(a2aStressFailsafe):
		t.Error("wake tick never completed")
		return false
	}
}

// tickUntil ticks in lockstep until stop is closed.
func (w *stressWake) tickUntil(t *testing.T, stop <-chan struct{}, ticks *atomic.Int32) (wait func()) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !w.awaitDone(t) {
			return
		}
		ticks.Add(1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			w.fire()
			if !w.awaitDone(t) {
				return
			}
			ticks.Add(1)
		}
	}()
	return wg.Wait
}

func quiet(context.Context, config.WakeCheck) wake.Observation {
	return wake.Observation{Exists: true}
}

// rpc posts one JSON-RPC call to the live test server.
func rpc(t *testing.T, url, method string, params interface{}) (testA2AResponse, bool) {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": "x", "method": method, "params": params}) //nolint:errcheck
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Errorf("%s POST: %v", method, err)
		return testA2AResponse{}, false
	}
	defer resp.Body.Close()
	var out testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Errorf("%s decode: %v", method, err)
		return out, false
	}
	if out.Error != nil {
		t.Errorf("%s rpc error: %+v", method, out.Error)
		return out, false
	}
	return out, true
}

func a2aSend(t *testing.T, url, text string) (string, bool) {
	out, ok := rpc(t, url, "SendMessage", srvSendMessageParams{
		Tenant:  "Researcher",
		Message: srvA2AMessage{MessageID: "m", Role: roleUser, Parts: []srvA2APart{{Text: text}}},
	})
	if !ok {
		return "", false
	}
	var res srvSendMessageResult
	if err := json.Unmarshal(out.Result, &res); err != nil || res.Task == nil {
		t.Errorf("SendMessage result: %v %s", err, out.Result)
		return "", false
	}
	return res.Task.ID, true
}

func a2aGet(t *testing.T, url, id string) (srvA2ATask, bool) {
	out, ok := rpc(t, url, "GetTask", srvGetTaskParams{ID: id})
	if !ok {
		return srvA2ATask{}, false
	}
	var task srvA2ATask
	if err := json.Unmarshal(out.Result, &task); err != nil {
		t.Errorf("GetTask result: %v", err)
		return task, false
	}
	return task, true
}

// a2aAwaitTerminal polls GetTask the way a real A2A client does (back-to-back
// round trips, no test sleep), bounded by a failsafe deadline.
func a2aAwaitTerminal(t *testing.T, url, id string) (srvA2ATask, bool) {
	deadline := time.Now().Add(a2aStressFailsafe)
	for time.Now().Before(deadline) {
		task, ok := a2aGet(t, url, id)
		if !ok {
			return task, false
		}
		if isTerminalTaskState(task.Status.State) {
			return task, true
		}
	}
	t.Errorf("task %s never reached a terminal state", id)
	return srvA2ATask{}, false
}

func a2aCancel(t *testing.T, url, id string) (srvA2ATask, bool) {
	out, ok := rpc(t, url, "CancelTask", srvCancelTaskParams{ID: id})
	if !ok {
		return srvA2ATask{}, false
	}
	var task srvA2ATask
	if err := json.Unmarshal(out.Result, &task); err != nil {
		t.Errorf("CancelTask result: %v", err)
		return task, false
	}
	return task, true
}

// a2aStressRun: "echo-N" completes with "echo:echo-N"; "block-N" signals
// started and blocks until its context is canceled.
func newA2AStressServer(started chan string, exited chan string) *httptest.Server {
	run := func(ctx context.Context, _ *config.Config, q string) (string, error) {
		if strings.HasPrefix(q, "block-") {
			started <- q
			<-ctx.Done()
			exited <- q
			return "", ctx.Err()
		}
		return "echo:" + q, nil
	}
	return httptest.NewServer(a2aHandlerFunc(testConfig(), run))
}

// TestStress_A2AHandlerConcurrentWithWakeSessions: 30 concurrent SendMessage+
// GetTask clients (20 completing, 10 canceled mid-run) while 6 wake sessions
// tick continuously.
func TestStress_A2AHandlerConcurrentWithWakeSessions(t *testing.T) {
	var wakeRuns atomic.Int32
	var ticks atomic.Int32
	stop := make(chan struct{})
	var waits []func()
	var wakes []*stressWake
	for i := 0; i < 6; i++ {
		w := startStressWake(t, quiet, func(context.Context, string) (string, error) {
			wakeRuns.Add(1)
			return "ok", nil
		})
		wakes = append(wakes, w)
		waits = append(waits, w.tickUntil(t, stop, &ticks))
	}

	started := make(chan string, 10)
	exited := make(chan string, 10)
	srv := newA2AStressServer(started, exited)
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := fmt.Sprintf("echo-%d", i)
			id, ok := a2aSend(t, srv.URL, q)
			if !ok {
				return
			}
			task, ok := a2aAwaitTerminal(t, srv.URL, id)
			if !ok {
				return
			}
			if task.Status.State != taskStateCompleted || len(task.Artifacts) != 1 || task.Artifacts[0].Parts[0].Text != "echo:"+q {
				t.Errorf("%s: state=%s artifacts=%+v", q, task.Status.State, task.Artifacts)
			}
		}(i)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := fmt.Sprintf("block-%d", i)
			id, ok := a2aSend(t, srv.URL, q)
			if !ok {
				return
			}
			if task, ok := a2aGet(t, srv.URL, id); ok && task.Status.State != taskStateWorking {
				t.Errorf("%s: state right after send = %s, want working", q, task.Status.State)
			}
			// Wait until some blocked run is live; cancel our own task only
			// after its own run started.
			for {
				select {
				case got := <-started:
					if got != q {
						started <- got // not ours; hand back
						continue
					}
				case <-time.After(a2aStressFailsafe):
					t.Errorf("%s never started", q)
					return
				}
				break
			}
			task, ok := a2aCancel(t, srv.URL, id)
			if !ok {
				return
			}
			if task.Status.State != taskStateCanceled {
				t.Errorf("%s: cancel returned state %s", q, task.Status.State)
			}
			select {
			case <-exited:
			case <-time.After(a2aStressFailsafe):
				t.Errorf("%s: run context never canceled", q)
			}
			if task, ok := a2aGet(t, srv.URL, id); ok && task.Status.State != taskStateCanceled {
				t.Errorf("%s: final state %s, want canceled", q, task.Status.State)
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	for _, w := range waits {
		w()
	}
	if wakeRuns.Load() != 0 {
		t.Errorf("quiet wake sessions made %d model calls under A2A load", wakeRuns.Load())
	}
	if ticks.Load() < int32(len(wakes)) {
		t.Errorf("only %d wake ticks completed", ticks.Load())
	}
	t.Logf("A2A: 20 completed + 10 canceled correct; %d wake ticks ran concurrently", ticks.Load())
}

// TestStress_A2AHandlerWhileWakeTurnInFlight: a wake session is mid wake-turn
// (runner blocked) while A2A SendMessage/GetTask/CancelTask run. A2A results
// must be correct and the wake turn must be neither canceled nor completed
// by the A2A traffic; it finishes normally once released.
func TestStress_A2AHandlerWhileWakeTurnInFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	var alarm atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarm.Load()}
	}
	w := startStressWake(t, probe, func(ctx context.Context, q string) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			finished <- ctx.Err()
			return "wake-done", nil
		case <-ctx.Done():
			finished <- ctx.Err()
			return "", ctx.Err()
		}
	})
	if !w.awaitDone(t) {
		return
	}
	alarm.Store(true)
	w.fire()
	if !w.awaitDone(t) {
		return
	}
	select {
	case <-started:
	case <-time.After(a2aStressFailsafe):
		t.Fatal("wake turn never started")
	}

	// A further tick while the wake turn is in flight must still complete.
	w.fire()
	if !w.awaitDone(t) {
		return
	}

	bstarted := make(chan string, 1)
	bexited := make(chan string, 1)
	srv := newA2AStressServer(bstarted, bexited)
	defer srv.Close()

	id1, ok := a2aSend(t, srv.URL, "echo-mid")
	if !ok {
		return
	}
	task, ok := a2aAwaitTerminal(t, srv.URL, id1)
	if !ok {
		return
	}
	if task.Status.State != taskStateCompleted || task.Artifacts[0].Parts[0].Text != "echo:echo-mid" {
		t.Errorf("mid-wake-turn task: %+v", task)
	}
	id2, ok := a2aSend(t, srv.URL, "block-mid")
	if !ok {
		return
	}
	<-bstarted
	canceled, ok := a2aCancel(t, srv.URL, id2)
	if !ok {
		return
	}
	if canceled.Status.State != taskStateCanceled {
		t.Errorf("cancel state = %s", canceled.Status.State)
	}
	<-bexited

	select {
	case err := <-finished:
		t.Fatalf("wake turn ended early (err=%v); A2A traffic must not touch it", err)
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("wake turn context was canceled: %v", err)
		}
	case <-time.After(a2aStressFailsafe):
		t.Fatal("wake turn never finished after release")
	}
	if !w.sess.WakeRunning() {
		t.Error("wake loop must still be running")
	}
}
