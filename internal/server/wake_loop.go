package server

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// WakeOptions configures the wake ticker for a ChatSession.
type WakeOptions struct {
	Dir       string // wake state dir
	Workdir   string
	Clock     wake.Clock                             // nil => wake.RealClock{}
	Checks    wake.CheckFunc                         // nil => wake.NewBuiltinChecks(cfg, Workdir, nil)
	After     func(d time.Duration) <-chan time.Time // nil => time.After; tests fire it by hand
	Getenv    func(string) string                    // nil => os.Getenv
	TurnAfter func(d time.Duration) <-chan time.Time // nil => time.After; for turn timeout, testable
	TickDone  func()                                 // nil => no-op; called after each tick processes in wakeLoop, for test sync

	// TaskRun runs one start_task launch (required when settings.wake.allow.configs is set).
	TaskRun wake.TaskRunFunc
	// TaskAfter is the task-timeout timer; nil => time.After. Tests fire it by hand.
	TaskAfter func(d time.Duration) <-chan time.Time
	// StopAfter times the StopWakeAndWait bound; nil => time.After. Separate from
	// After so leftover manual tick tokens cannot trip the stop timeout.
	StopAfter func(d time.Duration) <-chan time.Time
	// HeartbeatAfter times the heartbeat writer. nil => time.After, except when After
	// is injected, where it never fires (tests that drive After own all timing).
	HeartbeatAfter func(d time.Duration) <-chan time.Time

	afterDefaulted bool // After was nil on entry (real timers)
}

// wakeRunner holds the engine and control state for a wake ticker.
type wakeRunner struct {
	eng     *wake.Engine
	running atomic.Bool
	opts    WakeOptions
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closing bool              // set under wakeMu by StopWakeAndWait; no new goroutines may join wg after
	started time.Time         // loop (re)start time on opts.Clock; grace base for a missing heartbeat
	tasks   *wake.TaskManager // nil unless settings.wake.allow.configs is set
}

