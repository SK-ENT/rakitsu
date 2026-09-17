package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// loadTestConfig loads the shared minimal test config once per test — every
// ACP session in v1 runs against one config pinned at server startup (see
// cmd/rakitsu/acp.go), so tests need one loaded *config.Config to pass into
// NewServerWithIO instead of the old per-request config_path.
func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("testdata/agent.yaml")
	if err != nil {
		t.Fatalf("load test config: %v", err)
	}
	return cfg
}

// stubRunFunc returns a RunFunc that immediately returns the given result/error.
func stubRunFunc(result string, err error) RunFunc {
	return func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		return result, err
	}
}

// panicRunFunc returns a RunFunc that panics instead of returning.
func panicRunFunc(msg string) RunFunc {
	return func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		panic(msg)
	}
}

// eventPublishingRunFunc publishes n EventTokenChunk events on eventBus
// before returning — each maps to exactly one session/update notification,
// which lets ordering tests count them directly instead of using an
// unmapped event type that would produce no output at all.
func eventPublishingRunFunc(n int, result string) RunFunc {
	return func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		for i := 0; i < n; i++ {
			payload, _ := json.Marshal(telemetry.TokenChunkPayload{Text: fmt.Sprintf("chunk-%d", i)})
			eventBus.Publish(telemetry.AgentEvent{
				ID:        fmt.Sprintf("evt-%d", i),
				EventType: telemetry.EventTokenChunk,
				Payload:   payload,
			})
		}
		return result, nil
	}
}

// panicAfterEventsRunFunc publishes n mapped events, then panics — used to
// check the panic-recovery path still flushes pending session/update
// notifications in order and doesn't leak the event-drain goroutine.
func panicAfterEventsRunFunc(n int, msg string) RunFunc {
	return func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		for i := 0; i < n; i++ {
			payload, _ := json.Marshal(telemetry.TokenChunkPayload{Text: fmt.Sprintf("chunk-%d", i)})
			eventBus.Publish(telemetry.AgentEvent{
				ID:        fmt.Sprintf("evt-%d", i),
				EventType: telemetry.EventTokenChunk,
				Payload:   payload,
			})
		}
		panic(msg)
	}
}

// testServer wraps an ACP server with pipe I/O for testing.
// Output is continuously drained into a channel to prevent write deadlocks.
type testServer struct {
	inW   *io.PipeWriter
	lines chan string
}

// newTestServer creates a running ACP server backed by in-memory pipes.
// The returned CancelFunc stops it. All output is drained into ts.lines.
func newTestServer(cfg *config.Config, runFunc RunFunc) (*testServer, context.CancelFunc) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	srv := NewServerWithIO(cfg, runFunc, inR, outW)
	ctx, cancel := context.WithCancel(context.Background())

	ts := &testServer{
		inW:   inW,
		lines: make(chan string, 256),
	}

	go srv.Run(ctx, nil, nil) //nolint:errcheck

	// Continuously drain output so server writes never block.
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			ts.lines <- scanner.Text()
		}
		close(ts.lines)
	}()

	return ts, cancel
}

// send writes a JSON-RPC line to the server's stdin.
func (ts *testServer) send(t *testing.T, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b = append(b, '\n')
	if _, err := ts.inW.Write(b); err != nil {
		t.Fatalf("write to server: %v", err)
	}
}

// recv reads the next output line from the server within timeout.
func (ts *testServer) recv(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case line, ok := <-ts.lines:
		if !ok {
			t.Fatal("server output channel closed")
		}
		return line
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for server output after %s", timeout)
		return ""
	}
}

// recvResponse reads the next response (with ID) from the server.
func (ts *testServer) recvResponse(t *testing.T, timeout time.Duration) acpResponse {
	t.Helper()
	line := ts.recv(t, timeout)
	var resp acpResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (line=%q)", err, line)
	}
	return resp
}

// recvNotification reads the next notification (no ID) from the server.
func (ts *testServer) recvNotification(t *testing.T, timeout time.Duration) acpNotification {
	t.Helper()
	line := ts.recv(t, timeout)
	var notif acpNotification
	if err := json.Unmarshal([]byte(line), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v (line=%q)", err, line)
	}
	return notif
}

