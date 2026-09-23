// Package server provides HTTP and SSE server for the web UI
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"context"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	codexProvider "github.com/SK-ENT/rakitsu/internal/llm/codex"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tokenizer"
	"github.com/SK-ENT/rakitsu/internal/webui"
)

// SessionInfo holds metadata about the current debug session
type SessionInfo struct {
	Name       string   `json:"name"`
	ProjectID  string   `json:"project_id,omitempty"`
	Query      string   `json:"query"`
	ConfigPath string   `json:"config_path"`
	Agents     []string `json:"agents"`
	StartTime  string   `json:"start_time"`
}

// ActiveSession tracks a running CLI instance connected to the hub
type ActiveSession struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	Config    string    `json:"config_path"`
	Agents    []string  `json:"agents"`
	StartTime time.Time `json:"start_time"`
	Status    string    `json:"status"` // running, completed, error
	Debug     bool      `json:"debug"`  // debug mode attached?
	// Mode marks the session kind: "chat" for interactive TUI sessions,
	// "" (absent) for legacy/one-shot runs. Only "chat" sessions are
	// cross-session messaging targets.
	Mode string `json:"mode,omitempty"`
	// AcceptsMsg mirrors the remote session's settings.session_msg.enabled
	// so the hub can reject deliveries to opted-out sessions immediately.
	AcceptsMsg bool                   `json:"accepts_messages,omitempty"`
	Events     []telemetry.AgentEvent `json:"-"` // buffered events (not serialized in session list)
}

// DebugCommand is a pending command for a CLI instance to pick up
type DebugCommand struct {
	Action string                 `json:"action"` // debug-enable, debug-disable, set-breakpoint, clear-breakpoint, resume, set-params, clear-params
	Data   map[string]interface{} `json:"data,omitempty"`
}

// SSEServer handles Server-Sent Events connections
type SSEServer struct {
	eventBus        *telemetry.EventBus
	sessionStore    *store.SessionStore
	debugCtrl       *debug.DebugController
	runner          *AgentRunner
	chatManager     *ChatManager
	configStore     *ConfigStore
	version         string
	bootID          string // unique per server start — frontend uses to invalidate localStorage
	host            string
	port            int
	licenseRequired bool
	server          *http.Server
	sessionInfo     *SessionInfo
	activeSessions  map[string]*ActiveSession
	pendingCmds     map[string][]DebugCommand // session_id → pending commands
	// pendingWaits holds outcome channels for in-flight REMOTE session-message
	// waits, keyed by a one-shot wait_token minted in deliverMessage. A
	// remote CLI reports the outcome back via POST /api/hub/message-result
	// (handleHubMessageResult), which looks up and signals the channel.
	// Guarded by mu, same as pendingCmds/activeSessions.
	pendingWaits map[string]chan turnOutcome
	// pendingReplyWaits parks synchronous senders waiting on a STEERING
	// reply from a run-mode target (spec §4). Key: waiterID+"\x00"+targetID
	// (directional — resolved only by target→waiter traffic). Guarded by mu.
	pendingReplyWaits map[string]chan turnOutcome
	// msgLimiter enforces the cross-session messaging pairwise rate limit
	// (loop guard). See session_message.go.
	msgLimiter *sessionMsgLimiter
	// mailboxes holds external-sender mailboxes for cross-session messaging
	// (spec §3). See mailbox.go.
	mailboxes *mailboxStore
	// sessionRegistry indexes live one-shot runs and chat sessions.
	// Every /api/debug/* handler resolves session_id through this registry.
	sessionRegistry *session.Registry
	mu              sync.RWMutex
}

// NewSSEServer creates a new SSE server
func NewSSEServer(eventBus *telemetry.EventBus, host string, port int) *SSEServer {
	return &SSEServer{
		eventBus:          eventBus,
		host:              host,
		bootID:            fmt.Sprintf("%d", time.Now().UnixNano()),
		activeSessions:    make(map[string]*ActiveSession),
		pendingCmds:       make(map[string][]DebugCommand),
		pendingWaits:      make(map[string]chan turnOutcome),
		pendingReplyWaits: make(map[string]chan turnOutcome),
		msgLimiter:        newSessionMsgLimiter(),
		mailboxes:         newMailboxStore(),
		port:              port,
	}
}

// SetVersion sets the version string for the API
func (s *SSEServer) SetVersion(v string) { s.version = v }

// SetLicenseRequired enables the license acceptance gate in the web UI.
func (s *SSEServer) SetLicenseRequired(on bool) { s.licenseRequired = on }

// SetSessionInfo sets the session metadata
func (s *SSEServer) SetSessionInfo(info SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionInfo = &info
}

// SetSessionStore sets the session store for history endpoints
func (s *SSEServer) SetSessionStore(ss *store.SessionStore) {
	s.sessionStore = ss
}

// SetDebugController sets the debug controller for breakpoint/param/replay routes
func (s *SSEServer) SetDebugController(dc *debug.DebugController) {
	s.debugCtrl = dc
}

// SetRunner sets the agent runner for web-launched runs.
func (s *SSEServer) SetRunner(r *AgentRunner) {
	s.runner = r
	if s.sessionRegistry != nil && r != nil {
		r.SetSessionRegistry(s.sessionRegistry)
	}
}

// SetConfigStore sets the config store for config listing/upload.
func (s *SSEServer) SetConfigStore(cs *ConfigStore) {
	s.configStore = cs
}

