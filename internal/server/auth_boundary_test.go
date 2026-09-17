package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	neutools "github.com/SK-ENT/rakitsu/internal/tools"
)

// Regression coverage for the token-gate audit findings:
// the token gate must hold on every path of the standalone MCP listener and
// on every persisted-session route, and an unauthenticated request must be
// rejected before any side effect.

func doReq(t *testing.T, h http.Handler, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newSeededSessionServer(t *testing.T) (http.Handler, *store.SessionStore, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.rakitsu/sessions
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := ss.StartSession(store.SessionMeta{Name: "seed", Query: "secret query", ConfigYAML: "name: seed\n"}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	id := ss.CurrentSessionID()
	ss.EndSession(store.SessionSuccess)

	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.SetSessionStore(ss)
	return CorsMiddleware(AuthMiddleware(s.Mux())), ss, id
}

func TestPersistedSessionRoutes_RejectedWithoutToken(t *testing.T) {
	t.Setenv(apiTokenEnv, "s3cret")
	h, ss, id := newSeededSessionServer(t)

	cases := []struct{ method, path, body string }{
		{"GET", "/api/sessions", ""},
		{"GET", "/api/sessions/" + id, ""},
		{"GET", "/api/sessions/" + id + "/events", ""},
		{"GET", "/api/sessions/" + id + "/config", ""},
		{"POST", "/api/sessions/" + id + "/rerun", ""},
		{"DELETE", "/api/sessions/" + id, ""},
		{"DELETE", "/api/sessions", `{"ids":["` + id + `"]}`},
	}
	for _, c := range cases {
		for _, tok := range []string{"", "wrong"} {
			rec := doReq(t, h, c.method, c.path, tok, c.body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s token=%q: status %d, want 401", c.method, c.path, tok, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "secret query") {
				t.Errorf("%s %s token=%q: response leaked session content", c.method, c.path, tok)
			}
		}
	}
	// No side effect from the unauthenticated deletes.
	if _, err := ss.GetSession(id); err != nil {
		t.Fatalf("session was deleted by an unauthenticated request: %v", err)
	}
}

func TestPersistedSessionRoutes_ServedWithToken(t *testing.T) {
	t.Setenv(apiTokenEnv, "s3cret")
	h, ss, id := newSeededSessionServer(t)

	rec := doReq(t, h, "GET", "/api/sessions", "s3cret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions with token: %d %s", rec.Code, rec.Body.String())
	}
	var list []store.SessionMeta
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("GET /api/sessions with token: list = %+v, err = %v", list, err)
	}
	if rec := doReq(t, h, "GET", "/api/sessions/"+id+"/config", "s3cret", ""); rec.Code != http.StatusOK {
		t.Errorf("GET config with token: %d", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/api/sessions/"+id, "s3cret", ""); rec.Code != http.StatusOK {
		t.Errorf("DELETE with token: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := ss.GetSession(id); err == nil {
		t.Error("session still present after an authenticated DELETE")
	}
}

func TestPersistedSessionRoutes_OpenWithoutTokenConfigured(t *testing.T) {
	t.Setenv(apiTokenEnv, "")
	h, _, id := newSeededSessionServer(t)
	if rec := doReq(t, h, "GET", "/api/sessions/"+id, "", ""); rec.Code != http.StatusOK {
		t.Errorf("loopback trust model: GET without any token configured = %d, want 200", rec.Code)
	}
}

// The standalone --mcp-port listener: built exactly the way
// cmd/rakitsu/serve.go builds it, the token must be required on /mcp AND on
// every other path, and nothing but /mcp may reach the MCP handler.
func TestMCPListenerHandler_TokenCannotBeSidesteppedByPath(t *testing.T) {
	t.Setenv(apiTokenEnv, "s3cret")
	reg := neutools.NewToolRegistry()
	reg.RegisterTool(&stubTool{name: "marker", result: "marker-ran"})
	h := MCPListenerHandler(NewMCPServer(reg, "test"))

	init := `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2025-11-25"}}`

	// The mounted path is refused outright; anything else never reaches the
	// MCP handler (404 from the mux) — either
	// way no session may be created and no tool may run.
	for _, p := range []string{"/mcp", "/mcp/x"} {
		if rec := doReq(t, h, "POST", p, "", init); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without token: %d, want 401", p, rec.Code)
		}
	}
	for _, p := range []string{"/", "/anything", "/api/status", "/health"} {
		rec := doReq(t, h, "POST", p, "", init)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s without token: %d, want 404 (nothing but /mcp is mounted)", p, rec.Code)
		}
		if rec.Header().Get("Mcp-Session-Id") != "" || strings.Contains(rec.Body.String(), "protocolVersion") {
			t.Errorf("POST %s without token reached the MCP handler: %s", p, rec.Body.String())
		}
	}

	rec := doReq(t, h, "POST", "/mcp", "s3cret", init)
	if rec.Code != http.StatusOK || rec.Header().Get("Mcp-Session-Id") == "" {
		t.Fatalf("POST /mcp with token: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/", "/anything", "/mcp/x"} {
		rec := doReq(t, h, "POST", p, "s3cret", init)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s with token: %d, want 404 (only /mcp is mounted)", p, rec.Code)
		}
	}
}

func TestMCPListenerHandler_OpenWithoutTokenConfigured(t *testing.T) {
	t.Setenv(apiTokenEnv, "")
	h := MCPListenerHandler(NewMCPServer(neutools.NewToolRegistry(), "test"))
	init := `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	if rec := doReq(t, h, "POST", "/mcp", "", init); rec.Code != http.StatusOK {
		t.Errorf("POST /mcp with no token configured: %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, "POST", "/elsewhere", "", init); rec.Code != http.StatusNotFound {
		t.Errorf("POST /elsewhere with no token configured: %d, want 404", rec.Code)
	}
}
