package server

import (
	"runtime"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestRecordLoop_StopsOnStopCh regression-guards: recordLoop used
// `for ev := range ch` instead of selecting on s.stopCh (unlike the
// neighboring forwardLoop, which does select on it). ch is only closed by
// recordLoop's own deferred Unsubscribe — which only runs once the range
// loop itself exits — so closing stopCh (what Close() does) had no way to
// reach it. Isolated to just this one goroutine (bypassing the rest of
// ChatSession/StartChatSession) so the goroutine-count check isn't muddied
// by the session's other background loops winding down at different times.
func TestRecordLoop_StopsOnStopCh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := ss.StartSession(store.SessionMeta{Name: "test"}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	bus := telemetry.NewEventBus(8)
	sess := &ChatSession{
		eventBus:     bus,
		sessionStore: ss,
		stopCh:       make(chan struct{}),
	}

	before := runtime.NumGoroutine()
	go sess.recordLoop()
	// Give recordLoop a moment to actually start and subscribe before we
	// measure against it — otherwise "before" could already include it.
	time.Sleep(20 * time.Millisecond)

	close(sess.stopCh)

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.Gosched()
		if runtime.NumGoroutine() <= before {
			return // recordLoop exited — success
		}
		if time.Now().After(deadline) {
			t.Fatalf("recordLoop still running 2s after stopCh closed (goroutines: before=%d, now=%d)", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
