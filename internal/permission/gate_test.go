package permission

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakePrompter records calls and answers with a scripted function.
type fakePrompter struct {
	calls  atomic.Int32
	mu     sync.Mutex
	opts   [][]Option
	cards  []Card
	answer func(ctx context.Context, opts []Option) (Answer, error)
}

func (p *fakePrompter) Prompt(ctx context.Context, card Card, opts []Option) (Answer, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.opts = append(p.opts, opts)
	p.cards = append(p.cards, card)
	p.mu.Unlock()
	return p.answer(ctx, opts)
}

func (p *fakePrompter) lastOptions() []Option {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opts[len(p.opts)-1]
}

func choose(scope Scope) func(context.Context, []Option) (Answer, error) {
	return func(_ context.Context, opts []Option) (Answer, error) {
		for _, o := range opts {
			if o.Scope == scope {
				return Answer{OptionID: o.ID}, nil
			}
		}
		return Answer{}, errors.New("test: scope not offered")
	}
}

func chooseDeny(_ context.Context, opts []Option) (Answer, error) {
	for _, o := range opts {
		if o.Scope == ScopeNone {
			return Answer{OptionID: o.ID}, nil
		}
	}
	return Answer{}, errors.New("test: no deny option")
}

func testCard(req Request) (Card, error) { return Card{Title: "test", Body: req.ID}, nil }

func newTestGate(p Prompter, grants *SessionGrants, opts GateOptions) *Gate {
	g := NewGate(p, grants, opts)
	g.buildCard = testCard
	return g
}

func assertDeny(t *testing.T, v Verdict, source string) {
	t.Helper()
	if v.Decision != Deny {
		t.Fatalf("got %+v, want deny", v)
	}
	if source != "" && v.Source != source {
		t.Fatalf("source = %q, want %q (verdict %+v)", v.Source, source, v)
	}
	if v.Message == "" {
		t.Fatal("deny verdict must carry an agent-facing message")
	}
}

func assertAllow(t *testing.T, v Verdict, source string) {
	t.Helper()
	if v.Decision != Allow || v.Source != source {
		t.Fatalf("got %+v, want allow/%s", v, source)
	}
}

func TestVerdict_ZeroValueIsDeny(t *testing.T) {
	var v Verdict
	if v.Decision != Deny || v.Allowed() {
		t.Fatal("zero Verdict must be Deny")
	}
}

func TestAuthorize_NoGate_AskBecomesDeny(t *testing.T) {
	assertDeny(t, Authorize(context.Background(), cliReq(t)), SourceNoChannel)
	//nolint:staticcheck // a nil context must not panic
	assertDeny(t, Authorize(nil, cliReq(t)), SourceNoChannel)
}

func TestAuthorize_NoGate_CleanAllows(t *testing.T) {
	r := cliReq(t)
	r.Reasons = nil
	assertAllow(t, Authorize(context.Background(), r), SourcePolicy)
}

func TestAuthorize_NoGate_RiskyDenies(t *testing.T) {
	r := cliReq(t)
	r.Reasons = nil
	r.Risk = RiskShell
	assertDeny(t, Authorize(context.Background(), r), SourceNoChannel)
}

func TestAuthorize_CleanRequest_AllowsWithoutPrompt(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	r.Reasons = nil
	assertAllow(t, Authorize(WithGate(context.Background(), g), r), SourcePolicy)
	if p.calls.Load() != 0 {
		t.Fatalf("prompter called %d times", p.calls.Load())
	}
}

func TestAuthorize_NilPrompter_Denies(t *testing.T) {
	g := newTestGate(nil, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceNoChannel)
}

func TestAuthorize_DenyPrompter_Denies(t *testing.T) {
	g := newTestGate(DenyPrompter{Reason: "run has no approval channel"}, NewSessionGrants(10, nil), GateOptions{})
	v := g.Authorize(context.Background(), cliReq(t))
	assertDeny(t, v, SourceNoChannel)
	if !bytes.Contains([]byte(v.Message), []byte("run has no approval channel")) {
		t.Fatalf("DenyPrompter reason not surfaced: %q", v.Message)
	}
}