// SetChatManager sets the chat session manager for interactive chat.
func (s *SSEServer) SetChatManager(cm *ChatManager) {
	s.chatManager = cm
	if s.sessionRegistry != nil && cm != nil {
		cm.SetSessionRegistry(s.sessionRegistry)
	}
}

// SetSessionRegistry wires the runtime session registry (Phase 1 read-only
// facade over AgentRunner + ChatManager). Must be called before SetRunner /
// SetChatManager to propagate to owners.
func (s *SSEServer) SetSessionRegistry(reg *session.Registry) {
	s.sessionRegistry = reg
	if s.runner != nil {
		s.runner.SetSessionRegistry(reg)
	}
	if s.chatManager != nil {
		s.chatManager.SetSessionRegistry(reg)
	}
}

// Mux returns an http.ServeMux with all SSE routes registered.
func (s *SSEServer) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessionByID)
	// Live chat-mode sessions reachable for cross-session messaging. Exact
	// match, so it wins over the /api/sessions/ prefix route above.
	mux.HandleFunc("/api/sessions/live", s.handleLiveSessions)
	// Runtime sessions — live one-shot runs and chat sessions (Phase 1 of
	// multi-session debug). Distinct from /api/sessions which serves
	// persisted history.
	mux.HandleFunc("/api/runtime/sessions", s.handleRuntimeSessions)
	// Debug API routes (only functional when debugCtrl is set)
	mux.HandleFunc("/api/debug/breakpoints", s.handleDebugBreakpoints)
	mux.HandleFunc("/api/debug/resume", s.handleDebugResume)
	mux.HandleFunc("/api/debug/state", s.handleDebugState)
	mux.HandleFunc("/api/debug/params", s.handleDebugParams)
	mux.HandleFunc("/api/debug/pause", s.handleDebugPause)
	mux.HandleFunc("/api/debug/attach", s.handleDebugAttach)
	mux.HandleFunc("/api/debug/detach", s.handleDebugDetach)
	mux.HandleFunc("/api/debug/replay", s.handleDebugReplay)
	mux.HandleFunc("/api/debug/export", s.handleDebugExport)
	mux.HandleFunc("/api/debug/rerun", s.handleDebugRerun)
	mux.HandleFunc("/api/debug/user_input", s.handleDebugUserInput)
	// Run API routes (web-launched agent runs)
	mux.HandleFunc("/api/run", s.handleRun)
	mux.HandleFunc("/api/run/stop", s.handleRunStop)
	mux.HandleFunc("/api/workdir", s.handleWorkdir)
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/providers/models", s.handleProviderModels)
	mux.HandleFunc("/api/providers/model-info", s.handleProviderModelInfo)
	mux.HandleFunc("/api/configs", s.handleConfigs)
	mux.HandleFunc("/api/configs/", s.handleConfigByID)
	mux.HandleFunc("/api/configs/upload", s.handleConfigUpload)
	mux.HandleFunc("/api/configs/inline", s.handleConfigInline)
	mux.HandleFunc("/api/configs/upload-zip", s.handleConfigUploadZip)
	mux.HandleFunc("/api/configs/validate", s.handleConfigValidate)
	mux.HandleFunc("/api/demo", s.handleDemo)
	mux.HandleFunc("/api/tokenize", s.handleTokenize)
	// Hub endpoints — session registry + event ingestion
	mux.HandleFunc("/api/hub/register", s.handleHubRegister)
	mux.HandleFunc("/api/hub/deregister", s.handleHubDeregister)
	mux.HandleFunc("/api/hub/sessions", s.handleHubSessions)
	mux.HandleFunc("/api/hub/ingest", s.handleHubIngest)
	mux.HandleFunc("/api/hub/commands", s.handleHubCommands)
	mux.HandleFunc("/api/hub/message-result", s.handleHubMessageResult)
	mux.HandleFunc("/api/hub/debug", s.handleHubDebug)
	mux.HandleFunc("/api/hub/sessions/events", s.handleHubSessionEvents)
	// Chat API — interactive chat sessions (WebSocket + REST control plane)
	mux.HandleFunc("/api/chat", s.handleChatList)
	mux.HandleFunc("/api/chat/start", s.handleChatStart)
	mux.HandleFunc("/api/chat/", s.handleChatByID)
	mux.HandleFunc("/ws/chat/", s.handleChatWS)
	// Serve embedded frontend (SPA with fallback to index.html)
	if staticFS, err := webui.GetStaticFS(); err == nil {
		mux.Handle("/", spaFallback(staticFS, http.FileServer(staticFS)))
	}
	return mux
}

// Start starts the SSE server on its own port
func (s *SSEServer) Start() error {
	if err := RequireBindAllowed(s.host); err != nil {
		return err
	}
	mux := s.Mux()

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      GuardMiddleware(s.host, CorsMiddleware(AuthMiddleware(mux))),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
	}

	log.Printf("Debug SSE server on http://%s", addr)
	if !isLoopbackHost(s.host) {
		log.Printf("network bind %q enabled with %s auth on control-plane endpoints", s.host, apiTokenEnv)
	}

	return s.server.ListenAndServe()
}

