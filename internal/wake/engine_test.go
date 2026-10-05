package wake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func baseCfg(dir string) config.WakeConfig {
	return config.WakeConfig{
		Enabled: true, IntervalSeconds: 60, BackoffFactor: 1.5, MinIntervalSeconds: 30,
		MaxIntervalSeconds: 600, JitterPercent: 0, MaxTurnsPerHour: 3, TurnTimeoutSeconds: 180,
		KillSwitchFile: filepath.Join(dir, "STOP"), HeartbeatStaleSeconds: 180, ConsecutiveAlarms: 1,
		Checks: []config.WakeCheck{{Name: "f", Type: "file_contains", Path: "x", Contains: "BAD", TimeoutSeconds: 1}},
	}
}

type fakeProbe struct{ obs Observation }

func (p *fakeProbe) fn(ctx context.Context, c config.WakeCheck) Observation { return p.obs }

func newEngine(t *testing.T, mut func(*Deps), probe *fakeProbe) (*Engine, *FakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	clk := NewFakeClock(time.Unix(1_700_000_000, 0))
	d := Deps{Cfg: baseCfg(dir), SessionID: "s1", Dir: dir, Clock: clk, Checks: probe.fn,
		Rand: func() float64 { return 0.5 }, Getenv: func(string) string { return "" }}
	if mut != nil {
		mut(&d)
	}
	e, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, clk, dir
}

func accept(n *int) func(Escalation) error {
	return func(Escalation) error { *n++; return nil }
}

func TestQuietTicksNeverEnqueueAndBackOff(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: false}}
	e, _, _ := newEngine(t, nil, p)
	n := 0
	var delays []time.Duration
	for i := 0; i < 8; i++ {
		r := e.Tick(context.Background(), accept(&n))
		if r.Outcome != OutcomeQuiet {
			t.Fatalf("tick %d: %v", i, r.Outcome)
		}
		delays = append(delays, e.NextDelay())
	}
	if n != 0 {
		t.Fatalf("quiet ticks must never enqueue, got %d", n)
	}
	if delays[0] != 90*time.Second || delays[1] != 135*time.Second {
		t.Fatalf("x1.5 backoff from 60s: %v", delays[:2])
	}
	if delays[7] != 600*time.Second {
		t.Fatalf("backoff must cap at max: %v", delays[7])
	}
}

func TestChangeResetsBackoffAndEscalatesOnce(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, _ := newEngine(t, nil, p)
	n := 0
	for i := 0; i < 3; i++ {
		e.Tick(context.Background(), accept(&n))
	}
	p.obs = Observation{Exists: true, Contains: true}
	var got Escalation
	r := e.Tick(context.Background(), func(x Escalation) error { got = x; n++; return nil })
	if r.Outcome != OutcomeAlarm || !r.Escalated || n != 1 {
		t.Fatalf("alarm must escalate exactly once: %+v n=%d", r, n)
	}
	if !strings.Contains(got.Text, "[TIMER-SOURCED TURN") || !strings.Contains(got.Reason, "f") {
		t.Fatalf("escalation content: %+v", got)
	}
	if e.NextDelay() != 60*time.Second {
		t.Fatalf("alarm resets interval to base, got %v", e.NextDelay())
	}
}

func TestNextDelayNeverBelowMin(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, _ := newEngine(t, func(d *Deps) {
		d.Cfg.IntervalSeconds = 0
		d.Cfg.JitterPercent = 50
		d.Rand = func() float64 { return 0 }
	}, p)
	e.Tick(context.Background(), func(Escalation) error { return nil })
	if d := e.NextDelay(); d < 30*time.Second {
		t.Fatalf("delay %v below min_interval_seconds", d)
	}
}

func TestJitterBounds(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	lo, _, _ := newEngine(t, func(d *Deps) { d.Cfg.JitterPercent = 10; d.Rand = func() float64 { return 0 } }, p)
	hi, _, _ := newEngine(t, func(d *Deps) { d.Cfg.JitterPercent = 10; d.Rand = func() float64 { return 0.999999 } }, p)
	if lo.NextDelay() != 54*time.Second {
		t.Fatalf("-10%% of 60s = 54s, got %v", lo.NextDelay())
	}
	if h := hi.NextDelay(); h < 65*time.Second || h > 66*time.Second {
		t.Fatalf("+10%% of 60s ~ 66s, got %v", h)
	}
}

