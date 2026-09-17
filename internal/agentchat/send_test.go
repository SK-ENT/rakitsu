package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// fakeAgent implements HistoryRunner. It records the history it was given,
// optionally blocks until release is closed, and fires its sink with the
// composed transcript exactly as *agent.Agent does.
type fakeAgent struct {
	name    string
	reply   string
	err     error
	release chan struct{}
	// entered, if non-nil, is closed the moment RunWithHistory starts —
	// which happens strictly after Roster.claim has reserved the agent's
	// in-flight slot. Tests that need to know the slot is held (rather than
	// spin-and-hope, which can deadlock — see TestConcurrentSendToOneAgentIsBusy)
	// wait on this instead.
	entered chan struct{}
	sink    func([]llm.Message, error)
	// skipSink, if true, makes RunWithHistory succeed without invoking the
	// registered sink — simulating a HistoryRunner that violates the
	// sink-fires-before-return contract documented on HistoryRunner.
	skipSink bool
	// sinkErr is the outcome handed to the sink on an otherwise successful
	// return — agent.Agent does exactly this for a max_iterations stop.
	sinkErr error

	mu       sync.Mutex
	gotHist  []llm.Message
	gotQuery string
}

func (f *fakeAgent) RunWithHistory(ctx context.Context, query string, history []llm.Message) (string, error) {
	f.mu.Lock()
	f.gotHist = append([]llm.Message(nil), history...)
	f.gotQuery = query
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	if f.sink != nil && !f.skipSink {
		full := append(append([]llm.Message(nil), history...),
			llm.NewTextMessage("user", query),
			llm.NewTextMessage("assistant", f.reply))
		f.sink(full, f.sinkErr)
	}
	return f.reply, nil
}

func (f *fakeAgent) SetTranscriptSink(fn func([]llm.Message, error)) { f.sink = fn }
func (f *fakeAgent) GetName() string                                 { return f.name }

func builderFor(fa *fakeAgent, closed *int) Builder {
	return func(ctx context.Context, name, origin string) (HistoryRunner, func(), error) {
		return fa, func() {
			if closed != nil {
				*closed++
			}
		}, nil
	}
}

// wantRejectedEnd drains sub and asserts it holds exactly a DIRECT_CHAT_START
// followed by a DIRECT_CHAT_END with Status "rejected", returning the END
// payload. This is what pins Finding 2: with a real bus wired in (unlike nil,
// which the production code silently no-ops on), deleting Send's
// rejection-path r.emitEnd(...) call leaves sub without an END and this fails
// instead of the suite staying green. The leading START pins T5 — a rejection
// used to emit an END that nothing opened.
func wantRejectedEnd(t *testing.T, bus *telemetry.EventBus, sub <-chan telemetry.AgentEvent) telemetry.DirectChatEndPayload {
	t.Helper()
	bus.Unsubscribe(sub)

	var events []telemetry.AgentEvent
	for ev := range sub {
		events = append(events, ev)
	}
	if len(events) != 2 ||
		events[0].EventType != telemetry.EventDirectChatStart ||
		events[1].EventType != telemetry.EventDirectChatEnd {
		t.Fatalf("events = %v, want [DIRECT_CHAT_START DIRECT_CHAT_END]", eventTypeNames(events))
	}
	var payload telemetry.DirectChatEndPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("unmarshal DIRECT_CHAT_END payload: %v", err)
	}
	if payload.Status != "rejected" {
		t.Errorf("status = %q, want %q", payload.Status, "rejected")
	}
	return payload
}

func eventTypeNames(events []telemetry.AgentEvent) []telemetry.EventType {
	out := make([]telemetry.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.EventType
	}
	return out
}