// Stop stops the SSE server
func (s *SSEServer) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// handleSSE handles SSE connections
func (s *SSEServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	sessionFilter := r.URL.Query().Get("session_id")

	eventCh := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(eventCh)

	s.sendEvent(w, "connected", map[string]string{
		"status":  "connected",
		"message": "SSE connection established",
	})
	flusher.Flush()

	// Replay buffered events from active sessions so the frontend doesn't
	// miss events that arrived before this SSE connection was established.
	// This is the common case: CLI registers + starts emitting, then the
	// user opens the browser (or refreshes). Without this replay, the live
	// view appears empty until the next new event arrives.
	s.mu.RLock()
	for _, sess := range s.activeSessions {
		for _, ev := range sess.Events {
			if sessionFilter != "" && ev.SessionID != sessionFilter {
				continue
			}
			s.sendEvent(w, string(ev.EventType), ev)
		}
	}
	s.mu.RUnlock()
	flusher.Flush()

	for {
		select {
		case event := <-eventCh:
			if sessionFilter != "" && event.SessionID != sessionFilter {
				continue
			}
			// Runs started inside serve publish raw events on the bus;
			// redact before they reach the browser, like the session JSONL.
			event.Payload = telemetry.RedactEventPayload(event.EventType, event.Payload)
			s.sendEvent(w, string(event.EventType), event)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// sendEvent sends an SSE event
func (s *SSEServer) sendEvent(w http.ResponseWriter, eventType string, data interface{}) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
}

// handleHealth handles health check requests
func (s *SSEServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// handleStatus handles status requests
func (s *SSEServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	hasUserConfigs := false
	if s.configStore != nil {
		if entries, err := s.configStore.List(); err == nil {
			hasUserConfigs = len(entries) > 0
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "running",
		"version":          s.version,
		"boot_id":          s.bootID,
		"timestamp":        time.Now().Format(time.RFC3339),
		"has_user_configs": hasUserConfigs,
		"license_required": s.licenseRequired,
	})
}

// handleDemo registers the embedded demo config and returns the run parameters.
// GET /api/demo — returns available providers.
// GET /api/demo?provider=ollama&model=llama3.2 — registers config and returns run params.
func (s *SSEServer) handleDemo(w http.ResponseWriter, r *http.Request) {
	if s.configStore == nil {
		http.Error(w, "config store not available", http.StatusServiceUnavailable)
		return
	}

	provider := r.URL.Query().Get("provider")
	model := r.URL.Query().Get("model")

	// No provider selected — return the provider list so the UI can show options.
	if provider == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"providers": DemoProviders,
		})
		return
	}

	// Boundary validation: provider/model feed DemoYAML's unescaped template
	// substitution, so anything outside a plain identifier shape must be
	// rejected here, before it ever reaches DemoYAML — see DemoYAML's doc
	// comment (round-7 PR #9 review finding).
	if !validDemoIdentifier(provider) {
		http.Error(w, "invalid provider", http.StatusBadRequest)
		return
	}

	// Default model for the selected provider.
	if model == "" {
		for _, p := range DemoProviders {
			if p.Name == provider {
				model = p.Model
				break
			}
		}
		if model == "" {
			model = "gpt-4o-mini"
		}
	}
	if !validDemoIdentifier(model) {
		http.Error(w, "invalid model", http.StatusBadRequest)
		return
	}

	yaml := DemoYAML(provider, model)
	entry, err := s.configStore.Inline(yaml)
	if err != nil {
		http.Error(w, "failed to load demo config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"config_id":   entry.ID,
		"query":       DemoQuery,
		"breakpoints": DemoBreakpoints(),
	})
}

// handleSession returns current live session metadata
func (s *SSEServer) handleSession(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if s.sessionInfo == nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "no session"})
		return
	}
	json.NewEncoder(w).Encode(s.sessionInfo)
}

// handleListSessions returns all persisted sessions (GET) or bulk-deletes (DELETE)
func (s *SSEServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.sessionStore == nil {
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}

	if r.Method == http.MethodDelete {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
			http.Error(w, `{"error":"ids required"}`, http.StatusBadRequest)
			return
		}
		deleted := 0
		for _, id := range body.IDs {
			if err := s.sessionStore.DeleteSession(id); err == nil {
				deleted++
			}
		}
		json.NewEncoder(w).Encode(map[string]int{"deleted": deleted})
		return
	}

	sessions, err := s.sessionStore.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []store.SessionMeta{}
	}
	// Filter by project_id if provided
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		filtered := sessions[:0]
		for _, sess := range sessions {
			if sess.ProjectID == pid {
				filtered = append(filtered, sess)
			}
		}
		sessions = filtered
	}
	json.NewEncoder(w).Encode(sessions)
}