func TestHourlyCapAndWindowSlides(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, nil, p)
	n := 0
	var last TickResult
	for i := 0; i < 6; i++ {
		p.obs.Contains = true
		last = e.Tick(context.Background(), accept(&n))
		e.TurnDone()
		clk.Advance(time.Minute)
		p.obs.Contains = false // clear so the next trip is a new transition
		e.Tick(context.Background(), accept(&n))
		e.TurnDone()
		clk.Advance(time.Minute)
	}
	if n != 3 {
		t.Fatalf("cap 3/hour, enqueued %d", n)
	}
	if last.Suppressed != "hourly_cap" {
		t.Fatalf("want suppressed hourly_cap, got %q", last.Suppressed)
	}
	clk.Advance(61 * time.Minute)
	p.obs.Contains = true
	r := e.Tick(context.Background(), accept(&n))
	if !r.Escalated || n != 4 {
		t.Fatalf("window must slide after an hour: %+v n=%d", r, n)
	}
}

func TestSingleFlight(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, func(d *Deps) { d.Cfg.MaxTurnsPerHour = 60 }, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	clk.Advance(time.Minute)
	r := e.Tick(context.Background(), accept(&n))
	if n != 1 || r.Suppressed != "single_flight" || !e.InFlight() {
		t.Fatalf("second tick while in flight must not enqueue: n=%d %+v", n, r)
	}
	e.TurnDone()
	clk.Advance(time.Minute)
	e.Tick(context.Background(), accept(&n))
	if n != 1 {
		t.Fatalf("unchanged alarm after TurnDone stays suppressed, n=%d", n)
	}
}

func TestBusyEnqueueIsDeferredAndDoesNotCountAgainstCap(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, func(d *Deps) { d.Cfg.MaxTurnsPerHour = 1 }, p)
	r := e.Tick(context.Background(), func(Escalation) error { return errors.New("busy") })
	if !r.Deferred || r.Escalated || e.InFlight() {
		t.Fatalf("busy: %+v inflight=%v", r, e.InFlight())
	}
	clk.Advance(time.Minute)
	n := 0
	r = e.Tick(context.Background(), accept(&n))
	if !r.Escalated || n != 1 {
		t.Fatalf("retry on next tick must succeed and cap must be unspent: %+v", r)
	}
}

func TestKillSwitchStopsUntilFileRemovedAndResume(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, _, dir := newEngine(t, nil, p)
	stop := filepath.Join(dir, "STOP")
	_ = os.WriteFile(stop, nil, 0o600)
	n := 0
	r := e.Tick(context.Background(), accept(&n))
	if !r.Killed || n != 0 || !e.Stopped() {
		t.Fatalf("kill: %+v n=%d", r, n)
	}
	if err := e.Resume(); err == nil {
		t.Fatal("resume must fail while the kill file exists")
	}
	_ = os.Remove(stop)
	if err := e.Resume(); err != nil || e.Stopped() {
		t.Fatalf("resume after removal: %v stopped=%v", err, e.Stopped())
	}
}

func TestKillSwitchAppearingBeforeEnqueueBlocksIt(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, _, dir := newEngine(t, nil, p)
	n := 0
	r := e.Tick(context.Background(), func(Escalation) error {
		_ = os.WriteFile(filepath.Join(dir, "STOP"), nil, 0o600)
		n++
		return nil
	})
	_ = r
	r2 := e.Tick(context.Background(), accept(&n))
	if !r2.Killed || n != 1 {
		t.Fatalf("kill file must stop the very next tick: %+v n=%d", r2, n)
	}
}

