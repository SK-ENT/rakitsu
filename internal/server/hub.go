package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// handleHubRegister handles POST /api/hub/register — CLI registers a run session
func (s *SSEServer) handleHubRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Query  string   `json:"query"`
		Config string   `json:"config_path"`
		Agents []string `json:"agents"`
		// Mode "chat" marks an interactive TUI session (cross-session
		// messaging target); absent = one-shot run (legacy clients).
		Mode string `json:"mode,omitempty"`
		// AcceptsMsg mirrors the client's settings.session_msg.enabled.
		AcceptsMsg bool `json:"accepts_messages,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, `{"error":"id is required"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.activeSessions[req.ID] = &ActiveSession{
		ID:         req.ID,
		Name:       req.Name,
		Query:      req.Query,
		Config:     req.Config,
		Agents:     req.Agents,
		StartTime:  time.Now(),
		Status:     "running",
		Mode:       req.Mode,
		AcceptsMsg: req.AcceptsMsg,
	}
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"status": "registered", "id": req.ID})
}

// handleHubDeregister handles POST /api/hub/deregister — CLI deregisters on completion
func (s *SSEServer) handleHubDeregister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID     string `json:"id"`
		Status string `json:"status"` // completed, error
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if sess, ok := s.activeSessions[req.ID]; ok {
		sess.Status = req.Status
		sess.Debug = false
	}
	delete(s.pendingCmds, req.ID)
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHubSessions handles GET /api/hub/sessions — list active runs for UI
func (s *SSEServer) handleHubSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.RLock()
	defer s.mu.RUnlock()
	sessions := make([]*ActiveSession, 0, len(s.activeSessions))
	for _, sess := range s.activeSessions {
		sessions = append(sessions, sess)
	}
	json.NewEncoder(w).Encode(sessions)
}

// handleHubIngest handles POST /api/hub/ingest — CLI pushes batched events
func (s *SSEServer) handleHubIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var events []telemetry.AgentEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&events); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid events: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Buffer events per session and publish to EventBus
	s.mu.Lock()
	for i := range events {
		if sid := events[i].SessionID; sid != "" {
			if sess, ok := s.activeSessions[sid]; ok {
				sess.Events = append(sess.Events, events[i])
			}
		}
		s.eventBus.Publish(events[i])
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// handleHubCommands handles GET /api/hub/commands?id=SESSION_ID — CLI polls for debug commands
func (s *SSEServer) handleHubCommands(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, `{"error":"id query param required"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	cmds := s.pendingCmds[id]
	if len(cmds) > 0 {
		delete(s.pendingCmds, id)
	}
	s.mu.Unlock()

	if cmds == nil {
		cmds = []DebugCommand{}
	}
	json.NewEncoder(w).Encode(cmds)
}

// handleHubMessageResult handles POST /api/hub/message-result — a remote CLI
// reports the outcome of an inbound wait_token'd session_message turn, so
// deliverMessage's remote-wait branch (session_message.go) can unblock.
//
// Unknown/expired tokens (the sender already gave up and cleaned up its
// pendingWaits entry) are a silent no-op, not an error — the CLI has no way
// to know whether anyone is still listening, and a late report must never
// surface as a failure on its own turn completion. Always 200.
func (s *SSEServer) handleHubMessageResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		WaitToken   string `json:"wait_token"`
		Final       string `json:"final"`
		Interrupted bool   `json:"interrupted"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	ch, ok := s.pendingWaits[req.WaitToken]
	if ok {
		delete(s.pendingWaits, req.WaitToken)
	}
	s.mu.Unlock()

	if ok {
		select {
		case ch <- turnOutcome{Final: req.Final, Interrupted: req.Interrupted, Err: req.Error}:
		default: // waiter already timed out between the lookup and here
		}
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHubDebug handles POST /api/hub/debug — UI sends debug commands to a CLI instance
func (s *SSEServer) handleHubDebug(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string                 `json:"session_id"`
		Action    string                 `json:"action"` // debug-enable, debug-disable, set-breakpoint, clear-breakpoint, resume, set-params, clear-params
		Data      map[string]interface{} `json:"data,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.Action == "" {
		http.Error(w, `{"error":"session_id and action required"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	sess, ok := s.activeSessions[req.SessionID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	// Track debug state on the session
	if req.Action == "debug-enable" {
		sess.Debug = true
	} else if req.Action == "debug-disable" {
		sess.Debug = false
	}

	// Queue command for CLI to pick up
	s.pendingCmds[req.SessionID] = append(s.pendingCmds[req.SessionID], DebugCommand{
		Action: req.Action,
		Data:   req.Data,
	})
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
}

// handleHubSessionEvents handles GET /api/hub/sessions/events?id=SESSION_ID — fetch buffered events
func (s *SSEServer) handleHubSessionEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, `{"error":"id query param required"}`, http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	sess, ok := s.activeSessions[id]
	if !ok {
		s.mu.RUnlock()
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	// Copy the slice under lock to avoid races
	events := make([]telemetry.AgentEvent, len(sess.Events))
	copy(events, sess.Events)
	s.mu.RUnlock()

	json.NewEncoder(w).Encode(events)
}
