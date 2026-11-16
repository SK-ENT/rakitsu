package telemetry

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// EventBus — Subscribe / Publish
// ============================================================

func TestEventBus_Subscribe_ReceivesEvents(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.Emit("agent1", EventAgentStart, struct{}{})

	select {
	case evt := <-ch:
		if evt.AgentName != "agent1" {
			t.Errorf("AgentName = %q, want %q", evt.AgentName, "agent1")
		}
		if evt.EventType != EventAgentStart {
			t.Errorf("EventType = %q, want %q", evt.EventType, EventAgentStart)
		}
		if evt.ID == "" {
			t.Error("ID should be non-empty")
		}
		if evt.Timestamp.IsZero() {
			t.Error("Timestamp should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers_EachReceive(t *testing.T) {
	bus := NewEventBus(8)
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()
	defer bus.Unsubscribe(ch1)
	defer bus.Unsubscribe(ch2)

	bus.Emit("a", EventAgentEnd, struct{}{})

	for _, ch := range []<-chan AgentEvent{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.EventType != EventAgentEnd {
				t.Errorf("expected AgentEnd, got %q", evt.EventType)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestEventBus_NoSubscribers_NoBlock(t *testing.T) {
	bus := NewEventBus(4)
	// Should not block with no subscribers
	done := make(chan struct{})
	go func() {
		bus.Emit("a", EventAgentStart, struct{}{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked with no subscribers")
	}
}

func TestEventBus_FullBuffer_DropsWithoutBlocking(t *testing.T) {
	bus := NewEventBus(2) // tiny buffer
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	done := make(chan struct{})
	go func() {
		// Emit more events than buffer capacity — must not block
		for i := 0; i < 10; i++ {
			bus.Emit("a", EventAgentStart, struct{}{})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on full buffer")
	}
}

// ============================================================
// EventBus — Unsubscribe
// ============================================================

func TestEventBus_Unsubscribe_ClosesChannel(t *testing.T) {
	bus := NewEventBus(4)
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	// Channel should be closed — range over it should return immediately
	count := 0
	for range ch {
		count++
	}
	// No events were sent, so count == 0 and range ended (channel closed)
	_ = count
}

func TestEventBus_Unsubscribe_StopsReceiving(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	// Emit after unsubscribe — the removed subscriber should not receive it
	bus.Emit("a", EventAgentStart, struct{}{})

	// Drain any buffered events (there should be none)
	var received int
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto done
			}
			received++
		default:
			goto done
		}
	}
done:
	if received > 0 {
		t.Errorf("unsubscribed channel received %d events", received)
	}
}

func TestEventBus_SubscribeAfterEmit_NoReplay(t *testing.T) {
	bus := NewEventBus(8)
	bus.Emit("a", EventAgentStart, struct{}{}) // emitted before subscribe

	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	select {
	case <-ch:
		t.Error("should not receive events emitted before Subscribe")
	case <-time.After(50 * time.Millisecond):
		// correct — no replay
	}
}

// ============================================================
// Emit variants — field verification
// ============================================================

func TestEmit_PayloadSerialized(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	type myPayload struct {
		Msg string `json:"msg"`
	}
	bus.Emit("agent", EventAgentStart, myPayload{Msg: "hello"})

	evt := <-ch
	var p myPayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal failed: %v", err)
	}
	if p.Msg != "hello" {
		t.Errorf("Payload.Msg = %q, want %q", p.Msg, "hello")
	}
}

func TestEmitWithTokenUsage_SetsUsage(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.EmitWithTokenUsage("a", EventTokenUsage, struct{}{}, TokenUsage{
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
	})

	evt := <-ch
	if evt.TokenUsage == nil {
		t.Fatal("TokenUsage should be set")
	}
	if evt.TokenUsage.InputTokens != 100 || evt.TokenUsage.OutputTokens != 50 || evt.TokenUsage.TotalTokens != 150 {
		t.Errorf("TokenUsage mismatch: %+v", evt.TokenUsage)
	}
}

func TestEmitWithDuration_SetsDuration(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.EmitWithDuration("a", EventAgentEnd, struct{}{}, 42)

	evt := <-ch
	if evt.Duration != 42 {
		t.Errorf("Duration = %d, want 42", evt.Duration)
	}
}

func TestEmitWithParent_SetsParentID(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.EmitWithParent("a", EventToolCallStart, "parent-123", struct{}{})

	evt := <-ch
	if evt.ParentID != "parent-123" {
		t.Errorf("ParentID = %q, want %q", evt.ParentID, "parent-123")
	}
}

// TestWithOrigin_StampsOriginAndStillReachesSubscribers pins both halves of
// the origin substrate. A derived bus tags what it publishes so a consumer can
// tell a directed agent chat's events from the main run's — by origin, not by
// agent name, which two runs can share. And it forwards to the SAME
// subscribers, so nothing a side chat does stops being written to the session
// JSONL: the file is advertised as the truthful record of side chats.
func TestWithOrigin_StampsOriginAndStillReachesSubscribers(t *testing.T) {
	bus := NewEventBus(8)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	derived := bus.WithOrigin("direct-Reviewer-1")
	derived.Emit("Reviewer", EventToolCallStart, struct{}{})
	bus.Emit("Reviewer", EventToolCallStart, struct{}{})

	side := <-ch
	if side.Origin != "direct-Reviewer-1" {
		t.Errorf("derived-bus event Origin = %q, want direct-Reviewer-1", side.Origin)
	}
	main := <-ch
	if main.Origin != "" {
		t.Errorf("main-run event Origin = %q, want empty — only a directed send is tagged", main.Origin)
	}
}

// busRecvTimeout bounds every receive in this file's bus tests. Both failure
// modes these tests exist to catch — a subscriber registered on a bus nobody
// publishes to, and an Unsubscribe that never closes the channel — present as
// a channel that stays silent forever. Reading such a channel bare turns a
// failing assertion into a hung test: the package times out after ten
// minutes and names the whole run, not the broken behaviour.
const busRecvTimeout = 2 * time.Second

// recvEvent takes one event from ch, failing with msg rather than blocking
// when none arrives.
func recvEvent(t *testing.T, ch <-chan AgentEvent, msg string) AgentEvent {
	t.Helper()
	select {
	case evt, open := <-ch:
		if !open {
			t.Fatalf("%s: channel closed before an event arrived", msg)
		}
		return evt
	case <-time.After(busRecvTimeout):
		t.Fatalf("%s: no event after %s", msg, busRecvTimeout)
		return AgentEvent{}
	}
}

// TestWithOrigin_SharesSubscriberState: Subscribe/Unsubscribe on a derived bus
// must reach the root, or a caller holding only the derived bus would
// silently register a subscriber nothing ever publishes to.
func TestWithOrigin_SharesSubscriberState(t *testing.T) {
	bus := NewEventBus(8)
	derived := bus.WithOrigin("o1")

	ch := derived.Subscribe()
	bus.Emit("a", EventAgentStart, struct{}{})
	evt := recvEvent(t, ch, "Subscribe on a derived bus must register with the root")
	if evt.EventType != EventAgentStart {
		t.Fatalf("EventType = %q, want AGENT_START", evt.EventType)
	}

	derived.Unsubscribe(ch)
	select {
	case _, open := <-ch:
		if open {
			t.Error("Unsubscribe on a derived bus must close the root's subscriber channel")
		}
	case <-time.After(busRecvTimeout):
		t.Fatalf("Unsubscribe on a derived bus left the channel open after %s", busRecvTimeout)
	}
}

// TestWithOrigin_NilBusIsUsable: the roster may hold a nil bus (tests, and any
// caller that wants no telemetry), and deriving from it must stay a no-op
// rather than producing a live-looking bus with no subscribers.
func TestWithOrigin_NilBusIsUsable(t *testing.T) {
	var bus *EventBus
	derived := bus.WithOrigin("o1")
	if derived != nil {
		t.Fatalf("nil bus WithOrigin = %v, want nil", derived)
	}
}

func TestEmit_UniqueIDs(t *testing.T) {
	bus := NewEventBus(16)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	const N = 10
	for i := 0; i < N; i++ {
		bus.Emit("a", EventAgentStart, struct{}{})
	}

	seen := make(map[string]bool)
	for i := 0; i < N; i++ {
		evt := <-ch
		if seen[evt.ID] {
			t.Errorf("duplicate event ID: %q", evt.ID)
		}
		seen[evt.ID] = true
	}
}

// ============================================================
// Stress tests
// ============================================================

func TestStress_ConcurrentEmit(t *testing.T) {
	const N = 50
	bus := NewEventBus(N * 2)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			bus.Emit("agent", EventAgentStart, struct{}{})
		}()
	}
	wg.Wait()

	// Drain received events
	var received int
	timeout := time.After(time.Second)
	for received < N {
		select {
		case <-ch:
			received++
		case <-timeout:
			t.Errorf("only received %d/%d events before timeout", received, N)
			return
		}
	}
}

func TestStress_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	bus := NewEventBus(4)
	const N = 30
	var wg sync.WaitGroup
	var completed atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			ch := bus.Subscribe()
			bus.Emit("a", EventAgentStart, struct{}{})
			bus.Unsubscribe(ch)
			completed.Add(1)
		}()
	}
	wg.Wait()

	if got := completed.Load(); got != N {
		t.Errorf("expected %d completions, got %d", N, got)
	}
}

