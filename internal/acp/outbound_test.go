package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// outboundHarness is newTestServer plus access to the *Server (to call
// callClient directly) and to Run's return value.
type outboundHarness struct {
	*testServer
	srv    *Server
	runErr chan error
	cancel context.CancelFunc
}

// newOutboundHarness starts a server over pipes. mk, when non-nil, builds the
// RunFunc with access to the server, so a prompt turn can call callClient the
// way a prompter will.
func newOutboundHarness(t *testing.T, mk func(srv *Server) RunFunc) *outboundHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("ok", nil), inR, outW)
	if mk != nil {
		srv.runFunc = mk(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &outboundHarness{
		testServer: &testServer{inW: inW, lines: make(chan string, 4096)},
		srv:        srv,
		runErr:     make(chan error, 1),
		cancel:     cancel,
	}
	go func() { h.runErr <- srv.Run(ctx, nil, nil) }()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	t.Cleanup(func() { cancel(); inW.Close() })
	return h
}

// outboundLine is an agent-to-client request as seen on the wire.
type outboundLine struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// recvOutbound reads the next line and requires it to be one of our
// outbound requests (string id in the rk- namespace).
func (h *outboundHarness) recvOutbound(t *testing.T) (id string, line outboundLine) {
	t.Helper()
	raw := h.recv(t, 5*time.Second)
	if err := json.Unmarshal([]byte(raw), &line); err != nil {
		t.Fatalf("outbound line not JSON: %v (%s)", err, raw)
	}
	if err := json.Unmarshal(line.ID, &id); err != nil || !strings.HasPrefix(id, "rk-") {
		t.Fatalf("outbound id = %s, want a string \"rk-N\" (%s)", line.ID, raw)
	}
	return id, line
}

// expectInitializeNext sends initialize with id "after" and requires the very
// next output line to be its response, which proves nothing else (such as a
// reply to a stray response) was written in between.
func (h *outboundHarness) expectInitializeNext(t *testing.T) {
	t.Helper()
	h.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage(`"after"`), Method: "initialize"})
	line := h.recv(t, 5*time.Second)
	if !strings.Contains(line, `"id":"after"`) || strings.Contains(line, `"error"`) {
		t.Fatalf("next line = %s, want the initialize response and nothing before it", line)
	}
}

type callRes struct {
	result json.RawMessage
	err    error
}

func (h *outboundHarness) callAsync(ctx context.Context, sessionID string) <-chan callRes {
	ch := make(chan callRes, 1)
	go func() {
		r, err := h.srv.callClient(ctx, sessionID, "test/ping", map[string]string{"sessionId": sessionID})
		ch <- callRes{r, err}
	}()
	return ch
}

func waitCall(t *testing.T, ch <-chan callRes) callRes {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("callClient did not return")
		return callRes{}
	}
}

func (h *outboundHarness) pendingCount() int {
	h.srv.outMu.Lock()
	defer h.srv.outMu.Unlock()
	return len(h.srv.pending)
}

func TestACP_StrayResponse_GetsNoReply(t *testing.T) {
	h := newOutboundHarness(t, nil)
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":77,"result":{}}`))
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":"x","error":{"code":-32000,"message":"no"}}`))
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":null,"result":null}`))
	h.expectInitializeNext(t)
}

func TestACP_ResponseRoutedToPendingCall(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1")

	id, line := h.recvOutbound(t)
	if line.JSONRPC != "2.0" || line.Method != "test/ping" || string(line.Params) != `{"sessionId":"s1"}` {
		t.Fatalf("outbound request = %+v", line)
	}
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{"ok": "yes"}})

	r := waitCall(t, ch)
	if r.err != nil || string(r.result) != `{"ok":"yes"}` {
		t.Fatalf("callClient = %s, %v; want the routed result", r.result, r.err)
	}
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d after answer, want 0", n)
	}
	h.expectInitializeNext(t) // the answer itself got no reply
}

func TestACP_NullResultRoutedAsResult(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1")
	id, _ := h.recvOutbound(t)
	h.send(t, json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":null}`, id)))
	r := waitCall(t, ch)
	if r.err != nil || string(r.result) != "null" {
		t.Fatalf("callClient = %q, %v; want null result, no error", r.result, r.err)
	}
}

func TestACP_ErrorResponseRoutedAsError(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1")
	id, _ := h.recvOutbound(t)
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "error": map[string]interface{}{"code": -32000, "message": "nope"}})

	r := waitCall(t, ch)
	var rpcErr *acpError
	if !errors.As(r.err, &rpcErr) || rpcErr.Code != -32000 || rpcErr.Message != "nope" {
		t.Fatalf("callClient err = %v, want *acpError{-32000, nope}", r.err)
	}
	if r.result != nil {
		t.Fatalf("result = %s alongside an error, want nil", r.result)
	}
	h.expectInitializeNext(t)
}