func TestAuthorize_PrompterDisconnected_Denies(t *testing.T) {
	p := &fakePrompter{answer: func(context.Context, []Option) (Answer, error) {
		return Answer{}, ErrDisconnected
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceDisconnected)
}

func TestAuthorize_PrompterOtherError_Denies(t *testing.T) {
	p := &fakePrompter{answer: func(context.Context, []Option) (Answer, error) {
		return Answer{}, errors.New("boom")
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceNoChannel)
}

func TestAuthorize_PrompterErrorWithValidOption_Denies(t *testing.T) {
	// A prompter that returns an error must never be trusted, even if it
	// also returned a valid allow option.
	p := &fakePrompter{answer: func(_ context.Context, opts []Option) (Answer, error) {
		return Answer{OptionID: opts[0].ID}, errors.New("partial failure")
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceNoChannel)
}

func TestAuthorize_AnswerCancelled_Denies(t *testing.T) {
	p := &fakePrompter{answer: func(context.Context, []Option) (Answer, error) {
		return Answer{Cancelled: true}, nil
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceCancelled)
}

func TestAuthorize_CancelledWithOptionID_Denies(t *testing.T) {
	p := &fakePrompter{answer: func(_ context.Context, opts []Option) (Answer, error) {
		return Answer{Cancelled: true, OptionID: opts[0].ID}, nil
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceCancelled)
}

func TestAuthorize_DenyOption_Denies(t *testing.T) {
	p := &fakePrompter{answer: chooseDeny}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceClientSelected)
}

func TestAuthorize_UnknownOptionID_Denies(t *testing.T) {
	for _, id := range []string{"allow", "allow_once", "", "once", "session"} {
		p := &fakePrompter{answer: func(context.Context, []Option) (Answer, error) {
			return Answer{OptionID: id}, nil
		}}
		g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
		assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceUnknownOption)
	}
}

func TestAuthorize_OptionIDsRandomPerRequest(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		assertAllow(t, g.Authorize(context.Background(), cliReq(t)), SourceClientSelected)
		for _, o := range p.lastOptions() {
			if len(o.ID) < 16 {
				t.Fatalf("option ID %q too short to be unguessable", o.ID)
			}
			if seen[o.ID] {
				t.Fatalf("option ID %q reused", o.ID)
			}
			seen[o.ID] = true
		}
	}
}

func TestAuthorize_StaleOptionIDFromEarlierPrompt_Denies(t *testing.T) {
	var stale string
	p := &fakePrompter{}
	p.answer = func(ctx context.Context, opts []Option) (Answer, error) {
		if stale == "" {
			stale = opts[0].ID
			return chooseDeny(ctx, opts)
		}
		return Answer{OptionID: stale}, nil
	}
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	g := newTestGate(p, NewSessionGrants(10, clk.Now), GateOptions{})
	g.Authorize(context.Background(), cliReq(t))
	r := cliReq(t)
	r.Argv = []string{"/usr/bin/git", "log"} // different action, not coalesced
	assertDeny(t, g.Authorize(context.Background(), r), SourceUnknownOption)
}

func TestAuthorize_Timeout_Denies(t *testing.T) {
	p := &fakePrompter{answer: func(ctx context.Context, _ []Option) (Answer, error) {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{Timeout: 30 * time.Millisecond})
	start := time.Now()
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceTimeout)
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestAuthorize_LateAllowAfterDeadline_Denies(t *testing.T) {
	// A prompter that ignores its context and answers "allow" after the
	// deadline must not be honoured.
	p := &fakePrompter{answer: func(ctx context.Context, opts []Option) (Answer, error) {
		<-ctx.Done()
		return Answer{OptionID: opts[0].ID}, nil
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{Timeout: 20 * time.Millisecond})
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceTimeout)
}

func TestAuthorize_CallerCancelDuringPrompt_Denies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePrompter{answer: func(pctx context.Context, opts []Option) (Answer, error) {
		cancel()
		<-pctx.Done()
		return Answer{OptionID: opts[0].ID}, nil
	}}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(ctx, cliReq(t)), SourceCancelled)
}

func TestAuthorize_AlreadyCancelledCtx_DeniesWithoutPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	assertDeny(t, g.Authorize(ctx, cliReq(t)), SourceCancelled)
	if p.calls.Load() != 0 {
		t.Fatal("prompted on a cancelled context")
	}
}