func TestHeartbeatAndStateAtomicFilesAndModes(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, dir := newEngine(t, nil, p)
	e.Tick(context.Background(), func(Escalation) error { return nil })
	for _, f := range []string{"s1.heartbeat", "s1.state.json", "s1.audit.jsonl"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s missing: %v", f, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v, want 0600", f, st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		// t.TempDir is created 0700; engine must also create a missing dir as 0700.
		t.Fatalf("dir mode %v", st.Mode().Perm())
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(ents) != 0 {
		t.Fatalf("temp files leaked: %v", ents)
	}
}

func TestMissingDirCreated0700(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "wake")
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, err := New(Deps{Cfg: baseCfg(base), SessionID: "s", Dir: dir, Checks: p.fn})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", st, err)
	}
}

func TestRestartKeepsCapWindowAndTickCounter(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	dir := t.TempDir()
	clk := NewFakeClock(time.Unix(1_700_000_000, 0))
	mk := func() *Engine {
		e, err := New(Deps{Cfg: baseCfg(dir), SessionID: "s1", Dir: dir, Clock: clk, Checks: p.fn})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	e1 := mk()
	n := 0
	for i := 0; i < 3; i++ {
		p.obs.Contains = true
		e1.Tick(context.Background(), accept(&n))
		e1.TurnDone()
		clk.Advance(time.Minute)
		p.obs.Contains = false // clear so each trip is a new alarm
		e1.Tick(context.Background(), accept(&n))
		clk.Advance(time.Minute)
	}
	e1.Close() // simulated crash/restart
	e2 := mk()
	defer e2.Close()
	p.obs.Contains = true
	r := e2.Tick(context.Background(), accept(&n))
	if n != 3 || r.Suppressed != "hourly_cap" {
		t.Fatalf("cap window must survive restart: n=%d %+v", n, r)
	}
	if r.Tick != 7 {
		t.Fatalf("tick counter must continue, got %d", r.Tick)
	}
}

func TestCorruptStateFileFailsClosedNotPanics(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "s1.state.json"), []byte("{not json"), 0o600)
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, err := New(Deps{Cfg: baseCfg(dir), SessionID: "s1", Dir: dir, Checks: p.fn})
	if err != nil {
		return // refusing to start is acceptable
	}
	defer e.Close()
	n := 0
	r := e.Tick(context.Background(), accept(&n))
	if n > 1 {
		t.Fatalf("corrupt state must not allow a storm: %+v", r)
	}
}

func TestStateWriteFailureFailsClosed(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, _, dir := newEngine(t, nil, p)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	_ = os.Chmod(dir, 0o500) // read-only: temp file creation fails
	defer os.Chmod(dir, 0o700)
	n := 0
	r := e.Tick(context.Background(), accept(&n))
	if n != 0 || !r.Degraded || r.Suppressed != "degraded" {
		t.Fatalf("no enqueue without a durable write: n=%d %+v", n, r)
	}
}

func TestClockJumpForwardRunsImmediateTickAndResetsBackoff(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, clk, _ := newEngine(t, nil, p)
	for i := 0; i < 4; i++ {
		e.Tick(context.Background(), func(Escalation) error { return nil })
	}
	clk.Advance(3 * time.Hour) // laptop sleep
	r := e.Tick(context.Background(), func(Escalation) error { return nil })
	if !r.ResumedAfterGap {
		t.Fatal("gap must be flagged")
	}
	if e.NextDelay() != 90*time.Second { // reset to 60 then one quiet tick x1.5
		t.Fatalf("backoff reset after gap, got %v", e.NextDelay())
	}
}

func TestClockJumpBackwardIsHarmless(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, nil, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	e.TurnDone()
	clk.Advance(-2 * time.Hour)
	r := e.Tick(context.Background(), accept(&n))
	if n > 3 {
		t.Fatalf("backward jump must not reset the cap: n=%d %+v", n, r)
	}
}

func TestSecondEngineRefusedByLock(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, dir := newEngine(t, nil, p)
	_ = e
	_, err := New(Deps{Cfg: baseCfg(dir), SessionID: "s1", Dir: dir, Checks: p.fn})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
}

