package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// SlashLLMFactory builds a fresh LLM client for the `/model` slash command's
// mid-session swap. Injected from cmd/rakitsu (which owns the createLLMProvider
// machinery) so this package doesn't depend on the cmd/main wiring. ctx is the
// chat session's run context; cfg is the session's config (used for provider
// credentials + base_url lookup).
type SlashLLMFactory func(ctx context.Context, cfg *config.Config, providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)

// ChatManager owns all active chat sessions keyed by session ID.
//
// A chat session outlives individual WS connections: a client can disconnect
// and reconnect to the same session ID without losing conversation state.
// Sessions must be explicitly stopped via POST /api/chat/:id/stop or are
// collected when the server shuts down.
type ChatManager struct {
	mu       sync.RWMutex
	sessions map[string]*ChatSession
	// resuming reserves a resumeID for the duration of Start()'s resume path
	// (resolvePersistedSession + session construction is real I/O, not
	// instantaneous) so a second concurrent Start() for the same id is
	// rejected instead of racing to insert into sessions right behind it.
	resuming     map[string]struct{}
	configStore  *ConfigStore
	sessionStore *store.SessionStore
	buildFunc    ChatBuildFunc
	// hubBus is the server-wide event bus. Every session forwards its
	// events here tagged with session id so debugger UIs (which subscribe
	// to /events) see chat-mode telemetry like USER_INPUT_PENDING.
	hubBus *telemetry.EventBus
	// slashLLMFactory, when non-nil, enables the `/model` slash command to
	// build fresh LLM clients for mid-session swap. Injected from cmd/rakitsu;
	// nil leaves /model with a "not supported in this build" reply.
	slashLLMFactory SlashLLMFactory
	// selfURL is the serve process's own base URL, handed to each session's
	// BuildFunc so the send_message / list_sessions tools can reach the
	// /api/sessions/* messaging endpoints. "" disables those tools.
	selfURL string
	// sessionRegistry, when non-nil, receives a read-only adapter for each
	// active chat session. Phase 1: observational only.
	sessionRegistry *session.Registry
}

// SetSessionRegistry enables Phase-1 runtime session observability for chat.
func (m *ChatManager) SetSessionRegistry(reg *session.Registry) {
	m.mu.Lock()
	m.sessionRegistry = reg
	m.mu.Unlock()
}

// NewChatManager constructs a ChatManager. sessionStore may be nil to
// disable persistence; buildFunc is mandatory and is injected from
// cmd/rakitsu so this package does not import cmd/main. hubBus is used to
// forward session-local events to the server-wide SSE stream.
func NewChatManager(hubBus *telemetry.EventBus, configStore *ConfigStore, sessionStore *store.SessionStore, buildFunc ChatBuildFunc) *ChatManager {
	return &ChatManager{
		sessions:     make(map[string]*ChatSession),
		resuming:     make(map[string]struct{}),
		configStore:  configStore,
		sessionStore: sessionStore,
		buildFunc:    buildFunc,
		hubBus:       hubBus,
	}
}

// SetSlashLLMFactory wires the LLM factory the `/model` slash command uses to
// build a fresh provider client for mid-session model swap. Called from
// cmd/rakitsu after NewChatManager so this package stays independent of the
// cmd/main provider wiring.
// SetSelfURL records the serve process's own base URL for the cross-session
// messaging tools. Deliberately a loopback address in practice (see serve.go)
// so the self-call works regardless of the --host bind.
func (m *ChatManager) SetSelfURL(url string) {
	m.mu.Lock()
	m.selfURL = url
	m.mu.Unlock()
}

func (m *ChatManager) SetSlashLLMFactory(f SlashLLMFactory) {
	m.mu.Lock()
	m.slashLLMFactory = f
	m.mu.Unlock()
}

// Get returns an active session by id, or nil.
func (m *ChatManager) Get(id string) *ChatSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// List returns metadata snapshots for all active chat sessions.
// Sessions returns the live ChatSession values. Used by the cross-session
// messaging endpoints, which need typed access (List returns UI maps).
func (m *ChatManager) Sessions() []*ChatSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ChatSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *ChatManager) List() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]map[string]interface{}, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Meta())
	}
	return out
}