// handleSessionByID handles /api/sessions/{id} and /api/sessions/{id}/events
func (s *SSEServer) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	// Cross-session messaging targets LIVE sessions, not persisted history —
	// dispatched before the sessionStore gate so it works with persistence
	// disabled.
	if len(parts) == 2 && parts[1] == "message" {
		s.handleSessionMessage(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "inbox" {
		s.handleSessionInbox(w, r, id)
		return
	}

	if s.sessionStore == nil {
		http.Error(w, "no session store", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodDelete {
		if err := s.sessionStore.DeleteSession(id); err != nil {
			jsonErrorResponse(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		return
	}

	if len(parts) == 2 && parts[1] == "events" {
		events, err := s.sessionStore.GetSessionEvents(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(events)
		return
	}

	if len(parts) == 2 && parts[1] == "config" {
		meta, err := s.sessionStore.GetSession(id)
		if err != nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		// Prefer stored YAML snapshot; fall back to reading config_path from disk
		yamlContent := meta.ConfigYAML
		if yamlContent == "" && meta.ConfigPath != "" {
			data, err := os.ReadFile(meta.ConfigPath)
			if err == nil {
				yamlContent = string(data)
			}
		}
		if yamlContent == "" {
			http.Error(w, `{"error":"no config available"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"yaml": yamlContent})
		return
	}

	if len(parts) == 2 && parts[1] == "rerun" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if s.runner == nil {
			http.Error(w, `{"error":"runner not available"}`, http.StatusServiceUnavailable)
			return
		}
		// Stop any stale/active run before starting a new one
		s.runner.Stop()
		meta, err := s.sessionStore.GetSession(id)
		if err != nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		if meta.ConfigPath == "" && meta.ConfigYAML == "" {
			http.Error(w, `{"error":"session has no config path or YAML"}`, http.StatusBadRequest)
			return
		}
		// If config file is missing (temp file cleaned up), re-create from stored YAML
		configPath := meta.ConfigPath
		if configPath != "" {
			if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) && meta.ConfigYAML != "" {
				if s.configStore != nil {
					if entry, inlineErr := s.configStore.Inline(meta.ConfigYAML); inlineErr == nil {
						if _, recreatedPath, _ := s.configStore.Get(entry.ID); recreatedPath != "" {
							configPath = recreatedPath
						}
					}
				}
			}
		} else if meta.ConfigYAML != "" && s.configStore != nil {
			if entry, inlineErr := s.configStore.Inline(meta.ConfigYAML); inlineErr == nil {
				if _, recreatedPath, _ := s.configStore.Get(entry.ID); recreatedPath != "" {
					configPath = recreatedPath
				}
			}
		}
		wantDebug := r.URL.Query().Get("debug") == "true"

		// Parse optional breakpoints from request body (sent atomically with run)
		var body struct {
			Breakpoints []struct {
				EventType string `json:"event_type"`
				AgentName string `json:"agent_name"`
			} `json:"breakpoints"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) // ignore errors — body may be empty

		newID, err := s.runner.StartFromPath(r.Context(), configPath, meta.Query, 300, meta.Workdir, wantDebug)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}

		// Apply pre-loaded breakpoints to the debug controller BEFORE the run goroutine can hit them
		if wantDebug && len(body.Breakpoints) > 0 && s.debugCtrl != nil {
			for _, bp := range body.Breakpoints {
				s.debugCtrl.SetBreakpoint(debug.BreakpointKey{EventType: bp.EventType, AgentName: bp.AgentName})
			}
		}

		json.NewEncoder(w).Encode(map[string]string{"session_id": newID})
		return
	}

	meta, err := s.sessionStore.GetSession(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(meta)
}

// handleDebugBreakpoints handles GET/POST /api/debug/breakpoints.
// Phase 2: accepts optional `session_id` (query for GET, body for POST).
func (s *SSEServer) handleDebugBreakpoints(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// Empty-when-absent: the frontend polls this on every Debug tab open;
		// no session → no breakpoints to report. Write clauses (POST below)
		// still return proper errors so bad writes aren't silently dropped.
		dc, _, err := s.resolveDebugCtrl(r.URL.Query().Get("session_id"))
		if err != nil {
			json.NewEncoder(w).Encode([]interface{}{})
			return
		}
		json.NewEncoder(w).Encode(dc.ListBreakpoints())
	case http.MethodPost:
		var req struct {
			SessionID string `json:"session_id"`
			Action    string `json:"action"` // "set" or "clear" or "clear-all"
			EventType string `json:"event_type"`
			AgentName string `json:"agent_name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		dc, _, err := s.resolveDebugCtrl(req.SessionID)
		if err != nil {
			writeDebugError(w, err)
			return
		}
		if req.Action == "clear-all" {
			dc.ClearAllBreakpoints()
		} else {
			key := debug.BreakpointKey{EventType: req.EventType, AgentName: req.AgentName}
			if req.Action == "clear" {
				dc.ClearBreakpoint(key)
			} else {
				dc.SetBreakpoint(key)
			}
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleDebugResume handles POST /api/debug/resume
func (s *SSEServer) handleDebugResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID  string `json:"session_id,omitempty"`
		Action     string `json:"action"`
		Checkpoint string `json:"checkpoint,omitempty"`
		AgentName  string `json:"agent_name,omitempty"`
		ToolName   string `json:"tool_name,omitempty"`
		Iteration  int    `json:"iteration,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	dc, _, err := s.resolveDebugCtrl(req.SessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}

	action := debug.ResumeAction(req.Action)
	switch action {
	case debug.ActionResume, debug.ActionStep, debug.ActionStepIn, debug.ActionStepOut, debug.ActionStop:
		dc.Resume(action)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case debug.ActionRunUntil:
		dc.SetRunUntilCondition(&debug.RunUntilCondition{
			Checkpoint: req.Checkpoint,
			AgentName:  req.AgentName,
			ToolName:   req.ToolName,
			Iteration:  req.Iteration,
		})
		dc.Resume(action)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"invalid action"}`, http.StatusBadRequest)
	}
}

// handleDebugPause handles POST /api/debug/pause
func (s *SSEServer) handleDebugPause(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		// Allow body-based too, but body is optional here.
		var req struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
		sessionID = req.SessionID
	}
	dc, _, err := s.resolveDebugCtrl(sessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	dc.RequestPause()
	w.Write([]byte(`{"ok":true}`))
}

// handleDebugAttach handles POST /api/debug/attach — creates a debug
// controller and wires it into the session.
//
// Body: { session_id?, breakpoints[] }. When session_id is absent, falls back
// to the sole live oneshot session so legacy single-run clients keep working.
// Ambiguous (two chats, no id) returns 400.
func (s *SSEServer) handleDebugAttach(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SessionID   string            `json:"session_id,omitempty"`
		Breakpoints []BreakpointEntry `json:"breakpoints"`
	}
	json.NewDecoder(r.Body).Decode(&body) // ignore errors — empty body is fine

	sess, err := s.resolveDebugSession(body.SessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	keys := make([]debug.BreakpointKey, len(body.Breakpoints))
	for i, bp := range body.Breakpoints {
		keys[i] = debug.BreakpointKey{EventType: bp.EventType, AgentName: bp.AgentName}
	}
	if _, err := sess.AttachDebugger(keys); err != nil {
		writeDebugError(w, err)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "session_id": sess.ID()})
}

// handleDebugDetach handles POST /api/debug/detach — removes the debug controller.
func (s *SSEServer) handleDebugDetach(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SessionID string `json:"session_id,omitempty"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	sess, err := s.resolveDebugSession(body.SessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	if err := sess.DetachDebugger(); err != nil {
		writeDebugError(w, err)
		return
	}
	w.Write([]byte(`{"ok":true}`))
}

// handleDebugState handles GET /api/debug/state.
// Read-only: returns an empty "running" state with no breakpoints when no
// session is attached, rather than 404/503. Makes the frontend poll quiet
// when the debugger isn't active (there's nothing to report, which is fine).
func (s *SSEServer) handleDebugState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	dc, _, err := s.resolveDebugCtrl(r.URL.Query().Get("session_id"))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"state":       "running",
			"breakpoints": []interface{}{},
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"state":       dc.GetState(),
		"breakpoints": dc.ListBreakpoints(),
	})
}

// handleDebugParams handles POST/DELETE /api/debug/params
func (s *SSEServer) handleDebugParams(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPost:
		var req struct {
			SessionID     string   `json:"session_id,omitempty"`
			Agent         string   `json:"agent"`
			Temperature   *float64 `json:"temperature,omitempty"`
			MaxTokens     *int     `json:"max_tokens,omitempty"`
			Model         *string  `json:"model,omitempty"`
			TopP          *float64 `json:"top_p,omitempty"`
			SystemPrompt  *string  `json:"system_prompt,omitempty"`
			MaxIterations *int     `json:"max_iterations,omitempty"`
			Tools         []string `json:"tools,omitempty"`
			Sticky        bool     `json:"sticky"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.Agent == "" {
			http.Error(w, `{"error":"agent name required"}`, http.StatusBadRequest)
			return
		}
		dc, _, err := s.resolveDebugCtrl(req.SessionID)
		if err != nil {
			writeDebugError(w, err)
			return
		}
		dc.SetOverrides(req.Agent, &debug.ParamOverride{
			Temperature:   req.Temperature,
			MaxTokens:     req.MaxTokens,
			Model:         req.Model,
			TopP:          req.TopP,
			SystemPrompt:  req.SystemPrompt,
			MaxIterations: req.MaxIterations,
			Tools:         req.Tools,
			Sticky:        req.Sticky,
		})
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		agent := r.URL.Query().Get("agent")
		if agent == "" {
			http.Error(w, `{"error":"agent query param required"}`, http.StatusBadRequest)
			return
		}
		dc, _, err := s.resolveDebugCtrl(r.URL.Query().Get("session_id"))
		if err != nil {
			writeDebugError(w, err)
			return
		}
		dc.ClearOverrides(agent)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleDebugReplay handles POST /api/debug/replay — reconstruct history from a session
func (s *SSEServer) handleDebugReplay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.sessionStore == nil {
		http.Error(w, `{"error":"no session store"}`, http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID  string `json:"session_id"`
		EventIndex int    `json:"event_index"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	events, err := s.sessionStore.GetSessionEvents(req.SessionID)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusNotFound)
		return
	}

	replayID := fmt.Sprintf("replay-%s-%d", req.SessionID, req.EventIndex)

	s.eventBus.Emit("system", telemetry.EventReplayStart, telemetry.ReplayStartPayload{
		ReplayID:        replayID,
		OriginalSession: req.SessionID,
		FromIteration:   req.EventIndex,
	})

	history, iteration, agentName, query := debug.ReconstructHistory(events, req.EventIndex)

	s.eventBus.Emit("system", telemetry.EventReplayEnd, telemetry.ReplayEndPayload{
		ReplayID: replayID,
		Status:   "success",
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"history":    history,
		"iteration":  iteration,
		"agent_name": agentName,
		"query":      query,
		"events":     len(events),
		"replay_id":  replayID,
	})
}

// handleDebugExport handles POST /api/debug/export — export config with overrides
func (s *SSEServer) handleDebugExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string `json:"session_id,omitempty"`
		Agent     string `json:"agent"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	dc, _, err := s.resolveDebugCtrl(req.SessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}

	overrides := map[string]interface{}{}
	if req.Agent != "" {
		if ovr := dc.GetOverrides(req.Agent); ovr != nil {
			overrides[req.Agent] = ovr
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"overrides": overrides,
		"status":    "ok",
	})
}

// handleDebugRerun handles POST /api/debug/rerun — signal pipeline to re-run from a named step
func (s *SSEServer) handleDebugRerun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionID string                 `json:"session_id,omitempty"`
		StepName  string                 `json:"step_name"`
		Overrides map[string]interface{} `json:"overrides,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StepName == "" {
		http.Error(w, `{"error":"step_name required"}`, http.StatusBadRequest)
		return
	}
	dc, _, err := s.resolveDebugCtrl(req.SessionID)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	dc.RerunFromStep(req.StepName, req.Overrides)
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkdir returns the server's working directory as a default for web runs.
func (s *SSEServer) handleWorkdir(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cwd, _ := os.Getwd()
	json.NewEncoder(w).Encode(map[string]string{"workdir": cwd})
}

// handleBrowse lists subdirectories at a given path for the workspace folder picker.
// Restricted to user's home directory to prevent arbitrary filesystem traversal.
func (s *SSEServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Query().Get("path")
	if path == "" {
		path, _ = os.Getwd()
	}
	// Resolve to absolute
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// Restrict browsing to home directory or cwd — see withinDir (auth.go)
	// for why this is anchored on the path separator, not a bare prefix.
	homeDir, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	if homeDir != "" && !withinDir(absPath, homeDir) && !withinDir(absPath, cwd) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"path":  absPath,
			"dirs":  []string{},
			"error": "path outside allowed directory",
		})
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"path": absPath,
			"dirs": []string{},
		})
		return
	}
	dirs := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path": absPath,
		"dirs": dirs,
	})
}

