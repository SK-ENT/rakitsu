package session

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
)

// fakeSession is a minimal Session used to exercise the registry without
// pulling in server internals. It captures everything via a mutable
// SessionMeta so tests can toggle status mid-flight.
type fakeSession struct {
	mu   sync.Mutex
	meta SessionMeta
}

func newFake(id, configID string, mode SessionMode, status SessionStatus) *fakeSession {
	return &fakeSession{
		meta: SessionMeta{
			ID:        id,
			ConfigID:  configID,
			Mode:      mode,
			Status:    status,
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
}

func (f *fakeSession) ID() string       { return f.snap().ID }
func (f *fakeSession) ConfigID() string { return f.snap().ConfigID }
func (f *fakeSession) Mode() SessionMode {
	return f.snap().Mode
}
func (f *fakeSession) Status() SessionStatus { return f.snap().Status }
func (f *fakeSession) StartedAt() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, f.snap().StartedAt)
	return t
}
func (f *fakeSession) Meta() SessionMeta { return f.snap() }

// Phase 2: stub debug surface — tests in this package only exercise the read
// surface. Real attach behavior lives in internal/server/session_adapters_test.go.
func (f *fakeSession) DebugController() *debug.DebugController { return nil }
func (f *fakeSession) AttachDebugger(_ []debug.BreakpointKey) (*debug.DebugController, error) {
	return nil, ErrNotAttachable
}
func (f *fakeSession) DetachDebugger() error { return nil }

func (f *fakeSession) snap() SessionMeta {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta
}

func (f *fakeSession) setStatus(s SessionStatus) {
	f.mu.Lock()
	f.meta.Status = s
	f.mu.Unlock()
}

func TestRegistryEmpty(t *testing.T) {
	r := NewRegistry()
	if got := r.List(Filter{}); len(got) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(got))
	}
	if _, err := r.Get("anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistryRegisterGetList(t *testing.T) {
	r := NewRegistry()
	chat := newFake("chat-1", "cfg-a", ModeChat, StatusRunning)
	run := newFake("run-1", "cfg-a", ModeOneShot, StatusRunning)
	r.Register(chat)
	r.Register(run)

	got := r.List(Filter{})
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	s, err := r.Get("chat-1")
	if err != nil {
		t.Fatalf("Get chat-1: %v", err)
	}
	if s.Mode() != ModeChat {
		t.Fatalf("expected Chat mode, got %q", s.Mode())
	}
}

func TestRegistryFilterMode(t *testing.T) {
	r := NewRegistry()
	r.Register(newFake("chat-1", "cfg", ModeChat, StatusRunning))
	r.Register(newFake("run-1", "cfg", ModeOneShot, StatusRunning))

	chats := r.List(Filter{Mode: ModeChat})
	if len(chats) != 1 || chats[0].Mode != ModeChat {
		t.Fatalf("Mode=chat filter mismatch: %+v", chats)
	}
	runs := r.List(Filter{Mode: ModeOneShot})
	if len(runs) != 1 || runs[0].Mode != ModeOneShot {
		t.Fatalf("Mode=oneshot filter mismatch: %+v", runs)
	}
}

func TestRegistryFilterStatus(t *testing.T) {
	r := NewRegistry()
	running := newFake("a", "cfg", ModeChat, StatusRunning)
	stopped := newFake("b", "cfg", ModeChat, StatusStopped)
	r.Register(running)
	r.Register(stopped)

	got := r.List(Filter{Status: StatusRunning})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("Status=running filter mismatch: %+v", got)
	}

	// Toggle mid-flight: registry must reflect the current underlying status.
	running.setStatus(StatusErrored)
	got = r.List(Filter{Status: StatusRunning})
	if len(got) != 0 {
		t.Fatalf("expected 0 running after toggle, got %d", len(got))
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()
	r.Register(newFake("x", "cfg", ModeChat, StatusRunning))
	if !r.Unregister("x") {
		t.Fatal("Unregister returned false for known id")
	}
	if r.Unregister("x") {
		t.Fatal("Unregister returned true for unknown id")
	}
	if _, err := r.Get("x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after unregister, got %v", err)
	}
}

func TestRegistryListSortedByStartedAt(t *testing.T) {
	r := NewRegistry()
	// Forge timestamps directly via custom meta.
	older := &fakeSession{meta: SessionMeta{
		ID: "older", Mode: ModeChat, Status: StatusRunning,
		StartedAt: "2026-04-18T10:00:00Z",
	}}
	newer := &fakeSession{meta: SessionMeta{
		ID: "newer", Mode: ModeChat, Status: StatusRunning,
		StartedAt: "2026-04-18T10:05:00Z",
	}}
	r.Register(newer)
	r.Register(older)

	got := r.List(Filter{})
	if got[0].ID != "older" || got[1].ID != "newer" {
		t.Fatalf("expected [older, newer], got [%s, %s]", got[0].ID, got[1].ID)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			r.Register(newFake(fmt.Sprintf("s-%d", i), "cfg", ModeChat, StatusRunning))
		}()
		go func() {
			defer wg.Done()
			_ = r.List(Filter{})
		}()
	}
	wg.Wait()
	if r.Len() != N {
		t.Fatalf("expected %d sessions after concurrent register, got %d", N, r.Len())
	}
}

func TestRegistryRegisterNilAndEmptyID(t *testing.T) {
	r := NewRegistry()
	r.Register(nil) // must not panic
	r.Register(&fakeSession{meta: SessionMeta{ID: ""}})
	if r.Len() != 0 {
		t.Fatalf("nil/empty-ID registers must be no-ops, got len=%d", r.Len())
	}
}

func TestFilterMatch(t *testing.T) {
	m := SessionMeta{Mode: ModeChat, Status: StatusRunning}
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"empty filter matches", Filter{}, true},
		{"mode match", Filter{Mode: ModeChat}, true},
		{"mode mismatch", Filter{Mode: ModeOneShot}, false},
		{"status match", Filter{Status: StatusRunning}, true},
		{"status mismatch", Filter{Status: StatusStopped}, false},
		{"both match", Filter{Mode: ModeChat, Status: StatusRunning}, true},
		{"both, one mismatch", Filter{Mode: ModeChat, Status: StatusStopped}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(m); got != tc.want {
				t.Fatalf("Match=%v, want %v", got, tc.want)
			}
		})
	}
}
