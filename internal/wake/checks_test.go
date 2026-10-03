package wake

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