func TestAuthorize_OnceDoesNotGrant(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	if p.calls.Load() != 2 {
		t.Fatalf("allow once must not be remembered; calls = %d", p.calls.Load())
	}
}

func TestAuthorize_SessionGrant_SkipsSecondPrompt(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeSession)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	v := g.Authorize(context.Background(), r)
	assertAllow(t, v, SourceClientSelected)
	if v.Scope != ScopeSession {
		t.Fatalf("scope = %v", v.Scope)
	}
	assertAllow(t, g.Authorize(context.Background(), r), SourceSessionGrant)
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", p.calls.Load())
	}
	// A different subcommand is a different action.
	other := r
	other.Argv = []string{"/usr/bin/git", "log"}
	assertAllow(t, g.Authorize(context.Background(), other), SourceClientSelected)
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls.Load())
	}
}

func TestAuthorize_RiskyNeverOffersSession(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	r.Argv = []string{"/bin/sh", "-c", "echo hi"}
	r.BinaryPath = "/bin/sh"
	r.Risk = ClassifyRisk(r.Argv)
	r.Reasons = []Reason{ReasonRisky}
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	opts := p.lastOptions()
	scopes := map[Scope]int{}
	for _, o := range opts {
		scopes[o.Scope]++
	}
	if scopes[ScopeSession] != 0 || scopes[ScopeOnce] != 1 || scopes[ScopeNone] != 1 || len(opts) != 2 {
		t.Fatalf("risky options = %+v", opts)
	}
}

func TestAuthorize_PersistencePointNeverOffersSession(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := Request{Kind: KindFSWrite, Tool: "fs", Cwd: "/w", Paths: []string{"/w/.git/hooks/pre-commit"},
		Reasons: []Reason{ReasonPersistencePoint}, Boundary: Boundary{Type: "host"}}
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	for _, o := range p.lastOptions() {
		if o.Scope == ScopeSession {
			t.Fatal("persistence point offered a session option")
		}
	}
}

func TestAuthorize_RiskyAnswerSessionIDIsRejected(t *testing.T) {
	// First prompt (grantable) leaks a real session option ID; the forged
	// answer to the risky prompt reuses it.
	var sessionID string
	p := &fakePrompter{}
	p.answer = func(ctx context.Context, opts []Option) (Answer, error) {
		if sessionID == "" {
			for _, o := range opts {
				if o.Scope == ScopeSession {
					sessionID = o.ID
				}
			}
			return chooseDeny(ctx, opts)
		}
		return Answer{OptionID: sessionID}, nil
	}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	g.Authorize(context.Background(), cliReq(t))
	if sessionID == "" {
		t.Fatal("grantable request offered no session option")
	}
	r := cliReq(t)
	r.Argv = []string{"/bin/sh", "-c", "echo hi"}
	r.Risk = RiskShell
	r.Reasons = []Reason{ReasonRisky}
	assertDeny(t, g.Authorize(context.Background(), r), SourceUnknownOption)
	if g.grants.Has(KeyFor(r)) || len(g.grants.keys) != 0 {
		t.Fatal("a grant was recorded")
	}
}