// newSessionHelper sends session/new and returns the minted sessionId.
func newSessionHelper(t *testing.T, ts *testServer) string {
	t.Helper()
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"new"`),
		Method:  "session/new",
		Params:  json.RawMessage(`{"cwd":"/tmp","mcpServers":[]}`),
	})
	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("session/new error: %+v", resp.Error)
	}
	b, _ := json.Marshal(resp.Result)
	var res newSessionResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("unmarshal session/new result: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("expected non-empty sessionId")
	}
	return res.SessionID
}

// textPrompt builds a session/prompt params payload with a single text block.
func textPrompt(sessionID, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	})
	return b
}

// ─── TestACP_Initialize ───────────────────────────────────────────────────────

func TestACP_Initialize(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	id := json.RawMessage("1")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "initialize"})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected result, got nil")
	}

	b, _ := json.Marshal(resp.Result)
	var result initializeResult
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ProtocolVersion != protocolVersion {
		t.Errorf("want protocolVersion=%d, got %d", protocolVersion, result.ProtocolVersion)
	}
	if len(result.AuthMethods) != 0 {
		t.Errorf("want an empty authMethods array, got %#v", result.AuthMethods)
	}
}

// ─── TestACP_Prompt_NonStreamingResultReachesClient ────────────────────────────

// TestACP_Prompt_NonStreamingResultReachesClient covers the path where the
// run emits no agent_message_chunk events at all — the real shape of a
// no_stream_tools config or a non-StreamingProvider, where
// generateWithStreaming (the only EventTokenChunk emit site) never runs.
// stubRunFunc models this exactly: it returns a result without publishing
// any events. Before the fix, the client got zero content and just
// {"stopReason":"end_turn"} — this asserts the result is instead surfaced
// as a synthetic final agent_message_chunk.
func TestACP_Prompt_NonStreamingResultReachesClient(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("Hello from agent", nil))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hello"),
	})

	notif := ts.recvNotification(t, 2*time.Second)
	if notif.Method != "session/update" {
		t.Fatalf("want a session/update notification before the response, got method=%q", notif.Method)
	}
	b, _ := json.Marshal(notif.Params)
	var sup sessionUpdateParams
	if err := json.Unmarshal(b, &sup); err != nil {
		t.Fatalf("unmarshal sessionUpdateParams: %v", err)
	}
	ub, _ := json.Marshal(sup.Update)
	var chunk agentMessageChunkUpdate
	if err := json.Unmarshal(ub, &chunk); err != nil {
		t.Fatalf("unmarshal agentMessageChunkUpdate: %v", err)
	}
	if chunk.SessionUpdate != "agent_message_chunk" {
		t.Errorf("want sessionUpdate=agent_message_chunk, got %q", chunk.SessionUpdate)
	}
	if chunk.Content.Text != "Hello from agent" {
		t.Errorf("want the agent's result surfaced as the chunk text, got %q", chunk.Content.Text)
	}

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("session/prompt error: %+v", resp.Error)
	}
}

// TestACP_Prompt_StreamedResultNotDuplicated covers the opposite path: the
// run already streamed its answer via agent_message_chunk events, so the
// synthetic final-chunk fallback above must NOT also fire — that would
// duplicate the answer in the client's transcript.
func TestACP_Prompt_StreamedResultNotDuplicated(t *testing.T) {
	const numEvents = 3
	ts, cancel := newTestServer(loadTestConfig(t), eventPublishingRunFunc(numEvents, "done"))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hello"),
	})

	chunks := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line := ts.recv(t, 3*time.Second)
		var probe struct {
			Method string `json:"method"`
		}
		json.Unmarshal([]byte(line), &probe) //nolint:errcheck
		if probe.Method == "session/update" {
			chunks++
			continue
		}
		break // response (no method) — done
	}
	if chunks != numEvents {
		t.Errorf("want exactly %d session/update notifications (no synthetic extra), got %d", numEvents, chunks)
	}
}

// ─── TestACP_SessionPromptRoundTrip ────────────────────────────────────────────

func TestACP_SessionPromptRoundTrip(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("Hello from agent", nil))
	defer cancel()

	sessionID := newSessionHelper(t, ts)

	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hello"),
	})

	// stubRunFunc emits no events, so the non-streaming fallback (see
	// TestACP_Prompt_NonStreamingResultReachesClient) sends one synthetic
	// agent_message_chunk session/update before the response.
	ts.recvNotification(t, 2*time.Second)

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("session/prompt error: %+v", resp.Error)
	}
	b, _ := json.Marshal(resp.Result)
	var pr promptResult
	if err := json.Unmarshal(b, &pr); err != nil {
		t.Fatalf("unmarshal promptResult: %v", err)
	}
	if pr.StopReason != "end_turn" {
		t.Errorf("want stopReason=end_turn, got %q", pr.StopReason)
	}
}

// ─── TestACP_InvalidJSON ──────────────────────────────────────────────────────

func TestACP_InvalidJSON(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	// Send garbage then a valid request — server must survive.
	ts.inW.Write([]byte("not json\n"))  //nolint:errcheck
	ts.inW.Write([]byte("{bad json\n")) //nolint:errcheck
	id := json.RawMessage("1")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "initialize"})

	// Drain until we see a successful initialize response.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp := ts.recvResponse(t, 2*time.Second)
		if resp.Error == nil {
			return // initialize succeeded — server survived bad input
		}
		// parse error or method not found — keep reading
		if resp.Error.Code != -32700 && resp.Error.Code != -32601 {
			t.Errorf("unexpected error code: %d", resp.Error.Code)
		}
	}
	t.Fatal("never received successful initialize response after bad input")
}

// ─── TestACP_UnknownMethod ────────────────────────────────────────────────────

func TestACP_UnknownMethod(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	id := json.RawMessage("1")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "no_such_method"})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected error response for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("want code -32601, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "method not found") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// ─── TestACP_Prompt_MissingSessionID ───────────────────────────────────────────

func TestACP_Prompt_MissingSessionID(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "session/prompt",
		Params:  json.RawMessage(`{"prompt":[{"type":"text","text":"hi"}]}`),
	})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected error for missing sessionId")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("want code -32602, got %d", resp.Error.Code)
	}
}

// ─── TestACP_Prompt_MissingPrompt ──────────────────────────────────────────────

func TestACP_Prompt_MissingPrompt(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"prompt":[]}`, sessionID)),
	})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected error for empty prompt")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("want code -32602, got %d", resp.Error.Code)
	}
}

