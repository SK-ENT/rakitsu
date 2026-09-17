package server

import (
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func newTestEventBus() *telemetry.EventBus { return telemetry.NewEventBus(8) }

// TestOneShotAdapterNilRunnerRefusesAttach: adapters registered outside the
// AgentRunner lifecycle (read-only fixtures) must refuse attach cleanly.
func TestOneShotAdapterNilRunnerRefusesAttach(t *testing.T) {
	a := &oneShotSessionAdapter{
		id:        "run-orphan",
		configID:  "cfg",
		name:      "Orphan",
		startedAt: time.Now(),
		// runner intentionally nil — Phase-1-style fixture
	}
	if a.DebugController() != nil {
		t.Fatal("orphan adapter must report nil DebugController")
	}
	_, err := a.AttachDebugger(nil)
	if err != session.ErrNotAttachable {
		t.Fatalf("expected ErrNotAttachable, got %v", err)
	}
	if err := a.DetachDebugger(); err != nil {
		t.Fatalf("detach on orphan must be no-op, got %v", err)
	}
}

// TestOneShotAdapterStaleSessionIDRefusesAttach: when AgentRunner has moved
// on to a different sessionID, an attach against the old adapter fails.
func TestOneShotAdapterStaleSessionIDRefusesAttach(t *testing.T) {
	// Build a runner manually with a "running" state for a different session
	// than the adapter claims.
	r := &AgentRunner{
		status:    "running",
		sessionID: "web-other",
	}
	stale := &oneShotSessionAdapter{
		id:     "web-first",
		runner: r,
	}
	_, err := stale.AttachDebugger(nil)
	if err != session.ErrNotAttachable {
		t.Fatalf("expected stale adapter to return ErrNotAttachable, got %v", err)
	}
	// Detach on stale adapter must not touch the running session.
	if err := stale.DetachDebugger(); err != nil {
		t.Fatalf("detach should be no-op for stale adapter, got %v", err)
	}
	if r.status != "running" {
		t.Fatalf("stale detach must not affect the running session, status=%q", r.status)
	}
}

// TestOneShotAdapterAttachIdempotent: a second attach against a live adapter
// returns the same controller rather than replacing it.
func TestOneShotAdapterAttachIdempotent(t *testing.T) {
	s := &SSEServer{}
	eb := newTestEventBus()
	r := &AgentRunner{
		status:    "running",
		sessionID: "web-1",
		eventBus:  eb,
		sseServer: s,
	}
	a := &oneShotSessionAdapter{id: "web-1", runner: r}

	dc1, err := a.AttachDebugger([]debug.BreakpointKey{{EventType: "AGENT_START", AgentName: "*"}})
	if err != nil {
		t.Fatalf("attach 1: %v", err)
	}
	dc2, err := a.AttachDebugger(nil)
	if err != nil {
		t.Fatalf("attach 2: %v", err)
	}
	if dc1 != dc2 {
		t.Fatal("second Attach must return the same DebugController (idempotent)")
	}
	if len(dc1.ListBreakpoints()) != 1 {
		t.Fatalf("breakpoints must survive second attach, got %d", len(dc1.ListBreakpoints()))
	}
}

// TestOneShotAdapterDetachMatches: detach on the adapter whose id matches the
// current run clears the controller.
func TestOneShotAdapterDetachMatches(t *testing.T) {
	s := &SSEServer{}
	eb := newTestEventBus()
	r := &AgentRunner{
		status:    "running",
		sessionID: "web-1",
		eventBus:  eb,
		sseServer: s,
	}
	a := &oneShotSessionAdapter{id: "web-1", runner: r}

	if _, err := a.AttachDebugger(nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if r.debugCtrl == nil {
		t.Fatal("runner.debugCtrl should be set after attach")
	}
	if err := a.DetachDebugger(); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if r.debugCtrl != nil {
		t.Fatal("runner.debugCtrl should be nil after detach")
	}
	if a.DebugController() != nil {
		t.Fatal("adapter should report nil controller after detach")
	}
}
