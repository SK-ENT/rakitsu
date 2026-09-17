package server

import (
	"encoding/json"
	"net/http"

	"github.com/SK-ENT/rakitsu/internal/session"
)

// handleRuntimeSessions serves GET /api/runtime/sessions.
//
// Returns a JSON array of SessionMeta for every live execution (one-shot
// run or chat session). Optional filters:
//   - ?mode=chat|oneshot
//   - ?status=running|paused|stopped|errored|pending
//
// This endpoint backs the debugger's session picker. It is distinct from
// /api/sessions (persisted history); Phase 3 of the multi-session redesign
// will merge the two.
func (s *SSEServer) handleRuntimeSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.sessionRegistry == nil {
		// Graceful: return empty list rather than 500. An older client or a
		// stripped-down server config shouldn't break the picker.
		_ = json.NewEncoder(w).Encode([]session.SessionMeta{})
		return
	}
	filter := session.Filter{
		Mode:   session.SessionMode(r.URL.Query().Get("mode")),
		Status: session.SessionStatus(r.URL.Query().Get("status")),
	}
	out := s.sessionRegistry.List(filter)
	if out == nil {
		out = []session.SessionMeta{}
	}
	_ = json.NewEncoder(w).Encode(out)
}
