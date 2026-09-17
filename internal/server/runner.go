package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"gopkg.in/yaml.v3"
)

// RunFunc executes an agent run with the given config and query.
// The function should use the provided EventBus for emitting events
// and the DebugController for breakpoint support.
// The registrar callback (if non-nil) should be called once agents/orchestrators
// are created, to enable mid-run debug attach.
type RunFunc func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, debugCtrl *debug.DebugController, query string, registrar func([]debug.Attachable)) (string, error)

// staleRunGrace is the extra time beyond the configured timeout before a
// run is considered stale and auto-reaped. This covers slow cleanup code
// (session writes, event bus drain) that legitimately runs after the
// context deadline fires. Without it, a panicked goroutine leaves
// status="running" forever, blocking all future runs.
const staleRunGrace = 30 * time.Second

// RunRequest represents a request to start an agent run from the web UI.
// BreakpointEntry is a breakpoint to pre-load before run starts.
type BreakpointEntry struct {
	EventType string `json:"event_type"`
	AgentName string `json:"agent_name"`
}

type RunRequest struct {
	ConfigID    string            `json:"config_id"`
	ProjectID   string            `json:"project_id,omitempty"` // links run to a project (from web UI)
	Query       string            `json:"query"`
	Timeout     int               `json:"timeout"`              // seconds, default 300
	Debug       bool              `json:"debug"`
	Workdir     string            `json:"workdir,omitempty"`    // working directory for tool execution
	EnvVars     map[string]string `json:"env_vars,omitempty"`   // environment variables for this run
	Breakpoints []BreakpointEntry `json:"breakpoints,omitempty"` // pre-load breakpoints before run starts (debug mode)
}

// RunResponse is returned when a run is started.
type RunResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}


// AgentRunner manages agent execution lifecycle for web-launched runs.
// Only one run is active at a time (single-run mutex).
type AgentRunner struct {
	mu           sync.Mutex
	eventBus     *telemetry.EventBus
	sessionStore *store.SessionStore
	configStore  *ConfigStore
	sseServer    *SSEServer
	runFunc      RunFunc

	cancel     context.CancelFunc
	sessionID  string
	status     string // idle, running, completed, error, cancelled
	result     string
	runError   string
	startTime  time.Time
	runTimeout time.Duration // configured timeout for the current run (for staleness detection)

	// liveAttachables holds agents/orchestrators from the current run for mid-run debug attach.
	// Written by RegisterAttachables (called from runFunc), read by AttachDebugger.
	liveAttachables []debug.Attachable
	debugCtrl       *debug.DebugController

	// sessionRegistry, when non-nil, receives a read-only adapter for the
	// currently running one-shot. Phase 1: pure observation, no behavior change.
	sessionRegistry *session.Registry
	currentAdapter  *oneShotSessionAdapter
}

// NewAgentRunner creates a runner that executes configs using the provided RunFunc.
func NewAgentRunner(
	eventBus *telemetry.EventBus,
	sessionStore *store.SessionStore,
	configStore *ConfigStore,
	sseServer *SSEServer,
	runFunc RunFunc,
) *AgentRunner {
	return &AgentRunner{
		eventBus:     eventBus,
		sessionStore: sessionStore,
		configStore:  configStore,
		sseServer:    sseServer,
		runFunc:      runFunc,
		status:       "idle",
	}
}

// SetSessionRegistry enables Phase-1 runtime session observability. When set,
// the runner registers an adapter for each run (nil-safe; no-op if nil).
func (r *AgentRunner) SetSessionRegistry(reg *session.Registry) {
	r.mu.Lock()
	r.sessionRegistry = reg
	r.mu.Unlock()
}