func TestStress_ConcurrentEmitMultipleSubscribers(t *testing.T) {
	const subs = 5
	const emits = 20
	bus := NewEventBus(emits * 2)

	channels := make([]<-chan AgentEvent, subs)
	for i := range channels {
		channels[i] = bus.Subscribe()
	}

	var wg sync.WaitGroup
	wg.Add(emits)
	for i := 0; i < emits; i++ {
		go func() {
			defer wg.Done()
			bus.Emit("a", EventAgentStart, struct{}{})
		}()
	}
	wg.Wait()

	for _, ch := range channels {
		bus.Unsubscribe(ch)
	}
}

// ============================================================
// ConsoleTracer — lifecycle
// ============================================================

func TestConsoleTracer_StartStop_NoDeadlock(t *testing.T) {
	bus := NewEventBus(8)
	tracer := NewConsoleTracer()
	tracer.Start(bus)

	done := make(chan struct{})
	go func() {
		tracer.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked")
	}
}

// B14a regression: when a tool call fails, the tracer must show the tool's
// stderr output alongside the error message, not just the bare err.Error().
// Before the fix, operators watching `rakitsu run --trace` saw only
// "command failed: exit status 1" with no hint about WHY git rejected the
// command, which caused real dogfood runs to loop retrying nonsense variations
// because the human couldn't diagnose the failure in real time.
func TestConsoleTracer_ToolCallEnd_Failure_ShowsStderr(t *testing.T) {
	var buf bytes.Buffer
	bus := NewEventBus(16)
	tracer := NewConsoleTracerWithWriter(&buf)
	tracer.Start(bus)

	// Simulate a failing tool call: git rejected the --since arg and wrote
	// a recognizable message to stderr, which executeLocalRestricted then
	// returned via CombinedOutput as the Output field.
	bus.EmitWithDuration("agent-a", EventToolCallEnd, ToolCallEndPayload{
		ToolCallID: "call-1",
		ToolName:   "git",
		Output:     "fatal: bad revision '--since=\"7'\nfatal: Failed to parse date",
		Error:      "command failed: exit status 128",
	}, 42)

	// Let the tracer goroutine drain
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() { tracer.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked")
	}

	out := buf.String()

	// Must contain the error summary (already worked before the fix)
	if !strings.Contains(out, "failed") {
		t.Errorf("expected 'failed' marker in trace, got:\n%s", out)
	}
	if !strings.Contains(out, "git") {
		t.Errorf("expected tool name 'git' in trace, got:\n%s", out)
	}
	if !strings.Contains(out, "exit status 128") {
		t.Errorf("expected err.Error() text in trace, got:\n%s", out)
	}

	// NEW post-fix requirement: the stderr content must ALSO be visible to
	// the operator. Before the fix, p.Output was silently discarded on failure.
	if !strings.Contains(out, "fatal: bad revision") {
		t.Errorf("B14a regression: tracer failed to surface stderr content. Expected 'fatal: bad revision' in output, got:\n%s", out)
	}
}