func TestSendReplaysRetainedHistory(t *testing.T) {
	fa := &fakeAgent{name: "Researcher-1", reply: "the task named only the README"}
	closed := 0
	r := New(config.AgentChatConfig{}, builderFor(fa, &closed), telemetry.NewEventBus(16))
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	r.RecordTranscript("Researcher-1", []llm.Message{
		llm.NewTextMessage("user", "summarize the repo"),
		llm.NewTextMessage("assistant", "it has a README"),
	}, nil)

	reply, err := r.Send(context.Background(), "Researcher-1", "why skip the API docs?")
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if reply != fa.reply {
		t.Errorf("reply = %q, want %q", reply, fa.reply)
	}
	if len(fa.gotHist) != 2 || fa.gotHist[0].AsText() != "summarize the repo" {
		t.Errorf("retained history not replayed: %+v", fa.gotHist)
	}
	if fa.gotQuery != "why skip the API docs?" {
		t.Errorf("query = %q", fa.gotQuery)
	}
	if closed != 1 {
		t.Errorf("cleanup called %d times, want exactly 1", closed)
	}
}

func TestSendRecordsUpdatedTranscript(t *testing.T) {
	fa := &fakeAgent{name: "Reviewer", reply: "looks fine"}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")

	if _, err := r.Send(context.Background(), "Reviewer", "check this"); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	got, ok := r.LastReply("Reviewer")
	if !ok || got != "looks fine" {
		t.Errorf("LastReply = (%q, %v), want (looks fine, true)", got, ok)
	}
	if e := r.List()[0]; e.Status != StatusDone {
		t.Errorf("status after Send = %q, want %q", e.Status, StatusDone)
	}
}

func TestSendToRunningAgentIsRejected(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	r := New(config.AgentChatConfig{}, builderFor(&fakeAgent{}, nil), bus)
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	_, err := r.Send(context.Background(), "Researcher-1", "hi")
	if !errors.Is(err, ErrStillRunning) {
		t.Errorf("err = %v, want ErrStillRunning", err)
	}
	if payload := wantRejectedEnd(t, bus, sub); payload.Kind != string(KindSpawned) {
		t.Errorf("kind = %q, want %q", payload.Kind, KindSpawned)
	}
}

func TestSendToUnknownAgentNamesTheValidOnes(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	r := New(config.AgentChatConfig{}, builderFor(&fakeAgent{}, nil), bus)
	r.AddConfig("Reviewer", "m", "p")
	r.AddConfig("Coder", "m", "p")
	_, err := r.Send(context.Background(), "Nobody", "hi")
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}
	if !strings.Contains(err.Error(), "Reviewer") || !strings.Contains(err.Error(), "Coder") {
		t.Errorf("error should name the valid agents, got: %v", err)
	}
	if payload := wantRejectedEnd(t, bus, sub); payload.Kind != "unknown" {
		t.Errorf("kind = %q, want %q (an unrecognized name has no roster kind)", payload.Kind, "unknown")
	}
}

func TestSendToHostIsRejected(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	r := New(config.AgentChatConfig{}, builderFor(&fakeAgent{}, nil), bus)
	r.AddHost("Coordinator", "m", "p")
	_, err := r.Send(context.Background(), "Coordinator", "hi")
	if !errors.Is(err, ErrHostAgent) {
		t.Errorf("err = %v, want ErrHostAgent", err)
	}
	if payload := wantRejectedEnd(t, bus, sub); payload.Kind != string(KindHost) {
		t.Errorf("kind = %q, want %q", payload.Kind, KindHost)
	}
}