// StartWake begins or resumes the wake ticker for this session.
// No-op (nil) when wake is not enabled in cfg. Idempotent while running.
func (s *ChatSession) StartWake(o WakeOptions) error {
	w := s.cfg.Settings.Wake
	if !w.Enabled {
		return nil
	}
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wake != nil && s.wake.closing {
		return fmt.Errorf("wake is shutting down")
	}
	if s.wake != nil && s.wake.running.Load() {
		return nil // idempotent (resume reattach)
	}
	if o.Clock == nil {
		o.Clock = wake.RealClock{}
	}
	if o.Checks == nil {
		o.Checks = wake.NewBuiltinChecks(w, o.Workdir, nil)
	}
	if o.After == nil {
		o.After = time.After
		o.afterDefaulted = true
	}
	if o.TurnAfter == nil {
		o.TurnAfter = time.After
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	var eng *wake.Engine
	var tasks *wake.TaskManager
	var allow map[string]wake.ResolvedConfig
	if s.wake != nil {
		tasks = s.wake.tasks
	} else if len(w.Allow.Configs) > 0 {
		// Validate the allowlist before the first tick; failures block session registration.
		if o.TaskRun == nil {
			return fmt.Errorf("wake: allow.configs is set but no task runner is configured")
		}
		var err error
		if allow, err = wake.ResolveAllowlist(w, o.Workdir); err != nil {
			return err
		}
	}
	if s.wake != nil {
		eng = s.wake.eng // re-used after a kill-switch stop; Resume() already cleared it
	} else {
		var err error
		eng, err = wake.New(wake.Deps{
			Cfg:       w,
			SessionID: s.ID,
			Dir:       o.Dir,
			Clock:     o.Clock,
			Checks:    o.Checks,
			Getenv:    o.Getenv,
			Stderr:    os.Stderr,
			Summary:   s.SummaryLen,
		})
		if err != nil {
			return err
		}
	}
	if tasks == nil && allow != nil {
		tasks = wake.NewTaskManager(wake.TaskDeps{
			Engine: eng, Cfg: w, Allow: allow, Run: o.TaskRun, After: o.TaskAfter,
			Emit: s.emitTaskEvent,
		})
		if s.taskHandle != nil {
			s.taskHandle.Bind(tasks)
		}
	}
	r := &wakeRunner{eng: eng, opts: o, tasks: tasks, started: o.Clock.Now()}
	s.wake = r
	r.running.Store(true)

	// Start the ticker loop.
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go s.wakeLoop(r, ctx, cancel)
	s.startHeartbeat(r, ctx)
	return nil
}

// startHeartbeat runs the steady heartbeat writer for the lifetime of ctx (the
// wake loop cancels ctx when it exits, so a stopped loop stops beating and goes stale).
func (s *ChatSession) startHeartbeat(r *wakeRunner, ctx context.Context) {
	after := r.opts.HeartbeatAfter
	if after == nil {
		if r.opts.After != nil && !r.opts.afterDefaulted {
			after = func(time.Duration) <-chan time.Time { return nil } // injected After: tests own timing
		} else {
			after = time.After
		}
	}
	every := wake.HeartbeatEvery(s.cfg.Settings.Wake)
	_ = r.eng.Beat() // synchronously, so there is no heartbeat-less window after start
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case <-after(every):
				_ = r.eng.Beat()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ResumeWake resumes a previously stopped wake loop after the kill file is removed.
// Returns an error while the kill file still exists.
func (s *ChatSession) ResumeWake() error {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wake == nil || s.wake.eng == nil {
		return fmt.Errorf("wake not initialized")
	}
	if s.wake.closing {
		return fmt.Errorf("wake is shutting down")
	}
	if err := s.wake.eng.Resume(); err != nil {
		return err
	}
	if s.wake.tasks != nil {
		s.wake.tasks.Unblock()
	}
	if s.wake.running.Load() {
		return nil
	}
	s.wake.running.Store(true)
	s.wake.started = s.wake.opts.Clock.Now()
	ctx, cancel := context.WithCancel(context.Background())
	s.wake.cancel = cancel
	s.wake.wg.Add(1)
	go s.wakeLoop(s.wake, ctx, cancel)
	s.startHeartbeat(s.wake, ctx)
	return nil
}

// WakeRunning reports whether the wake ticker is currently running.
func (s *ChatSession) WakeRunning() bool {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wake == nil {
		return false
	}
	return s.wake.running.Load()
}

// StopWake stops the wake ticker by creating the kill-switch file.
// Idempotent. Does not stop the session itself; the session can be manually
// used or resumed afterward. The session stays alive.
func (s *ChatSession) StopWake() error {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wake == nil || s.wake.eng == nil {
		return nil // not initialized, idempotent
	}
	// Block new task starts now and cancel running ones (unless cancel_on_stop is false).
	if s.wake.tasks != nil {
		s.wake.tasks.Stop(s.cfg.Settings.Wake.CancelOnStopEnabled())
	}
	// Create kill-switch file so the ticker stops on next tick.
	// The wake engine manages the kill-switch file path via its state.
	// For now, delegate to the engine's config which knows the path.
	killFile := s.cfg.Settings.Wake.KillSwitchFile
	if killFile == "" {
		return nil // wake not configured with a kill-switch path
	}
	// Create the file (idempotent, okay if it already exists)
	if err := wake.CreateKillSwitch(killFile); err != nil {
		return fmt.Errorf("create kill-switch file: %w", err)
	}
	// Emit audit event for the stop action
	s.eventBus.Emit(s.ID, telemetry.EventWakeTick, telemetry.WakeTickPayload{
		Result: "stopped_by_user",
	})
	return nil
}

// StopWakeAndWait stops the wake loop and waits for it to exit.
// Used by Close() to ensure the wake loop has fully stopped before
// continuing with cleanup (e.g., TempDir deletion).
//
// The wait is bounded by wakeStopTimeout (timed via WakeOptions.StopAfter). On
// timeout a wake_stop_timeout audit line is written and an error returned;
// the stuck goroutines are left behind rather than hanging the caller.
func (s *ChatSession) StopWakeAndWait() error {
	s.wakeMu.Lock()
	if s.wake == nil {
		s.wakeMu.Unlock()
		return nil
	}
	r := s.wake
	r.closing = true // ResumeWake/StartWake can no longer Add to r.wg while we Wait
	cancel := r.cancel
	tasks := r.tasks
	s.wakeMu.Unlock()

	if cancel != nil {
		cancel()
	}
	// The loop remains counted while it can start turn watchers, so Wait
	// also covers watchers added while cancellation is being processed.
	joined := make(chan struct{})
	go func() {
		// Session is closing: nothing started from it may outlive it. Close
		// waits on task goroutines, so it sits inside the bounded wait too.
		if tasks != nil {
			tasks.Close() // blocks new starts first, then waits for task goroutines
		}
		r.wg.Wait()
		close(joined)
	}()
	after := r.opts.StopAfter
	if after == nil {
		after = time.After
	}
	select {
	case <-joined:
		return nil
	case <-after(wakeStopTimeout):
		if r.eng != nil {
			r.eng.Audit(map[string]any{"event": "wake_stop_timeout", "timeout_seconds": int(wakeStopTimeout.Seconds())})
		}
		return fmt.Errorf("wake loop did not stop within %s", wakeStopTimeout)
	}
}

// wakeStopTimeout bounds StopWakeAndWait.
const wakeStopTimeout = 10 * time.Second

// wakeLoop runs the ticker goroutine for a wake session.
func (s *ChatSession) wakeLoop(r *wakeRunner, ctx context.Context, cancel context.CancelFunc) {
	defer r.wg.Done()
	defer r.running.Store(false)
	defer cancel()

	for {
		// Run one tick.
		enqueue := func(esc wake.Escalation) error {
			done, err := s.SubmitWake(esc.Text, esc.Reason)
			if err != nil {
				return err
			}
			// Watch the turn in the background.
			timeout := time.Duration(s.cfg.Settings.Wake.TurnTimeoutSeconds) * time.Second
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				s.watchWakeTurn(ctx, r, done, timeout, r.opts.TurnAfter)
			}()
			return nil
		}

		res := r.eng.Tick(ctx, enqueue)

		// Emit events and check kill-switch.
		if res.Killed {
			if r.tasks != nil {
				r.tasks.Stop(s.cfg.Settings.Wake.CancelOnStopEnabled())
			}
			result := "killed"
			if res.LockLost {
				result = "lock_replaced"
			}
			s.eventBus.Emit(s.ID, telemetry.EventWakeTick, telemetry.WakeTickPayload{
				Tick:         res.Tick,
				Outcome:      string(res.Outcome),
				Result:       result,
				SummaryChars: res.SummaryChars,
				SummaryCap:   res.SummaryCap,
			})
			if r.opts.TickDone != nil {
				r.opts.TickDone()
			}
			return // Stop loop.
		}

		if res.Degraded {
			s.eventBus.Emit(s.ID, telemetry.EventWakeTick, telemetry.WakeTickPayload{
				Tick:            res.Tick,
				IntervalSeconds: res.IntervalSeconds,
				Outcome:         string(res.Outcome),
				Result:          "degraded",
				SummaryChars:    res.SummaryChars,
				SummaryCap:      res.SummaryCap,
			})
		}

		// Emit full tick event for non-quiet ticks only.
		if res.Outcome != wake.OutcomeQuiet {
			results := make([]telemetry.WakeCheckResult, len(res.Results))
			for i, cr := range res.Results {
				results[i] = telemetry.WakeCheckResult{
					Name:   cr.Name,
					Status: string(cr.Status),
					Detail: cr.Detail,
					Label:  cr.Label,
				}
			}
			s.eventBus.Emit(s.ID, telemetry.EventWakeTick, telemetry.WakeTickPayload{
				Tick:            res.Tick,
				IntervalSeconds: res.IntervalSeconds,
				Results:         results,
				Outcome:         string(res.Outcome),
				SummaryChars:    res.SummaryChars,
				SummaryCap:      res.SummaryCap,
			})
		}

		// Emit escalation event if applicable.
		if res.Escalated || res.Deferred || res.Suppressed != "" {
			s.eventBus.Emit(s.ID, telemetry.EventWakeEscalate, telemetry.WakeEscalatePayload{
				Tick:         res.Tick,
				Reason:       "", // TODO: reason from escalation
				Deferred:     res.Deferred,
				Suppressed:   res.Suppressed,
				Level:        "L2",
				SummaryChars: res.SummaryChars,
				SummaryCap:   res.SummaryCap,
			})
		}

		// Signal tick completion for tests.
		if r.opts.TickDone != nil {
			r.opts.TickDone()
		}

		// Wait for next tick.
		select {
		case <-r.opts.After(r.eng.NextDelay()):
		case <-s.stopCh:
			_ = r.eng.Close()
			return
		case <-ctx.Done():
			_ = r.eng.Close()
			return
		}
	}
}

// watchWakeTurn monitors a wake turn and interrupts it if it times out.
func (s *ChatSession) watchWakeTurn(ctx context.Context, r *wakeRunner, done <-chan turnOutcome, timeout time.Duration, afterFunc func(time.Duration) <-chan time.Time) {
	if afterFunc == nil {
		afterFunc = time.After
	}
	select {
	case out := <-done:
		r.eng.TurnDone()
		// Emit completion event with result.
		result := "done"
		if out.Err != "" {
			result = "error"
		}
		s.eventBus.Emit(s.ID, telemetry.EventWakeEscalate, telemetry.WakeEscalatePayload{
			Result: result,
			Level:  "L2",
		})
	case <-afterFunc(timeout):
		// Turn timed out. Interrupt it if still active.
		if s.wakeTurnActive.Load() {
			s.Interrupt()
		}
		// Wait for done up to 5 seconds.
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-waitCtx.Done():
		case <-ctx.Done():
		}
		r.eng.TurnDone()
		r.eng.NoteTurnResult(0, "timeout") // TODO: get actual tick number
		s.eventBus.Emit(s.ID, telemetry.EventWakeEscalate, telemetry.WakeEscalatePayload{
			Result: "timeout",
			Level:  "L2",
		})
	case <-ctx.Done():
		r.eng.TurnDone()
	case <-s.stopCh:
		r.eng.TurnDone()
	}
}

// CancelWakeTask cancels one started task. Backs POST /api/chat/{id}/wake/tasks/{task-id}/cancel.
func (s *ChatSession) CancelWakeTask(id string) error {
	s.wakeMu.Lock()
	var tasks *wake.TaskManager
	if s.wake != nil {
		tasks = s.wake.tasks
	}
	s.wakeMu.Unlock()
	if tasks == nil {
		return fmt.Errorf("no tasks for this session")
	}
	return tasks.Cancel(id)
}

// emitTaskEvent maps a wake task event onto the session event bus.
func (s *ChatSession) emitTaskEvent(ev wake.TaskEvent) {
	et := telemetry.EventWakeTaskStart
	if ev.Phase == "end" {
		et = telemetry.EventWakeTaskEnd
	}
	s.eventBus.Emit(s.ID, et, telemetry.WakeTaskPayload{
		TaskID: ev.TaskID, Config: ev.Config, Status: ev.Status, Reason: ev.Reason, Args: ev.Args, Summary: ev.Summary,
	})
}