func TestConsoleTracer_ToolCallEnd_Failure_NoOutput_DoesNotPanic(t *testing.T) {
	// Some failure modes (SecurityError, path validation) return empty output.
	// The tracer must still show the error line without trying to print a
	// second blank "stderr" line.
	var buf bytes.Buffer
	bus := NewEventBus(16)
	tracer := NewConsoleTracerWithWriter(&buf)
	tracer.Start(bus)

	bus.EmitWithDuration("agent-a", EventToolCallEnd, ToolCallEndPayload{
		ToolCallID: "call-1",
		ToolName:   "git",
		Output:     "",
		Error:      "security error: command 'sh' blocked: command not in system whitelist",
	}, 1)

	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { tracer.Stop(); close(done) }()
	<-done

	out := buf.String()
	if !strings.Contains(out, "security error") {
		t.Errorf("expected security error in trace, got:\n%s", out)
	}
	// Should NOT contain an empty stderr block. Count lines starting with
	// the stderr prefix "│" — for empty output there should be zero.
	if strings.Contains(out, "│") {
		lines := strings.Split(out, "\n")
		stderrLines := 0
		for _, l := range lines {
			if strings.Contains(l, "│") {
				stderrLines++
			}
		}
		if stderrLines > 0 {
			t.Errorf("expected no stderr block for empty Output, got %d stderr line(s):\n%s", stderrLines, out)
		}
	}
}

func TestConsoleTracer_HandlesEvents_NoPanic(t *testing.T) {
	bus := NewEventBus(16)
	tracer := NewConsoleTracer()
	tracer.Start(bus)

	// Emit a variety of event types — tracer should handle all without panic
	events := []EventType{
		EventAgentStart, EventAgentEnd,
		EventThoughtStart, EventThoughtEnd,
		EventToolCallStart, EventToolCallEnd,
		EventAgentHandoff, EventRetryAttempt,
		EventPipelineStart, EventPipelineEnd,
		EventPipelineStepStart, EventPipelineStepEnd,
		EventContextCompressed,
	}
	for _, et := range events {
		bus.Emit("test-agent", et, struct{}{})
	}

	// Give tracer goroutine time to process
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() { tracer.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked after emitting events")
	}
}

func TestEventBusWithSessionPreservesNestedOrigin(t *testing.T) {
	bus := NewEventBus(2)
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.WithSession("builder").WithOrigin("delegated-turn").Emit("agent", EventAgentStart, nil)
	bus.Emit("other", EventAgentStart, nil)

	event := <-ch
	if event.SessionID != "builder" || event.Origin != "delegated-turn" {
		t.Fatalf("unexpected run event tags: session=%q origin=%q", event.SessionID, event.Origin)
	}
	if event := <-ch; event.SessionID != "" {
		t.Fatalf("unscoped event inherited session %q", event.SessionID)
	}
}
