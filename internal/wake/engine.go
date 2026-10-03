// Wake-up timer engine with checks, backoff, cap, and kill-switch.
package wake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

var (
	ErrLocked        = errors.New("wake: state dir locked by another process")
	ErrMissingSecret = errors.New("wake: secret_env variable not set")
)

type Deps struct {
	Cfg           config.WakeConfig // defaulted and validated
	SessionID     string
	Dir           string // wake state dir (0700 created if missing)
	Clock         Clock  // nil => RealClock
	Checks        CheckFunc
	Rand          func() float64          // nil => math/rand; returns [0,1)
	Getenv        func(string) string     // nil => os.Getenv
	Stderr        io.Writer               // nil => io.Discard
	Summary       func() (chars, cap int) // nil => (0,0)
	AuditMaxBytes int64                   // 0 => 1 MiB
}

type Escalation struct {
	Reason string // e.g. "alarm: log-fresh"
	Text   string // fixed-format turn text (BuildTurnText)
}

type TickResult struct {
	Tick            int
	Outcome         Outcome
	Results         []CheckResult
	Killed          bool // kill-switch present; caller must stop the loop
	Degraded        bool // state/audit write failed; no enqueue
	ResumedAfterGap bool
	Escalated       bool   // enqueue callback accepted the turn
	Deferred        bool   // enqueue callback returned an error (busy); alarm kept
	Suppressed      string // "" | "hourly_cap" | "single_flight" | "degraded"
	IntervalSeconds int
	SummaryChars    int
	SummaryCap      int
}

type Engine struct {
	cfg          config.WakeConfig
	sessionID    string
	dir          string
	clock        Clock
	checks       CheckFunc
	rand         func() float64
	getenv       func(string) string
	stderr       io.Writer
	summary      func() (int, int)
	auditMax     int64
	lock         *os.File
	state        *persisted
	interval     float64
	lastTickTime time.Time
	inflight     atomic.Bool
	lastTick     atomic.Int64 // tick number for Beat; Beat must not take mu (a tick may hold it during slow checks)
	stopped      bool
	mu           sync.Mutex
}

// New initializes an Engine. Returns ErrLocked if another process holds the lock, or ErrMissingSecret if a secret is missing.
func New(d Deps) (*Engine, error) {
	if d.Clock == nil {
		d.Clock = RealClock{}
	}
	if d.Rand == nil {
		d.Rand = func() float64 {
			return 0.5 // Deterministic for tests; real code should use math/rand.Float64()
		}
	}
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.Stderr == nil {
		d.Stderr = io.Discard
	}
	if d.Summary == nil {
		d.Summary = func() (int, int) { return 0, 0 }
	}

	// Create wake dir
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Chmod(d.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod dir: %w", err)
	}

	// Try to acquire lock
	lockPath := filepath.Join(d.Dir, d.SessionID+".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	if err := tryLock(lockFile); err != nil {
		lockFile.Close()
		return nil, ErrLocked
	}

	// Check secrets
	for _, name := range d.Cfg.SecretEnv {
		if val := d.Getenv(name); val == "" {
			_ = appendAudit(d.Dir, d.SessionID, map[string]any{"event": "secret_missing", "name": name}, d.AuditMaxBytes)
			fmt.Fprintf(d.Stderr, "ERROR: secret_env %s not set\n", name)
			lockFile.Close()
			return nil, fmt.Errorf("%w: %s", ErrMissingSecret, name)
		}
	}

	// Load state
	st, err := loadState(d.Dir, d.SessionID)
	if err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("load state: %w", err)
	}

	e := &Engine{
		cfg:       d.Cfg,
		sessionID: d.SessionID,
		dir:       d.Dir,
		clock:     d.Clock,
		checks:    d.Checks,
		rand:      d.Rand,
		getenv:    d.Getenv,
		stderr:    d.Stderr,
		summary:   d.Summary,
		auditMax:  d.AuditMaxBytes,
		lock:      lockFile,
		state:     st,
		interval:  float64(d.Cfg.IntervalSeconds),
		stopped:   st.Stopped,
	}

	// Set owner PID if not set
	if st.OwnerPID == 0 {
		st.OwnerPID = os.Getpid()
	}

	return e, nil
}

