//go:build stress

package stress

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestDebugControllerConcurrent: 20 goroutines Check(), 10 set/clear breakpoints,
// 5 Resume(), 5 param overrides — all concurrent for 30s.
func TestDebugControllerConcurrent(t *testing.T) {
	const (
		timeout         = 30 * time.Second
		numCheckers     = 20
		numBPSetters    = 10
		numResumers     = 5
		numOverriders   = 5
	)

	eb := telemetry.NewEventBus(100)
	dc := debug.NewDebugController(eb)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	var checks, resumes, bpOps, ovrOps int64

	// Checkers: call Check() continuously
	for i := 0; i < numCheckers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentName := "agent"
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Non-blocking check — we run in running state mostly
					checkCtx, checkCancel := context.WithTimeout(ctx, 50*time.Millisecond)
					dc.Check(checkCtx, "pre_thought", agentName, int(atomic.AddInt64(&checks, 1)), nil)
					checkCancel()
				}
			}
		}(i)
	}

	// Breakpoint setters/clearers
	for i := 0; i < numBPSetters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					key := debug.BreakpointKey{EventType: "pre_thought", AgentName: "agent"}
					dc.SetBreakpoint(key)
					atomic.AddInt64(&bpOps, 1)
					time.Sleep(time.Millisecond)
					dc.ClearBreakpoint(key)
					atomic.AddInt64(&bpOps, 1)
				}
			}
		}(i)
	}

	// Resumers: send resume actions
	for i := 0; i < numResumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actions := []debug.ResumeAction{
				debug.ActionResume, debug.ActionStep, debug.ActionStepIn,
				debug.ActionStepOut, debug.ActionStop,
			}
			idx := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					dc.Resume(actions[idx%len(actions)])
					atomic.AddInt64(&resumes, 1)
					idx++
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	// Override setters/clearers
	for i := 0; i < numOverriders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			temp := 0.5
			for {
				select {
				case <-ctx.Done():
					return
				default:
					dc.SetOverrides("agent", &debug.ParamOverride{Temperature: &temp, Sticky: true})
					atomic.AddInt64(&ovrOps, 1)
					time.Sleep(time.Millisecond)
					dc.GetOverrides("agent")
					dc.ClearOverrides("agent")
					atomic.AddInt64(&ovrOps, 1)
				}
			}
		}(i)
	}

	<-ctx.Done()
	wg.Wait()

	t.Logf("Checks: %d, Resumes: %d, BP ops: %d, Override ops: %d",
		atomic.LoadInt64(&checks), atomic.LoadInt64(&resumes),
		atomic.LoadInt64(&bpOps), atomic.LoadInt64(&ovrOps))
	t.Log("DebugController concurrent stress test completed without deadlock or panic")
}

// TestDebugPauseResumeUnderLoad: pause and resume while events flow.
func TestDebugPauseResumeUnderLoad(t *testing.T) {
	const cycles = 100

	eb := telemetry.NewEventBus(100)
	dc := debug.NewDebugController(eb)

	for i := 0; i < cycles; i++ {
		dc.RequestPause()
		// Check should pause
		go func() {
			time.Sleep(10 * time.Millisecond)
			dc.Resume(debug.ActionResume)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := dc.Check(ctx, "pre_thought", "agent", i, nil)
		cancel()
		if err != nil {
			t.Fatalf("Check returned error on cycle %d: %v", i, err)
		}
	}
	t.Logf("Completed %d pause/resume cycles", cycles)
}
