package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// failsafe bounds every blocking receive so a regression fails the test with a
// message instead of hanging until the package timeout. It is never a wait the
// passing path depends on.
const stressFailsafe = 20 * time.Second

// wakeFleet is n wake-enabled sessions driven by hand-fired fake timers. Each
// session is driven in lockstep: fire one tick, then receive its TickDone.
type wakeFleet struct {
	t        *testing.T
	sessions []*ChatSession
	dirs     []string
	fires    []func()
	dones    []chan struct{}
	ticks    atomic.Int32
	runs     atomic.Int32 // model calls made by any fleet session (must stay 0 when quiet)
}

func newWakeFleet(t *testing.T, n int, probe wake.CheckFunc) *wakeFleet {
	t.Helper()
	return newWakeFleetWith(t, n, probe, nil)
}

// newWakeFleetWith calls onDir(dir) for each session before its wake loop starts.
func newWakeFleetWith(t *testing.T, n int, probe wake.CheckFunc, onDir func(string)) *wakeFleet {
	t.Helper()
	f := &wakeFleet{t: t}
	for i := 0; i < n; i++ {
		dir := t.TempDir()
		if onDir != nil {
			onDir(dir)
		}
		bf := makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
			f.runs.Add(1)
			return "ok", nil
		}, nil)
		sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
		after, fire := manualAfter()
		done := make(chan struct{}, 4)
		if err := sess.StartWake(WakeOptions{
			Dir:       dir,
			Checks:    probe,
			After:     after,
			TurnAfter: func(time.Duration) <-chan time.Time { return nil },
			Clock:     wake.NewFakeClock(time.Unix(1_700_000_000, 0)),
			TickDone: func() {
				f.ticks.Add(1)
				done <- struct{}{}
			},
		}); err != nil {
			t.Fatal(err)
		}
		if !sess.WakeRunning() { // a cfg without wake.enabled makes StartWake a silent no-op
			t.Fatalf("session %d: wake loop not running after StartWake", i)
		}
		f.sessions = append(f.sessions, sess)
		f.dirs = append(f.dirs, dir)
		f.fires = append(f.fires, fire)
		f.dones = append(f.dones, done)
	}
	return f
}

func quietProbe(context.Context, config.WakeCheck) wake.Observation {
	return wake.Observation{Exists: true}
}

func (f *wakeFleet) awaitDone(i int) bool {
	select {
	case <-f.dones[i]:
		return true
	case <-time.After(stressFailsafe):
		f.t.Errorf("wake session %d: tick never completed", i)
		return false
	}
}

// tick drives every session through `rounds` ticks (plus the immediate first
// tick) in the background; the returned func waits for all of them.
func (f *wakeFleet) tick(rounds int) (wait func()) {
	var wg sync.WaitGroup
	for i := range f.sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !f.awaitDone(i) {
				return
			}
			for r := 0; r < rounds; r++ {
				f.fires[i]()
				if !f.awaitDone(i) {
					return
				}
			}
		}(i)
	}
	return wg.Wait
}

// tickUntil keeps all sessions ticking until stop is closed.
func (f *wakeFleet) tickUntil(stop <-chan struct{}) (wait func()) {
	var wg sync.WaitGroup
	for i := range f.sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !f.awaitDone(i) {
				return
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				f.fires[i]()
				if !f.awaitDone(i) {
					return
				}
			}
		}(i)
	}
	return wg.Wait
}

func (f *wakeFleet) closeAll() {
	for i, s := range f.sessions {
		s.Close()
		if s.WakeRunning() {
			f.t.Errorf("wake session %d still running after Close", i)
		}
	}
}