// providerKeyHeader carries the provider API key for model-discovery
// probes. It is separate from Authorization, which carries the Rakitsu
// control-plane token when RAKITSU_API_TOKEN is set.
const providerKeyHeader = "X-Provider-Key"

// providerAPIKeyFromRequest reads the provider API key to use for an
// outbound probe: the X-Provider-Key header first (never logged or kept in
// browser history). A bearer Authorization header is only accepted as the
// provider key when no control-plane token is configured — otherwise it IS
// that token, and forwarding it to a caller-chosen base_url would hand the
// control plane to whoever runs that URL. Falls back to the legacy
// ?api_key= query param for backward compatibility.
func providerAPIKeyFromRequest(r *http.Request) string {
	if k := r.Header.Get(providerKeyHeader); k != "" {
		return k
	}
	if !TokenConfigured() {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}
	return r.URL.Query().Get("api_key")
}

// handleProviderModels proxies GET /v1/models to an OpenAI-compatible endpoint.
// Query params: base_url (required). API key: Authorization: Bearer header
// (preferred) or the legacy ?api_key= query param.
func (s *SSEServer) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// The codex provider has no OpenAI-style endpoint; its catalog comes from
	// the subscription backend using the local Codex CLI login.
	if r.URL.Query().Get("type") == "codex" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		models, err := codexProvider.ListModels(ctx, &llm.ProviderConfig{CredentialsFile: r.URL.Query().Get("credentials_file")})
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"models": []string{}, "error": err.Error()})
			return
		}
		slugs := codexProvider.ListedSlugs(models)
		if slugs == nil {
			slugs = []string{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"models": slugs})
		return
	}

	baseURL := strings.TrimRight(r.URL.Query().Get("base_url"), "/")
	apiKey := providerAPIKeyFromRequest(r)
	if baseURL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"models": []string{}})
		return
	}

	// SSRF protection: block requests to private/link-local IP ranges
	if !isAllowedProxyTarget(baseURL) {
		http.Error(w, `{"error":"base_url targets a private or restricted network"}`, http.StatusForbidden)
		return
	}

	client := proxyHTTPClient

	// Try /v1/models first, then /models, then /model/info (LiteLLM variants)
	var body []byte
	for _, path := range []string{"/v1/models", "/models", "/model/info"} {
		req, err := http.NewRequest("GET", baseURL+path, nil)
		if err != nil {
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		b, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != 200 {
			continue
		}
		body = b
		break
	}
	if body == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"models": []string{}})
		return
	}

	// Parse response — try OpenAI format first, then LiteLLM /model/info format
	var models []string

	// Format 1: OpenAI { data: [{ id: "model-name" }, ...] }
	var parsed struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && len(parsed.Data) > 0 {
		// Try as array (OpenAI /v1/models)
		var dataArr []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(parsed.Data, &dataArr) == nil && len(dataArr) > 0 {
			for _, m := range dataArr {
				if m.ID != "" {
					models = append(models, m.ID)
				}
			}
		} else {
			// Try as map (LiteLLM /model/info: { data: { "model": {...}, ... } })
			var dataMap map[string]interface{}
			if json.Unmarshal(parsed.Data, &dataMap) == nil {
				for name := range dataMap {
					models = append(models, name)
				}
			}
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"models": models})
}

