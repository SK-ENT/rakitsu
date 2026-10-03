package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Red-team row: an MCP server that hangs or dies mid tools/call.

func newHelperStdio(t *testing.T) *StdioClient {
	t.Helper()
	c, err := NewStdioClient(os.Args[0], nil, map[string]string{helperProcessEnvVar: "1"}, 30, 0)
	if err != nil {
		t.Fatalf("NewStdioClient: %v", err)
	}
	return c
}

func TestStdioClient_ServerHangsMidCall_ReturnsAtDeadlineAndCleansUp(t *testing.T) {
	c := newHelperStdio(t)

	// Fake clock: the "deadline" is a manual cancel fired once the call is
	// registered in flight (yielding, not sleeping).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			c.mu.Lock()
			n := len(c.pending)
			c.mu.Unlock()
			if n > 0 {
				cancel()
				return
			}
			runtime.Gosched()
		}
	}()
	_, err := c.CallTool(ctx, "hang", nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("want deadline error, got %v (ctx.Err=%v)", err, ctx.Err())
	}

	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending calls leaked: %d", pending)
	}

	// Close must kill the hung subprocess and let readLoop exit.
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }() //nolint:errcheck
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a hung server")
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop goroutine still running after Close")
	}
}

func TestStdioClient_ServerCrashesMidCall_FailsFastAndStaysFailed(t *testing.T) {
	c := newHelperStdio(t)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // safety net only
	defer cancel()
	_, err := c.CallTool(ctx, "crash", nil)
	if err == nil {
		t.Fatal("want error when server dies mid-call")
	}
	if ctx.Err() != nil {
		t.Fatalf("call only failed at the safety deadline, not on crash: %v", err)
	}

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop did not exit after server crash")
	}
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending calls leaked after crash: %d", pending)
	}

	// A later call must error promptly, not hang on the dead process.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if _, err := c.CallTool(ctx2, "greet", nil); err == nil || ctx2.Err() != nil {
		t.Fatalf("call after crash: err=%v ctxErr=%v, want prompt error", err, ctx2.Err())
	}
}

func TestHTTPClient_ServerHangsMidCall_ReturnsAtDeadline(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c, err := NewHTTPClient(srv.URL, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Fake clock: cancel manually once the server is holding the request.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-entered; cancel() }()
	_, err = c.CallTool(ctx, "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("want context canceled error, got %v", err)
	}
}