// TestStress_WakeSessions20WithNormal10Concurrent: 20 quiet wake sessions tick
// while 10 normal sessions run multi-turn chats. Normal turns must be
// untouched, quiet ticks must make zero model calls, every tick must complete.
func TestStress_WakeSessions20WithNormal10Concurrent(t *testing.T) {
	const wakeN, normalN, rounds, turns = 20, 10, 50, 3
	fleet := newWakeFleet(t, wakeN, quietProbe)
	waitTicks := fleet.tick(rounds)

	type rec struct {
		mu       sync.Mutex
		queries  []string
		canceled int
	}
	recs := make([]*rec, normalN)
	var wg sync.WaitGroup
	for i := 0; i < normalN; i++ {
		r := &rec{}
		recs[i] = r
		finished := make(chan struct{}, 1)
		bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
			r.mu.Lock()
			r.queries = append(r.queries, q)
			if ctx.Err() != nil {
				r.canceled++
			}
			r.mu.Unlock()
			finished <- struct{}{}
			return "response-to-" + q, nil
		}, nil)
		sess := startSession(t, bf)
		wg.Add(1)
		go func(s *ChatSession, idx int) {
			defer wg.Done()
			for turn := 0; turn < turns; turn++ {
				if err := s.Submit(fmt.Sprintf("q-%d-%d", idx, turn)); err != nil {
					t.Errorf("submit: %v", err)
					return
				}
				select {
				case <-finished:
				case <-time.After(stressFailsafe):
					t.Errorf("normal session %d turn %d never finished", idx, turn)
					return
				}
			}
		}(sess, i)
	}
	wg.Wait()
	waitTicks()

	for i, r := range recs {
		r.mu.Lock()
		if len(r.queries) != turns || r.canceled != 0 {
			t.Errorf("normal session %d: got %d turns (want %d), %d canceled", i, len(r.queries), turns, r.canceled)
		}
		for turn, q := range r.queries {
			// Conversation memory wraps follow-up turns in a history preamble;
			// the current question is always the suffix.
			if want := fmt.Sprintf("q-%d-%d", i, turn); !strings.HasSuffix(q, want) {
				t.Errorf("normal session %d turn %d: query %q does not end with %q", i, turn, q, want)
			}
		}
		r.mu.Unlock()
	}
	if got, want := fleet.ticks.Load(), int32(wakeN*(rounds+1)); got != want {
		t.Errorf("ticks completed = %d, want %d", got, want)
	}
	if n := fleet.runs.Load(); n != 0 {
		t.Errorf("quiet ticks made %d model calls, want 0", n)
	}
	fleet.closeAll()
}

// TestStress_UserTurnInterruptsWakeTurnAndRecovers: a user turn submitted
// while a wake turn runs cancels it, the user turn completes, and the wake
// loop stays alive.
func TestStress_UserTurnInterruptsWakeTurnAndRecovers(t *testing.T) {
	dir := t.TempDir()
	wakeStarted := make(chan struct{}, 1)
	wakeInterrupted := make(chan struct{}, 1)
	userDone := make(chan string, 1)
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		// Later turns carry earlier ones as history, so classify by the
		// current question (the suffix), not by Contains.
		if !strings.HasSuffix(q, "user-query") {
			wakeStarted <- struct{}{}
			<-ctx.Done()
			wakeInterrupted <- struct{}{}
			return "", ctx.Err()
		}
		userDone <- "user-response-to-" + q
		return "user-response-to-" + q, nil
	}, nil)
	sess := startWakeSession(t, wakeTestCfg(dir, true), bf)
	after, fire := manualAfter()
	tickDone := make(chan struct{}, 4)
	var alarm atomic.Bool
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		return wake.Observation{Exists: true, Contains: alarm.Load()}
	}
	if err := sess.StartWake(WakeOptions{
		Dir: dir, Checks: probe, After: after,
		TurnAfter: func(time.Duration) <-chan time.Time { return nil },
		Clock:     wake.NewFakeClock(time.Unix(1_700_000_000, 0)),
		TickDone:  func() { tickDone <- struct{}{} },
	}); err != nil {
		t.Fatal(err)
	}
	recv := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(stressFailsafe):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	recv(tickDone, "first tick")
	alarm.Store(true)
	fire()
	recv(tickDone, "alarm tick")
	recv(wakeStarted, "wake turn start")

	if err := sess.Submit("user-query"); err != nil {
		t.Fatal(err)
	}
	recv(wakeInterrupted, "wake turn interruption")
	select {
	case got := <-userDone:
		if !strings.HasSuffix(got, "user-query") { // interrupted wake turn stays in history
			t.Fatalf("user turn answer %q", got)
		}
	case <-time.After(stressFailsafe):
		t.Fatal("user turn never completed")
	}
	if !sess.WakeRunning() {
		t.Fatal("wake loop must survive an interrupted wake turn")
	}
	sess.Close()
}

// TestStress_DeferredWakeTurnAfterUserSubmit: a second wake submission while
// one runs and one is queued returns ErrSessionBusy instead of queueing.
func TestStress_DeferredWakeTurnAfterUserSubmit(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var runCount atomic.Int32
	bf := makeFakeChatBuildFunc("a1", func(ctx context.Context, q string, _ *telemetry.EventBus) (string, error) {
		runCount.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "ok", nil
	}, nil)
	sess := startSession(t, bf)
	if _, err := sess.SubmitWake("wake-1", ""); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := sess.SubmitWake("wake-2", ""); err != nil { // fills the 1-slot queue
		t.Fatalf("second wake submission should queue: %v", err)
	}
	if _, err := sess.SubmitWake("wake-3", ""); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("third wake submission must be ErrSessionBusy, got %v", err)
	}
	if runCount.Load() != 1 {
		t.Fatal("a wake submission must not restart the running turn")
	}
	close(release)
}

