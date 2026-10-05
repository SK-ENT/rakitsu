package wake

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestFileChecksReadOnlyAndConfined(t *testing.T) {
	work := t.TempDir()
	logs := filepath.Join(work, "logs")
	_ = os.MkdirAll(logs, 0o755)
	_ = os.WriteFile(filepath.Join(logs, "app.log"), []byte("all fine\nFAILED step 3\n"), 0o644)
	cfg := config.WakeConfig{Allow: config.WakeAllow{Paths: []string{"./logs"}}}
	run := NewBuiltinChecks(cfg, work, nil)

	o := run(context.Background(), config.WakeCheck{Type: "file_contains", Path: "./logs/app.log", Contains: "FAILED"})
	if o.Err != nil || !o.Contains || !o.Exists {
		t.Fatalf("contains: %+v", o)
	}
	o = run(context.Background(), config.WakeCheck{Type: "file_mtime", Path: "./logs/app.log"})
	if o.Err != nil || !o.Exists || o.ModTime.IsZero() {
		t.Fatalf("mtime: %+v", o)
	}
	o = run(context.Background(), config.WakeCheck{Type: "file_mtime", Path: "./logs/missing.log"})
	if o.Err != nil || o.Exists {
		t.Fatalf("missing file is Exists=false without error: %+v", o)
	}
}

func TestFileSymlinkEscapeRefused(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("FAILED"), 0o644)
	_ = os.MkdirAll(filepath.Join(work, "logs"), 0o755)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(work, "logs", "link.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{Paths: []string{"./logs"}}}, work, nil)
	o := run(context.Background(), config.WakeCheck{Type: "file_contains", Path: "./logs/link.txt", Contains: "FAILED"})
	if o.Err == nil || o.Contains {
		t.Fatalf("symlink escaping the workdir must be refused: %+v", o)
	}
}

func TestFileContainsReadsAtMost64KiB(t *testing.T) {
	work := t.TempDir()
	_ = os.MkdirAll(filepath.Join(work, "logs"), 0o755)
	big := strings.Repeat("a", 70*1024) + "FAILED"
	_ = os.WriteFile(filepath.Join(work, "logs", "big.log"), []byte(big), 0o644)
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{Paths: []string{"./logs"}}}, work, nil)
	o := run(context.Background(), config.WakeCheck{Type: "file_contains", Path: "./logs/big.log", Contains: "FAILED"})
	if o.Contains {
		t.Fatal("marker beyond the 64 KiB cap must not be seen")
	}
}

func hostOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

func TestHTTPStatusAndJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("no auth headers may be sent")
		}
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(204)
		case "/price":
			_, _ = w.Write([]byte(`{"bitcoin":{"usd":61234.5}}`))
		case "/str":
			_, _ = w.Write([]byte(`{"bitcoin":{"usd":"x"}}`))
		case "/notjson":
			_, _ = w.Write([]byte(`<html>`))
		case "/huge":
			_, _ = w.Write([]byte(`{"a":"` + strings.Repeat("x", 200*1024) + `"}`))
		}
	}))
	defer srv.Close()
	cfg := config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}
	run := NewBuiltinChecks(cfg, t.TempDir(), nil)
	ctx := context.Background()

	if o := run(ctx, config.WakeCheck{Type: "http_status", URL: srv.URL + "/health", TimeoutSeconds: 2}); o.Err != nil || o.HTTPStatus != 204 {
		t.Fatalf("status: %+v", o)
	}
	o := run(ctx, config.WakeCheck{Type: "http_json", URL: srv.URL + "/price", Field: "bitcoin.usd", TimeoutSeconds: 2})
	if o.Err != nil || o.Number == nil || *o.Number != 61234.5 {
		t.Fatalf("json: %+v", o)
	}
}