// reapStaleRun checks if the current "running" state is stale (i.e. the run
// goroutine panicked or the timeout+grace elapsed). If stale, it force-resets
// the runner to "idle" so new runs aren't permanently blocked. Must be called
// with r.mu held. Returns true if a stale run was reaped.
func (r *AgentRunner) reapStaleRun() bool {
	if r.status != "running" {
		return false
	}
	deadline := r.startTime.Add(r.runTimeout + staleRunGrace)
	if time.Now().Before(deadline) {
		return false // still within expected lifetime
	}
	// Stale — force cleanup
	if r.cancel != nil {
		r.cancel()
	}
	r.status = "idle"
	r.cancel = nil
	r.liveAttachables = nil
	if r.sseServer != nil {
		r.sseServer.SetDebugController(nil)
	}
	if r.sessionRegistry != nil && r.currentAdapter != nil {
		r.sessionRegistry.Unregister(r.currentAdapter.id)
		r.currentAdapter = nil
	}
	return true
}

// Start begins an agent run. Returns error if already running.
func (r *AgentRunner) Start(req RunRequest) (*RunResponse, error) {
	r.mu.Lock()
	if r.status == "running" {
		r.reapStaleRun() // auto-clear stale runs
	}
	if r.status == "running" {
		sid := r.sessionID
		r.mu.Unlock()
		return &RunResponse{SessionID: sid, Status: "error", Error: "a run is already in progress"}, nil
	}

	// Load config with run-scoped env overrides (never touches process env)
	cfg, configPath, err := r.configStore.GetWithEnv(req.ConfigID, req.EnvVars)
	if err != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("config not found: %w", err)
	}

	applyWorkdirToTools(cfg, req.Workdir)

	// Set up timeout
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)

	// Create debug controller if requested
	var debugCtrl *debug.DebugController
	if req.Debug {
		debugCtrl = debug.NewDebugController(r.eventBus)
		// Pre-load breakpoints before run starts (no race condition)
		for _, bp := range req.Breakpoints {
			debugCtrl.SetBreakpoint(debug.BreakpointKey{EventType: bp.EventType, AgentName: bp.AgentName})
		}
		r.sseServer.SetDebugController(debugCtrl)
	}

	// Generate session ID
	sessionID := fmt.Sprintf("web-%s", time.Now().Format("20060102-150405"))

	// Resolve project ID: prefer request (from web UI), fall back to config (auto-generated if missing)
	projectID := req.ProjectID
	if projectID == "" {
		projectID = cfg.ProjectID
	}

	// Update state
	r.cancel = cancel
	r.sessionID = sessionID
	r.status = "running"
	r.startTime = time.Now()
	r.runTimeout = time.Duration(timeout) * time.Second
	r.result = ""
	r.runError = ""
	r.debugCtrl = debugCtrl

	// Phase 1: register read-only session adapter for the debugger session picker.
	if r.sessionRegistry != nil {
		r.currentAdapter = &oneShotSessionAdapter{
			id:        sessionID,
			configID:  req.ConfigID,
			name:      cfg.Name,
			startedAt: r.startTime,
			runner:    r,
		}
		r.sessionRegistry.Register(r.currentAdapter)
	}

	// Set session info on SSE server
	agentNames := make([]string, len(cfg.Agents))
	for i, a := range cfg.Agents {
		agentNames[i] = a.Name
	}
	r.sseServer.SetSessionInfo(SessionInfo{
		Name:       cfg.Name,
		ProjectID:  projectID,
		Query:      req.Query,
		ConfigPath: configPath,
		Agents:     agentNames,
		StartTime:  time.Now().Format(time.RFC3339),
	})

	// Start session recording
	if r.sessionStore != nil {
		configYAML := ""
		if yamlBytes, err := yaml.Marshal(config.Redacted(cfg)); err == nil {
			configYAML = string(yamlBytes)
		}
		_ = r.sessionStore.StartSession(store.SessionMeta{
			ProjectID:  projectID,
			Name:       cfg.Name,
			Query:      req.Query,
			ConfigPath: configPath,
			ConfigYAML: configYAML,
			Workdir:    req.Workdir,
			Agents:     agentNames,
		})
	}

	r.mu.Unlock()

	// Capture in closure so a subsequent run replacing r.currentAdapter
	// doesn't cause this goroutine to unregister the wrong adapter.
	myAdapterID := sessionID

	// Run in goroutine
	go func() {
		// recover from panics in runFunc so status doesn't stay "running" forever.
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				r.status = "error"
				r.runError = fmt.Sprintf("agent panic: %v", p)
				r.mu.Unlock()
				cancel()
			}
			// Phase 1: unregister the session adapter on goroutine exit. Safety
			// net — Stop() already unregisters synchronously, this covers the
			// normal-completion and panic paths. Idempotent if already gone.
			if r.sessionRegistry != nil {
				r.sessionRegistry.Unregister(myAdapterID)
			}
			r.mu.Lock()
			if r.currentAdapter != nil && r.currentAdapter.id == myAdapterID {
				r.currentAdapter = nil
			}
			r.mu.Unlock()
		}()

		var wg sync.WaitGroup
		var eventCh <-chan telemetry.AgentEvent

		// Subscribe for session recording
		if r.sessionStore != nil {
			eventCh = r.eventBus.Subscribe()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ev := range eventCh {
					r.sessionStore.WriteEvent(ev)
				}
			}()
		}

		// Execute
		result, runErr := r.runFunc(ctx, cfg, r.eventBus, debugCtrl, req.Query, r.RegisterAttachables)

		// Emit error/completion event BEFORE unsubscribing so session recording captures it
		if runErr != nil {
			r.eventBus.Emit("system", telemetry.EventError, telemetry.ErrorPayload{
				Message: runErr.Error(),
			})
		}

		// Stop recording
		if eventCh != nil {
			r.eventBus.Unsubscribe(eventCh)
			wg.Wait()
		}

		// Update state
		r.mu.Lock()
		if ctx.Err() == context.Canceled && runErr != nil {
			r.status = "cancelled"
		} else if runErr != nil {
			r.status = "error"
			r.runError = runErr.Error()
		} else {
			r.status = "completed"
			r.result = result
		}
		r.mu.Unlock()

		// End session
		if r.sessionStore != nil {
			sessionStatus := store.SessionSuccess
			if r.status == "error" || r.status == "cancelled" {
				sessionStatus = store.SessionError
			}
			r.sessionStore.EndSession(sessionStatus)
		}

		cancel()
	}()

	return &RunResponse{SessionID: sessionID, Status: "running"}, nil
}

