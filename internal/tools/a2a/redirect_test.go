package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// Red-team row: outbound HTTP redirects. A remote peer (or anything
// on the path) can answer the A2A POST with a 3xx. These tests pin what the
// client does with that: whether the bearer token travels along, and whether
// a redirect to loopback is followed.

const okRPCBody = `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`

func newRedirectTool(t *testing.T, endpoint string) *A2ATool {
	t.Helper()
	tool, err := NewA2ATool(&config.ToolDefinition{
		Name: "peer", URL: endpoint, AgentName: "Agent", APIKey: "redteam-fake-peer-credential", Timeout: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func redirectTarget(t *testing.T, gotAuth *atomic.Value, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okRPCBody)) //nolint:errcheck
	}))
}

func TestA2ATool_Redirect_CrossHostDoesNotLeakBearerToken(t *testing.T) {
	var gotAuth atomic.Value
	var hits atomic.Int32
	target := redirectTarget(t, &gotAuth, &hits)
	defer target.Close()
	// Same loopback listener, different hostname: net/http treats it as a
	// different host and must drop Authorization on the redirected request.
	crossHostURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/a2a"

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, crossHostURL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	var out map[string]interface{}
	if err := newRedirectTool(t, redirector.URL).call(context.Background(), "GetTask", map[string]string{"id": "x"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1 (redirect followed)", hits.Load())
	}
	if v, _ := gotAuth.Load().(string); v != "" {
		t.Fatalf("bearer token leaked to a different host on redirect: %q", v)
	}
}

// Pinned current behavior, not an endorsement: a redirect to another service
// on the SAME hostname (e.g. another loopback port) is followed and the token
// goes with it. The endpoint URL is operator-configured.
func TestA2ATool_Redirect_SameHostIsFollowedWithToken(t *testing.T) {
	var gotAuth atomic.Value
	var hits atomic.Int32
	target := redirectTarget(t, &gotAuth, &hits)
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/a2a", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	var out map[string]interface{}
	if err := newRedirectTool(t, redirector.URL).call(context.Background(), "GetTask", map[string]string{"id": "x"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", hits.Load())
	}
	if v, _ := gotAuth.Load().(string); v != "Bearer redteam-fake-peer-credential" {
		t.Fatalf("Authorization on same-host redirect = %q", v)
	}
}

func TestA2ATool_Redirect_LoopTerminatesWithError(t *testing.T) {
	var hits atomic.Int32
	var self string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, self, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	self = srv.URL + "/a2a"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out map[string]interface{}
	err := newRedirectTool(t, srv.URL).call(ctx, "GetTask", map[string]string{"id": "x"}, &out)
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("want stopped-after-redirects error, got %v", err)
	}
	if n := hits.Load(); n > 11 {
		t.Fatalf("followed %d redirects, want the stdlib cap (10)", n)
	}
}