func TestAuthorize_ConcurrentDuplicates_OnePromptThenGrant(t *testing.T) {
	release := make(chan struct{})
	p := &fakePrompter{}
	p.answer = func(ctx context.Context, opts []Option) (Answer, error) {
		<-release
		return choose(ScopeSession)(ctx, opts)
	}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	const n = 8
	var wg sync.WaitGroup
	verdicts := make([]Verdict, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			verdicts[i] = g.Authorize(context.Background(), r)
		}(i)
	}
	// Wait until one goroutine is inside the prompter, then let it answer.
	deadline := time.Now().Add(5 * time.Second)
	for p.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if c := p.calls.Load(); c != 1 {
		t.Fatalf("prompter calls = %d, want 1", c)
	}
	grant := 0
	for _, v := range verdicts {
		if v.Decision != Allow {
			t.Fatalf("verdict %+v, want allow", v)
		}
		if v.Source == SourceSessionGrant {
			grant++
		}
	}
	if grant != n-1 {
		t.Fatalf("session_grant verdicts = %d, want %d", grant, n-1)
	}
}

func TestAuthorize_CancelledCtxWhileQueued_Denies(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := &fakePrompter{}
	p.answer = func(ctx context.Context, opts []Option) (Answer, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return choose(ScopeOnce)(ctx, opts)
	}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})

	first := make(chan Verdict, 1)
	go func() { first <- g.Authorize(context.Background(), cliReq(t)) }()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan Verdict, 1)
	r2 := cliReq(t)
	go func() { queued <- g.Authorize(ctx, r2) }()
	time.Sleep(20 * time.Millisecond) // let it block on the prompt lock
	cancel()
	select {
	case v := <-queued:
		assertDeny(t, v, SourceCancelled)
	case <-time.After(5 * time.Second):
		t.Fatal("queued Authorize did not return after cancel")
	}

	close(release)
	assertAllow(t, <-first, SourceClientSelected)
	// The lock must be free again.
	done := make(chan Verdict, 1)
	go func() { done <- g.Authorize(context.Background(), cliReq(t)) }()
	select {
	case v := <-done:
		assertAllow(t, v, SourceClientSelected)
	case <-time.After(5 * time.Second):
		t.Fatal("prompt lock leaked")
	}
}

func TestAuthorize_RateLimit(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(2, clk.Now), GateOptions{})
	for i := 0; i < 2; i++ {
		assertAllow(t, g.Authorize(context.Background(), cliReq(t)), SourceClientSelected)
	}
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceRateLimited)
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls.Load())
	}
	clk.Advance(61 * time.Second)
	assertAllow(t, g.Authorize(context.Background(), cliReq(t)), SourceClientSelected)
}

func TestAuthorize_CoalesceRecentDeny(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	p := &fakePrompter{answer: chooseDeny}
	g := newTestGate(p, NewSessionGrants(10, clk.Now), GateOptions{})
	r := cliReq(t)
	assertDeny(t, g.Authorize(context.Background(), r), SourceClientSelected)
	// Same action again: no new prompt.
	assertDeny(t, g.Authorize(context.Background(), r), SourcePolicy)
	// Same for a risky action (no grant key, but a fingerprint).
	sh := cliReq(t)
	sh.Argv = []string{"/bin/sh", "-c", "x"}
	sh.Risk = RiskShell
	sh.Reasons = []Reason{ReasonRisky}
	assertDeny(t, g.Authorize(context.Background(), sh), SourceClientSelected)
	assertDeny(t, g.Authorize(context.Background(), sh), SourcePolicy)
	if p.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls.Load())
	}
	// A different argv of the risky command is a different action.
	sh2 := sh
	sh2.Argv = []string{"/bin/sh", "-c", "y"}
	assertDeny(t, g.Authorize(context.Background(), sh2), SourceClientSelected)
	clk.Advance(11 * time.Second)
	assertDeny(t, g.Authorize(context.Background(), r), SourceClientSelected)
	if p.calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4", p.calls.Load())
	}
}