// ─── TestACP_Prompt_UnknownSession ─────────────────────────────────────────────

func TestACP_Prompt_UnknownSession(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "session/prompt",
		Params:  textPrompt("nonexistent", "hi"),
	})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected error for unknown session")
	}
	if resp.Error.Code != -32001 {
		t.Errorf("want code -32001, got %d", resp.Error.Code)
	}
}

// ─── TestACP_Prompt_NonTextBlock_Rejected ──────────────────────────────────────

func TestACP_Prompt_NonTextBlock_Rejected(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "image", "data": "xx", "mimeType": "image/png"},
		},
	})
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "session/prompt", Params: params})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected error for a non-text content block")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("want code -32602, got %d", resp.Error.Code)
	}
}

// ─── TestACP_Prompt_MultipleTextBlocks_Joined ──────────────────────────────────

func TestACP_Prompt_MultipleTextBlocks_Joined(t *testing.T) {
	var gotQuery string
	captureRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		gotQuery = query
		return "ok", nil
	})
	ts, cancel := newTestServer(loadTestConfig(t), captureRunFunc)
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": "part one"},
			{"type": "text", "text": "part two"},
		},
	})
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "session/prompt", Params: params})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if want := "part one\n\npart two"; gotQuery != want {
		t.Errorf("want joined query %q, got %q", want, gotQuery)
	}
}

// ─── TestACP_Cancel_UnknownSession_NoResponse ──────────────────────────────────

func TestACP_Cancel_UnknownSession_NoResponse(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	// session/cancel is a notification per the ACP spec — no response ever,
	// not even an error for an unknown session (unlike the old request/
	// response agent/cancel, which returned -32001 here).
	ts.send(t, json.RawMessage(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"nonexistent"}}`))

	select {
	case line, ok := <-ts.lines:
		if ok {
			t.Fatalf("expected no response to session/cancel, got: %s", line)
		}
	case <-time.After(300 * time.Millisecond):
		// No output arrived — correct.
	}

	// The server must still be alive and processing real requests.
	id := json.RawMessage("1")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "initialize"})
	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("server did not survive session/cancel: %+v", resp.Error)
	}
}

// ─── TestACP_Cancel_MidRun_StopsWithCancelledStopReason ────────────────────────

func TestACP_Cancel_MidRun_StopsWithCancelledStopReason(t *testing.T) {
	started := make(chan struct{})
	blockingRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	ts, cancel := newTestServer(loadTestConfig(t), blockingRunFunc)
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hi"),
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session/prompt's runFunc never started")
	}

	ts.send(t, json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":%q}}`, sessionID)))

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("unexpected error after cancel: %+v", resp.Error)
	}
	b, _ := json.Marshal(resp.Result)
	var pr promptResult
	json.Unmarshal(b, &pr) //nolint:errcheck
	if pr.StopReason != "cancelled" {
		t.Errorf("want stopReason=cancelled, got %q", pr.StopReason)
	}
}

// ─── TestACP_RunFunc_PanicDoesNotCrashServer ───────────────────────────────────

func TestACP_RunFunc_PanicDoesNotCrashServer(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), panicRunFunc("boom"))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hi"),
	})

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error == nil {
		t.Fatal("expected an error response after the runFunc panic")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("want code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "agent panic") {
		t.Errorf("want a panic-flavored error message, got %q", resp.Error.Message)
	}

	// The real point of this test: the server process itself must still be
	// alive and answering other requests, not just this one session.
	id2 := json.RawMessage("3")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id2, Method: "initialize"})
	initResp := ts.recvResponse(t, 2*time.Second)
	if initResp.Error != nil {
		t.Fatalf("server did not survive the panic: %+v", initResp.Error)
	}
}

// ─── TestACP_OversizedLine_DoesNotKillServer ───────────────────────────────────