// TestStress_WakeLoopFaultInjection_CheckerError: checker errors on every
// second probe; loop keeps ticking and reports no panic.
func TestStress_WakeLoopFaultInjection_CheckerError(t *testing.T) {
	var calls, errs atomic.Int32
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		if calls.Add(1)%2 == 0 {
			errs.Add(1)
			return wake.Observation{Err: errors.New("simulated checker error")}
		}
		return wake.Observation{Exists: true}
	}
	fleet := newWakeFleet(t, 1, probe)
	fleet.tick(10)()
	if calls.Load() != 11 || errs.Load() != 5 {
		t.Fatalf("probe calls=%d errors=%d, want 11 and 5", calls.Load(), errs.Load())
	}
	if !fleet.sessions[0].WakeRunning() {
		t.Fatal("loop must keep running despite checker errors")
	}
	fleet.closeAll()
}

// TestStress_WakeLoopFaultInjection_CheckerHangs: a checker that never returns
// on its own must not wedge shutdown; Close cancels it and the loop exits.
func TestStress_WakeLoopFaultInjection_CheckerHangs(t *testing.T) {
	entered := make(chan struct{}, 1)
	probe := func(ctx context.Context, _ config.WakeCheck) wake.Observation {
		entered <- struct{}{}
		<-ctx.Done()
		return wake.Observation{Err: ctx.Err()}
	}
	fleet := newWakeFleet(t, 1, probe)
	select {
	case <-entered:
	case <-time.After(stressFailsafe):
		t.Fatal("checker never entered")
	}
	closed := make(chan struct{})
	go func() { fleet.closeAll(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(stressFailsafe):
		t.Fatal("Close wedged behind a hung checker")
	}
}

// TestStress_WakeLoopFaultInjection_KillSwitchMidTick: the kill file appears
// while a tick runs; the loop stops on the next tick and probes no more.
func TestStress_WakeLoopFaultInjection_KillSwitchMidTick(t *testing.T) {
	var calls atomic.Int32
	var killFile atomic.Value
	probe := func(context.Context, config.WakeCheck) wake.Observation {
		if calls.Add(1) == 3 {
			_ = os.WriteFile(killFile.Load().(string), nil, 0o600)
		}
		return wake.Observation{Exists: true}
	}
	fleet := newWakeFleetWith(t, 1, probe, func(dir string) { killFile.Store(filepath.Join(dir, "STOP")) })
	for i := 0; i < 3; i++ { // ticks 2, 3 and the killed tick
		fleet.fires[0]()
		if !fleet.awaitDone(0) {
			return
		}
	}
	fleet.sessions[0].StopWakeAndWait()
	if fleet.sessions[0].WakeRunning() {
		t.Fatal("loop must stop when the kill switch appears")
	}
	if calls.Load() != 3 {
		t.Fatalf("probe calls = %d, want 3 (no probing after kill)", calls.Load())
	}
	fleet.closeAll()
}

// percentile of an ascending-sorted slice (nearest rank).
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(float64(len(sorted))*p+0.9999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestStress_LatencyMeasurement_P50P99: turn latency (Submit -> runner
// finished) with 0 vs 20 continuously ticking wake sessions.
func TestStress_LatencyMeasurement_P50P99(t *testing.T) {
	const samples = 200
	measure := func(wakeN int) (p50, p99 time.Duration) {
		stop := make(chan struct{})
		var waitTicks func()
		var fleet *wakeFleet
		if wakeN > 0 {
			fleet = newWakeFleet(t, wakeN, quietProbe)
			waitTicks = fleet.tickUntil(stop)
		}
		finished := make(chan struct{}, 1)
		sess := startSession(t, makeFakeChatBuildFunc("a1", func(context.Context, string, *telemetry.EventBus) (string, error) {
			finished <- struct{}{}
			return "ok", nil
		}, nil))
		lat := make([]time.Duration, 0, samples)
		for i := 0; i < samples; i++ {
			start := time.Now()
			if err := sess.Submit(fmt.Sprintf("q-%d", i)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-time.After(stressFailsafe):
				t.Fatalf("turn %d never finished", i)
			}
			lat = append(lat, time.Since(start))
		}
		sess.Close()
		if fleet != nil {
			close(stop)
			waitTicks()
			t.Logf("  (%d wake ticks completed during measurement)", fleet.ticks.Load())
			if fleet.runs.Load() != 0 {
				t.Errorf("quiet wake sessions made %d model calls", fleet.runs.Load())
			}
			fleet.closeAll()
		}
		sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
		return percentile(lat, 0.50), percentile(lat, 0.99)
	}
	b50, b99 := measure(0)
	w50, w99 := measure(20)
	t.Logf("baseline (0 wake):  p50=%v p99=%v  (%d samples)", b50, b99, samples)
	t.Logf("with 20 wake:       p50=%v p99=%v  (%d samples)", w50, w99, samples)
	t.Logf("p99 ratio = %.2fx (note: each turn includes a fixed ~50ms drain in chat_session.go, which dilutes the ratio)", float64(w99)/float64(b99))
	if w99 > 5*b99 {
		t.Fatalf("p99 with wake load %v exceeds 5x baseline p99 %v", w99, b99)
	}
}

// --- orchestrator under wake load -------------------------------------------

// scriptProvider returns a fixed response sequence (one instance per run).
type scriptProvider struct {
	mu   sync.Mutex
	resp []llm.GenerateResult
	i    int
}

func (p *scriptProvider) Generate(context.Context, string, []llm.Message, []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.i >= len(p.resp) {
		return nil, fmt.Errorf("scriptProvider exhausted after %d calls", p.i)
	}
	r := p.resp[p.i]
	p.i++
	return &r, nil
}
func (p *scriptProvider) GetName() string  { return "script" }
func (p *scriptProvider) GetModel() string { return "stub" }

type fixedProvider struct{ answer string }

func (p *fixedProvider) Generate(context.Context, string, []llm.Message, []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return &llm.GenerateResult{Response: p.answer, FinishReason: "stop"}, nil
}
func (p *fixedProvider) GetName() string  { return "fixed" }
func (p *fixedProvider) GetModel() string { return "stub" }

// runSupervisor builds a fresh ReAct supervisor with two real worker agents and
// runs it once: delegate to researcher, delegate to writer, then synthesize.
func runSupervisor(ctx context.Context, query string) (string, error) {
	bus := telemetry.NewEventBus(256)
	mk := func(name, answer string) *agent.Agent {
		return agent.NewAgent(&config.AgentDefinition{Name: name, SystemPrompt: "You are " + name},
			&fixedProvider{answer: answer}, tools.NewToolRegistry(), bus, nil)
	}
	workers := map[string]agent.Runner{
		"researcher": mk("researcher", "findings: AI trends"),
		"writer":     mk("writer", "report: AI trends"),
	}
	call := func(name string) llm.GenerateResult {
		return llm.GenerateResult{
			FinishReason: "tool_calls",
			ToolCalls:    []llm.ToolCall{{ID: "call_" + name, Name: name, Arguments: map[string]interface{}{"task": "do it"}}},
		}
	}
	sup := &scriptProvider{resp: []llm.GenerateResult{
		call("delegate_to_researcher"),
		call("delegate_to_writer"),
		{Response: "FINAL: synthesis of AI trends", FinishReason: "stop"},
	}}
	orch := agent.NewOrchestrator(&config.OrchestratorConfig{
		Name: "Supervisor", Strategy: "ReAct", Agents: []string{"researcher", "writer"},
	}, sup, bus, workers)
	return orch.Run(ctx, query)
}

// TestStress_OrchestratorRunsUnaffectedByWakeSessions: real multi-agent
// orchestrator runs (ReAct supervisor delegating to two agents) produce the
// same output as the no-wake baseline while 20 wake sessions tick.
func TestStress_OrchestratorRunsUnaffectedByWakeSessions(t *testing.T) {
	baseline, err := runSupervisor(context.Background(), "build report")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(baseline, "synthesis of AI trends") {
		t.Fatalf("baseline output unexpected: %q", baseline)
	}

	fleet := newWakeFleet(t, 20, quietProbe)
	stop := make(chan struct{})
	waitTicks := fleet.tickUntil(stop)

	const runs = 40
	outs := make([]string, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = runSupervisor(context.Background(), "build report")
		}(i)
	}
	wg.Wait()
	close(stop)
	waitTicks()

	for i := range outs {
		if errs[i] != nil || outs[i] != baseline {
			t.Errorf("run %d: out=%q err=%v, want %q", i, outs[i], errs[i], baseline)
		}
	}
	if fleet.ticks.Load() < 20 {
		t.Errorf("only %d wake ticks completed", fleet.ticks.Load())
	}
	if fleet.runs.Load() != 0 {
		t.Errorf("quiet wake sessions made %d model calls", fleet.runs.Load())
	}
	t.Logf("%d orchestrator runs matched baseline; %d wake ticks ran concurrently", runs, fleet.ticks.Load())
	fleet.closeAll()
}