func TestMissingSecretEnvRefuses(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	dir := t.TempDir()
	cfg := baseCfg(dir)
	cfg.SecretEnv = []string{"WAKE_API_TOKEN"}
	var stderr strings.Builder
	_, err := New(Deps{Cfg: cfg, SessionID: "s1", Dir: dir, Checks: p.fn, Stderr: &stderr,
		Getenv: func(string) string { return "" }})
	if !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("want ErrMissingSecret, got %v", err)
	}
	audit, _ := os.ReadFile(filepath.Join(dir, "s1.audit.jsonl"))
	if !strings.Contains(string(audit), "WAKE_API_TOKEN") || !strings.Contains(stderr.String(), "WAKE_API_TOKEN") {
		t.Fatalf("alert must name the variable in audit and stderr: %q / %q", audit, stderr.String())
	}
	// Names only: a value must never be written anywhere.
	cfg2 := baseCfg(dir)
	cfg2.SecretEnv = []string{"WAKE_API_TOKEN"}
	e, err := New(Deps{Cfg: cfg2, SessionID: "s2", Dir: dir, Checks: p.fn,
		Getenv: func(string) string { return "sekrit-value-123" }})
	if err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background(), func(Escalation) error { return nil })
	e.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "s2.*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "sekrit-value-123") {
			t.Fatalf("secret value leaked into %s", f)
		}
	}
}

func TestAuditRecordsSummaryFieldsAndRedactsQuery(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	cfg := func(d *Deps) {
		d.Cfg.Checks = []config.WakeCheck{{Name: "api", Type: "http_status", URL: "http://h/x?token=abc123", Expect: 200}}
		d.Summary = func() (int, int) { return 494, 1500 }
	}
	p.obs = Observation{HTTPStatus: 500}
	e, _, dir := newEngine(t, cfg, p)
	e.Tick(context.Background(), func(Escalation) error { return nil })
	b, _ := os.ReadFile(filepath.Join(dir, "s1.audit.jsonl"))
	s := string(b)
	if !strings.Contains(s, `"summary_chars":494`) || !strings.Contains(s, `"summary_cap":1500`) {
		t.Fatalf("audit must carry summary fields: %s", s)
	}
	if strings.Contains(s, "abc123") {
		t.Fatalf("query strings must be redacted in audit: %s", s)
	}
}

func TestAuditRotatesBySize(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, dir := newEngine(t, func(d *Deps) { d.AuditMaxBytes = 2048 }, p)
	for i := 0; i < 100; i++ {
		e.Tick(context.Background(), func(Escalation) error { return nil })
	}
	if _, err := os.Stat(filepath.Join(dir, "s1.audit.jsonl.1")); err != nil {
		t.Fatalf("expected a rotated audit file: %v", err)
	}
}

func TestBandFlipIsChangedAndEscalates(t *testing.T) {
	p := &fakeProbe{}
	num := func(v float64) Observation { return Observation{Number: &v} }
	cfg := func(d *Deps) {
		d.Cfg.Checks = []config.WakeCheck{{Name: "lbl", Type: "http_json", URL: "http://h", Field: "x",
			LabelBand: &config.WakeBand{Upper: 1, Lower: -1, Exit: 0.5}}}
	}
	e, clk, _ := newEngine(t, cfg, p)
	n := 0
	p.obs = num(0.2)
	e.Tick(context.Background(), accept(&n)) // seeds neutral
	for _, v := range []float64{0.3, 0.4, 0.2} {
		p.obs = num(v)
		clk.Advance(time.Minute)
		if r := e.Tick(context.Background(), accept(&n)); r.Outcome != OutcomeQuiet {
			t.Fatalf("stable label must be quiet: %+v", r)
		}
	}
	if n != 0 {
		t.Fatalf("stable label costs zero turns, got %d", n)
	}
	p.obs = num(1.4)
	clk.Advance(time.Minute)
	r := e.Tick(context.Background(), accept(&n))
	if r.Outcome != OutcomeChanged || n != 1 {
		t.Fatalf("flip must be changed and escalate once: %+v n=%d", r, n)
	}
}