func TestACP_OversizedLine_DoesNotKillServer(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	// Bigger than the old fixed bufio.Scanner buffer (1<<20 bytes) this
	// replaces — that scanner died permanently on a line over its buffer
	// size (bufio.ErrTooLong), silently dropping every request afterward.
	huge := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":"` + strings.Repeat("x", 2<<20) + `"}` + "\n"
	if _, err := ts.inW.Write([]byte(huge)); err != nil {
		t.Fatalf("write huge line: %v", err)
	}

	resp := ts.recvResponse(t, 3*time.Second)
	if resp.Error != nil {
		t.Fatalf("oversized-but-valid line should still be processed, got error: %+v", resp.Error)
	}

	// Confirm the server is still alive for subsequent requests too.
	id := json.RawMessage("2")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "initialize"})
	resp2 := ts.recvResponse(t, 2*time.Second)
	if resp2.Error != nil {
		t.Fatalf("server did not survive the oversized line: %+v", resp2.Error)
	}
}

// ─── TestACP_Run_RespectsContextCancellation ───────────────────────────────────

func TestACP_Run_RespectsContextCancellation(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()
	outR, outW := io.Pipe()
	go io.Copy(io.Discard, outR) //nolint:errcheck

	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("", nil), inR, outW)
	ctx, cancel := context.WithCancel(context.Background())

	runReturned := make(chan error, 1)
	go func() {
		runReturned <- srv.Run(ctx, nil, nil)
	}()

	// Give Run a moment to start blocking on the read — in stays open the
	// whole time, so only ctx cancellation should make Run return.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runReturned:
		if err != context.Canceled {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation, even though in stayed open")
	}
}

// ─── TestACP_Run_WaitsForInFlightSessionPromptOnEOF ────────────────────────────

func TestACP_Run_WaitsForInFlightSessionPromptOnEOF(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	slowRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		close(started)
		<-release
		return "done", nil
	})

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	srv := NewServerWithIO(loadTestConfig(t), slowRunFunc, inR, outW)

	runReturned := make(chan error, 1)
	go func() {
		runReturned <- srv.Run(context.Background(), nil, nil)
	}()

	send := func(v interface{}) {
		b, _ := json.Marshal(v)
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	recvLine := func() string {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("output closed")
			}
			return line
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for output")
			return ""
		}
	}

	send(acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"new"`),
		Method:  "session/new",
		Params:  json.RawMessage(`{"cwd":"/tmp","mcpServers":[]}`),
	})
	var newResp acpResponse
	json.Unmarshal([]byte(recvLine()), &newResp) //nolint:errcheck
	var newRes newSessionResult
	b, _ := json.Marshal(newResp.Result)
	json.Unmarshal(b, &newRes) //nolint:errcheck
	if newRes.SessionID == "" {
		t.Fatal("expected non-empty sessionId")
	}

	send(acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "session/prompt",
		Params:  textPrompt(newRes.SessionID, "hello"),
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session/prompt never started")
	}

	// EOF while the session/prompt call is still blocked inside runFunc.
	inW.Close()

	select {
	case err := <-runReturned:
		t.Fatalf("Run returned (%v) before the in-flight session/prompt finished", err)
	case <-time.After(200 * time.Millisecond):
		// Good — Run is still waiting on the in-flight call.
	}

	close(release)

	select {
	case <-runReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the in-flight session/prompt finished")
	}
}

// ─── TestACP_StringID_RoundTrips ───────────────────────────────────────────────

func TestACP_StringID_RoundTrips(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	ts.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":"abc-123","method":"initialize"}`))

	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if string(resp.ID) != `"abc-123"` {
		t.Errorf("want id %q, got %q", `"abc-123"`, string(resp.ID))
	}
}

// ─── TestACP_EventOrdering_ResponseArrivesAfterAllUpdates ──────────────────────

func TestACP_EventOrdering_ResponseArrivesAfterAllUpdates(t *testing.T) {
	const numEvents = 5
	ts, cancel := newTestServer(loadTestConfig(t), eventPublishingRunFunc(numEvents, "done"))
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hello"),
	})

	seenUpdates := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line := ts.recv(t, 3*time.Second)
		var probe struct {
			Method string `json:"method"`
		}
		json.Unmarshal([]byte(line), &probe) //nolint:errcheck
		if probe.Method == "session/update" {
			seenUpdates++
			continue
		}
		// No method → this is the session/prompt response.
		if seenUpdates != numEvents {
			t.Errorf("session/prompt response arrived after only %d/%d session/update notifications", seenUpdates, numEvents)
		}
		return
	}
	t.Fatal("never received the session/prompt response")
}

// ─── TestACP_Notification_GetsNoResponse ───────────────────────────────────────