// TestConcurrentSendToOneAgentIsBusy pins Send's single-in-flight-slot
// guarantee. The original version of this test spun a Send-in-a-loop on the
// main goroutine hoping to observe ErrBusy; that can deadlock outright: if
// the spinning goroutine's own call wins the race to claim the slot (nothing
// guarantees the background goroutine's Send claims first), that very call
// proceeds into RunWithHistory and blocks on release — which is only closed
// *after* the loop breaks, so the loop can never get there. Waiting on
// fa.entered instead is deterministic: it closes only after claim() has
// already reserved the slot (RunWithHistory runs strictly after claim
// succeeds), so the probe below is guaranteed to observe ErrBusy on its
// first and only attempt, no spinning required.
func TestConcurrentSendToOneAgentIsBusy(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	fa := &fakeAgent{name: "Reviewer", reply: "done", release: release, entered: entered}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")

	done := make(chan error, 1)
	go func() {
		_, err := r.Send(context.Background(), "Reviewer", "first")
		done <- err
	}()

	<-entered // the first Send has claimed the slot and is now blocked in RunWithHistory

	if _, err := r.Send(context.Background(), "Reviewer", "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Send err = %v, want ErrBusy", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Send err = %v", err)
	}

	// Finding 1: the in-flight slot must be released once the first Send
	// finishes, or this agent is stuck at "busy" for the rest of the
	// session — un-addressable, with nothing saying so. Deleting Send's
	// `defer r.release(agentName)` breaks exactly this while every test
	// above it still passes, since none of them issue a second, sequential
	// Send to the same agent.
	//
	// fa.entered was already closed by the first RunWithHistory call above;
	// nil it out so this second, sequential call doesn't try to close it
	// again (which would panic). This write happens after <-done, which is
	// itself after the background goroutine's last touch of fa, so there is
	// no race with it.
	fa.entered = nil
	reply, err := r.Send(context.Background(), "Reviewer", "third")
	if err != nil {
		t.Fatalf("Send after the first one released = %v, want nil (the agent must not stay busy forever)", err)
	}
	if reply != fa.reply {
		t.Errorf("reply = %q, want %q", reply, fa.reply)
	}
}

func TestSendWhenDisabled(t *testing.T) {
	off := false
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	r := New(config.AgentChatConfig{Enabled: &off}, builderFor(&fakeAgent{}, nil), bus)
	r.AddConfig("Reviewer", "m", "p")
	if _, err := r.Send(context.Background(), "Reviewer", "hi"); !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
	if payload := wantRejectedEnd(t, bus, sub); payload.Kind != "unknown" {
		t.Errorf("kind = %q, want %q (a disabled roster never resolves a kind)", payload.Kind, "unknown")
	}
}

func TestSendToEvictedSpawnedChildExplains(t *testing.T) {
	fa := &fakeAgent{name: "A-1", reply: "fresh"}
	r := New(config.AgentChatConfig{MaxRetained: retainNothingConfigValue}, builderFor(fa, nil), nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", []llm.Message{llm.NewTextMessage("user", "q"), llm.NewTextMessage("assistant", "a")}, nil)

	reply, err := r.Send(context.Background(), "A-1", "follow up")
	if err != nil {
		t.Fatalf("Send to an evicted child should still work with empty context: %v", err)
	}
	if reply != "fresh" {
		t.Errorf("reply = %q", reply)
	}
	if len(fa.gotHist) != 0 {
		t.Errorf("evicted child should start from empty history, got %+v", fa.gotHist)
	}
}

func TestSendEmitsStartAndEnd(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	fa := &fakeAgent{name: "Reviewer", reply: "ok"}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), bus)
	r.AddConfig("Reviewer", "m", "p")

	if _, err := r.Send(context.Background(), "Reviewer", "hi"); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	bus.Unsubscribe(sub)

	var types []telemetry.EventType
	for ev := range sub {
		types = append(types, ev.EventType)
	}
	if len(types) != 2 || types[0] != telemetry.EventDirectChatStart || types[1] != telemetry.EventDirectChatEnd {
		t.Errorf("events = %v, want [DIRECT_CHAT_START DIRECT_CHAT_END]", types)
	}
}

func TestSendWithoutBuilder(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()
	r := New(config.AgentChatConfig{}, nil, bus)
	r.AddConfig("Reviewer", "m", "p")
	if _, err := r.Send(context.Background(), "Reviewer", "hi"); !errors.Is(err, ErrNoBuilder) {
		t.Errorf("err = %v, want ErrNoBuilder", err)
	}
	if payload := wantRejectedEnd(t, bus, sub); payload.Kind != "unknown" {
		t.Errorf("kind = %q, want %q (no builder wired never resolves a kind)", payload.Kind, "unknown")
	}
}