// StartFromPath starts a run using a file-system config path (used by session rerun).
// Options: enableDebug[0] = debug mode, enableDebug[1] is unused; workdir is separate.
func (r *AgentRunner) StartFromPath(_ context.Context, configPath, query string, timeout int, workdir string, enableDebug ...bool) (string, error) {
	r.mu.Lock()
	if r.status == "running" {
		r.reapStaleRun() // auto-clear stale runs
	}
	if r.status == "running" {
		sid := r.sessionID
		r.mu.Unlock()
		return sid, fmt.Errorf("a run is already in progress")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		r.mu.Unlock()
		return "", fmt.Errorf("load config: %w", err)
	}

	applyWorkdirToTools(cfg, workdir)

	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)

	// Create debug controller if requested
	wantDebug := len(enableDebug) > 0 && enableDebug[0]
	var debugCtrl *debug.DebugController
	if wantDebug {
		debugCtrl = debug.NewDebugController(r.eventBus)
		r.sseServer.SetDebugController(debugCtrl)
	}

	sessionID := fmt.Sprintf("web-%s", time.Now().Format("20060102-150405"))
	agentNames := make([]string, len(cfg.Agents))
	for i, a := range cfg.Agents {
		agentNames[i] = a.Name
	}

	r.cancel = cancel
	r.sessionID = sessionID
	r.status = "running"
	r.startTime = time.Now()
	r.runTimeout = time.Duration(timeout) * time.Second
	r.result = ""
	r.runError = ""
	r.debugCtrl = debugCtrl

	// Phase 1: register read-only session adapter (same as Start).
	if r.sessionRegistry != nil {
		r.currentAdapter = &oneShotSessionAdapter{
			id:        sessionID,
			configID:  configPath,
			name:      cfg.Name,
			startedAt: r.startTime,
			runner:    r,
		}
		r.sessionRegistry.Register(r.currentAdapter)
	}

	r.sseServer.SetSessionInfo(SessionInfo{
		Name:       cfg.Name,
		ProjectID:  cfg.ProjectID,
		Query:      query,
		ConfigPath: configPath,
		Agents:     agentNames,
		StartTime:  time.Now().Format(time.RFC3339),
	})

	if r.sessionStore != nil {
		configYAML := ""
		if yamlBytes, err := yaml.Marshal(config.Redacted(cfg)); err == nil {
			configYAML = string(yamlBytes)
		}
		_ = r.sessionStore.StartSession(store.SessionMeta{
			ProjectID:  cfg.ProjectID,
			Name:       cfg.Name,
			Query:      query,
			ConfigPath: configPath,
			ConfigYAML: configYAML,
			Workdir:    workdir,
			Agents:     agentNames,
		})
	}

	r.mu.Unlock()

	// Capture in closure — see Start() for rationale.
	myAdapterID := sessionID

	go func() {
		// recover from panics so status doesn't stay "running" forever.
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				r.status = "error"
				r.runError = fmt.Sprintf("agent panic: %v", p)
				r.mu.Unlock()
				cancel()
			}
			// Phase 1: safety-net unregister on goroutine exit. Idempotent.
			if r.sessionRegistry != nil {
				r.sessionRegistry.Unregister(myAdapterID)
			}
			r.mu.Lock()
			if r.currentAdapter != nil && r.currentAdapter.id == myAdapterID {
				r.currentAdapter = nil
			}
			r.mu.Unlock()
		}()

		var wg sync.WaitGroup
		var eventCh <-chan telemetry.AgentEvent

		if r.sessionStore != nil {
			eventCh = r.eventBus.Subscribe()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ev := range eventCh {
					r.sessionStore.WriteEvent(ev)
				}
			}()
		}

		result, runErr := r.runFunc(ctx, cfg, r.eventBus, debugCtrl, query, r.RegisterAttachables)

		// Emit error event BEFORE unsubscribing so session recording captures it
		if runErr != nil {
			r.eventBus.Emit("system", telemetry.EventError, telemetry.ErrorPayload{
				Message: runErr.Error(),
			})
		}

		if eventCh != nil {
			r.eventBus.Unsubscribe(eventCh)
			wg.Wait()
		}

		r.mu.Lock()
		if ctx.Err() == context.Canceled && runErr != nil {
			r.status = "cancelled"
		} else if runErr != nil {
			r.status = "error"
			r.runError = runErr.Error()
		} else {
			r.status = "completed"
			r.result = result
		}
		r.mu.Unlock()

		if r.sessionStore != nil {
			sessionStatus := store.SessionSuccess
			if r.status == "error" || r.status == "cancelled" {
				sessionStatus = store.SessionError
			}
			r.sessionStore.EndSession(sessionStatus)
		}

		cancel()
	}()

	return sessionID, nil
}