func TestACP_Notification_GetsNoResponse(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	// A JSON-RPC 2.0 Notification (no "id" member at all) must never
	// receive a response, even for a method that would otherwise error.
	ts.send(t, json.RawMessage(`{"jsonrpc":"2.0","method":"no_such_method"}`))

	select {
	case line, ok := <-ts.lines:
		if ok {
			t.Fatalf("expected no response to a notification, got: %s", line)
		}
	case <-time.After(300 * time.Millisecond):
		// No output arrived — correct.
	}

	// The server must still be alive and processing real requests.
	id := json.RawMessage("1")
	ts.send(t, acpRequest{JSONRPC: "2.0", ID: id, Method: "initialize"})
	resp := ts.recvResponse(t, 2*time.Second)
	if resp.Error != nil {
		t.Fatalf("server did not survive the notification: %+v", resp.Error)
	}
}

// ─── TestACP_ExplicitNullID_StillRespondsWithNull ──────────────────────────────

func TestACP_ExplicitNullID_StillRespondsWithNull(t *testing.T) {
	ts, cancel := newTestServer(loadTestConfig(t), stubRunFunc("", nil))
	defer cancel()

	// An explicit `"id": null` is a real request, not a Notification — the
	// server must still respond, with id: null, per the JSON-RPC 2.0 spec.
	ts.send(t, json.RawMessage(`{"jsonrpc":"2.0","id":null,"method":"initialize"}`))

	resp := ts.recvResponse(t, 2*time.Second)
	if string(resp.ID) != "null" {
		t.Errorf("want id \"null\", got %q", string(resp.ID))
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

// ─── TestACP_Run_NoWaitGroupRaceOnConcurrentCancelAndDispatch ──────────────────

// TestACP_Run_NoWaitGroupRaceOnConcurrentCancelAndDispatch stresses the exact
// interleaving the round-2 review flagged: ctx cancellation racing the
// reader goroutine's own s.wg.Add(1) calls. It doesn't assert much on its
// own — the point is `go test -race` catching a concurrent Add/Wait if the
// shutdownMu guard in Run ever regresses.
func TestACP_Run_NoWaitGroupRaceOnConcurrentCancelAndDispatch(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()
	outR, outW := io.Pipe()
	go io.Copy(io.Discard, outR) //nolint:errcheck

	srv := NewServerWithIO(loadTestConfig(t), stubRunFunc("ok", nil), inR, outW)
	ctx, cancel := context.WithCancel(context.Background())

	runReturned := make(chan error, 1)
	go func() {
		runReturned <- srv.Run(ctx, nil, nil)
	}()

	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			req := acpRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", i)), Method: "initialize"}
			b, _ := json.Marshal(req)
			if _, err := inW.Write(append(b, '\n')); err != nil {
				return
			}
		}
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	close(stop)

	select {
	case <-runReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation under concurrent dispatch")
	}
}

// ─── TestACP_ConcurrentSessionPrompts_NoRace ───────────────────────────────────

// TestACP_ConcurrentSessionPrompts_NoRace fires two concurrent session/prompt
// calls against the same server (and therefore the same pinned *config.Config
// — see Server.cfg) and checks they don't cross-talk. Verifies the assumption
// that executeConfig only reads cfg, documented but not otherwise proven, in
// internal/acp/server.go's Server.cfg comment. Run with -race.
func TestACP_ConcurrentSessionPrompts_NoRace(t *testing.T) {
	echoRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		return "echo:" + query, nil
	})
	ts, cancel := newTestServer(loadTestConfig(t), echoRunFunc)
	defer cancel()

	sessionA := newSessionHelper(t, ts)

	// A second session/new, sent with a distinct id so it doesn't race the
	// first's response on ts.lines (sequential like every other test here).
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"new2"`),
		Method:  "session/new",
		Params:  json.RawMessage(`{"cwd":"/tmp","mcpServers":[]}`),
	})
	resp := ts.recvResponse(t, 2*time.Second)
	b, _ := json.Marshal(resp.Result)
	var newRes2 newSessionResult
	json.Unmarshal(b, &newRes2) //nolint:errcheck
	sessionB := newRes2.SessionID

	var wg sync.WaitGroup
	results := make(chan acpResponse, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage("10"), Method: "session/prompt", Params: textPrompt(sessionA, "from-a")})
	}()
	go func() {
		defer wg.Done()
		ts.send(t, acpRequest{JSONRPC: "2.0", ID: json.RawMessage("11"), Method: "session/prompt", Params: textPrompt(sessionB, "from-b")})
	}()
	wg.Wait()

	// Each session/prompt now also emits one synthetic agent_message_chunk
	// session/update (echoRunFunc emits no events, same non-streaming
	// fallback as TestACP_Prompt_NonStreamingResultReachesClient), so 4
	// lines arrive total, interleaved unpredictably across the two
	// concurrent turns — skip notifications, collect only the 2 responses.
	for len(results) < 2 {
		line := ts.recv(t, 3*time.Second)
		var probe struct {
			Method string `json:"method"`
		}
		json.Unmarshal([]byte(line), &probe) //nolint:errcheck
		if probe.Method != "" {
			continue // session/update notification
		}
		var resp acpResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal response: %v (line=%q)", err, line)
		}
		results <- resp
	}
	close(results)

	seen := map[string]bool{}
	for r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %+v", r.Error)
		}
		var pr promptResult
		b, _ := json.Marshal(r.Result)
		json.Unmarshal(b, &pr) //nolint:errcheck
		if pr.StopReason != "end_turn" {
			t.Errorf("want stopReason=end_turn, got %q", pr.StopReason)
		}
		seen[string(r.ID)] = true
	}
	if !seen["10"] || !seen["11"] {
		t.Errorf("expected responses for both request ids 10 and 11, got %v", seen)
	}
}

// ─── TestACP_Prompt_SecondTurnSeesFirstTurnHistory ─────────────────────────────

// Regression: handlePrompt called runFunc with only the current turn's raw
// query text — session/new mints one session identity that multiple
// session/prompt calls can be sent against, but nothing threaded prior
// turns' content into a later call, so each turn was functionally a
// brand-new, context-free run. Reported live against a real ACP client:
// "each chat reply is disconnected, not in a session." Proves the second
// turn's runFunc call actually receives the first turn's question and
// answer, not just the new question.
func TestACP_Prompt_SecondTurnSeesFirstTurnHistory(t *testing.T) {
	var receivedQueries []string
	echoRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		receivedQueries = append(receivedQueries, query)
		return fmt.Sprintf("answer-%d", len(receivedQueries)), nil
	})
	ts, cancel := newTestServer(loadTestConfig(t), echoRunFunc)
	defer cancel()

	sessionID := newSessionHelper(t, ts)

	ts.send(t, acpRequest{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "session/prompt",
		Params: textPrompt(sessionID, "what is 2+2"),
	})
	// echoRunFunc emits no events, so the non-streaming fallback (see
	// TestACP_Prompt_NonStreamingResultReachesClient) sends one synthetic
	// agent_message_chunk session/update before each response.
	ts.recvNotification(t, 2*time.Second)
	resp1 := ts.recvResponse(t, 2*time.Second)
	if resp1.Error != nil {
		t.Fatalf("first prompt error: %+v", resp1.Error)
	}

	ts.send(t, acpRequest{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "session/prompt",
		Params: textPrompt(sessionID, "what did I just ask"),
	})
	ts.recvNotification(t, 2*time.Second)
	resp2 := ts.recvResponse(t, 2*time.Second)
	if resp2.Error != nil {
		t.Fatalf("second prompt error: %+v", resp2.Error)
	}

	if len(receivedQueries) != 2 {
		t.Fatalf("runFunc invoked %d times, want 2", len(receivedQueries))
	}
	if receivedQueries[0] != "what is 2+2" {
		t.Errorf("first turn has no prior history — query should be unmodified, got %q", receivedQueries[0])
	}
	second := receivedQueries[1]
	if !strings.Contains(second, "what is 2+2") {
		t.Errorf("second turn's query should include the first turn's question, got %q", second)
	}
	if !strings.Contains(second, "answer-1") {
		t.Errorf("second turn's query should include the first turn's answer, got %q", second)
	}
	if !strings.Contains(second, "what did I just ask") {
		t.Errorf("second turn's query should still include the new question, got %q", second)
	}
}

// ─── TestACP_RunFunc_PanicRecoveryFlushesUpdatesAndDoesNotLeakDrainGoroutine ───

// Regression: the panic-recovery path in handlePrompt wrote the error
// response directly, without unsubscribing from the event bus or waiting for
// the drain goroutine first. That broke two things at once: the response
// could arrive before pending session/update notifications were flushed,
// and — since eventCh was never closed — the drain goroutine's `for event :=
// range eventCh` blocked forever, leaking one goroutine (and its EventBus)
// per panicking run for the life of the process.
//
// Calls handlePrompt directly (bypassing Run's reader loop and
// newTestServer's own pipe/scanner goroutines entirely) so the only
// goroutines in play are the ones handlePrompt itself spawns — otherwise the
// harness's own long-lived plumbing swamps the specific leak this test
// exists to catch. handlePrompt is fully synchronous now (no inner
// background goroutine the way the old handleRun had), so no s.wg.Wait is
// needed after calling it — by the time it returns, everything, including
// cleanupEvents, has already finished.
func TestACP_RunFunc_PanicRecoveryFlushesUpdatesAndDoesNotLeakDrainGoroutine(t *testing.T) {
	const numEvents = 5
	const numRuns = 20

	runtime.GC()
	before := runtime.NumGoroutine()

	cfg := loadTestConfig(t)
	for i := 0; i < numRuns; i++ {
		var buf syncBuffer
		s := NewServerWithIO(cfg, panicAfterEventsRunFunc(numEvents, "boom"), nil, &buf)
		s.sessions.add(newSession("s1"))
		req := acpRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage("1"),
			Method:  "session/prompt",
			Params:  textPrompt("s1", "hello"),
		}
		s.handlePrompt(context.Background(), req)

		seenUpdates := 0
		gotResponse := false
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var probe struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal([]byte(line), &probe); err != nil {
				continue
			}
			if probe.Method == "session/update" {
				seenUpdates++
				continue
			}
			gotResponse = true // no method → the session/prompt error response
		}
		if seenUpdates != numEvents {
			t.Errorf("run %d: got %d/%d session/update notifications before the response", i, seenUpdates, numEvents)
		}
		if !gotResponse {
			t.Fatalf("run %d: no session/prompt response found in output: %s", i, buf.String())
		}
	}

	// handlePrompt returning already guarantees the drain goroutine saw
	// eventCh close and its range loop returned (cleanupEvents awaits
	// drainDone synchronously before the error response is written) — the
	// only remaining slack is the scheduler actually retiring the
	// now-finished goroutine before NumGoroutine's next snapshot.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Errorf("goroutine count grew from %d to %d after %d panicking runs — the event-drain goroutine may be leaking", before, after, numRuns)
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes writeLine
// makes under s.mu — reads are only ever done here after the writer(s) have
// already finished.
type syncBuffer struct {
	bytes.Buffer
}

