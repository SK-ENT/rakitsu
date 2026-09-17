package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// slowFakeChatBuildFunc sleeps for delay before returning, widening the
// window inside ChatManager.Start() where the resume-collision check has
// already passed but the session isn't registered into m.sessions yet —
// wide enough that two concurrent callers reliably overlap in it, instead
// of relying on luck-of-scheduling to catch the race.
func slowFakeChatBuildFunc(name string, delay time.Duration) ChatBuildFunc {
	return func(
		_ context.Context,
		_ *config.Config,
		eventBus *telemetry.EventBus,
		_ chan userinput.InputRequest,
		_ chan string,
		_ string,
		_ string,
	) (agent.Runner, agent.Runner, func(), string, string, error) {
		time.Sleep(delay)
		r := &fakeRunner{name: name, bus: eventBus}
		return r, r, func() {}, name, "fake", nil
	}
}

// TestChatManager_Start_ConcurrentResumeDoesNotClobber regression-guards a
// TOCTOU race: Start() checked m.sessions[resumeID] under RLock, then did
// real I/O (resolvePersistedSession, config load, session construction)
// before registering the session under Lock. Two concurrent resumes for the
// same id both passed the early check, both built a full session against
// the same persisted transcript, and the second insert into m.sessions
// silently clobbered the first — leaving one ChatSession orphaned but still
// running (own goroutines, own WS attachment) against the same JSONL file
// as the one left registered.
func TestChatManager_Start_ConcurrentResumeDoesNotClobber(t *testing.T) {
	m, configID := newForkTestManager(t)
	m.buildFunc = slowFakeChatBuildFunc("a1", 100*time.Millisecond)

	sessionID := "resume-race"
	persistForkFixture(t, m, sessionID, configID)

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan *ChatSession, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sess, err := m.Start(context.Background(), "", "", nil, sessionID)
			if err != nil {
				errs <- err
				return
			}
			results <- sess
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	var succeeded []*ChatSession
	for s := range results {
		succeeded = append(succeeded, s)
	}
	var failures []error
	for e := range errs {
		failures = append(failures, e)
	}

	if len(succeeded) != 1 {
		t.Fatalf("expected exactly 1 of 2 concurrent resumes to succeed, got %d (failures: %v)", len(succeeded), failures)
	}
	if len(failures) != 1 {
		t.Fatalf("expected exactly 1 of 2 concurrent resumes to be rejected as already-active, got %d rejected", len(failures))
	}

	m.mu.RLock()
	registered := m.sessions[sessionID]
	count := len(m.sessions)
	m.mu.RUnlock()

	if count != 1 {
		t.Fatalf("m.sessions has %d entries after the race, want 1", count)
	}
	if registered != succeeded[0] {
		t.Fatal("the session left registered in m.sessions is not the one Start() returned to the caller — the winner was clobbered")
	}
}