// handleProviderModelInfo proxies GET /model/info to a LiteLLM-compatible endpoint.
// Returns per-model capabilities: max_tokens, vision, function calling, context window.
func (s *SSEServer) handleProviderModelInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	baseURL := strings.TrimRight(r.URL.Query().Get("base_url"), "/")
	apiKey := providerAPIKeyFromRequest(r)
	if baseURL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"models": []interface{}{}})
		return
	}

	// SSRF protection
	if !isAllowedProxyTarget(baseURL) {
		http.Error(w, `{"error":"base_url targets a private or restricted network"}`, http.StatusForbidden)
		return
	}

	client := proxyHTTPClient

	// Try LiteLLM /model/info first, then /model_group/info
	endpoints := []string{"/model/info", "/model_group/info"}
	for _, ep := range endpoints {
		req, err := http.NewRequest("GET", baseURL+ep, nil)
		if err != nil {
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}

		// Forward the raw JSON response — let frontend parse it
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
		resp.Body.Close()
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"models": []interface{}{}})
}

// handleRun handles POST /api/run (start) and GET /api/run (status)
func (s *SSEServer) handleRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPost:
		if s.runner == nil {
			http.Error(w, `{"error":"runner not configured"}`, http.StatusServiceUnavailable)
			return
		}
		var req RunRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			http.Error(w, `{"error":"query is required"}`, http.StatusBadRequest)
			return
		}
		if req.ConfigID == "" {
			http.Error(w, `{"error":"config_id is required"}`, http.StatusBadRequest)
			return
		}
		resp, err := s.runner.Start(req)
		if err != nil {
			jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
			return
		}
		if resp.Error != "" {
			w.WriteHeader(http.StatusConflict)
		}
		json.NewEncoder(w).Encode(resp)

	case http.MethodGet:
		if s.runner == nil {
			json.NewEncoder(w).Encode(map[string]string{"status": "idle"})
			return
		}
		json.NewEncoder(w).Encode(s.runner.Status())

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleRunStop handles POST /api/run/stop
func (s *SSEServer) handleRunStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.runner == nil {
		http.Error(w, `{"error":"runner not configured"}`, http.StatusServiceUnavailable)
		return
	}
	s.runner.Stop()
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// handleConfigByID handles DELETE /api/configs/{id} — removes an uploaded
// or inline config from the temp store. On-disk configs (scanned from
// examples/, configs/) are read-only and return an error.
func (s *SSEServer) handleConfigByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.configStore == nil {
		http.Error(w, `{"error":"config store not configured"}`, http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/configs/")
	id = strings.Trim(id, "/")
	// Don't shadow sibling routes handled by specific HandleFuncs.
	switch id {
	case "", "upload", "inline", "upload-zip", "validate":
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if err := s.configStore.Delete(id); err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleConfigs handles GET /api/configs
func (s *SSEServer) handleConfigs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.configStore == nil {
		json.NewEncoder(w).Encode([]ConfigEntry{})
		return
	}
	configs, err := s.configStore.List()
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if configs == nil {
		configs = []ConfigEntry{}
	}
	json.NewEncoder(w).Encode(configs)
}

// handleConfigUpload handles POST /api/configs/upload (multipart file)
func (s *SSEServer) handleConfigUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.configStore == nil {
		http.Error(w, `{"error":"config store not configured"}`, http.StatusServiceUnavailable)
		return
	}

	// ParseMultipartForm's argument is only the in-memory threshold — larger
	// parts spill to temp files — so cap the whole body first.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20+64<<10)
	r.ParseMultipartForm(1 << 20) // 1MB max
	file, header, err := r.FormFile("config")
	if err != nil {
		http.Error(w, `{"error":"file upload required (field: config)"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		http.Error(w, `{"error":"failed to read file"}`, http.StatusBadRequest)
		return
	}

	entry, err := s.configStore.Upload(content, header.Filename)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(entry)
}

// handleConfigInline handles POST /api/configs/inline (inline YAML string)
func (s *SSEServer) handleConfigInline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.configStore == nil {
		http.Error(w, `{"error":"config store not configured"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.YAML == "" {
		http.Error(w, `{"error":"yaml field is required"}`, http.StatusBadRequest)
		return
	}

	entry, err := s.configStore.Inline(req.YAML)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(entry)
}

// handleConfigValidate handles POST /api/configs/validate (validate YAML without saving)
func (s *SSEServer) handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.YAML == "" {
		http.Error(w, `{"error":"yaml field is required"}`, http.StatusBadRequest)
		return
	}

	validationErrs, err := config.ValidateYAML(req.YAML)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid":  false,
			"errors": []map[string]string{{"field": "yaml", "message": err.Error()}},
		})
		return
	}

	if len(validationErrs) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"valid": true, "errors": []string{}})
		return
	}

	errList := make([]map[string]string, len(validationErrs))
	for i, e := range validationErrs {
		errList[i] = map[string]string{"field": e.Field, "message": e.Message}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "errors": errList})
}