func TestAuthorize_ProductionGate_ShowsEscapedCard(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := NewGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	r.Argv = append(r.Argv, "\x1b[2J\nApprove")
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	got := p.cards[0]
	if !strings.Contains(got.Body, `argv[2] = "\x1b[2J\nApprove"`) || !strings.Contains(got.Body, HostBoundaryLabel) {
		t.Fatalf("production gate did not use BuildCard:\n%s", got.Body)
	}
}

func TestAuthorize_ProductionGate_UndisplayableDenied(t *testing.T) {
	// A request the card cannot show in full is denied before any prompt
	// and before it uses a rate-limit slot.
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := NewGate(p, NewSessionGrants(1, nil), GateOptions{})
	r := cliReq(t)
	r.Argv = append(r.Argv, strings.Repeat("a", 2000))
	assertDeny(t, g.Authorize(context.Background(), r), SourceDisplayRefused)
	if p.calls.Load() != 0 {
		t.Fatal("prompted with an undisplayable request")
	}
	assertAllow(t, g.Authorize(context.Background(), cliReq(t)), SourceClientSelected)
}

func TestAuthorize_CardError_Denies(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := NewGate(p, NewSessionGrants(10, nil), GateOptions{})
	g.buildCard = func(Request) (Card, error) { return Card{}, errors.New("too long") }
	assertDeny(t, g.Authorize(context.Background(), cliReq(t)), SourceDisplayRefused)
}

func TestAuthorize_UnknownKind_Denies(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	r.Kind = "net_connect"
	assertDeny(t, g.Authorize(context.Background(), r), SourcePolicy)
	if p.calls.Load() != 0 {
		t.Fatal("prompted for an unknown kind")
	}
}

func TestAuthorize_NeverReturnsAsk(t *testing.T) {
	answers := []func(context.Context, []Option) (Answer, error){
		choose(ScopeOnce), choose(ScopeSession), chooseDeny,
		func(context.Context, []Option) (Answer, error) { return Answer{OptionID: "x"}, nil },
		func(context.Context, []Option) (Answer, error) { return Answer{Cancelled: true}, nil },
		func(context.Context, []Option) (Answer, error) { return Answer{}, ErrNoChannel },
	}
	risks := []Risk{RiskNone, RiskShell, Risk("bogus")}
	reasons := [][]Reason{nil, {ReasonNotAllowlisted}, {ReasonPersistencePoint}, {Reason("bogus")}}
	for _, a := range answers {
		for _, rk := range risks {
			for _, rs := range reasons {
				p := &fakePrompter{answer: a}
				g := newTestGate(p, NewSessionGrants(60, nil), GateOptions{})
				r := cliReq(t)
				r.Risk, r.Reasons = rk, rs
				v := g.Authorize(context.Background(), r)
				if v.Decision != Allow && v.Decision != Deny {
					t.Fatalf("Authorize returned %v", v.Decision)
				}
				if v.Decision == Allow && v.Source == SourceSessionGrant && !SessionAllowed(r) {
					t.Fatalf("session grant on a non-grantable request %+v", r)
				}
			}
		}
	}
}

func TestAuthorize_RequestIDIsBrokerGenerated(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	r := cliReq(t)
	r.ID = "forged-id"
	g.Authorize(context.Background(), r)
	p.mu.Lock()
	body := p.cards[0].Body
	p.mu.Unlock()
	if body == "forged-id" || len(body) < 16 {
		t.Fatalf("request ID not broker-generated: %q", body)
	}
}

