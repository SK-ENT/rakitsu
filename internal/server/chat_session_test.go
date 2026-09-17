package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// fakeRunner is a controllable agent.Runner used to drive ChatSession tests.
//
// The Run closure is set per test; it receives ctx+query and a bus handle so
// it can emit token chunks like a real provider would. Cancellation is
// delivered via ctx.
type fakeRunner struct {
	name string
	bus  *telemetry.EventBus
	run  func(ctx context.Context, query string, bus *telemetry.EventBus) (string, error)
}

func (f *fakeRunner) Run(ctx context.Context, query string) (string, error) {
	if f.run == nil {
		return "ok", nil
	}
	return f.run(ctx, query, f.bus)
}
func (f *fakeRunner) GetName() string                             { return f.name }
func (f *fakeRunner) GetRole() agent.AgentRole                    { return agent.RoleWorker }
func (f *fakeRunner) GetTools() []string                          { return nil }
func (f *fakeRunner) SetDebugController(_ *debug.DebugController) {}

func makeFakeChatBuildFunc(name string, runFn func(ctx context.Context, query string, bus *telemetry.EventBus) (string, error), userInputHandler func(chan userinput.InputRequest, chan string)) ChatBuildFunc {
	return func(
		ctx context.Context,
		cfg *config.Config,
		eventBus *telemetry.EventBus,
		userReq chan userinput.InputRequest,
		userResp chan string,
		sessionID string,
		selfURL string,
	) (agent.Runner, agent.Runner, func(), string, string, error) {
		if userInputHandler != nil {
			go userInputHandler(userReq, userResp)
		}
		r := &fakeRunner{name: name, bus: eventBus, run: runFn}
		// Tests use a single fakeRunner — runner and innerRunner are the same.
		return r, r, func() {}, name, "fake", nil
	}
}

func startSession(t *testing.T, bf ChatBuildFunc) *ChatSession {
	t.Helper()
	cfg := &config.Config{
		Name:   "test",
		Agents: []config.AgentDefinition{{Name: "a1"}},
	}
	sess, err := StartChatSession(context.Background(), ChatSessionOptions{
		ConfigID:  "test",
		Cfg:       cfg,
		BuildFunc: bf,
	})
	if err != nil {
		t.Fatalf("StartChatSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// collector captures broadcast messages by attaching a chatClient stub.
type collector struct {
	mu   sync.Mutex
	msgs []serverMsg
	c    *chatClient
}

func newCollector(sess *ChatSession) *collector {
	col := &collector{}
	col.c = &chatClient{
		session: sess,
		sendCh:  make(chan serverMsg, 256),
		closed:  make(chan struct{}),
	}
	sess.attach(col.c)
	go func() {
		for {
			select {
			case m, ok := <-col.c.sendCh:
				if !ok {
					return
				}
				col.mu.Lock()
				col.msgs = append(col.msgs, m)
				col.mu.Unlock()
			case <-col.c.closed:
				// Drain any buffered messages before exiting — the
				// close signal can race with a final broadcast.
				for {
					select {
					case m := <-col.c.sendCh:
						col.mu.Lock()
						col.msgs = append(col.msgs, m)
						col.mu.Unlock()
					default:
						return
					}
				}
			}
		}
	}()
	return col
}

func (c *collector) waitFor(pred func([]serverMsg) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := pred(c.msgs)
		c.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (c *collector) snapshot() []serverMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]serverMsg, len(c.msgs))
	copy(out, c.msgs)
	return out
}

// TestSimpleTurnRoundTrip — submit, receive turn_started and turn_done.
func TestSimpleTurnRoundTrip(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "hello " + q, nil
		},
		nil,
	))
	col := newCollector(sess)

	if err := sess.Submit("world"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ok := col.waitFor(func(msgs []serverMsg) bool {
		var started, done bool
		for _, m := range msgs {
			if m.Type == "turn_started" {
				started = true
			}
			if m.Type == "turn_done" && m.Final == "hello world" {
				done = true
			}
		}
		return started && done
	}, 2*time.Second)
	if !ok {
		t.Fatalf("expected turn_started + turn_done(final=hello world), got %+v", col.snapshot())
	}
}