// handleTokenize handles POST /api/tokenize — returns individual tokens with IDs for visualization.
func (s *SSEServer) handleTokenize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = "gpt-4"
	}
	result, err := tokenizer.Tokenize(req.Text, req.Model)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}

// handleConfigUploadZip handles POST /api/configs/upload-zip (modular config as zip)
func (s *SSEServer) handleConfigUploadZip(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.configStore == nil {
		http.Error(w, `{"error":"config store not configured"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20+64<<10) // see handleConfigUpload
	r.ParseMultipartForm(10 << 20) // 10MB max
	file, _, err := r.FormFile("config")
	if err != nil {
		http.Error(w, `{"error":"file upload required (field: config)"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil {
		http.Error(w, `{"error":"failed to read file"}`, http.StatusBadRequest)
		return
	}

	entry, err := s.configStore.UploadZip(content)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(entry)
}

// spaFallback wraps a file server to serve index.html for non-file routes
// (SPA client-side routing, e.g. /sessions/abc123). Checks fsys directly for
// the requested path rather than trying to detect a 404 after the fact —
// simpler and avoids wrapping http.ResponseWriter.
func spaFallback(fsys http.FileSystem, static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		if p != "/" {
			f, err := fsys.Open(p)
			if err != nil {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				static.ServeHTTP(w, r2)
				return
			}
			f.Close()
		}
		static.ServeHTTP(w, r)
	})
}

// CorsMiddleware adds CORS headers restricted to same-origin and localhost.
func CorsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+providerKeyHeader)
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// jsonErrorResponse writes a safe JSON error response, properly escaping the message.
func jsonErrorResponse(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// isAllowedProxyTarget validates a URL is safe to proxy to (blocks private/link-local IPs).
// Allows localhost (for local Ollama/LiteLLM) but blocks cloud metadata endpoints and private ranges.
func isAllowedProxyTarget(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()

	// Block cloud metadata endpoints by name too — cheap insurance alongside
	// the IP-based check below.
	if host == "169.254.169.254" || host == "metadata.google.internal" {
		return false
	}

	if ip := net.ParseIP(host); ip != nil {
		return isAllowedProxyIP(ip)
	}

	// Hostname, not a literal IP: resolve it and check every address it
	// comes back with. The outbound request this gates uses the same
	// resolution, so a hostname whose DNS points at a blocked range (e.g.
	// attacker-controlled DNS pointed at the cloud metadata IP) must be
	// rejected the same as if the URL had used that IP directly — skipping
	// this check for hostnames would make it bypassable by name alone.
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || !isAllowedProxyIP(ip) {
			return false
		}
	}
	return true
}

