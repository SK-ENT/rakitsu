package server

import (
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
)

// oneShotSessionAdapter projects a single AgentRunner run as a session.Session.
// It is registered with the SessionRegistry when a run starts and unregistered
// when the run ends (completed / error / cancelled / reaped).
//
// Phase 2 adds the debug-attach surface: AttachDebugger guards on session id
// (the runner is single-run, so if the current run's sessionID doesn't match
// this adapter's id we refuse the attach rather than silently mis-targeting).
type oneShotSessionAdapter struct {
	id        string
	configID  string
	name      string
	startedAt time.Time

	// runner is the AgentRunner that owns the live run this adapter projects.
	// Non-nil for adapters registered via AgentRunner.Start; nil in Phase-1
	// fixtures or test harnesses that register adapters directly for read-only
	// coverage — in that case Attach returns ErrNotAttachable.
	runner *AgentRunner
}

func (a *oneShotSessionAdapter) ID() string                 { return a.id }
func (a *oneShotSessionAdapter) ConfigID() string           { return a.configID }
func (a *oneShotSessionAdapter) Mode() session.SessionMode  { return session.ModeOneShot }
// Status returns Running while the adapter is in the registry — owners
// unregister on completion so a stale "running" never lingers.
func (a *oneShotSessionAdapter) Status() session.SessionStatus { return session.StatusRunning }
func (a *oneShotSessionAdapter) StartedAt() time.Time       { return a.startedAt }

func (a *oneShotSessionAdapter) Meta() session.SessionMeta {
	return session.SessionMeta{
		ID:        a.id,
		ConfigID:  a.configID,
		Name:      a.name,
		Mode:      session.ModeOneShot,
		Status:    session.StatusRunning,
		StartedAt: a.startedAt.UTC().Format(time.RFC3339),
	}
}

// DebugController returns the current run's DebugController, or nil. The
// runner owns the mutex so a nil runner (fixture mode) returns nil.
func (a *oneShotSessionAdapter) DebugController() *debug.DebugController {
	if a.runner == nil {
		return nil
	}
	return a.runner.CurrentDebugController()
}

// AttachDebugger attaches to the one-shot run this adapter represents. Guards
// on session id: if the runner has moved on to a different run (adapter is
// stale), returns ErrNotAttachable rather than mis-targeting the new run.
func (a *oneShotSessionAdapter) AttachDebugger(breakpoints []debug.BreakpointKey) (*debug.DebugController, error) {
	if a.runner == nil {
		return nil, session.ErrNotAttachable
	}
	entries := make([]BreakpointEntry, len(breakpoints))
	for i, k := range breakpoints {
		entries[i] = BreakpointEntry{EventType: k.EventType, AgentName: k.AgentName}
	}
	return a.runner.AttachDebuggerForSession(a.id, entries)
}

// DetachDebugger removes the debugger from the current run if (and only if)
// the current run matches this adapter's session id.
func (a *oneShotSessionAdapter) DetachDebugger() error {
	if a.runner == nil {
		return nil
	}
	return a.runner.DetachDebuggerForSession(a.id)
}

// chatSessionAdapter projects a ChatSession as a session.Session. Owned by
// ChatManager: created at Start(), dropped at Stop(). Keeps ChatSession's
// public API (ID is still a field) intact while exposing the interface shape.
//
// Phase 2 adds a per-session DebugController: each chat owns its own
// controller, wired into the ChatSession's runner on Attach.
type chatSessionAdapter struct {
	sess *ChatSession

	// Debug state is guarded by dbgMu; the rest of the adapter is read-only.
	dbgMu sync.Mutex
	dc    *debug.DebugController
}

func (a *chatSessionAdapter) ID() string       { return a.sess.ID }
func (a *chatSessionAdapter) ConfigID() string { return a.sess.configID }
func (a *chatSessionAdapter) Mode() session.SessionMode {
	return session.ModeChat
}
// Status reflects chat session state. Phase 1 maps the "generating" flag to
// Running and default to Running otherwise (chats are always live while the
// adapter is in the registry). Paused/Errored are reserved for Phase 2.
func (a *chatSessionAdapter) Status() session.SessionStatus {
	if a.sess.closed.Load() {
		return session.StatusStopped
	}
	return session.StatusRunning
}
func (a *chatSessionAdapter) StartedAt() time.Time { return a.sess.created }

func (a *chatSessionAdapter) Meta() session.SessionMeta {
	return session.SessionMeta{
		ID:        a.sess.ID,
		ConfigID:  a.sess.configID,
		Name:      a.sess.cfg.Name,
		Mode:      session.ModeChat,
		Status:    a.Status(),
		StartedAt: a.sess.created.UTC().Format(time.RFC3339),
		Agent:     a.sess.agentName,
		Model:     a.sess.modelLabel,
	}
}

// DebugController returns the attached controller, or nil.
func (a *chatSessionAdapter) DebugController() *debug.DebugController {
	a.dbgMu.Lock()
	defer a.dbgMu.Unlock()
	return a.dc
}

// AttachDebugger creates a DebugController, preloads breakpoints, and wires it
// into the ChatSession's runner. Idempotent: a second Attach returns the same
// controller without rewiring the runner.
func (a *chatSessionAdapter) AttachDebugger(breakpoints []debug.BreakpointKey) (*debug.DebugController, error) {
	if a.sess.closed.Load() {
		return nil, session.ErrNotAttachable
	}
	a.dbgMu.Lock()
	defer a.dbgMu.Unlock()
	if a.dc != nil {
		return a.dc, nil
	}
	// Use the session's private event bus so DEBUG_PAUSED events flow through
	// the ChatSession.forwardLoop and land on the hub bus tagged with
	// session_id — same path every other chat-mode event takes.
	dc := debug.NewDebugController(a.sess.eventBus)
	for _, k := range breakpoints {
		dc.SetBreakpoint(k)
	}
	a.sess.SetDebugController(dc)
	a.dc = dc
	return dc, nil
}

// DetachDebugger clears the controller from the ChatSession's runner and
// unblocks any paused checkpoint. Idempotent.
//
// SetDebugController(nil) runs inside the same dbgMu critical section as the
// a.dc clear, not after releasing it: releasing the lock first left a window
// where a concurrent AttachDebugger could see a.dc == nil, fully attach a new
// controller (a.dc = newDC, session wired to newDC), and then have THIS
// call's now-delayed SetDebugController(nil) run last — wiping the session's
// live controller back to nil while a.dc still (correctly, from the
// adapter's own bookkeeping) points at newDC. Matches how AttachDebugger
// already does its own SetDebugController call under the same lock.
func (a *chatSessionAdapter) DetachDebugger() error {
	a.dbgMu.Lock()
	defer a.dbgMu.Unlock()
	dc := a.dc
	a.dc = nil
	if dc == nil {
		return nil
	}
	dc.Resume(debug.ActionResume)
	a.sess.SetDebugController(nil)
	return nil
}

// Compile-time interface assertions.
var _ session.Session = (*oneShotSessionAdapter)(nil)
var _ session.Session = (*chatSessionAdapter)(nil)