func TestHeartbeatEvery(t *testing.T) {
	cases := []struct {
		interval, stale int
		want            time.Duration
	}{
		{60, 180, 60 * time.Second},
		{600, 180, 60 * time.Second},
		{60, 30, 10 * time.Second},
		{60, 0, 60 * time.Second},
		{0, 2, time.Second},
	}
	for _, c := range cases {
		got := HeartbeatEvery(config.WakeConfig{IntervalSeconds: c.interval, HeartbeatStaleSeconds: c.stale})
		if got != c.want {
			t.Errorf("interval=%d stale=%d: got %v want %v", c.interval, c.stale, got, c.want)
		}
	}
}

func readAudit(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "s1.audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestPersistentAlarmEscalatesOnceThenReArms(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, func(d *Deps) { d.Cfg.MaxTurnsPerHour = 100 }, p)
	n := 0
	for i := 0; i < 8; i++ {
		r := e.Tick(context.Background(), accept(&n))
		e.TurnDone() // turn finished: single_flight no longer applies
		if i > 0 && (r.Escalated || r.Suppressed != "single_flight") {
			t.Fatalf("tick %d: persistent alarm must be suppressed: %+v", i, r)
		}
		clk.Advance(20 * time.Second)
	}
	if n != 1 {
		t.Fatalf("persistent alarm over 8 ticks must give exactly 1 turn, got %d", n)
	}
	p.obs.Contains = false
	e.Tick(context.Background(), accept(&n))
	p.obs.Contains = true
	e.Tick(context.Background(), accept(&n))
	if n != 2 {
		t.Fatalf("clear then re-trip must escalate again, got %d", n)
	}
}