// Start builds a runner and begins a new chat session, optionally resuming
// a previously persisted one when resumeID is non-empty. On resume the new
// session keeps the resumed session_id and rehydrates its branching turn
// history from the on-disk .chat.json blob. configID may be empty on
// resume — it is then resolved from the persisted envelope.
//
// Each call yields an independent SessionStore handle for jsonl recording;
// resume does not yet thread continuity into the JSONL audit log (deferred
// to PR C).
func (m *ChatManager) Start(ctx context.Context, configID, workdir string, envVars map[string]string, resumeID string) (*ChatSession, error) {
	if m.configStore == nil {
		return nil, fmt.Errorf("config store not configured")
	}

	// Resume path: load and validate the persisted tree before touching the
	// config store so a corrupt blob fails fast and the JSONL recorder
	// never starts a half-set-up session.
	var (
		resumeTree    *turntree.Tree[transcriptEntry]
		resumeCreated time.Time
	)
	if resumeID != "" {
		if m.sessionStore == nil {
			return nil, fmt.Errorf("resume requested but session store is disabled")
		}
		// Collision: refuse to resume into a session id that is already
		// active in memory, or currently being resumed by a concurrent
		// call. Check-and-reserve atomically under one Lock — resolving and
		// building the session below is real I/O, so a second caller must
		// see the reservation, not just a snapshot of sessions that's
		// already stale by the time it acts on it.
		m.mu.Lock()
		_, active := m.sessions[resumeID]
		_, reserved := m.resuming[resumeID]
		if active || reserved {
			m.mu.Unlock()
			return nil, fmt.Errorf("session %s is already active", resumeID)
		}
		m.resuming[resumeID] = struct{}{}
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.resuming, resumeID)
			m.mu.Unlock()
		}()

		var rerr error
		resumeTree, resumeCreated, configID, _, rerr = m.resolvePersistedSession(resumeID, configID, envVars)
		if rerr != nil {
			return nil, fmt.Errorf("resume %s: %w", resumeID, rerr)
		}
	}

	cfg, configPath, err := m.configStore.GetWithEnv(configID, envVars)
	if err != nil {
		return nil, fmt.Errorf("config not found: %w", err)
	}

	// PR D-5.7: persisted YAMLs on resume contain literal "[REDACTED]"
	// where the live config had ${VAR}-substituted secrets at run time.
	// Viper's substitution has nothing to substitute on the second
	// pass, so the literal mask flows straight to the LLM client → 401.
	// Per-request env_vars are the canonical secret channel for the
	// daemon; reinject them by the <UPPER_PROVIDER_NAME>_API_KEY
	// convention. No host-env fallback — secrets are request-scoped.
	applyRedactedKeyOverrides(cfg, envVars)

	// Inject the browser-selected workspace into every tool's sandbox +
	// WorkingDir. Without this, chat-mode tools fall back to the server's
	// CWD and their sandbox allowlist stays empty — fs reads/writes land in
	// the wrong place or get denied. Matches what AgentRunner.Start does
	// for one-shot runs.
	applyWorkdirToTools(cfg, workdir)

	// Per-session SessionStore so concurrent chats don't clobber each
	// other's jsonl file (SessionStore is single-session state).
	var sessStore *store.SessionStore
	if m.sessionStore != nil {
		if ss, err := store.NewSessionStore(); err == nil {
			sessStore = ss
		}
	}

	// slashLLMFactory and selfURL both have setters callable after
	// construction (SetSlashLLMFactory, SetSelfURL) — read them under lock
	// rather than off m directly, even though the setters themselves lock,
	// an unlocked read on this side is still a race with a locked write.
	m.mu.RLock()
	slashLLMFactory := m.slashLLMFactory
	selfURL := m.selfURL
	m.mu.RUnlock()

	// Per-session BuildLLM closure: captures cfg + this Start's ctx so the
	// /model slash command can build new clients against the right provider
	// table. nil when the factory isn't wired (tests, daemon without
	// cmd/rakitsu's provider setup) — /model then reports "not supported".
	var buildLLM func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)
	if slashLLMFactory != nil {
		factory := slashLLMFactory
		buildLLM = func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error) {
			return factory(ctx, cfg, providerName, model, mc)
		}
	}
	knownProviders := make([]string, 0, len(cfg.Settings.Providers))
	for name := range cfg.Settings.Providers {
		knownProviders = append(knownProviders, name)
	}
	sort.Strings(knownProviders)

	sess, err := StartChatSession(ctx, ChatSessionOptions{
		ConfigID:       configID,
		ConfigPath:     configPath,
		Cfg:            cfg,
		Workdir:        workdir,
		SessionStore:   sessStore,
		BuildFunc:      m.buildFunc,
		ForwardBus:     m.hubBus,
		SelfURL:        selfURL,
		ResumeID:       resumeID,
		ResumeTree:     resumeTree,
		ResumeCreated:  resumeCreated,
		BuildLLM:       buildLLM,
		KnownProviders: knownProviders,
	})
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[sess.ID] = sess
	reg := m.sessionRegistry
	m.mu.Unlock()

	// Phase 1: project this chat session into the read-only registry.
	if reg != nil {
		reg.Register(&chatSessionAdapter{sess: sess})
	}

	return sess, nil
}

