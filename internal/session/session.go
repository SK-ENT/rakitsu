// Package session defines a read-only abstraction over every live execution
// in the server — both one-shot agent runs and long-lived chat sessions.
//
// Phase 1 scope (2026-04-18): the interface is read-only. Registry supports
// List(filter) and Get(id) only. Writes (Register/Unregister) happen via
// concrete types passing *already-constructed* Session instances in. No
// behavior change for attach/debug paths.
//
// Phases 2+ will add per-session DebugController, attach routing, and
// collapsing of AgentRunner + ChatManager into this registry.
package session

import (
	"errors"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
)

// SessionMode distinguishes one-shot runs from long-lived chats.
type SessionMode string

const (
	ModeOneShot SessionMode = "oneshot"
	ModeChat    SessionMode = "chat"
)

// SessionStatus is the lifecycle state. Phase 1 only emits a subset
// (running, paused, stopped, errored); Pending is reserved for Phase 4 when
// concurrent runs can queue.
type SessionStatus string

const (
	StatusPending  SessionStatus = "pending"
	StatusRunning  SessionStatus = "running"
	StatusPaused   SessionStatus = "paused"
	StatusStopped  SessionStatus = "stopped"
	StatusErrored  SessionStatus = "errored"
)

// SessionMeta is the JSON-serializable snapshot returned by /api/runtime/sessions.
// Fields are optional where it makes sense; keep this shape stable because the
// frontend SessionPicker depends on it.
type SessionMeta struct {
	ID        string        `json:"id"`
	ConfigID  string        `json:"config_id"`
	Name      string        `json:"name,omitempty"`       // cfg.Name for display
	Mode      SessionMode   `json:"mode"`
	Status    SessionStatus `json:"status"`
	StartedAt string        `json:"started_at,omitempty"` // RFC3339
	EndedAt   string        `json:"ended_at,omitempty"`   // RFC3339, optional
	Agent     string        `json:"agent,omitempty"`      // best-effort display label
	Model     string        `json:"model,omitempty"`      // best-effort display label
}

// Session is the view of one execution. Read surface was defined in Phase 1;
// Phase 2 adds the debug-attach surface so each session owns its own
// DebugController instead of the server-global `s.debugCtrl`.
//
// Implementations must be safe for concurrent use — getters are called from
// HTTP handler goroutines and may race with the owning session's lifecycle.
type Session interface {
	ID() string
	ConfigID() string
	Mode() SessionMode
	Status() SessionStatus
	StartedAt() time.Time
	Meta() SessionMeta

	// DebugController returns the currently attached debug controller, or nil
	// when the session has no debugger. Safe to call at any time.
	DebugController() *debug.DebugController

	// AttachDebugger creates a DebugController (or returns the existing one)
	// and wires it into the session's running agents/orchestrators. Idempotent:
	// a second Attach against an already-attached session returns the existing
	// controller without re-wiring. Returns ErrNotAttachable when the session's
	// underlying run has already ended.
	AttachDebugger(breakpoints []debug.BreakpointKey) (*debug.DebugController, error)

	// DetachDebugger removes the debug controller from the session's agents
	// and clears the local reference. Idempotent — calling on a non-attached
	// session is a no-op.
	DetachDebugger() error
}

// ErrNotFound is returned by Registry.Get when the id is not registered.
var ErrNotFound = errors.New("session not found")

// ErrNotAttachable indicates a debug attach was attempted against a session
// whose underlying run is no longer live (ended, reaped, cancelled).
var ErrNotAttachable = errors.New("session is not attachable")

// Filter constrains Registry.List. Zero-value fields mean "don't filter."
type Filter struct {
	Mode   SessionMode
	Status SessionStatus
}

// Match reports whether a session's meta satisfies the filter.
func (f Filter) Match(m SessionMeta) bool {
	if f.Mode != "" && m.Mode != f.Mode {
		return false
	}
	if f.Status != "" && m.Status != f.Status {
		return false
	}
	return true
}