// ─── TestACP_Prompt_RespectsConfiguredTimeoutSeconds ───────────────────────────

// Regression: handlePrompt hardcoded every turn to defaultPromptTimeout
// (300s) and never consulted the pinned config's own
// settings.execution.timeout_seconds — silently contradicting
// cmd/rakitsu/acp.go's help text, which claims a session runs "the same way"
// rakitsu run does. Proves a short configured timeout is actually honored
// instead of overridden to 300s: recvResponse's own deadline below is the
// proof — if the 300s default were still in effect, the response would never
// arrive in time and the test would fail on that timeout, not on the
// assertion.
func TestACP_Prompt_RespectsConfiguredTimeoutSeconds(t *testing.T) {
	cfg := loadTestConfig(t)
	cfg.Settings.Execution.TimeoutSeconds = 1 // far shorter than defaultPromptTimeout

	started := make(chan struct{})
	blockingRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})

	ts, cancel := newTestServer(cfg, blockingRunFunc)
	defer cancel()

	sessionID := newSessionHelper(t, ts)
	ts.send(t, acpRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "session/prompt",
		Params:  textPrompt(sessionID, "hi"),
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session/prompt's runFunc never started")
	}

	resp := ts.recvResponse(t, 3*time.Second)
	if resp.Error == nil {
		t.Fatal("expected an error response once the configured 1s timeout elapsed")
	}
}