func TestHTTPJSONProductionDoesNotRetainPerProbeConnections(t *testing.T) {
	var mu sync.Mutex
	connections := make(map[net.Conn]http.ConnState)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":1}`))
	}))
	srv.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		if state == http.StateClosed || state == http.StateHijacked {
			delete(connections, conn)
			return
		}
		connections[conn] = state
	}
	srv.Start()
	defer srv.Close()

	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}, t.TempDir(), nil)
	for i := 0; i < 5; i++ {
		o := run(context.Background(), config.WakeCheck{Type: "http_json", URL: srv.URL, Field: "value", TimeoutSeconds: 2})
		if o.Err != nil || o.Number == nil || *o.Number != 1 {
			t.Fatalf("probe %d: %+v", i, o)
		}
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		idle := 0
		for _, state := range connections {
			if state == http.StateIdle {
				idle++
			}
		}
		mu.Unlock()
		if idle >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	idle := 0
	for _, state := range connections {
		if state == http.StateIdle {
			idle++
		}
	}
	if idle > 1 {
		t.Fatalf("five probes left %d idle connections; expected a reused or closed transport", idle)
	}
}

func TestHTTPJSONBadBodyIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/str":
			_, _ = w.Write([]byte(`{"bitcoin":{"usd":"x"}}`))
		case "/notjson":
			_, _ = w.Write([]byte(`<html>`))
		}
	}))
	defer srv.Close()
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}, t.TempDir(), nil)
	for _, p := range []string{"/str", "/notjson", "/missing"} {
		o := run(context.Background(), config.WakeCheck{Type: "http_json", URL: srv.URL + p, Field: "bitcoin.usd", TimeoutSeconds: 2})
		if o.Err == nil || o.Number != nil {
			t.Fatalf("%s must be an error observation, got %+v", p, o)
		}
	}
}

func TestHTTPJSONBodyCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":"` + strings.Repeat("x", 200*1024) + `","n":1}`))
	}))
	defer srv.Close()
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_json", URL: srv.URL, Field: "n", TimeoutSeconds: 2})
	if o.Err == nil {
		t.Fatalf("truncated body must not parse as success: %+v", o)
	}
}

func TestHTTPRedirectToDisallowedHostRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(redir)}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: redir.URL, TimeoutSeconds: 2})
	if o.Err == nil {
		t.Fatalf("redirect to a host outside allow.url_hosts must be refused: %+v", o)
	}
}

func TestHTTPTimeoutIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(1500 * time.Millisecond) }))
	defer srv.Close()
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: srv.URL, TimeoutSeconds: 1})
	if o.Err == nil {
		t.Fatal("timeout must be an error observation")
	}
}

func TestHTTPRedirectNeverFollowed(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	// Both hosts allowed: the redirect must still not be followed.
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(redir), hostOf(target)}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: redir.URL, TimeoutSeconds: 2})
	if o.Err == nil || hits != 0 {
		t.Fatalf("redirect followed: %+v hits=%d", o, hits)
	}
}

func TestHTTPCheckRejectsUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{hostOf(srv)}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: "http://user:pw@" + hostOf(srv), TimeoutSeconds: 2})
	if o.Err == nil {
		t.Fatalf("userinfo URL accepted: %+v", o)
	}
}

func TestHTTPUnlistedMetadataAddressRefused(t *testing.T) {
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{"169.254.169.254"}}}, t.TempDir(), nil)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: "http://169.254.169.253/", TimeoutSeconds: 1})
	if o.Err == nil {
		t.Fatal("unlisted host must be refused")
	}
}

func TestHTTPInjectedClientHonored(t *testing.T) {
	var used bool
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return &http.Response{StatusCode: 204, Body: http.NoBody, Request: r, Header: http.Header{}}, nil
	})}
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{"example.test"}}}, t.TempDir(), hc)
	o := run(context.Background(), config.WakeCheck{Type: "http_status", URL: "http://example.test/", TimeoutSeconds: 2})
	if !used || o.Err != nil || o.HTTPStatus != 204 {
		t.Fatalf("injected client not used: used=%v %+v", used, o)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// With no injected client the production path must use the netsafe client,
// which never uses proxies. The default transport would send this request to
// the proxy named in HTTP_PROXY.
func TestHTTPNilClientIgnoresProxyEnv(t *testing.T) {
	var proxied int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied++ }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	run := NewBuiltinChecks(config.WakeConfig{Allow: config.WakeAllow{URLHosts: []string{"wake-proxy-test.invalid"}}}, t.TempDir(), nil)
	_ = run(context.Background(), config.WakeCheck{Type: "http_status", URL: "http://wake-proxy-test.invalid/", TimeoutSeconds: 2})
	if proxied != 0 {
		t.Fatal("request went through the proxy: default transport in use")
	}
}