// TestTokenChunksForwarded — TOKEN_CHUNK telemetry events reach the client as
// "token" messages tagged with the current gen_token.
func TestTokenChunksForwarded(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			bus.Emit("a1", telemetry.EventTokenChunk, telemetry.TokenChunkPayload{Text: "hel"})
			bus.Emit("a1", telemetry.EventTokenChunk, telemetry.TokenChunkPayload{Text: "lo"})
			time.Sleep(10 * time.Millisecond) // let forwarder drain
			return "hello", nil
		},
		nil,
	))
	col := newCollector(sess)
	if err := sess.Submit("hi"); err != nil {
		t.Fatal(err)
	}

	ok := col.waitFor(func(msgs []serverMsg) bool {
		var tokens []string
		var tok int64
		for _, m := range msgs {
			if m.Type == "token" {
				tokens = append(tokens, m.Text)
				tok = m.GenToken
			}
		}
		return len(tokens) == 2 && tokens[0] == "hel" && tokens[1] == "lo" && tok > 0
	}, 2*time.Second)
	if !ok {
		t.Fatalf("expected two token messages, got %+v", col.snapshot())
	}
}

// TestInterruptCancelsRun — Interrupt() cancels the Runner's ctx and the turn
// ends with an error (fluid-chat semantics).
func TestInterruptCancelsRun(t *testing.T) {
	running := make(chan struct{})
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			close(running)
			<-ctx.Done()
			return "", ctx.Err()
		},
		nil,
	))
	col := newCollector(sess)

	if err := sess.Submit("block"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("runner never started")
	}
	sess.Interrupt()

	ok := col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "turn_done" && m.Err != "" {
				return true
			}
		}
		return false
	}, 2*time.Second)
	if !ok {
		t.Fatalf("expected turn_done with error after interrupt, got %+v", col.snapshot())
	}
}

// TestFluidChatInterruptAndResubmit — Submit during an active turn cancels
// the prior turn and starts a new one with a fresh gen_token.
func TestFluidChatInterruptAndResubmit(t *testing.T) {
	var callCount int
	var mu sync.Mutex
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			mu.Lock()
			callCount++
			n := callCount
			mu.Unlock()
			if n == 1 {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "final-" + q, nil
		},
		nil,
	))
	col := newCollector(sess)

	if err := sess.Submit("first"); err != nil {
		t.Fatal(err)
	}
	// Give first turn a chance to start.
	time.Sleep(20 * time.Millisecond)
	if err := sess.Submit("second"); err != nil {
		t.Fatal(err)
	}

	ok := col.waitFor(func(msgs []serverMsg) bool {
		var tokens []int64
		var finalSeen bool
		for _, m := range msgs {
			if m.Type == "turn_started" {
				tokens = append(tokens, m.GenToken)
			}
			// Phase 0: the resubmitted turn now carries multi-turn history,
			// so the runner sees a composed query ending in "second" rather
			// than the bare word — match the suffix.
			if m.Type == "turn_done" && strings.HasSuffix(m.Final, "second") {
				finalSeen = true
			}
		}
		return len(tokens) >= 2 && tokens[1] > tokens[0] && finalSeen
	}, 3*time.Second)
	if !ok {
		t.Fatalf("expected interrupt+resubmit, got %+v", col.snapshot())
	}
}

// TestUserInputRoundTrip — agent asks, client responds, tool returns answer.
func TestUserInputRoundTrip(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			// Imitate a user_input tool: send a request and wait for answer.
			// The handler channels come via the build func closure.
			return "unused", nil
		},
		func(reqCh chan userinput.InputRequest, respCh chan string) {
			// This handler simulates the user_input tool. Not directly used
			// by this test — the test drives the channels manually.
			_ = reqCh
			_ = respCh
		},
	))

	col := newCollector(sess)

	// Inject a pending user_input request manually as the tool would.
	go func() {
		sess.userInputReqCh <- userinput.InputRequest{Question: "name?", Default: "world"}
	}()

	// Wait for the bridge to broadcast it.
	var reqID string
	ok := col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "user_input_request" {
				reqID = m.RequestID
				return m.Question == "name?" && m.Default == "world"
			}
		}
		return false
	}, time.Second)
	if !ok {
		t.Fatalf("expected user_input_request, got %+v", col.snapshot())
	}

	// Respond.
	sess.RespondUserInput(reqID, "Paulus")
	select {
	case got := <-sess.userInputRespCh:
		if got != "Paulus" {
			t.Fatalf("expected response 'Paulus', got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("response was not delivered to userInputRespCh")
	}
}

// TestCloseBroadcastsClosed — after Close() clients receive a "closed" msg.
func TestCloseBroadcastsClosed(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "", errors.New("unused")
		},
		nil,
	))
	col := newCollector(sess)
	sess.Close()

	ok := col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "closed" {
				return true
			}
		}
		return false
	}, time.Second)
	if !ok {
		t.Fatalf("expected closed msg, got %+v", col.snapshot())
	}
	if !sess.closed.Load() {
		t.Fatal("expected session.closed=true")
	}
}