func TestACP_MalformedErrorObject_FailsCallClosed(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1")
	id, _ := h.recvOutbound(t)
	// Both result and a non-object error: must be an error, never a result.
	h.send(t, json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"outcome":"selected"},"error":"boom"}`, id)))
	r := waitCall(t, ch)
	if r.err == nil || r.result != nil {
		t.Fatalf("callClient = %s, %v; want an error and no result", r.result, r.err)
	}
	h.expectInitializeNext(t)
}

func TestACP_LateResponseDropped(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch := h.callAsync(ctx, "s1")
	id, _ := h.recvOutbound(t)

	cancel() // deadline/cancel wins first
	r := waitCall(t, ch)
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("callClient err = %v, want context.Canceled", r.err)
	}
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d after cancel, want 0", n)
	}

	// The answer arrives afterwards: no panic, no reply, server responsive.
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{"outcome": "selected"}})
	h.expectInitializeNext(t)
}

func TestACP_DeadlineExpired_FailsClosed(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r := waitCall(t, h.callAsync(ctx, "s1"))
	if !errors.Is(r.err, context.DeadlineExceeded) || r.result != nil {
		t.Fatalf("callClient = %s, %v; want DeadlineExceeded", r.result, r.err)
	}
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestACP_OutboundIDsUniqueUnderConcurrency(t *testing.T) {
	const n = 100
	h := newOutboundHarness(t, nil)

	// Responder: echo each request's own id back inside the result.
	go func() {
		for i := 0; i < n; i++ {
			raw, ok := <-h.lines
			if !ok {
				return
			}
			var l outboundLine
			if json.Unmarshal([]byte(raw), &l) != nil {
				return
			}
			b, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": l.ID, "result": map[string]json.RawMessage{"echo": l.ID}})
			if _, err := h.inW.Write(append(b, '\n')); err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r, err := h.srv.callClient(ctx, fmt.Sprintf("s%d", i%7), "test/ping", nil)
			if err != nil {
				errs <- err
				return
			}
			var res struct{ Echo string }
			if err := json.Unmarshal(r, &res); err != nil || !strings.HasPrefix(res.Echo, "rk-") {
				errs <- fmt.Errorf("bad echo %s", r)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[res.Echo] {
				errs <- fmt.Errorf("id %s delivered twice", res.Echo)
			}
			seen[res.Echo] = true
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(seen) != n {
		t.Fatalf("distinct ids = %d, want %d", len(seen), n)
	}
	if c := h.pendingCount(); c != 0 {
		t.Fatalf("pending = %d, want 0", c)
	}
}

// A prompt turn blocked on an outbound call must not hold Run open after EOF:
// the call fails closed before Run waits for in-flight turns.
func TestACP_EOF_FailsPendingAndRunReturns(t *testing.T) {
	callErr := make(chan error, 1)
	h := newOutboundHarness(t, func(srv *Server) RunFunc {
		return func(ctx context.Context, _ *config.Config, _ *telemetry.EventBus, _ *debug.DebugController, _ string, _ func([]debug.Attachable), _ *ConvTurn) (string, error) {
			_, err := srv.callClient(ctx, "s1", "test/ping", nil)
			callErr <- err
			return "", err
		}
	})
	h.srv.sessions.add(newSession("s1"))
	h.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "session/prompt", Params: textPrompt("s1", "go")})
	h.recvOutbound(t) // the turn is now waiting on the client

	h.inW.Close()

	select {
	case err := <-callErr:
		if !errors.Is(err, errClientDisconnected) {
			t.Fatalf("callClient err = %v, want errClientDisconnected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending call not failed on EOF")
	}
	select {
	case err := <-h.runErr:
		if err != nil {
			t.Fatalf("Run = %v, want nil on EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after EOF with a pending outbound call")
	}
}

func TestACP_RunCtxCancel_FailsPendingAndRunReturns(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1") // caller ctx never ends by itself
	h.recvOutbound(t)

	h.cancel()

	if r := waitCall(t, ch); !errors.Is(r.err, errClientDisconnected) {
		t.Fatalf("callClient err = %v, want errClientDisconnected", r.err)
	}
	select {
	case <-h.runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestACP_CallClientAfterClose_FailsImmediately(t *testing.T) {
	h := newOutboundHarness(t, nil)
	h.inW.Close()
	select {
	case <-h.runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	r := waitCall(t, h.callAsync(context.Background(), "s1"))
	if !errors.Is(r.err, errClientDisconnected) {
		t.Fatalf("callClient after close = %v, want errClientDisconnected", r.err)
	}
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d after close, want 0", n)
	}
}

func TestACP_CallClient_EmptySessionRejected(t *testing.T) {
	h := newOutboundHarness(t, nil)
	r := waitCall(t, h.callAsync(context.Background(), ""))
	if r.err == nil {
		t.Fatal("callClient with empty session id succeeded, want error")
	}
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestACP_SessionCancel_FailsOnlyThatSessionsPending(t *testing.T) {
	h := newOutboundHarness(t, nil)
	sa := newSessionHelper(t, h.testServer)
	sb := newSessionHelper(t, h.testServer)

	chA := h.callAsync(context.Background(), sa)
	h.recvOutbound(t)
	chB := h.callAsync(context.Background(), sb)
	idB, _ := h.recvOutbound(t)

	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": sa}})

	if r := waitCall(t, chA); !errors.Is(r.err, errSessionCancelled) {
		t.Fatalf("session A call = %v, want errSessionCancelled", r.err)
	}
	select {
	case r := <-chB:
		t.Fatalf("session B call ended by A's cancel: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": idB, "result": map[string]string{"ok": "b"}})
	if r := waitCall(t, chB); r.err != nil || string(r.result) != `{"ok":"b"}` {
		t.Fatalf("session B call = %s, %v; want its own answer", r.result, r.err)
	}
}

// An empty or unknown sessionId in session/cancel must not fail anyone's call.
func TestACP_SessionCancel_EmptyOrUnknownSession_FailsNothing(t *testing.T) {
	h := newOutboundHarness(t, nil)
	sa := newSessionHelper(t, h.testServer)
	ch := h.callAsync(context.Background(), sa)
	id, _ := h.recvOutbound(t)

	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":""}}`))
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","method":"session/cancel","params":{}}`))
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"nope"}}`))
	h.expectInitializeNext(t) // cancels processed, no output

	select {
	case r := <-ch:
		t.Fatalf("call ended by an unrelated cancel: %+v", r)
	default:
	}
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{}})
	if r := waitCall(t, ch); r.err != nil {
		t.Fatalf("call = %v, want its answer", r.err)
	}
}

func TestACP_ClientIDCollisionIgnored(t *testing.T) {
	h := newOutboundHarness(t, nil)
	// Pre-emptive answers for ids we have not issued yet, plus a numeric 1
	// that must never match the string id "rk-1".
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":"rk-1","result":{"outcome":"forged"}}`))
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"outcome":"forged"}}`))
	h.expectInitializeNext(t)

	ch := h.callAsync(context.Background(), "s1")
	id, _ := h.recvOutbound(t)
	if id != "rk-1" {
		t.Fatalf("first outbound id = %q, want rk-1", id)
	}
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"outcome":"forged"}}`))
	select {
	case r := <-ch:
		t.Fatalf("numeric id 1 answered rk-1: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{"outcome": "real"}})
	if r := waitCall(t, ch); r.err != nil || string(r.result) != `{"outcome":"real"}` {
		t.Fatalf("call = %s, %v; want the real answer", r.result, r.err)
	}
}

// A duplicate answer for an id already settled is dropped, not re-delivered.
func TestACP_DuplicateResponse_SecondDropped(t *testing.T) {
	h := newOutboundHarness(t, nil)
	ch := h.callAsync(context.Background(), "s1")
	id, _ := h.recvOutbound(t)
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{"n": "1"}})
	h.send(t, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]string{"n": "2"}})
	if r := waitCall(t, ch); string(r.result) != `{"n":"1"}` {
		t.Fatalf("call = %s, want the first answer", r.result)
	}
	h.expectInitializeNext(t)
}

