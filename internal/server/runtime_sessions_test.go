package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/session"
)

// newTestSSEWithRegistry wires a minimal SSEServer with a registry — enough
// to exercise /api/runtime/sessions without booting the full server stack.
func newTestSSEWithRegistry() (*SSEServer, *session.Registry) {
	s := &SSEServer{
		activeSessions: map[string]*ActiveSession{},
		pendingCmds:    map[string][]DebugCommand{},
	}
	reg := session.NewRegistry()
	s.SetSessionRegistry(reg)
	return s, reg
}

func TestRuntimeSessionsEmpty(t *testing.T) {
	s, _ := newTestSSEWithRegistry()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions", nil)
	s.handleRuntimeSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []session.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, rec.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(got))
	}
}

func TestRuntimeSessionsListsOneShotAndChat(t *testing.T) {
	s, reg := newTestSSEWithRegistry()
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	reg.Register(&oneShotSessionAdapter{
		id:        "run-1",
		configID:  "cfg-a",
		name:      "Config A",
		startedAt: now,
	})
	reg.Register(&oneShotSessionAdapter{
		id:        "run-2",
		configID:  "cfg-b",
		name:      "Config B",
		startedAt: now.Add(time.Minute),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions", nil)
	s.handleRuntimeSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []session.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].ID != "run-1" || got[1].ID != "run-2" {
		t.Fatalf("expected sorted by StartedAt, got [%s, %s]", got[0].ID, got[1].ID)
	}
	if got[0].Mode != session.ModeOneShot {
		t.Fatalf("expected oneshot mode, got %q", got[0].Mode)
	}
	if got[0].Status != session.StatusRunning {
		t.Fatalf("expected running status, got %q", got[0].Status)
	}
}

func TestRuntimeSessionsFilterByMode(t *testing.T) {
	s, reg := newTestSSEWithRegistry()
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	reg.Register(&oneShotSessionAdapter{
		id: "run-1", configID: "cfg", name: "c", startedAt: now,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions?mode=chat", nil)
	s.handleRuntimeSessions(rec, req)

	var got []session.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("mode=chat filter should exclude oneshot, got %d: %+v", len(got), got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/runtime/sessions?mode=oneshot", nil)
	s.handleRuntimeSessions(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("mode=oneshot filter: expected 1, got %d", len(got))
	}
}

func TestRuntimeSessionsFilterByStatus(t *testing.T) {
	s, reg := newTestSSEWithRegistry()
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	reg.Register(&oneShotSessionAdapter{
		id: "run-1", configID: "cfg", name: "c", startedAt: now,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions?status=stopped", nil)
	s.handleRuntimeSessions(rec, req)
	var got []session.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("status=stopped should exclude running entries, got %d", len(got))
	}
}

func TestRuntimeSessionsRejectsNonGET(t *testing.T) {
	s, _ := newTestSSEWithRegistry()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions", nil)
	s.handleRuntimeSessions(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestRuntimeSessionsNoRegistryReturnsEmpty(t *testing.T) {
	s := &SSEServer{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions", nil)
	s.handleRuntimeSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with no registry, got %d", rec.Code)
	}
	var got []session.SessionMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty array, got %d", len(got))
	}
}

// TestMetaShape locks the JSON field names the frontend depends on. Changing
// this test means you are breaking the picker contract — update useSessionList.ts.
func TestRuntimeSessionsMetaShape(t *testing.T) {
	s, reg := newTestSSEWithRegistry()
	reg.Register(&oneShotSessionAdapter{
		id: "x", configID: "cfg", name: "n",
		startedAt: time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions", nil)
	s.handleRuntimeSessions(rec, req)

	var raw []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(raw))
	}
	required := []string{"id", "config_id", "mode", "status", "started_at"}
	for _, k := range required {
		if _, ok := raw[0][k]; !ok {
			t.Errorf("missing required field %q in response: %+v", k, raw[0])
		}
	}
}
