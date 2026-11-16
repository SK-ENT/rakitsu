package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Red-team rows: stdin close,
// stalled-client flood, and permission requests with no responder.

// goroutinesWith counts goroutines whose stack contains needle.
func goroutinesWith(needle string) int {
	buf := make([]byte, 8<<20)
	n := runtime.Stack(buf, true)
	count := 0
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(g, needle) {
			count++
		}
	}
	return count
}

// goroutineGone polls (yielding, not sleeping) until at most baseline
// goroutine stacks contain needle, or the deadline passes. The baseline is
// taken before the server under test starts: other tests in the package
// (newTestServer never closes stdin) leave their own readers parked, which
// made a process-wide "none left" check fail under -count>1.
func goroutineGone(needle string, baseline int, within time.Duration) bool {
	deadline := time.After(within)
	for {
		if goroutinesWith(needle) <= baseline {
			return true
		}
		select {
		case <-deadline:
			return false
		default:
			runtime.Gosched()
		}
	}
}

func TestACP_StdinClosed_RunReturnsCleanlyAndReaderExits(t *testing.T) {
	const reader = "acp.(*Server).Run.func1"
	baseline := goroutinesWith(reader)
	inR, inW := io.Pipe()
	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("x", nil), inR, io.Discard)

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), nil, nil) }()

	inW.Close() // client hangs up

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run on clean EOF = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin closed")
	}
	if !goroutineGone(reader, baseline, 5*time.Second) {
		t.Fatal("reader goroutine still alive after stdin closed")
	}
}

func TestACP_StdinReadError_RunReturnsError(t *testing.T) {
	inR, inW := io.Pipe()
	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("x", nil), inR, io.Discard)
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), nil, nil) }()

	boom := errors.New("pipe broke")
	inW.CloseWithError(boom)

	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("Run = %v, want %v", err, boom)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin read error")
	}
}

// gatedWriter blocks every Write until gate is closed (a client that stopped
// reading its pipe), then forwards each written line to lines.
type gatedWriter struct {
	gate  chan struct{}
	lines chan string
}

func (g *gatedWriter) Write(b []byte) (int, error) {
	<-g.gate
	g.lines <- strings.TrimRight(string(b), "\n")
	return len(b), nil
}

func TestACP_StalledClientFlood_BoundedAndRecovers(t *testing.T) {
	const flood = 5000 // far above the 1024-event bus buffer
	runDone := make(chan struct{})
	inner := eventPublishingRunFunc(flood, "done")
	run := RunFunc(func(ctx context.Context, cfg *config.Config, bus *telemetry.EventBus, d *debug.DebugController, q string, r func([]debug.Attachable), _ *ConvTurn) (string, error) {
		res, err := inner(ctx, cfg, bus, d, q, r, nil)
		close(runDone)
		return res, err
	})

	inR, inW := io.Pipe()
	w := &gatedWriter{gate: make(chan struct{}), lines: make(chan string, flood+16)}
	srv := NewServerWithIO(loadTestConfig(t), run, inR, w)
	srv.sessions.add(newSession("s1")) // session/new would itself block on the stalled writer

	runReturned := make(chan error, 1)
	go func() { runReturned <- srv.Run(context.Background(), nil, nil) }()

	req, _ := json.Marshal(acpRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "session/prompt", Params: textPrompt("s1", "go")})
	if _, err := inW.Write(append(req, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Backpressure must not stall the agent: the bus drops instead of blocking.
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("runFunc blocked behind a stalled client; bus should drop, not block")
	}

	// Client wakes up and drains everything.
	close(w.gate)
	updates := 0
	for {
		select {
		case line := <-w.lines:
			if strings.Contains(line, `"stopReason"`) {
				// Bounded: bus buffer (1024) + one write already in flight.
				if updates == 0 || updates > 1024+1 {
					t.Fatalf("session/update count = %d, want 1..1025 (bounded)", updates)
				}
				inW.Close()
				select {
				case err := <-runReturned:
					if err != nil {
						t.Fatalf("Run = %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("Run did not return after recovery + EOF")
				}
				return
			}
			if strings.Contains(line, `"session/update"`) {
				updates++
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no final response after client resumed (updates=%d)", updates)
		}
	}
}

// ACP v1 never issues session/request_permission, so there is no responder
// loop that could hang. Pin that: the server neither emits one nor waits on
// one, and rejects/ignores a client trying to use the method or answer it.
func TestACP_PermissionRequest_NotModeled_NoHang(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), eventPublishingRunFunc(3, "ok"))
	defer cancel()
	sid := newSessionHelper(t, ts)

	// 1. A client calling the agent-side-absent method gets method-not-found.
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage(`"perm"`), Method: "session/request_permission", Params: json.RawMessage(`{}`)})
	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("session/request_permission = %+v, want -32601", resp)
	}

	// 2. A stray permission *answer* for a request never made must not wedge
	// the server, and gets no reply at all (JSON-RPC forbids answering a
	// response; it is routed to the outbound pending map and dropped).
	ts.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": 77, "result": map[string]interface{}{"outcome": map[string]string{"outcome": "selected", "optionId": "allow"}}})

	// 3. A full prompt turn completes and never asks for permission.
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage(`"p"`), Method: "session/prompt", Params: textPrompt(sid, "hi")})
	for {
		line := ts.recv(t, 5*time.Second)
		if strings.Contains(line, "request_permission") {
			t.Fatalf("server emitted a permission request: %s", line)
		}
		if strings.Contains(line, `"id":77`) {
			t.Fatalf("server replied to a stray response: %s", line)
		}
		if strings.Contains(line, `"id":"p"`) {
			if !strings.Contains(line, "end_turn") {
				t.Fatalf("prompt did not complete normally: %s", line)
			}
			return
		}
	}
}
