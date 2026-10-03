package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/server"
)

// Red-team rows: A2A inbound authentication and slow request body.

const sendMessageBody = `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"tenant":"Researcher","message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`

// TestA2A_Auth_MissingAndWrongTokenNeverReachRun wires the handler the way
// serve.go does (AuthMiddleware in front of the /a2a mux entry) and checks
// that an unauthenticated caller can neither run an agent nor see task state.
func TestA2A_Auth_MissingAndWrongTokenNeverReachRun(t *testing.T) {
	const token = "redteam-fake-api-credential"
	t.Setenv("RAKITSU_API_TOKEN", token)

	var runs atomic.Int32
	h := a2aHandlerFunc(testConfig(), func(ctx context.Context, cfg *config.Config, q string) (string, error) {
		runs.Add(1)
		return "ok", nil
	})
	mux := http.NewServeMux()
	mux.Handle("/a2a", h)
	ts := httptest.NewServer(server.AuthMiddleware(mux))
	defer ts.Close()

	post := func(auth string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/a2a", strings.NewReader(sendMessageBody))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return resp.StatusCode
	}

	for name, auth := range map[string]string{
		"missing":           "",
		"wrong":             "Bearer " + token + "x",
		"prefix-only":       "Bearer ",
		"wrong-scheme":      "Basic " + token,
		"token-no-scheme":   token + "x",
		"empty-vs-nonempty": "Bearer  ",
	} {
		if code := post(auth); code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, code)
		}
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("agent ran %d time(s) for unauthenticated callers", n)
	}
	if code := post("Bearer " + token); code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", code)
	}
}

// blockingBody never produces data until closed, like a client that sent
// headers and then stalled.
type blockingBody struct{ unblock chan struct{} }

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.unblock
	return 0, io.ErrUnexpectedEOF
}

// TestA2A_SlowBody_HandlerHoldsNoStateAndReleasesOnError: a stalled body parks
// only that handler goroutine; no task is created and no agent starts. The
// deadline that frees it lives on the http.Server (serve.go ReadTimeout).
func TestA2A_SlowBody_HandlerHoldsNoStateAndReleasesOnError(t *testing.T) {
	var runs atomic.Int32
	h := a2aHandlerFunc(testConfig(), func(ctx context.Context, cfg *config.Config, q string) (string, error) {
		runs.Add(1)
		return "ok", nil
	})

	body := &blockingBody{unblock: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, "/a2a", body)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h(rec, req); close(done) }()

	select {
	case <-done:
		t.Fatal("handler returned before the body finished")
	default:
	}
	close(body.unblock) // read deadline fires / connection drops
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not release after body read error")
	}
	if !strings.Contains(rec.Body.String(), "-32700") {
		t.Fatalf("want parse error for truncated body, got %q", rec.Body.String())
	}
	if runs.Load() != 0 {
		t.Fatal("agent started from a truncated body")
	}
}

// stalledConn is a net.Conn that delivers a partial request and then stalls
// until the test fires the "deadline", at which point Read returns a timeout
// error exactly as a real conn does when its read deadline passes. It records
// the deadlines the http.Server sets, so no wall-clock time is involved.
type stalledConn struct {
	in       *bytes.Reader
	fire     chan struct{}
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	deadline time.Time
	out      bytes.Buffer
}

func (c *stalledConn) Read(p []byte) (int, error) {
	if c.in.Len() > 0 {
		return c.in.Read(p)
	}
	select {
	case <-c.fire:
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}
func (c *stalledConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}
func (c *stalledConn) Close() error         { c.once.Do(func() { close(c.closed) }); return nil }
func (c *stalledConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *stalledConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }
func (c *stalledConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}
func (c *stalledConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !t.IsZero() {
		c.deadline = t
	}
	return nil
}
func (c *stalledConn) SetWriteDeadline(time.Time) error { return nil }

// oneConnListener hands out a single conn, then blocks until closed.
type oneConnListener struct {
	conn   net.Conn
	given  bool
	closed chan struct{}
	once   sync.Once
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if !l.given {
		l.given = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *oneConnListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *oneConnListener) Addr() net.Addr { return &net.TCPAddr{} }

// TestA2A_SlowBody_ServerReadTimeoutCutsStalledClient serves a client that
// promises 1000 bytes and sends 10, through the real http.Server built by
// serve.go's constructor. The server must arm a read deadline on the conn;
// when it fires (simulated by the fake conn), the handler is released with no
// agent run and the connection is closed.
func TestA2A_SlowBody_ServerReadTimeoutCutsStalledClient(t *testing.T) {
	var runs atomic.Int32
	h := a2aHandlerFunc(testConfig(), func(ctx context.Context, cfg *config.Config, q string) (string, error) {
		runs.Add(1)
		return "ok", nil
	})
	srv := newServeHTTPServer("x", h)

	conn := &stalledConn{
		in:     bytes.NewReader([]byte("POST /a2a HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"jsonrpc\"")),
		fire:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	ln := &oneConnListener{conn: conn, closed: make(chan struct{})}
	served := make(chan struct{})
	go func() { srv.Serve(ln); close(served) }() //nolint:errcheck
	defer func() { srv.Close(); <-served }()

	close(conn.fire) // the read deadline "passes"
	select {
	case <-conn.closed:
	case <-time.After(5 * time.Second): // failure-only safety net
		t.Fatal("server did not cut off a stalled client after its read deadline")
	}

	conn.mu.Lock()
	deadline, out := conn.deadline, conn.out.String()
	conn.mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("server never armed a read deadline on the connection")
	}
	if runs.Load() != 0 {
		t.Fatal("agent started from a stalled body")
	}
	if strings.Contains(out, "200 OK") && !strings.Contains(out, "-32700") {
		t.Fatalf("stalled body produced a success response: %q", out)
	}
}

// TestServeHTTPServer_Timeouts pins the slow-client protections wired in serve.go.
func TestServeHTTPServer_Timeouts(t *testing.T) {
	srv := newServeHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %v, want 15s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (SSE streams are long-lived)", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", srv.IdleTimeout)
	}
}