// Tick runs one tick of the check loop.
func (e *Engine) Tick(ctx context.Context, enqueue func(Escalation) error) TickResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	r := TickResult{Tick: e.state.Tick + 1, IntervalSeconds: int(e.interval)}

	// Check kill switch
	killPath := expandPath(e.cfg.KillSwitchFile)
	if _, err := os.Stat(killPath); err == nil {
		e.stopped = true
		r.Killed = true
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": "quiet", "result": "killed",
		}, e.auditMax)
		return r
	}

	// Detect clock jump (gap)
	now := e.clock.Now()
	if e.lastTickTime.IsZero() {
		e.lastTickTime = now
	} else {
		gap := now.Sub(e.lastTickTime)
		threshhold := time.Duration(int64(e.interval)*3) * time.Second
		if gap > threshhold {
			r.ResumedAfterGap = true
			e.interval = float64(e.cfg.IntervalSeconds)
			e.state.IntervalS = e.interval
		}
		e.lastTickTime = now
	}

	// Write heartbeat
	e.lastTick.Store(int64(r.Tick))
	if err := e.writeHeartbeat(r.Tick, now); err != nil {
		r.Degraded = true
		r.Suppressed = "degraded"
		_ = e.saveDurableState()
		return r
	}

	// Run checks
	for _, ch := range e.cfg.Checks {
		ctxT, cancel := context.WithTimeout(ctx, time.Duration(ch.TimeoutSeconds)*time.Second)
		obs := e.checks(ctxT, ch)
		cancel()

		if e.state.Checks[ch.Name] == nil {
			e.state.Checks[ch.Name] = &checkState{}
		}
		result := evaluate(ch, obs, now, e.state.Checks[ch.Name], e.cfg.ConsecutiveAlarms)
		r.Results = append(r.Results, result)
	}

	// Compute outcome and interval adjustment
	r.Outcome = overall(r.Results)
	if r.Outcome == OutcomeQuiet {
		e.interval = min(e.interval*e.cfg.BackoffFactor, float64(e.cfg.MaxIntervalSeconds))
	} else {
		e.interval = float64(e.cfg.IntervalSeconds)
	}

	// Record summary
	r.SummaryChars, r.SummaryCap = e.summary()

	// Persist state
	e.state.Tick = r.Tick
	e.state.IntervalS = e.interval
	if err := e.saveDurableState(); err != nil {
		r.Degraded = true
		r.Suppressed = "degraded"
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": r.Outcome, "result": "state_write_error",
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	// Append audit for quiet ticks
	if r.Outcome == OutcomeQuiet {
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": "quiet",
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	// Non-quiet: decide whether to escalate
	if r.Degraded {
		r.Suppressed = "degraded"
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": r.Outcome, "result": "degraded",
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	if e.inflight.Load() {
		r.Suppressed = "single_flight"
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": r.Outcome, "suppressed": "single_flight",
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	// Check cap
	cutoff := now.Add(-time.Hour).UnixNano()
	kept := e.state.CapWindow[:0]
	for _, t := range e.state.CapWindow {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	e.state.CapWindow = kept

	if len(e.state.CapWindow) >= e.cfg.MaxTurnsPerHour {
		r.Suppressed = "hourly_cap"
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": r.Outcome, "suppressed": "hourly_cap",
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	// Check kill switch again
	if _, err := os.Stat(killPath); err == nil {
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "tick", "tick": r.Tick, "outcome": r.Outcome, "result": "killed",
		}, e.auditMax)
		e.stopped = true
		r.Killed = true
		return r
	}

	// Build escalation
	reason := ""
	for _, res := range r.Results {
		if res.Status != StatusOK {
			reason = fmt.Sprintf("%s: %s", res.Status, res.Name)
			break
		}
	}

	esc := Escalation{Reason: reason, Text: BuildTurnText(r.Results)}

	// Add to cap window and mark inflight
	e.state.CapWindow = append(e.state.CapWindow, now.UnixNano())
	e.inflight.Store(true)

	// Save state before enqueuing
	if err := e.saveDurableState(); err != nil {
		e.inflight.Store(false)
		e.state.CapWindow = e.state.CapWindow[:len(e.state.CapWindow)-1]
		_ = e.saveDurableState()
		r.Degraded = true
		r.Suppressed = "degraded"
		return r
	}

	// Try to enqueue
	if err := enqueue(esc); err != nil {
		// Deferred: revert cap window entry
		e.inflight.Store(false)
		e.state.CapWindow = e.state.CapWindow[:len(e.state.CapWindow)-1]
		_ = e.saveDurableState()
		r.Deferred = true
		_ = appendAudit(e.dir, e.sessionID, map[string]any{
			"event": "escalate", "tick": r.Tick, "reason": reason, "deferred": true,
			"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
		}, e.auditMax)
		return r
	}

	r.Escalated = true
	_ = appendAudit(e.dir, e.sessionID, map[string]any{
		"event": "escalate", "tick": r.Tick, "reason": reason, "level": "L2",
		"summary_chars": r.SummaryChars, "summary_cap": r.SummaryCap,
	}, e.auditMax)

	return r
}

func (e *Engine) writeHeartbeat(tick int, now time.Time) error {
	hbPath := filepath.Join(e.dir, e.sessionID+".heartbeat")
	return writeAtomic(hbPath, []byte(fmt.Sprintf("%d %d", tick, now.Unix())))
}

// Beat refreshes the heartbeat file without running checks or touching state. The
// wake loop calls it on a steady cadence (HeartbeatEvery) so the heartbeat proves
// the clock is alive even while quiet ticks back off to max_interval_seconds.
func (e *Engine) Beat() error {
	return e.writeHeartbeat(int(e.lastTick.Load()), e.clock.Now())
}

// HeartbeatEvery is the heartbeat cadence: min(interval_seconds, heartbeat_stale_seconds/3),
// at least 1s, so a stale window always holds at least three beats.
func HeartbeatEvery(cfg config.WakeConfig) time.Duration {
	d := time.Duration(cfg.IntervalSeconds) * time.Second
	if st := time.Duration(cfg.HeartbeatStaleSeconds) * time.Second / 3; cfg.HeartbeatStaleSeconds > 0 && (d <= 0 || st < d) {
		d = st
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}

// NextDelay returns the current interval with jitter, never below min_interval_seconds.
func (e *Engine) NextDelay() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()

	base := e.interval
	jitter := float64(e.cfg.JitterPercent) / 100.0
	randomFactor := 2.0*e.rand() - 1.0 // -1 to 1
	adjusted := base * (1.0 + jitter*randomFactor)

	min := float64(e.cfg.MinIntervalSeconds)
	max := float64(e.cfg.MaxIntervalSeconds) * (1.0 + jitter)

	if adjusted < min {
		adjusted = min
	}
	if adjusted > max {
		adjusted = max
	}

	return time.Duration(adjusted) * time.Second
}

// TurnDone clears the single-flight flag.
func (e *Engine) TurnDone() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inflight.Store(false)
}

// InFlight returns whether a wake turn is currently in flight.
func (e *Engine) InFlight() bool {
	return e.inflight.Load()
}

// Resume clears the stopped flag if the kill file is not present.
func (e *Engine) Resume() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	killPath := expandPath(e.cfg.KillSwitchFile)
	if _, err := os.Stat(killPath); err == nil {
		return errors.New("kill switch file present")
	}

	e.stopped = false
	e.state.Stopped = false
	_ = e.saveDurableState()
	return nil
}

// Stopped returns whether the engine is stopped.
func (e *Engine) Stopped() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped
}