// resolvePersistedSession loads a previously-persisted chat session from
// disk — its branching turn history, original creation timestamp, a
// resolvable config id, and the workspace it ran in. It is the shared
// load-and-resolve core behind both resume (ChatManager.Start) and
// fork-on-past (handleChatFork): neither needs the session to still be
// live in memory.
//
// The tree comes from the .chat.json envelope when present, else is rebuilt
// from the JSONL event log (CLI runs, sessions killed before turn-finalize,
// anything pre-PR-B/3). configID, when non-empty and still resolvable, is
// honored; otherwise it is recovered from the .chat.json envelope, and
// failing that the session's inline ConfigYAML (captured in SessionMeta) is
// re-registered. An unresolvable config is a hard error — the caller cannot
// build a runner without one.
//
// Errors are returned unprefixed; callers add operation context.
func (m *ChatManager) resolvePersistedSession(sessionID, configID string, envVars map[string]string) (
	tree *turntree.Tree[transcriptEntry], created time.Time, resolvedConfigID, workdir string, err error,
) {
	if m.sessionStore == nil {
		return nil, time.Time{}, "", "", fmt.Errorf("session store is disabled")
	}
	// SessionMeta is the universal source of truth for per-session metadata
	// persisted alongside the JSONL event log — needed for the JSONL
	// fallback path, inline-config re-registration, and workdir recovery.
	meta, _ := m.sessionStore.GetSession(sessionID)

	raw, lerr := m.sessionStore.LoadChatTree(sessionID)
	if lerr == nil {
		file, perr := LoadChatTreeFile(raw)
		if perr != nil {
			return nil, time.Time{}, "", "", perr
		}
		tree = file.Tree
		created = file.Created
		if configID == "" {
			configID = file.ConfigID
		}
	} else {
		// JSONL fallback: no .chat.json on disk. Rebuild from the event log.
		if meta == nil {
			return nil, time.Time{}, "", "", fmt.Errorf("load chat tree: %w", lerr)
		}
		events, evErr := m.sessionStore.GetSessionEvents(sessionID)
		if evErr != nil {
			return nil, time.Time{}, "", "", fmt.Errorf("load events: %w", evErr)
		}
		rebuilt := reconstructTreeFromEvents(events)
		if rebuilt == nil || len(rebuilt.Nodes) == 0 {
			return nil, time.Time{}, "", "", fmt.Errorf("no conversation history (no .chat.json and no turn events)")
		}
		tree = rebuilt
		if !meta.StartTime.IsZero() {
			created = meta.StartTime
		}
	}

	// In-memory ConfigStore is volatile across server restarts, so a
	// configID persisted in the .chat.json envelope (or originally taken
	// from a Builder upload) may no longer be known. Caller-supplied
	// configID still wins when it resolves cleanly; otherwise re-register
	// from whatever inline YAML SessionMeta has captured.
	if configID != "" {
		if _, _, lookupErr := m.configStore.GetWithEnv(configID, envVars); lookupErr != nil {
			configID = ""
		}
	}
	if configID == "" && meta != nil && meta.ConfigYAML != "" {
		entry, inlineErr := m.configStore.Inline(meta.ConfigYAML)
		if inlineErr != nil {
			return nil, time.Time{}, "", "", fmt.Errorf("re-register inline config: %w", inlineErr)
		}
		configID = entry.ID
	}
	if configID == "" {
		path := ""
		if meta != nil {
			path = meta.ConfigPath
		}
		suffix := ""
		if path != "" {
			suffix = " (original config_path: " + path + ")"
		}
		return nil, time.Time{}, "", "", fmt.Errorf(
			"cannot resolve config — no usable config_id and session has no inline config_yaml to re-register%s",
			suffix)
	}

	if meta != nil {
		workdir = meta.Workdir
	}
	return tree, created, configID, workdir, nil
}

