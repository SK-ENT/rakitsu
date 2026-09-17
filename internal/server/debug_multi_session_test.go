package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// fakeMultiSession implements session.Session with its own DebugController.
// Used to test the multi-session routing layer without booting a ChatSession
// or an AgentRunner.
type fakeMultiSession struct {
	id       string
	configID string
	mode     session.SessionMode
	status   session.SessionStatus
	started  time.Time
	eventBus *telemetry.EventBus

	mu sync.Mutex
	dc *debug.DebugController
	// attached records whether SetDebugController was called with non-nil —
	// proxy for "wired into the runner" in adapter tests.
	attached bool
}

func newFakeMultiSession(id string, mode session.SessionMode) *fakeMultiSession {
	return &fakeMultiSession{
		id:       id,
		configID: "cfg-" + id,
		mode:     mode,
		status:   session.StatusRunning,
		started:  time.Now(),
		eventBus: telemetry.NewEventBus(8),
	}
}

func (f *fakeMultiSession) ID() string                   { return f.id }
func (f *fakeMultiSession) ConfigID() string             { return f.configID }
func (f *fakeMultiSession) Mode() session.SessionMode    { return f.mode }
func (f *fakeMultiSession) Status() session.SessionStatus { return f.status }
func (f *fakeMultiSession) StartedAt() time.Time         { return f.started }
func (f *fakeMultiSession) Meta() session.SessionMeta {
	return session.SessionMeta{
		ID:        f.id,
		ConfigID:  f.configID,
		Mode:      f.mode,
		Status:    f.status,
		StartedAt: f.started.UTC().Format(time.RFC3339Nano),
	}
}

func (f *fakeMultiSession) DebugController() *debug.DebugController {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dc
}

func (f *fakeMultiSession) AttachDebugger(keys []debug.BreakpointKey) (*debug.DebugController, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != session.StatusRunning {
		return nil, session.ErrNotAttachable
	}
	if f.dc != nil {
		return f.dc, nil
	}
	dc := debug.NewDebugController(f.eventBus)
	for _, k := range keys {
		dc.SetBreakpoint(k)
	}
	f.dc = dc
	f.attached = true
	return dc, nil
}

func (f *fakeMultiSession) DetachDebugger() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dc == nil {
		return nil
	}
	f.dc = nil
	f.attached = false
	return nil
}

func newMultiDebugServer(t *testing.T) (*SSEServer, *session.Registry) {
	t.Helper()
	s := &SSEServer{
		eventBus:       telemetry.NewEventBus(8),
		activeSessions: map[string]*ActiveSession{},
		pendingCmds:    map[string][]DebugCommand{},
	}
	reg := session.NewRegistry()
	s.SetSessionRegistry(reg)
	return s, reg
}