// LastHeartbeat reads the heartbeat file and returns the last tick time (as a time.Time).
// Returns nil if the heartbeat file does not exist or cannot be parsed.
func (e *Engine) LastHeartbeat(now time.Time) *time.Time {
	hbPath := filepath.Join(e.dir, e.sessionID+".heartbeat")
	data, err := os.ReadFile(hbPath)
	if err != nil {
		return nil
	}
	// Heartbeat format: "tick unix_seconds"
	var tick int
	var unixSec int64
	_, err = fmt.Sscanf(string(data), "%d %d", &tick, &unixSec)
	if err != nil {
		return nil
	}
	t := time.Unix(unixSec, 0)
	return &t
}

// TurnsLastHour returns the count of cap window entries from the last hour.
func (e *Engine) TurnsLastHour(now time.Time) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := now.Add(-time.Hour).UnixNano()
	count := 0
	for _, t := range e.state.CapWindow {
		if t >= cutoff {
			count++
		}
	}
	return count
}

// LastAlarm returns the timestamp of the most recent cap window entry,
// or nil if the cap window is empty.
func (e *Engine) LastAlarm(now time.Time) *time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.state.CapWindow) == 0 {
		return nil
	}
	lastNano := e.state.CapWindow[len(e.state.CapWindow)-1]
	t := time.Unix(0, lastNano)
	return &t
}