func TestACP_ResponseWithoutResultOrError_InvalidRequest(t *testing.T) {
	h := newOutboundHarness(t, nil)
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":5}`))
	resp := h.recvResponse(t, 2*time.Second)
	if resp.Error == nil || resp.Error.Code != -32600 || string(resp.ID) != "5" {
		t.Fatalf("reply = %+v, want -32600 with id 5", resp)
	}
	// Without an id there is nothing to answer.
	h.send(t, json.RawMessage(`{"jsonrpc":"2.0"}`))
	h.expectInitializeNext(t)
}

func TestACP_StrayResponseFlood_NoReplyStaysResponsive(t *testing.T) {
	h := newOutboundHarness(t, nil)
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, `{"jsonrpc":"2.0","id":"rk-%d","result":{}}`+"\n", i)
	}
	if _, err := h.inW.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	h.expectInitializeNext(t)
	if n := h.pendingCount(); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

// enteredWriter is a gatedWriter that reports each Write it has entered.
type enteredWriter struct {
	gatedWriter
	entered chan struct{}
}

func (e *enteredWriter) Write(b []byte) (int, error) {
	e.entered <- struct{}{}
	return e.gatedWriter.Write(b)
}

// With the client not reading stdout, a call must still end on its deadline,
// and the request it never managed to send is not written afterwards.
func TestACP_StalledOutput_DeadlineFailsClosedAndSkipsWrite(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()
	w := &enteredWriter{gatedWriter: gatedWriter{gate: make(chan struct{}), lines: make(chan string, 16)}, entered: make(chan struct{}, 16)}
	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("ok", nil), inR, w)
	go srv.Run(context.Background(), nil, nil) //nolint:errcheck

	// Occupy the writer so the outbound request queues behind it.
	go srv.writeNotification("session/update", map[string]string{"blocker": "1"})
	<-w.entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := srv.callClient(ctx, "s1", "test/ping", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("callClient = %v, want DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callClient blocked behind a stalled writer past its deadline")
	}

	close(w.gate)
	got := <-w.lines
	if !strings.Contains(got, "blocker") {
		t.Fatalf("first line = %s, want the blocker", got)
	}
	select {
	case l := <-w.lines:
		t.Fatalf("expired request was written after its deadline: %s", l)
	case <-time.After(200 * time.Millisecond):
	}
}