// Stop cancels the current run.
func (r *AgentRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.debugCtrl != nil {
		r.debugCtrl.Resume(debug.ActionResume) // unblock any paused checkpoint
		r.debugCtrl = nil
	}
	if r.cancel != nil && r.status == "running" {
		r.cancel()
	}
	r.status = "idle"
	r.cancel = nil
	r.liveAttachables = nil
	// Clear debug controller so stale state doesn't affect next run
	if r.sseServer != nil {
		r.sseServer.SetDebugController(nil)
	}
	// Phase 1: unregister synchronously. The goroutine's deferred unregister
	// only fires when the goroutine exits, but a stuck LLM call that ignores
	// ctx cancellation can keep the goroutine alive indefinitely. Stop() is
	// the user's escape hatch — it must reflect in the picker immediately.
	if r.sessionRegistry != nil && r.currentAdapter != nil {
		r.sessionRegistry.Unregister(r.currentAdapter.id)
		r.currentAdapter = nil
	}
}

// RegisterAttachables stores agents/orchestrators from the current run so that
// a debug controller can be attached mid-run via AttachDebugger.
// Called from within runFunc (executeConfig) once agents are created.
func (r *AgentRunner) RegisterAttachables(attachables []debug.Attachable) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveAttachables = attachables
}

// CurrentDebugController returns the controller attached to the live run, or
// nil. Used by the Phase-2 oneShotSessionAdapter when the server-wide
// `debugCtrl` is no longer the source of truth.
func (r *AgentRunner) CurrentDebugController() *debug.DebugController {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.debugCtrl
}

