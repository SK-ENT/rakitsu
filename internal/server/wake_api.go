package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleChatWakeStatus responds to GET /api/chat/{id}/wake/status with the wake session status.
// Same auth as other /api/chat routes (requiresAuth).
// Returns 404 if session not found, 409 if session has no wake config.
func (s *SSEServer) handleChatWakeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Extract session id from /api/chat/{id}/wake/status
	// Pattern: /api/chat/{id}/wake/status
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/chat/"), "/")
	if len(parts) < 3 || parts[1] != "wake" || parts[2] != "status" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	sessionID := parts[0]

	// Get the session
	sess := s.chatManager.Get(sessionID)
	if sess == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Verify the session has wake enabled
	if sess.cfg == nil || !sess.cfg.Settings.Wake.Enabled {
		w.WriteHeader(http.StatusConflict) // 409: session has no wake config
		return
	}

	// Get the wake status
	status := sess.WakeStatus()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}