// Close releases the lock file.
func (e *Engine) Close() error {
	if e.lock != nil {
		unlock(e.lock)
		e.lock.Close()
	}
	return nil
}

// NoteTurnResult records the outcome of a wake turn in the audit log.
func (e *Engine) NoteTurnResult(tick int, result string) {
	_ = appendAudit(e.dir, e.sessionID, map[string]any{
		"event": "turn_result", "tick": tick, "result": result,
	}, e.auditMax)
}

// ErrTaskCap is returned by ReserveTask when the rolling hourly task cap is reached.
var ErrTaskCap = errors.New("wake: max_tasks_per_hour reached")

// ReserveTask records one start_task launch in the rolling-hour window persisted
// in the state file (so it survives restarts and session resume). It fails with
// ErrTaskCap when max launches already happened in the last hour.
func (e *Engine) ReserveTask(max int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock.Now()
	cutoff := now.Add(-time.Hour).UnixNano()
	kept := make([]int64, 0, len(e.state.TaskWindow)+1)
	for _, t := range e.state.TaskWindow {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= max {
		e.state.TaskWindow = kept
		return ErrTaskCap
	}
	prev := e.state.TaskWindow
	e.state.TaskWindow = append(kept, now.UnixNano())
	if err := e.saveDurableState(); err != nil {
		e.state.TaskWindow = prev
		return fmt.Errorf("wake: persist task window: %w", err)
	}
	return nil
}

// KillSwitchPresent reports whether the kill-switch file exists right now.
func (e *Engine) KillSwitchPresent() bool {
	_, err := os.Stat(expandPath(e.cfg.KillSwitchFile))
	return err == nil
}

// Audit appends a record to this session's wake audit log.
func (e *Engine) Audit(rec map[string]any) {
	_ = appendAudit(e.dir, e.sessionID, rec, e.auditMax)
}

// Dir returns the wake state directory.
func (e *Engine) Dir() string { return e.dir }

// SessionID returns the session this engine belongs to.
func (e *Engine) SessionID() string { return e.sessionID }

func (e *Engine) saveDurableState() error {
	return writeState(e.dir, e.sessionID, e.state)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func expandPath(p string) string {
	if p == "" {
		return p
	}
	if p[:1] == "~" {
		home, _ := os.UserHomeDir()
		if home != "" {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
