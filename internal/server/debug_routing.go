package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
)

// Multi-session debug routing.
//
// Every /api/debug/* handler resolves the target session_id (query param or
// JSON body) to a per-session DebugController via the registry. This file
// centralises resolution so individual handlers stay short.
//
// Back-compat: when session_id is absent and exactly one live session exists
// (oneshot preferred), resolveDebugSession targets it — so legacy single-run
// clients keep working without modification.

// errAmbiguousSession means no session_id was passed and more than one live
// session exists — the server refuses to guess.
var errAmbiguousSession = errors.New("ambiguous: multiple live sessions; specify session_id")

// errNoActiveDebugCtrl is returned when a handler is called but no debug
// controller is attached to the resolved session (or the legacy global).
var errNoActiveDebugCtrl = errors.New("debug controller not active")

// resolveDebugSession returns the session targeted by `id`. When id is empty
// and there is exactly one live session (oneshot preferred, else the sole
// chat), returns that one for backward compatibility with single-session
// clients. Returns errAmbiguousSession if the default is undetermined.
func (s *SSEServer) resolveDebugSession(id string) (session.Session, error) {
	if s.sessionRegistry == nil {
		return nil, errors.New("session registry not configured")
	}
	if id != "" {
		return s.sessionRegistry.Get(id)
	}
	metas := s.sessionRegistry.List(session.Filter{Status: session.StatusRunning})
	if len(metas) == 0 {
		return nil, session.ErrNotFound
	}
	// Prefer a oneshot when there is exactly one — the legacy single-run
	// clients (RunLauncher, Stop-from-BreakpointBar) never specified session_id.
	var oneshot *session.SessionMeta
	for i := range metas {
		if metas[i].Mode == session.ModeOneShot {
			if oneshot != nil {
				return nil, errAmbiguousSession
			}
			oneshot = &metas[i]
		}
	}
	if oneshot != nil {
		return s.sessionRegistry.Get(oneshot.ID)
	}
	if len(metas) == 1 {
		return s.sessionRegistry.Get(metas[0].ID)
	}
	return nil, errAmbiguousSession
}

// resolveDebugCtrl returns the DebugController for a debug.* handler by
// resolving session_id via the registry. Returns errNoActiveDebugCtrl if the
// targeted session exists but hasn't been attached yet.
func (s *SSEServer) resolveDebugCtrl(sessionID string) (*debug.DebugController, session.Session, error) {
	sess, err := s.resolveDebugSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	dc := sess.DebugController()
	if dc == nil {
		return nil, sess, errNoActiveDebugCtrl
	}
	return dc, sess, nil
}

// writeDebugError maps resolve errors to HTTP responses with consistent
// shape. Uses json.Encoder rather than http.Error: http.Error unconditionally
// sets Content-Type: text/plain even though the body is always a JSON
// literal, and the default branch's error message needs real JSON escaping
// — a message containing a `"`, `\`, or newline broke raw string
// concatenation into invalid JSON.
func writeDebugError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	msg := err.Error()
	switch {
	case errors.Is(err, session.ErrNotFound):
		status, msg = http.StatusNotFound, "session not found"
	case errors.Is(err, errAmbiguousSession):
		status, msg = http.StatusBadRequest, "ambiguous; specify session_id"
	case errors.Is(err, errNoActiveDebugCtrl):
		status, msg = http.StatusServiceUnavailable, "debug controller not active"
	case errors.Is(err, session.ErrNotAttachable):
		status, msg = http.StatusConflict, "session is not attachable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