// TestSendRunsCleanupOnRunError pins Finding 3's first guarantee: cleanup()
// still runs when RunWithHistory itself fails, and the in-flight slot is
// still released afterward — this agent must not be left permanently busy
// just because its run errored.
func TestSendRunsCleanupOnRunError(t *testing.T) {
	runErr := errors.New("boom")
	fa := &fakeAgent{name: "Reviewer", err: runErr}
	closed := 0
	r := New(config.AgentChatConfig{}, builderFor(fa, &closed), nil)
	r.AddConfig("Reviewer", "m", "p")

	_, err := r.Send(context.Background(), "Reviewer", "hi")
	if !errors.Is(err, runErr) {
		t.Fatalf("err = %v, want wrapping %v", err, runErr)
	}
	if closed != 1 {
		t.Errorf("cleanup called %d times, want exactly 1", closed)
	}
	if _, err := r.Send(context.Background(), "Reviewer", "again"); errors.Is(err, ErrBusy) {
		t.Errorf("second Send after a run error = ErrBusy, want the slot released")
	}
}

// TestSendReleasesSlotWhenBuilderFails pins Finding 3's second guarantee:
// when Builder itself fails, there is no HistoryRunner and no cleanup to
// call yet, but claim() already reserved the in-flight slot — Send must
// still release it, or the agent is stuck busy after a single failed
// rebuild attempt.
func TestSendReleasesSlotWhenBuilderFails(t *testing.T) {
	buildErr := errors.New("cannot construct agent")
	failingBuilder := func(ctx context.Context, name, origin string) (HistoryRunner, func(), error) {
		return nil, nil, buildErr
	}
	r := New(config.AgentChatConfig{}, failingBuilder, nil)
	r.AddConfig("Reviewer", "m", "p")

	_, err := r.Send(context.Background(), "Reviewer", "hi")
	if !errors.Is(err, buildErr) {
		t.Fatalf("err = %v, want wrapping %v", err, buildErr)
	}
	if _, err := r.Send(context.Background(), "Reviewer", "again"); errors.Is(err, ErrBusy) {
		t.Errorf("second Send after a builder failure = ErrBusy, want the slot released")
	}
}

// TestSendToleratesNilCleanup pins Finding 6: Builder is injected by another
// package and its doc only says cleanup "must be safe to call exactly once"
// — nothing requires it to be non-nil. A builder that succeeds with a nil
// cleanup func must not make Send panic.
func TestSendToleratesNilCleanup(t *testing.T) {
	fa := &fakeAgent{name: "Reviewer", reply: "ok"}
	nilCleanupBuilder := func(ctx context.Context, name, origin string) (HistoryRunner, func(), error) {
		return fa, nil, nil
	}
	r := New(config.AgentChatConfig{}, nilCleanupBuilder, nil)
	r.AddConfig("Reviewer", "m", "p")

	if _, err := r.Send(context.Background(), "Reviewer", "hi"); err != nil {
		t.Fatalf("Send with a nil cleanup func = %v, want nil (and no panic)", err)
	}
}

// TestSendFallsBackWhenSinkNeverFires pins Finding 4: if RunWithHistory
// returns successfully without ever firing the sink registered via
// SetTranscriptSink, Send must not silently leave the previous transcript in
// place — the next Send would then replay history missing the exchange that
// just happened, with nothing saying so. It must fall back to a transcript
// composed from the replayed history, the user's message, and the reply.
func TestSendFallsBackWhenSinkNeverFires(t *testing.T) {
	fa := &fakeAgent{name: "Reviewer", reply: "fine", skipSink: true}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")
	r.RecordTranscript("Reviewer", []llm.Message{
		llm.NewTextMessage("user", "old question"),
		llm.NewTextMessage("assistant", "old answer"),
	}, nil)

	reply, err := r.Send(context.Background(), "Reviewer", "new question")
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if reply != "fine" {
		t.Errorf("reply = %q", reply)
	}

	got, ok := r.LastReply("Reviewer")
	if !ok || got != "fine" {
		t.Errorf("LastReply after a non-firing sink = (%q, %v), want (%q, true) — stale transcript was left in place",
			got, ok, "fine")
	}
	if e := r.List()[0]; e.Turns != 2 {
		t.Errorf("turns = %d, want 2 (old question + new question — the fallback must include replayed history, not just the new exchange)", e.Turns)
	}
}