// ─── TestPromptTimeout_OverridePrecedence ──────────────────────────────────────

// Regression: promptTimeout must give an explicit --timeout override (see
// cmd/rakitsu/acp.go's SetTimeoutOverride) precedence over
// settings.execution.timeout_seconds, and the override follows run.go's
// <=0-disables convention directly (no "0 means unset" substitution, unlike
// the config field, since a cobra flag can't carry "unset" the way a YAML
// zero-value can't be told apart from "not set").
func TestPromptTimeout_OverridePrecedence(t *testing.T) {
	overrideOf := func(sec int) *int { return &sec }

	tests := []struct {
		name       string
		cfgSeconds int
		override   *int
		wantOK     bool
		wantDur    time.Duration
	}{
		{"no override, config unset -> default", 0, nil, true, defaultPromptTimeout},
		{"no override, config negative -> disabled", -1, nil, false, 0},
		{"no override, config positive -> as configured", 7, nil, true, 7 * time.Second},
		{"override positive beats configured positive", 7, overrideOf(42), true, 42 * time.Second},
		{"override positive beats configured negative (disabled)", -1, overrideOf(5), true, 5 * time.Second},
		{"override zero disables regardless of config default", 0, overrideOf(0), false, 0},
		{"override negative disables regardless of positive config", 99, overrideOf(-1), false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadTestConfig(t)
			cfg.Settings.Execution.TimeoutSeconds = tt.cfgSeconds

			d, ok := promptTimeout(cfg, tt.override)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && d != tt.wantDur {
				t.Fatalf("duration = %v, want %v", d, tt.wantDur)
			}
		})
	}
}

// ─── TestACP_Prompt_RejectsConcurrentPromptOnSameSession ───────────────────────

