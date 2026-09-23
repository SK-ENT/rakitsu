package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guardReq(method, host, origin string) *http.Request {
	r := httptest.NewRequest(method, "/api/run", strings.NewReader(`{}`))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestGuardMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct {
		name     string
		bindHost string
		req      *http.Request
		want     int
	}{
		{"cli client, no origin", "localhost", guardReq("POST", "localhost:9100", ""), 200},
		{"ui on localhost", "localhost", guardReq("POST", "localhost:9100", "http://localhost:9100"), 200},
		{"ui via 127.0.0.1", "localhost", guardReq("POST", "127.0.0.1:9100", "http://127.0.0.1:9100"), 200},
		{"csrf from other site", "localhost", guardReq("POST", "localhost:9100", "https://evil.example"), 403},
		{"csrf DELETE", "localhost", guardReq("DELETE", "localhost:9100", "https://evil.example"), 403},
		{"cross-site GET passes (CORS hides response)", "localhost", guardReq("GET", "localhost:9100", "https://evil.example"), 200},
		{"dns rebinding host", "localhost", guardReq("POST", "evil.example:9100", ""), 403},
		{"dns rebinding GET", "localhost", guardReq("GET", "evil.example:9100", ""), 403},
		{"network bind, same-origin ui", "0.0.0.0", guardReq("POST", "192.168.1.5:9100", "http://192.168.1.5:9100"), 200},
		{"network bind, csrf", "0.0.0.0", guardReq("POST", "192.168.1.5:9100", "https://evil.example"), 403},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		GuardMiddleware(c.bindHost, ok).ServeHTTP(rec, c.req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

// With a control-plane token configured, the bearer Authorization header is
// that token and must never be forwarded to a provider base_url.
func TestProviderAPIKeyFromRequest_NeverUsesControlToken(t *testing.T) {
	t.Setenv(apiTokenEnv, "control-token")
	r := httptest.NewRequest("GET", "/api/providers/models", nil)
	r.Header.Set("Authorization", "Bearer control-token")
	if got := providerAPIKeyFromRequest(r); got != "" {
		t.Fatalf("control token used as provider key: %q", got)
	}
	r.Header.Set(providerKeyHeader, "sk-provider")
	if got := providerAPIKeyFromRequest(r); got != "sk-provider" {
		t.Fatalf("X-Provider-Key not used: %q", got)
	}
}

func TestProviderAPIKeyFromRequest_NoTokenKeepsBearerCompat(t *testing.T) {
	t.Setenv(apiTokenEnv, "")
	r := httptest.NewRequest("GET", "/api/providers/models", nil)
	r.Header.Set("Authorization", "Bearer sk-provider")
	if got := providerAPIKeyFromRequest(r); got != "sk-provider" {
		t.Fatalf("bearer provider key not accepted without a control token: %q", got)
	}
}