// TestEveryDirectChatEndHasAMatchingStart is the T5 repro. Send emitted
// DIRECT_CHAT_END on every rejection path but DIRECT_CHAT_START only after
// claim succeeded, so a rejected send produced an END with nothing opening
// it. The verified sequence for one accepted then one rejected send was
// [START END END]. The session JSONL is advertised as the truthful record of
// side chats, so an unpairable END is a correctness bug in the audit trail.
func TestEveryDirectChatEndHasAMatchingStart(t *testing.T) {
	bus := telemetry.NewEventBus(32)
	r := New(config.AgentChatConfig{}, builderFor(&fakeAgent{name: "Reviewer", reply: "ok"}, nil), bus)
	r.AddConfig("Reviewer", "m", "p")
	sub := bus.Subscribe()

	if _, err := r.Send(context.Background(), "Reviewer", "hi"); err != nil {
		t.Fatalf("accepted send: %v", err)
	}
	if _, err := r.Send(context.Background(), "Ghost", "hi"); err == nil {
		t.Fatal("Send to an unknown agent must fail")
	}

	bus.Unsubscribe(sub)
	var got []telemetry.EventType
	for ev := range sub {
		if ev.EventType == telemetry.EventDirectChatStart || ev.EventType == telemetry.EventDirectChatEnd {
			got = append(got, ev.EventType)
		}
	}
	want := []telemetry.EventType{
		telemetry.EventDirectChatStart, telemetry.EventDirectChatEnd,
		telemetry.EventDirectChatStart, telemetry.EventDirectChatEnd,
	}
	if len(got) != len(want) {
		t.Fatalf("sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", got, want)
		}
	}
}

// TestListReportsRunningWhileSendInFlight is the M3 repro: claim reserved the
// slot with the unexported inFlight flag only, so List (and therefore the
// picker) kept reporting "done"/"idle" for an agent mid-turn.
func TestListReportsRunningWhileSendInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fa := &fakeAgent{name: "Reviewer", reply: "ok", entered: entered, release: release}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Send(context.Background(), "Reviewer", "hi")
	}()
	<-entered

	var got Status
	for _, e := range r.List() {
		if e.Name == "Reviewer" {
			got = e.Status
		}
	}
	close(release)
	<-done

	if got != StatusRunning {
		t.Errorf("Status during an in-flight Send = %q, want %q", got, StatusRunning)
	}
}

// TestSendRecordsAMaxIterationsTurnAsIncomplete: Send discarded the sink's
// outcome and always recorded nil, so a side-chat turn that used up its
// iteration budget came back as "done — N turns" in the picker even though
// the reply it stored is literally "[max_iterations reached — partial
// result]". The reply is still returned to the user; only the label changes.
func TestSendRecordsAMaxIterationsTurnAsIncomplete(t *testing.T) {
	fa := &fakeAgent{name: "Reviewer", reply: "[max_iterations reached — partial result]", sinkErr: agent.ErrMaxIterations}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")

	reply, err := r.Send(context.Background(), "Reviewer", "why?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if reply != fa.reply {
		t.Errorf("reply = %q, want the partial answer %q", reply, fa.reply)
	}
	if got := r.List()[0].Status; got != StatusIncomplete {
		t.Errorf("Status = %q, want %q", got, StatusIncomplete)
	}
}

// TestSendRecordsASuccessfulTurnAsDone is the control: only a run that really
// did not finish gets the new label.
func TestSendRecordsASuccessfulTurnAsDone(t *testing.T) {
	fa := &fakeAgent{name: "Reviewer", reply: "because it was out of scope"}
	r := New(config.AgentChatConfig{}, builderFor(fa, nil), nil)
	r.AddConfig("Reviewer", "m", "p")

	if _, err := r.Send(context.Background(), "Reviewer", "why?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := r.List()[0].Status; got != StatusDone {
		t.Errorf("Status = %q, want %q", got, StatusDone)
	}
}