// Stop terminates a session and removes it from the registry.
func (m *ChatManager) Stop(id string) bool {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	reg := m.sessionRegistry
	m.mu.Unlock()
	if !ok {
		return false
	}
	sess.Close()
	if reg != nil {
		reg.Unregister(id)
	}
	return true
}

// StopAll is called on server shutdown.
func (m *ChatManager) StopAll() {
	m.mu.Lock()
	sessions := make([]*ChatSession, 0, len(m.sessions))
	ids := make([]string, 0, len(m.sessions))
	for id, s := range m.sessions {
		sessions = append(sessions, s)
		ids = append(ids, id)
	}
	m.sessions = map[string]*ChatSession{}
	reg := m.sessionRegistry
	m.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	if reg != nil {
		for _, id := range ids {
			reg.Unregister(id)
		}
	}
}

// applyRedactedKeyOverrides replaces literal "[REDACTED]" API keys in cfg
// with values supplied via envVars, keyed by <UPPER(provider_name)>_API_KEY.
// Provider-name hyphens and dots normalize to underscores so a provider
// called "openai-direct" matches OPENAI_DIRECT_API_KEY. Both the named
// provider map (cfg.Settings.Providers) and the legacy flat map
// (cfg.Settings.APIKeys) are covered. Called on every chat start, not
// just resume, so a Builder upload of a previously-persisted YAML also
// recovers cleanly. No host-env fallback: see [[server-secrets-are-per-request]].
func applyRedactedKeyOverrides(cfg *config.Config, envVars map[string]string) {
	if cfg == nil || len(envVars) == 0 {
		return
	}
	const mask = "[REDACTED]"
	norm := func(name string) string {
		s := strings.ReplaceAll(name, "-", "_")
		s = strings.ReplaceAll(s, ".", "_")
		return strings.ToUpper(s)
	}
	for name, p := range cfg.Settings.Providers {
		if p.APIKey != mask {
			continue
		}
		if val := envVars[norm(name)+"_API_KEY"]; val != "" {
			p.APIKey = val
			cfg.Settings.Providers[name] = p
		}
	}
	for name, v := range cfg.Settings.APIKeys {
		if v != mask {
			continue
		}
		if val := envVars[norm(name)+"_API_KEY"]; val != "" {
			cfg.Settings.APIKeys[name] = val
		}
	}
}

// ============================================================
// HTTP handlers
// ============================================================

// handleChatStart: POST /api/chat/start
func (s *SSEServer) handleChatStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.chatManager == nil {
		http.Error(w, `{"error":"chat manager not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		ConfigID string            `json:"config_id"`
		Workdir  string            `json:"workdir,omitempty"`
		EnvVars  map[string]string `json:"env_vars,omitempty"`
		ResumeID string            `json:"resume_id,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.ConfigID == "" && req.ResumeID == "" {
		http.Error(w, `{"error":"config_id or resume_id is required"}`, http.StatusBadRequest)
		return
	}
	sess, err := s.chatManager.Start(r.Context(), req.ConfigID, req.Workdir, req.EnvVars, req.ResumeID)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(sess.Meta())
}

// handleChatList: GET /api/chat — list active chat sessions.
func (s *SSEServer) handleChatList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.chatManager == nil {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
		return
	}
	json.NewEncoder(w).Encode(s.chatManager.List())
}

