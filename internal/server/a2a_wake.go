package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/SK-ENT/rakitsu/internal/wake"
)

// A2A wake skills: wake_status, wake_stop, wake_resume
// These are exposed via the /a2a endpoint and allow agents to control wake sessions.

// handleA2AWakeStatus returns the wake status for a session (RPC skill).
// Input: {"session_id": "monitor-web-prod"}
// Output: same as GET /api/chat/{id}/wake/status
func (s *SSEServer) handleA2AWakeStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := s.chatManager.Get(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if sess.cfg == nil || !sess.cfg.Settings.Wake.Enabled {
		http.Error(w, "session has no wake config", http.StatusConflict)
		return
	}

	status := sess.WakeStatus()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleA2AWakeStop stops a wake session (RPC skill).
// Input: {"session_id": "monitor-web-prod"}
// Output: {"state":"stopped"}
func (s *SSEServer) handleA2AWakeStop(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := s.chatManager.Get(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if sess.cfg == nil || !sess.cfg.Settings.Wake.Enabled {
		http.Error(w, "session has no wake config", http.StatusConflict)
		return
	}

	// Call the existing stop logic
	if err := sess.StopWake(); err != nil {
		http.Error(w, fmt.Sprintf("failed to stop wake: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"state": WakeStateStopped})
}

// handleA2AWakeResume resumes a stopped wake session (RPC skill).
// Input: {"session_id": "monitor-web-prod"}
// Output: {"state":"ok"} or error with state
func (s *SSEServer) handleA2AWakeResume(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := s.chatManager.Get(sessionID)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if sess.cfg == nil || !sess.cfg.Settings.Wake.Enabled {
		http.Error(w, "session has no wake config", http.StatusConflict)
		return
	}

	// Call the existing resume logic
	if err := sess.ResumeWake(); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, wake.ErrKillSwitchPresent) {
			code = http.StatusConflict
		}
		http.Error(w, fmt.Sprintf("failed to resume wake: %v", err), code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"state": WakeStateOK})
}