func TestAuthorize_EmitsEventsWithoutRationale(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	ch := bus.Subscribe()
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{SessionID: "s1"}).WithBus(bus)
	r := cliReq(t)
	r.Rationale = "RATIONALE_MARKER"
	r.Argv = []string{"/usr/bin/curl", "-H", "Authorization: Bearer SYNTH_TOKEN_123"}
	r.Risk = ClassifyRisk(r.Argv)
	r.Reasons = []Reason{ReasonRisky}
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)

	var got []telemetry.AgentEvent
	for len(got) < 2 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-time.After(2 * time.Second):
			t.Fatalf("got %d events, want 2", len(got))
		}
	}
	if got[0].EventType != telemetry.EventPermissionRequested || got[1].EventType != telemetry.EventPermissionDecided {
		t.Fatalf("event types: %s, %s", got[0].EventType, got[1].EventType)
	}
	for _, ev := range got {
		if bytes.Contains(ev.Payload, []byte("RATIONALE_MARKER")) {
			t.Fatalf("rationale leaked into telemetry: %s", ev.Payload)
		}
		if bytes.Contains(ev.Payload, []byte("SYNTH_TOKEN_123")) {
			t.Fatalf("secret leaked into telemetry: %s", ev.Payload)
		}
		if ev.SessionID != "s1" {
			t.Fatalf("session id not set: %+v", ev)
		}
	}
	for _, want := range []string{`"decision":"allow"`, `"scope":"once"`, `"source":"client_selected"`} {
		if !bytes.Contains(got[1].Payload, []byte(want)) {
			t.Fatalf("decided payload %s lacks %s", got[1].Payload, want)
		}
	}
	if !bytes.Contains(got[0].Payload, []byte(`"options":["once","deny"]`)) {
		t.Fatalf("requested payload options: %s", got[0].Payload)
	}
}

func TestWithBus_SharesGrantsAndLock(t *testing.T) {
	p := &fakePrompter{answer: choose(ScopeSession)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	g2 := g.WithBus(telemetry.NewEventBus(4))
	r := cliReq(t)
	assertAllow(t, g.Authorize(context.Background(), r), SourceClientSelected)
	assertAllow(t, g2.Authorize(context.Background(), r), SourceSessionGrant)
	if g2.buildCard == nil || g.grants != g2.grants {
		t.Fatal("WithBus must be a shallow copy")
	}
}

func TestGateFrom(t *testing.T) {
	if _, ok := GateFrom(context.Background()); ok {
		t.Fatal("no gate expected")
	}
	g := NewGate(nil, nil, GateOptions{})
	got, ok := GateFrom(WithGate(context.Background(), g))
	if !ok || got != g {
		t.Fatal("gate not found")
	}
	// A nil gate stored in the context is the same as no gate.
	if _, ok := GateFrom(WithGate(context.Background(), nil)); ok {
		t.Fatal("nil gate reported as present")
	}
}

func TestCallInfo_RoundTrip(t *testing.T) {
	chain := []string{"root", "child"}
	ctx := WithCall(context.Background(), CallInfo{ToolCallID: "c1", Agent: "child", ParentChain: chain, Rationale: "why"})
	chain[0] = "mutated"
	ci := CallFrom(ctx)
	if ci.ToolCallID != "c1" || ci.Agent != "child" || ci.ParentChain[0] != "root" || ci.Rationale != "why" {
		t.Fatalf("got %+v", ci)
	}
	if z := CallFrom(context.Background()); z.ToolCallID != "" || z.Agent != "" || z.ParentChain != nil || z.Rationale != "" {
		t.Fatalf("missing call info must be zero, got %+v", z)
	}
	//nolint:staticcheck // a nil context must not panic
	_ = CallFrom(nil)
}

func TestAuthorize_ZeroValueGate_FailsClosed(t *testing.T) {
	var nilGate *Gate
	assertDeny(t, nilGate.Authorize(context.Background(), cliReq(t)), SourceNoChannel)
	assertDeny(t, (&Gate{}).Authorize(context.Background(), cliReq(t)), SourceNoChannel)
	assertDeny(t, Authorize(WithGate(context.Background(), &Gate{}), cliReq(t)), SourceNoChannel)
}