// handleDebugUserInput: POST /api/debug/user_input — debugger injects an
// answer for a pending user_input wait. Routes to the target chat session's
// existing response channel. Only works against ChatManager sessions (chat
// mode); one-shot AgentRunner runs don't register the user_input tool.
//
// Request body: { session_id, request_id, text }
func (s *SSEServer) handleDebugUserInput(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.chatManager == nil {
		http.Error(w, `{"error":"chat manager not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		Text      string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, `{"error":"session_id required"}`, http.StatusBadRequest)
		return
	}
	sess := s.chatManager.Get(req.SessionID)
	if sess == nil {
		http.Error(w, `{"error":"session not found or not a chat session"}`, http.StatusNotFound)
		return
	}
	sess.RespondUserInput(req.RequestID, req.Text)
	// Announce the debug-injected answer so any other listeners (other
	// debuggers watching the same session) clear their pending state too.
	if s.eventBus != nil {
		s.eventBus.Publish(telemetry.AgentEvent{
			EventType: telemetry.EventUserInputAnswered,
			SessionID: req.SessionID,
			Payload:   mustJSON(telemetry.UserInputAnsweredPayload{RequestID: req.RequestID, Source: "debug"}),
		})
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// mustJSON marshals a payload, ignoring errors — the types we pass are
// always serializable so failure would indicate a programming error.
func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// handleChatByID routes /api/chat/{id}, /api/chat/{id}/stop.
func (s *SSEServer) handleChatByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.chatManager == nil {
		http.Error(w, `{"error":"chat manager not configured"}`, http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/chat/")
	parts := strings.SplitN(path, "/", 3)
	id := parts[0]
	if id == "" || id == "start" {
		http.Error(w, `{"error":"missing session id"}`, http.StatusBadRequest)
		return
	}

	if len(parts) >= 2 {
		switch parts[1] {
		case "stop":
			if r.Method != http.MethodPost {
				http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
				return
			}
			if !s.chatManager.Stop(id) {
				http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
			return
		case "fork":
			s.handleChatFork(w, r, id)
			return
		case "turn":
			if len(parts) < 3 || parts[2] == "" {
				http.Error(w, `{"error":"missing turn id"}`, http.StatusBadRequest)
				return
			}
			s.handleGetTurn(w, r, id, parts[2])
			return
		case "tree":
			s.handleChatTree(w, r, id)
			return
		}
	}

	sess := s.chatManager.Get(id)
	if sess == nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(sess.Meta())
}

// handleChatTree serves GET /api/chat/{id}/tree. Returns the same
// treeSnapshot shape the WebSocket emits in tree_snapshot frames so the
// frontend can render a session's branching turn history either live (via
// useChatSession's WS) or read-only (via this REST endpoint, used by the
// Sessions tab Inspect viewer's Tree sub-tab). Two paths:
//
//  1. Live in memory  — chatManager.Get(id) returns a session; snapshot it
//     under its mutex via SnapshotTree.
//  2. Past on disk    — sessionStore.LoadChatTree(id) reads the persisted
//     .chat.json blob; LoadChatTreeFile parses + hydrates;
//     snapshotTreeFromTree builds the wire shape.
//
// 404 only when neither memory nor disk knows the id.
func (s *SSEServer) handleChatTree(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if sess := s.chatManager.Get(id); sess != nil {
		json.NewEncoder(w).Encode(sess.SnapshotTree())
		return
	}
	if s.sessionStore == nil {
		http.Error(w, `{"error":"session store not configured"}`, http.StatusServiceUnavailable)
		return
	}
	raw, err := s.sessionStore.LoadChatTree(id)
	if err == nil {
		f, perr := LoadChatTreeFile(raw)
		if perr != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, perr.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(snapshotTreeFromTree(f.Tree))
		return
	}
	// PR D-5.6 fallback: no .chat.json — try rebuilding from JSONL events.
	// Lets the Sessions tab Tree sub-tab + Resume probe succeed for CLI
	// runs and pre-PR-B/3 chat sessions.
	events, evErr := s.sessionStore.GetSessionEvents(id)
	if evErr != nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	rebuilt := reconstructTreeFromEvents(events)
	if rebuilt == nil {
		http.Error(w, `{"error":"session has no chat tree (no .chat.json and no turn events)"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(snapshotTreeFromTree(rebuilt))
}