// isAllowedProxyIP applies the IP-range policy shared by both the literal-IP
// and resolved-hostname paths in isAllowedProxyTarget.
func isAllowedProxyIP(ip net.IP) bool {
	// Block link-local (169.254.x.x) and unspecified (0.0.0.0)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	// Loopback (127.x.x.x, ::1) and private ranges (10.x, 172.16-31.x,
	// 192.168.x) are allowed — this proxy exists specifically to reach
	// local/Tailscale-networked LLM providers.
	return true
}

// proxyHTTPClient is the http.Client shared by every model-list/model-info
// proxy request. It enforces isAllowedProxyTarget's IP policy at the
// actual dial, not just at the callers' pre-check.
//
// isAllowedProxyTarget resolves the hostname once via net.LookupHost to
// decide whether to even attempt the request. The http.Client then does
// its own, separate DNS resolution when it actually connects — a
// malicious DNS server can answer those two lookups differently (classic
// TOCTOU/DNS-rebinding SSRF): a safe address for the pre-check, then a
// blocked one (e.g. the cloud metadata IP) moments later for the real
// connection. Overriding DialContext to resolve-and-validate at dial time
// closes that gap; CheckRedirect re-validates a redirect target the same
// way, since a redirect response isn't covered by either lookup.
//
// A single shared client (rather than one built per request) matters
// beyond the obvious allocation cost: each client's Transport keeps its
// own idle-connection pool, and building a fresh one per request leaves
// its keep-alive sockets open with nothing left holding a reference to
// close them — a busy or repeatedly-hit endpoint would accumulate open
// connections and file descriptors across those abandoned transports
// instead of reusing (and eventually idle-timing-out) one pool.
// http.Client is safe for concurrent use, so one package-level instance
// serves every call.
var proxyHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{DialContext: ssrfSafeDialContext},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !isAllowedProxyTarget(req.URL.String()) {
			return fmt.Errorf("redirect to a blocked address")
		}
		return nil
	},
}

// ssrfSafeDialContext resolves addr (if it's a hostname, not a literal IP)
// and validates it against isAllowedProxyIP immediately before dialing —
// the same policy isAllowedProxyTarget's pre-check applies, but re-checked
// at connect time so a rebinding DNS server answering the two lookups
// differently can't smuggle a blocked address past the pre-check. Split
// out from newProxyHTTPClient so it's unit-testable without a real
// network dial.
var proxyDialer = &net.Dialer{Timeout: 10 * time.Second}

func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, lookupErr := net.DefaultResolver.LookupHost(ctx, host)
		if lookupErr != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("proxy dial: cannot resolve %s", host)
		}
		ip = nil
		for _, a := range addrs {
			if candidate := net.ParseIP(a); candidate != nil && isAllowedProxyIP(candidate) {
				ip = candidate
				break
			}
		}
		if ip == nil {
			return nil, fmt.Errorf("proxy dial: %s resolves to a blocked address", host)
		}
	} else if !isAllowedProxyIP(ip) {
		return nil, fmt.Errorf("proxy dial: %s is a blocked address", host)
	}
	return proxyDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}

// isAllowedOrigin checks if the origin is localhost or loopback.
func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return true // same-origin requests have no Origin header
	}
	for _, prefix := range []string{
		"http://localhost", "https://localhost",
		"http://127.0.0.1", "https://127.0.0.1",
		"http://[::1]", "https://[::1]",
	} {
		if origin == prefix || (len(origin) > len(prefix) && origin[:len(prefix)+1] == prefix+":") {
			return true
		}
	}
	return false
}