// AttachDebuggerForSession attaches a debugger to the live run only if the
// current sessionID matches `id`. Guards against a stale adapter targeting a
// subsequent run. Returns session.ErrNotAttachable when the run has moved on
// or completed.
func (r *AgentRunner) AttachDebuggerForSession(id string, breakpoints []BreakpointEntry) (*debug.DebugController, error) {
	r.mu.Lock()
	if r.status != "running" || r.sessionID != id {
		r.mu.Unlock()
		return nil, session.ErrNotAttachable
	}
	if r.debugCtrl != nil {
		dc := r.debugCtrl
		r.mu.Unlock()
		return dc, nil
	}
	dc := debug.NewDebugController(r.eventBus)
	for _, bp := range breakpoints {
		dc.SetBreakpoint(debug.BreakpointKey{EventType: bp.EventType, AgentName: bp.AgentName})
	}
	r.debugCtrl = dc
	attachables := r.liveAttachables
	sse := r.sseServer
	r.mu.Unlock()

	if sse != nil {
		sse.SetDebugController(dc)
	}
	for _, a := range attachables {
		a.SetDebugController(dc)
	}
	return dc, nil
}

// DetachDebuggerForSession removes the debugger if the current run matches
// `id`. Idempotent: returns nil when the adapter is stale or no debugger is
// attached — the user's intent (no debugger on this session) is already met.
func (r *AgentRunner) DetachDebuggerForSession(id string) error {
	r.mu.Lock()
	if r.sessionID != id {
		r.mu.Unlock()
		return nil
	}
	dc := r.debugCtrl
	r.debugCtrl = nil
	attachables := r.liveAttachables
	sse := r.sseServer
	r.mu.Unlock()

	if dc != nil {
		dc.Resume(debug.ActionResume)
	}
	if sse != nil {
		sse.SetDebugController(nil)
	}
	for _, a := range attachables {
		a.SetDebugController(nil)
	}
	return nil
}

// AttachDebugger creates a debug controller and attaches it to all live agents
// and orchestrators. Returns the new controller, or error if no run is active.
func (r *AgentRunner) AttachDebugger(breakpoints []BreakpointEntry) (*debug.DebugController, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != "running" {
		return nil, fmt.Errorf("no active run to attach debugger to")
	}
	if r.debugCtrl != nil {
		// Already attached — just return existing controller
		return r.debugCtrl, nil
	}
	dc := debug.NewDebugController(r.eventBus)
	for _, bp := range breakpoints {
		dc.SetBreakpoint(debug.BreakpointKey{EventType: bp.EventType, AgentName: bp.AgentName})
	}
	r.debugCtrl = dc
	r.sseServer.SetDebugController(dc)
	// Attach to all live agents/orchestrators
	for _, a := range r.liveAttachables {
		a.SetDebugController(dc)
	}
	return dc, nil
}

// DetachDebugger removes the debug controller from the current run.
func (r *AgentRunner) DetachDebugger() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.debugCtrl != nil {
		// Resume if paused
		r.debugCtrl.Resume(debug.ActionResume)
		r.debugCtrl = nil
	}
	r.sseServer.SetDebugController(nil)
	for _, a := range r.liveAttachables {
		a.SetDebugController(nil)
	}
}

// Status returns the current run state.
func (r *AgentRunner) Status() map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := map[string]interface{}{
		"status": r.status,
	}
	if r.sessionID != "" {
		result["session_id"] = r.sessionID
	}
	if r.status == "running" {
		result["elapsed_ms"] = time.Since(r.startTime).Milliseconds()
	}
	if r.result != "" {
		result["result"] = r.result
	}
	if r.runError != "" {
		result["error"] = r.runError
	}
	return result
}