func TestPersistentAlarmSuppressionSurvivesRestartAndCapStillBounds(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, dir := newEngine(t, func(d *Deps) { d.Cfg.MaxTurnsPerHour = 2 }, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	e.TurnDone()
	e.Close()
	cfg2 := baseCfg(dir)
	cfg2.MaxTurnsPerHour = 2
	e2, err := New(Deps{Cfg: cfg2, SessionID: "s1", Dir: dir, Clock: clk, Checks: p.fn,
		Rand: func() float64 { return 0.5 }, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	clk.Advance(time.Minute)
	e2.Tick(context.Background(), accept(&n))
	if n != 1 {
		t.Fatalf("restart must not re-escalate an unchanged alarm, n=%d", n)
	}
	// distinct trips are bounded by the cap
	for i := 0; i < 4; i++ {
		p.obs.Contains = false
		e2.Tick(context.Background(), accept(&n))
		p.obs.Contains = true
		e2.Tick(context.Background(), accept(&n))
		e2.TurnDone()
	}
	if n != 2 {
		t.Fatalf("cap 2/hour must bound re-trips, n=%d", n)
	}
}

func TestDeferredAndCappedTicksDoNotConsumeAlarm(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, _ := newEngine(t, nil, p)
	e.Tick(context.Background(), func(Escalation) error { return errors.New("busy") })
	clk.Advance(time.Minute)
	n := 0
	if r := e.Tick(context.Background(), accept(&n)); !r.Escalated {
		t.Fatalf("deferred alarm must still escalate next tick: %+v", r)
	}
}

func TestKilledTickNumberNotRepeatedAfterResume(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, dir := newEngine(t, nil, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	stop := filepath.Join(dir, "STOP")
	_ = os.WriteFile(stop, nil, 0o600)
	k := e.Tick(context.Background(), accept(&n))
	if !k.Killed || k.Tick != 2 {
		t.Fatalf("killed tick: %+v", k)
	}
	_ = os.Remove(stop)
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	r := e.Tick(context.Background(), accept(&n))
	if r.Tick != 3 {
		t.Fatalf("tick after resume must be 3, got %d", r.Tick)
	}
}

func TestResumeWithKillFileReturnsSentinel(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true}}
	e, _, dir := newEngine(t, nil, p)
	_ = os.WriteFile(filepath.Join(dir, "STOP"), nil, 0o600)
	if err := e.Resume(); !errors.Is(err, ErrKillSwitchPresent) {
		t.Fatalf("want ErrKillSwitchPresent, got %v", err)
	}
}

func TestAuditRecordsCarryRFC3339UTCTimestamp(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, _, dir := newEngine(t, nil, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	e.NoteTurnResult(1, "done")
	recs := readAudit(t, dir)
	if len(recs) < 2 {
		t.Fatalf("records: %v", recs)
	}
	for _, r := range recs {
		ts, ok := r["ts"].(string)
		if !ok {
			t.Fatalf("missing ts: %v", r)
		}
		tm, err := time.Parse(time.RFC3339, ts)
		if err != nil || !strings.HasSuffix(ts, "Z") || tm.Location() != time.UTC {
			t.Fatalf("ts must be RFC3339 UTC: %q (%v)", ts, err)
		}
	}
}

func TestRestartAfterQuietClearedAlarmEscalatesOnce(t *testing.T) {
	p := &fakeProbe{obs: Observation{Exists: true, Contains: true}}
	e, clk, dir := newEngine(t, func(d *Deps) { d.Cfg.MaxTurnsPerHour = 100 }, p)
	n := 0
	e.Tick(context.Background(), accept(&n))
	e.TurnDone()
	clk.Advance(time.Minute)
	p.obs.Contains = false
	e.Tick(context.Background(), accept(&n)) // quiet tick clears the alarm
	st, err := os.ReadFile(filepath.Join(dir, "s1.state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st), "alarm_sig") {
		t.Fatalf("saved state must not hold alarm_sig after a quiet tick: %s", st)
	}
	e.Close()
	cfg := baseCfg(dir)
	cfg.MaxTurnsPerHour = 100
	e2, err := New(Deps{Cfg: cfg, SessionID: "s1", Dir: dir, Clock: clk, Checks: p.fn,
		Rand: func() float64 { return 0.5 }, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	p.obs.Contains = true
	for i := 0; i < 4; i++ {
		clk.Advance(time.Minute)
		e2.Tick(context.Background(), accept(&n))
		e2.TurnDone()
	}
	if n != 2 {
		t.Fatalf("re-tripped alarm after restart must give exactly one more turn (total 2), got %d", n)
	}
}

func TestAlarmChangedWhileInFlightEscalatesOnceAfterTurnDone(t *testing.T) {
	cfg2 := func(d *Deps) {
		d.Cfg.MaxTurnsPerHour = 100
		d.Cfg.Checks = []config.WakeCheck{
			{Name: "a", Type: "file_contains", Path: "x", Contains: "A", TimeoutSeconds: 1},
			{Name: "b", Type: "file_contains", Path: "x", Contains: "B", TimeoutSeconds: 1},
		}
	}
	var contA, contB bool
	probe := func(_ context.Context, c config.WakeCheck) Observation {
		return Observation{Exists: true, Contains: (c.Name == "a" && contA) || (c.Name == "b" && contB)}
	}
	e, clk, dir := newEngine(t, func(d *Deps) { cfg2(d); d.Checks = probe }, &fakeProbe{})
	n := 0
	contA = true
	e.Tick(context.Background(), accept(&n)) // A escalates, turn in flight
	clk.Advance(time.Minute)
	contA, contB = false, true
	r := e.Tick(context.Background(), accept(&n)) // B appears while A in flight
	if r.Escalated || r.Suppressed != "single_flight" {
		t.Fatalf("B during flight must be suppressed: %+v", r)
	}
	e.TurnDone()
	for i := 0; i < 5; i++ {
		clk.Advance(time.Minute)
		e.Tick(context.Background(), accept(&n))
		e.TurnDone()
	}
	if n != 2 {
		t.Fatalf("A once plus B exactly once, got %d", n)
	}
	unchanged := 0
	for _, rec := range readAudit(t, dir) {
		if rec["reason"] == "alarm_unchanged" {
			unchanged++
		}
	}
	if unchanged != 4 {
		t.Fatalf("want 4 alarm_unchanged suppressions, got %d", unchanged)
	}
}
