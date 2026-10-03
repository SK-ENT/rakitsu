package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// mockMonitorLister returns a fixed set of monitor states for testing.
type mockMonitorLister struct {
	states []MonitorState
}

func (m *mockMonitorLister) MonitorStates() []MonitorState {
	return m.states
}

func TestHealthz_NoMonitors(t *testing.T) {
	s := &SSEServer{}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}

	var resp healthzResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "ok" {
		t.Errorf("status = %q; want ok", resp.Status)
	}
	if len(resp.Monitors) != 0 {
		t.Errorf("monitors len = %d; want 0", len(resp.Monitors))
	}
}

func TestHealthz_AllOK(t *testing.T) {
	s := &SSEServer{
		monitorLister: &mockMonitorLister{
			states: []MonitorState{
				{ID: "web-prod", SessionID: "monitor-web-prod", State: WakeStateOK},
				{ID: "db", SessionID: "monitor-db", State: WakeStateOK},
			},
		},
		healthzConfig: config.HealthzConfig{},
	}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}

	var resp healthzResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "ok" {
		t.Errorf("status = %q; want ok", resp.Status)
	}
	if resp.Monitors["web-prod"] != WakeStateOK {
		t.Errorf("monitors[web-prod] = %q; want ok", resp.Monitors["web-prod"])
	}
	if resp.Monitors["db"] != WakeStateOK {
		t.Errorf("monitors[db] = %q; want ok", resp.Monitors["db"])
	}
}

func TestHealthz_OneFails(t *testing.T) {
	s := &SSEServer{
		monitorLister: &mockMonitorLister{
			states: []MonitorState{
				{ID: "web-prod", SessionID: "monitor-web-prod", State: WakeStateOK},
				{ID: "db", SessionID: "monitor-db", State: WakeStateStale, Err: "heartbeat too old"},
			},
		},
		healthzConfig: config.HealthzConfig{},
	}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", w.Code)
	}

	var resp healthzResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "fail" {
		t.Errorf("status = %q; want fail", resp.Status)
	}
	if resp.Monitors["db"] != WakeStateStale {
		t.Errorf("monitors[db] = %q; want stale", resp.Monitors["db"])
	}
}

func TestHealthz_StoppedMonitor(t *testing.T) {
	for _, failOnStopped := range []bool{true, false} {
		name := "false"
		if failOnStopped {
			name = "true"
		}
		t.Run("failOnStopped="+name, func(t *testing.T) {
			cfg := config.HealthzConfig{FailOnStopped: &failOnStopped}
			s := &SSEServer{
				monitorLister: &mockMonitorLister{
					states: []MonitorState{
						{ID: "web-prod", SessionID: "monitor-web-prod", State: WakeStateStopped},
					},
				},
				healthzConfig: cfg,
			}
			handler := http.HandlerFunc(s.handleHealthz)

			req := httptest.NewRequest("GET", "/healthz", nil)
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			expectedCode := http.StatusOK
			expectedStatus := "ok"
			if failOnStopped {
				expectedCode = http.StatusServiceUnavailable
				expectedStatus = "fail"
			}

			if w.Code != expectedCode {
				t.Errorf("status code = %d; want %d", w.Code, expectedCode)
			}

			var resp healthzResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}

			if resp.Status != expectedStatus {
				t.Errorf("status = %q; want %q", resp.Status, expectedStatus)
			}
		})
	}
}

func TestHealthz_NoSecretsInResponse(t *testing.T) {
	s := &SSEServer{
		monitorLister: &mockMonitorLister{
			states: []MonitorState{
				{ID: "web-prod", SessionID: "monitor-web-prod", State: WakeStateFailed, Err: "POSTGRES_PASSWORD=secret123 not accessible"},
			},
		},
		healthzConfig: config.HealthzConfig{},
	}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body, _ := io.ReadAll(w.Body)
	bodyStr := string(body)

	// The Err field should NOT appear in the JSON response.
	if !json.Valid(body) {
		t.Fatalf("response is not valid JSON: %s", bodyStr)
	}

	var resp healthzResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}

	// Verify response doesn't contain secret value
	if bytes.Contains(body, []byte("secret123")) {
		t.Error("response contains secret value")
	}

	if bytes.Contains(body, []byte("POSTGRES_PASSWORD")) {
		t.Error("response contains secret name")
	}
}

func TestHealthz_MethodNotAllowed(t *testing.T) {
	s := &SSEServer{}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("POST", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

func TestHealthz_StripMonitorPrefixFromLabel(t *testing.T) {
	s := &SSEServer{
		monitorLister: &mockMonitorLister{
			states: []MonitorState{
				{ID: "web-prod", SessionID: "monitor-web-prod", State: WakeStateOK},
				{ID: "db-backup", SessionID: "monitor-db-backup", State: WakeStateOK},
			},
		},
		healthzConfig: config.HealthzConfig{},
	}
	handler := http.HandlerFunc(s.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp healthzResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// Labels should not include "monitor-" prefix.
	if _, ok := resp.Monitors["web-prod"]; !ok {
		t.Error("monitors missing web-prod label")
	}
	if _, ok := resp.Monitors["db-backup"]; !ok {
		t.Error("monitors missing db-backup label")
	}
}