func postJSON(t *testing.T, h func(http.ResponseWriter, *http.Request), path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestAttachRoutesToSession: /api/debug/attach with session_id creates a
// per-session DebugController and leaves other sessions untouched.
func TestAttachRoutesToSession(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	b := newFakeMultiSession("B", session.ModeChat)
	reg.Register(a)
	reg.Register(b)

	rec := postJSON(t, s.handleDebugAttach, "/api/debug/attach", map[string]interface{}{
		"session_id":  "A",
		"breakpoints": []map[string]string{{"event_type": "AGENT_START", "agent_name": "*"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("attach A: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if a.DebugController() == nil {
		t.Fatal("session A should have a DebugController after attach")
	}
	if b.DebugController() != nil {
		t.Fatal("session B must not be affected by attach to A")
	}
	if len(a.DebugController().ListBreakpoints()) != 1 {
		t.Fatalf("breakpoint not applied to A: %+v", a.DebugController().ListBreakpoints())
	}
}

// TestAttachUnknownSession returns 404 when session_id is not registered.
func TestAttachUnknownSession(t *testing.T) {
	s, _ := newMultiDebugServer(t)
	rec := postJSON(t, s.handleDebugAttach, "/api/debug/attach", map[string]interface{}{
		"session_id": "nonexistent",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAttachAmbiguousWithoutSessionID returns 400 when no session_id is
// provided and the fallback can't unambiguously pick one.
func TestAttachAmbiguousWithoutSessionID(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	// Two chats, no oneshot → ambiguous.
	reg.Register(newFakeMultiSession("A", session.ModeChat))
	reg.Register(newFakeMultiSession("B", session.ModeChat))

	rec := postJSON(t, s.handleDebugAttach, "/api/debug/attach", map[string]interface{}{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 ambiguous, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAttachFallbackToSoleOneshot: when session_id is empty and a single
// oneshot exists, routes to it (back-compat).
func TestAttachFallbackToSoleOneshot(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	run := newFakeMultiSession("run-1", session.ModeOneShot)
	chat := newFakeMultiSession("chat-1", session.ModeChat)
	reg.Register(run)
	reg.Register(chat)

	rec := postJSON(t, s.handleDebugAttach, "/api/debug/attach", map[string]interface{}{})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 fallback-to-oneshot, got %d: %s", rec.Code, rec.Body.String())
	}
	if run.DebugController() == nil {
		t.Fatal("oneshot session should have been attached by fallback")
	}
	if chat.DebugController() != nil {
		t.Fatal("chat session must not be touched by fallback")
	}
}

// TestCrossSessionBreakpointIsolation: setting a breakpoint on A must not
// appear on B's controller.
func TestCrossSessionBreakpointIsolation(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	b := newFakeMultiSession("B", session.ModeChat)
	reg.Register(a)
	reg.Register(b)

	// Attach both
	if _, err := a.AttachDebugger(nil); err != nil {
		t.Fatalf("attach A: %v", err)
	}
	if _, err := b.AttachDebugger(nil); err != nil {
		t.Fatalf("attach B: %v", err)
	}

	// Breakpoint against A
	rec := postJSON(t, s.handleDebugBreakpoints, "/api/debug/breakpoints", map[string]interface{}{
		"session_id": "A",
		"action":     "set",
		"event_type": "AGENT_START",
		"agent_name": "worker",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set bp on A: %d: %s", rec.Code, rec.Body.String())
	}

	if len(a.dc.ListBreakpoints()) != 1 {
		t.Fatalf("A should have 1 bp, got %d", len(a.dc.ListBreakpoints()))
	}
	if len(b.dc.ListBreakpoints()) != 0 {
		t.Fatalf("B must have 0 bps, got %d", len(b.dc.ListBreakpoints()))
	}
}

// TestDetachRoutesToSession: /api/debug/detach with session_id leaves other
// sessions' controllers intact.
func TestDetachRoutesToSession(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	b := newFakeMultiSession("B", session.ModeChat)
	reg.Register(a)
	reg.Register(b)
	_, _ = a.AttachDebugger(nil)
	_, _ = b.AttachDebugger(nil)

	rec := postJSON(t, s.handleDebugDetach, "/api/debug/detach", map[string]interface{}{
		"session_id": "A",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("detach A: %d: %s", rec.Code, rec.Body.String())
	}
	if a.DebugController() != nil {
		t.Fatal("A should have no controller after detach")
	}
	if b.DebugController() == nil {
		t.Fatal("B must still be attached after detaching A")
	}
}

// TestResumeRoutesToSession: Resume hits the targeted session's controller.
// Verified indirectly by showing the other session's state is unaffected.
func TestResumeRoutesToSession(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	b := newFakeMultiSession("B", session.ModeChat)
	reg.Register(a)
	reg.Register(b)
	_, _ = a.AttachDebugger(nil)
	_, _ = b.AttachDebugger(nil)

	// Pause A via RequestPause so a subsequent Resume transitions state.
	a.dc.RequestPause()

	rec := postJSON(t, s.handleDebugResume, "/api/debug/resume", map[string]interface{}{
		"session_id": "A",
		"action":     "resume",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("resume A: %d: %s", rec.Code, rec.Body.String())
	}
	// B should never have paused; nothing about its state changes.
	if b.dc.GetState() != debug.StateRunning {
		t.Fatalf("B state should remain Running, got %q", b.dc.GetState())
	}
}

// TestStateEndpointWithSessionID: GET /api/debug/state?session_id=X returns
// the right controller's state.
func TestStateEndpointWithSessionID(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	reg.Register(a)
	_, _ = a.AttachDebugger(nil)
	a.dc.SetBreakpoint(debug.BreakpointKey{EventType: "AGENT_START", AgentName: "*"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/debug/state?session_id=A", nil)
	s.handleDebugState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state: %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	bps, _ := resp["breakpoints"].([]interface{})
	if len(bps) != 1 {
		t.Fatalf("expected 1 bp in state, got %+v", resp)
	}
}

// TestResolveDebugSessionPrefersOneshot: with one oneshot + one chat, empty
// id resolves to the oneshot.
func TestResolveDebugSessionPrefersOneshot(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	chat := newFakeMultiSession("chat-1", session.ModeChat)
	run := newFakeMultiSession("run-1", session.ModeOneShot)
	reg.Register(chat)
	reg.Register(run)

	sess, err := s.resolveDebugSession("")
	if err != nil {
		t.Fatalf("resolve empty: %v", err)
	}
	if sess.ID() != "run-1" {
		t.Fatalf("expected run-1 preferred, got %s", sess.ID())
	}
}

// TestResolveDebugSessionAmbiguousOneshots: two oneshots + no explicit id →
// ambiguous. (This is a forward-looking check; Phase 4 relaxes the single-run
// mutex but the resolver must still refuse to pick silently.)
func TestResolveDebugSessionAmbiguousOneshots(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	reg.Register(newFakeMultiSession("run-1", session.ModeOneShot))
	reg.Register(newFakeMultiSession("run-2", session.ModeOneShot))

	_, err := s.resolveDebugSession("")
	if err == nil || err != errAmbiguousSession {
		t.Fatalf("expected errAmbiguousSession, got %v", err)
	}
}

// TestAttachNonAttachable: an adapter that reports itself not-attachable
// returns 409 Conflict (mapped from session.ErrNotAttachable).
func TestAttachNonAttachable(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	a.status = session.StatusStopped
	reg.Register(a)

	rec := postJSON(t, s.handleDebugAttach, "/api/debug/attach", map[string]interface{}{
		"session_id": "A",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 not-attachable, got %d: %s", rec.Code, rec.Body.String())
	}
}

// smoke test that the resolver is exercised by every debug handler —
// regression guard in case someone adds a new handler and forgets to wire
// resolveDebugCtrl.
func TestAllHandlersAcceptSessionID(t *testing.T) {
	s, reg := newMultiDebugServer(t)
	a := newFakeMultiSession("A", session.ModeChat)
	reg.Register(a)
	_, _ = a.AttachDebugger(nil)

	cases := []struct {
		name string
		req  *http.Request
		h    func(http.ResponseWriter, *http.Request)
	}{
		{"state", mustRequest(http.MethodGet, "/api/debug/state?session_id=A", nil), s.handleDebugState},
		{"pause", mustRequest(http.MethodPost, "/api/debug/pause?session_id=A", nil), s.handleDebugPause},
		{"breakpoints-get", mustRequest(http.MethodGet, "/api/debug/breakpoints?session_id=A", nil), s.handleDebugBreakpoints},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.h(rec, c.req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d: %s", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func mustRequest(method, path string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ensure interface compliance at compile time for the fake.
var _ session.Session = (*fakeMultiSession)(nil)

// keep helper import alive for future expansion.
var _ = fmt.Sprintf