// Regression: handlePrompt dispatches on its own goroutine per request (see
// Run's reader loop), so two session/prompt calls for the same sessionId
// could run concurrently. Session.startTurn had no in-flight guard — a
// second startTurn silently overwrote the first turn's cancel func, and
// whichever turn's endTurn() ran first nilled it out from under the other,
// so a subsequent session/cancel could hit the wrong turn or none at all.
// Proves the second, overlapping prompt is rejected before it ever reaches
// runFunc, not merely that it comes back with *some* error.
//
// runCount (not a shared close-once channel) is deliberate: an earlier
// version of this test had both goroutines share firstStarted/releaseFirst
// directly in the runFunc body, so a broken guard let the second call reach
// runFunc too and panic on close(firstStarted) being called twice — caught
// by handlePrompt's own panic recovery and reported as *an* error, which
// satisfied a bare "Error != nil" check for the wrong reason. runCount plus
// the exact "session busy" message assertion below make sure this test can
// only pass because the guard fired, not because of an unrelated panic.
func TestACP_Prompt_RejectsConcurrentPromptOnSameSession(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var runCount atomic.Int32
	blockingRunFunc := RunFunc(func(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable)) (string, error) {
		if runCount.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return "done", nil
	})

	cfg := loadTestConfig(t)
	var buf syncBuffer
	s := NewServerWithIO(cfg, blockingRunFunc, nil, &buf)
	s.sessions.add(newSession("s1"))

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		s.handlePrompt(context.Background(), acpRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`"first"`), Method: "session/prompt",
			Params: textPrompt("s1", "first"),
		})
	}()

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first session/prompt's runFunc never started")
	}

	// The second prompt against the same session, while the first is still
	// in flight, must return promptly on its own — it must not block on the
	// first turn's blockingRunFunc, which proves it was rejected up front
	// rather than queued or left to race the first.
	second := make(chan struct{})
	go func() {
		defer close(second)
		s.handlePrompt(context.Background(), acpRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`"second"`), Method: "session/prompt",
			Params: textPrompt("s1", "second"),
		})
	}()

	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("second session/prompt did not return promptly — want an immediate rejection, not blocking on the first turn")
	}

	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first session/prompt never finished after being released")
	}

	var firstResp, secondResp acpResponse
	var sawFirst, sawSecond bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var resp acpResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		switch string(resp.ID) {
		case `"first"`:
			firstResp, sawFirst = resp, true
		case `"second"`:
			secondResp, sawSecond = resp, true
		}
	}
	if !sawSecond {
		t.Fatal("no response observed for the second, overlapping session/prompt")
	}
	if secondResp.Error == nil {
		t.Fatal("expected an error rejecting the overlapping session/prompt, got a success response")
	}
	if !strings.Contains(secondResp.Error.Message, "session busy") {
		t.Errorf("want a \"session busy\" rejection, got: %+v", secondResp.Error)
	}
	if !sawFirst {
		t.Fatal("no response observed for the first session/prompt")
	}
	if firstResp.Error != nil {
		t.Errorf("first (legitimate) turn should have succeeded once released, got error: %+v", firstResp.Error)
	}
	// The real proof the guard fired, not just that some error came back:
	// runFunc must have been invoked exactly once. If startTurn's in-flight
	// guard ever regresses, the second prompt reaches runFunc too and this
	// count goes to 2 regardless of what error (if any) the response carries.
	if got := runCount.Load(); got != 1 {
		t.Errorf("runFunc invoked %d times — want exactly 1: the rejected prompt must never reach runFunc", got)
	}
}

// ─── TestSessionUpdateFromEvent_ToolCallEnd_ExitCode ───────────────────────────

// TestSessionUpdateFromEvent_ToolCallEnd_ExitCode covers ToolCallEndPayload's
// ExitCode being meaningless for non-process tools (fs, mcp_server, a2a,
// memory), which never set it — the zero value is indistinguishable from a
// real process that exited 0, so it must be omitted rather than asserted.
func TestSessionUpdateFromEvent_ToolCallEnd_ExitCode(t *testing.T) {
	newEvent := func(exitCode int) telemetry.AgentEvent {
		payload, _ := json.Marshal(telemetry.ToolCallEndPayload{
			ToolCallID: "tc-1",
			ToolName:   "read-file",
			Output:     "contents",
			ExitCode:   exitCode,
		})
		return telemetry.AgentEvent{EventType: telemetry.EventToolCallEnd, Payload: payload}
	}

	t.Run("zero exit code omitted", func(t *testing.T) {
		update, ok := sessionUpdateFromEvent(newEvent(0))
		if !ok {
			t.Fatal("expected ok=true")
		}
		tc, ok := update.(toolCallPayload)
		if !ok {
			t.Fatalf("want toolCallPayload, got %T", update)
		}
		if _, present := tc.RawOutput["exitCode"]; present {
			t.Errorf("exitCode=0 should be omitted (ambiguous: real 0 exit vs. non-process tool), got %v", tc.RawOutput)
		}
	})

	t.Run("nonzero exit code present", func(t *testing.T) {
		update, ok := sessionUpdateFromEvent(newEvent(1))
		if !ok {
			t.Fatal("expected ok=true")
		}
		tc, ok := update.(toolCallPayload)
		if !ok {
			t.Fatalf("want toolCallPayload, got %T", update)
		}
		if got, present := tc.RawOutput["exitCode"]; !present || got != 1 {
			t.Errorf("want exitCode=1 present, got %v (present=%v)", got, present)
		}
	})
}