// TestAssistantEntryCarriesErrorText — regression: when a turn
// ends in error, the persisted assistant transcriptEntry must carry the
// error text (not just Interrupted:true), so REST readers of the turn tree
// (GET /api/chat/{id}/turn/{id}) can see why it failed.
func TestAssistantEntryCarriesErrorText(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "", errors.New("boom")
		},
		nil,
	))
	col := newCollector(sess)
	if err := sess.Submit("hi"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ok := col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "turn_done" && m.Err != "" {
				return true
			}
		}
		return false
	}, 2*time.Second)
	if !ok {
		t.Fatalf("expected turn_done with error, got %+v", col.snapshot())
	}

	var found bool
	for _, e := range sess.snapshotTranscript() {
		if e.Kind == "assistant" {
			found = true
			if !e.Interrupted {
				t.Errorf("assistant entry Interrupted = false, want true")
			}
			if e.Err != "boom" {
				t.Errorf("assistant entry Err = %q, want %q", e.Err, "boom")
			}
		}
	}
	if !found {
		t.Fatal("no assistant entry in transcript")
	}
}

// TestSubmitExternalWaitHappyPath — a synchronous wait gets the turn's reply
// back with no error.
func TestSubmitExternalWaitHappyPath(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "pong", nil
		},
		nil,
	))
	out, err := sess.SubmitExternalWait(context.Background(), "ping", "s1", "A")
	if err != nil {
		t.Fatalf("SubmitExternalWait: %v", err)
	}
	if out.Final != "pong" || out.Interrupted || out.Err != "" {
		t.Fatalf("outcome = %+v, want Final=pong Interrupted=false Err=\"\"", out)
	}
}

// TestSubmitExternalWaitTurnError — a synchronous wait against an erroring
// turn gets Interrupted+Err back, not a Go error (delivery succeeded — the
// turn failed).
func TestSubmitExternalWaitTurnError(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "", errors.New("boom")
		},
		nil,
	))
	out, err := sess.SubmitExternalWait(context.Background(), "ping", "s1", "A")
	if err != nil {
		t.Fatalf("SubmitExternalWait: %v", err)
	}
	if !out.Interrupted || out.Err != "boom" {
		t.Fatalf("outcome = %+v, want Interrupted=true Err=boom", out)
	}
}

// TestSubmitExternalWaitCtxTimeoutTurnStillCompletes — a wait that times out
// returns ctx.Err() promptly WITHOUT cancelling the queued turn; the turn
// keeps running and its result is still recorded once it finishes.
func TestSubmitExternalWaitCtxTimeoutTurnStillCompletes(t *testing.T) {
	release := make(chan struct{})
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			<-release
			return "late-reply", nil
		},
		nil,
	))
	col := newCollector(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := sess.SubmitExternalWait(ctx, "ping", "s1", "A")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SubmitExternalWait error = %v, want context.DeadlineExceeded", err)
	}

	close(release)
	if !col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "turn_done" && m.Final == "late-reply" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		t.Fatalf("turn never completed in the background, got %+v", col.snapshot())
	}
}

// TestSubmitExternalWaitRespectsBusy — the single-slot turn queue rejects a
// second SubmitExternalWait the same way it rejects a second SubmitExternal.
func TestSubmitExternalWaitRespectsBusy(t *testing.T) {
	release := make(chan struct{})
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}, nil))
	defer close(release)
	col := newCollector(sess)

	if err := sess.Submit("first"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !col.waitFor(func(ms []serverMsg) bool {
		for _, m := range ms {
			if m.Type == "turn_started" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		t.Fatal("first turn never started")
	}

	if err := sess.SubmitExternal("one", "s1", "A"); err != nil {
		t.Fatalf("first SubmitExternal should queue: %v", err)
	}
	if _, err := sess.SubmitExternalWait(context.Background(), "two", "s1", "A"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("SubmitExternalWait = %v, want ErrSessionBusy", err)
	}
}

// TestServerMsgRoundTripJSON — ensure the protocol round-trips correctly.
func TestServerMsgRoundTripJSON(t *testing.T) {
	m := serverMsg{
		Type:     "token",
		Text:     "hi",
		GenToken: 42,
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got serverMsg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, m)
	}
}
