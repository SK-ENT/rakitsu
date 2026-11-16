package permission

import (
	"context"
	"strings"
	"testing"
)

func TestPolicyGateAskIgnoresLegacyAllowAndGrant(t *testing.T) {
	a := policyAction(KindFSWrite, "/workspace/file")
	p := &fakePrompter{answer: choose(ScopeSession)}
	grants := NewSessionGrants(10, nil)
	grants.Add(KeyFor(a.Request))
	g := newTestGate(p, grants, GateOptions{})
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, policyInputs())
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, a), SourceClientSelected)
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, a), SourceSessionGrant)
	if p.calls.Load() != 1 {
		t.Fatalf("Ask must prompt once despite clean legacy allow and grant, got %d", p.calls.Load())
	}
}

func TestPolicyGateDenialsPrecedeSessionGrants(t *testing.T) {
	a := policyAction(KindFSWrite, "/workspace/file")
	grants := NewSessionGrants(10, nil)
	prompter := &fakePrompter{answer: choose(ScopeSession)}
	g := newTestGate(prompter, grants, GateOptions{})
	ask := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, policyInputs())
	assertAllow(t, g.AuthorizeAction(context.Background(), ask, a), SourceClientSelected)
	grants.Add(KeyFor(a.Request))
	for _, tc := range []struct {
		name      string
		selection Selection
		action    PolicyAction
	}{
		{"auto requires preauthorization", Selection{Mode: ModeAuto}, a},
		{"plan write", Selection{Focus: FocusPlan}, a},
		{"plan delegation", Selection{Focus: FocusPlan}, PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: policyAction(KindFSRead, "/workspace/file").Request, Delegate: true}},
		{"outside workflow", Selection{}, policyAction(KindFSWrite, "/outside/file")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := mustPolicy(t, policyConfig(ModeAsk), tc.selection, nil, policyInputs())
			assertDeny(t, g.AuthorizeAction(context.Background(), policy, tc.action), SourcePolicy)
		})
	}
	if prompter.calls.Load() != 1 {
		t.Fatalf("denied actions prompted %d times", prompter.calls.Load()-1)
	}
}

func TestPolicyGateNoChannelAndZeroPolicy(t *testing.T) {
	a := policyAction(KindFSRead, "/workspace/file")
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, policyInputs())
	g := newTestGate(nil, nil, GateOptions{})
	assertDeny(t, g.AuthorizeAction(context.Background(), policy, a), SourceNoChannel)
	assertDeny(t, g.AuthorizeAction(context.Background(), EffectivePolicy{}, a), SourcePolicy)
	assertDeny(t, AuthorizeAction(context.Background(), policy, a), SourceNoChannel)
}

func TestPolicyGateSelectionNeverAddsAuthority(t *testing.T) {
	a := policyAction(KindFSWrite, "/outside/file")
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, nil, GateOptions{})
	for _, mode := range []Mode{ModeAsk, ModeSemiAuto, ModeAuto} {
		in := policyInputs()
		in.Preauthorized = []Authority{policyScope("/")}
		in.Routine = []Authority{policyScope("/")}
		policy := mustPolicy(t, policyConfig(mode), Selection{}, nil, in)
		assertDeny(t, g.AuthorizeAction(context.Background(), policy, a), SourcePolicy)
	}
	if p.calls.Load() != 0 {
		t.Fatal("authority denial reached prompter")
	}
}

func TestPolicyGateAllowsOnlyPolicyAuthorizedAction(t *testing.T) {
	a := policyAction(KindFSWrite, "/workspace/file")
	in := policyInputs()
	in.Preauthorized = []Authority{policyScope("/workspace")}
	policy := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, nil, GateOptions{})
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, a), SourcePolicy)
	a.Network = namedEffect("https://unapproved.test")
	assertDeny(t, g.AuthorizeAction(context.Background(), policy, a), SourcePolicy)
	if p.calls.Load() != 0 {
		t.Fatal("Auto action prompted")
	}
}

func TestPolicyGateExactSessionApprovalDimensions(t *testing.T) {
	a := policyAction(KindFSRead, "/workspace/file")
	in := policyInputs()
	bound := policyScope("/workspace")
	for i := range bound.Rules {
		bound.Rules[i].EnvNames = append(bound.Rules[i].EnvNames, "PATH")
		bound.Rules[i].Network = namedEffect(append(bound.Rules[i].Network.Names, "https://other.test")...)
		bound.Rules[i].Credentials = namedEffect(append(bound.Rules[i].Credentials.Names, "other-provider")...)
	}
	otherBoundary := cloneAuthority(bound)
	for i := range otherBoundary.Rules {
		otherBoundary.Rules[i].Boundary = Boundary{Type: "host", Label: "other host"}
	}
	bound.Rules = append(bound.Rules, otherBoundary.Rules...)
	short := cloneAuthorityRule(bound.Rules[2])
	short.Command.Argv = append(short.Command.Argv, "--short")
	bound.Rules = append(bound.Rules, short)
	in.Operator.HardLimits, in.Operator.Requestable, in.Workflow = bound, bound, bound
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	p := &fakePrompter{answer: choose(ScopeSession)}
	g := newTestGate(p, NewSessionGrants(100, nil), GateOptions{})
	variants := []struct {
		name   string
		change func(*PolicyAction)
	}{
		{"network", func(a *PolicyAction) { a.Network = namedEffect("https://example.test") }},
		{"credentials", func(a *PolicyAction) { a.Credentials = namedEffect("provider") }},
		{"delegation", func(a *PolicyAction) { a.Delegate = true }},
		{"boundary label", func(a *PolicyAction) { a.Request.Boundary.Label = "other host" }},
		{"environment", func(a *PolicyAction) { a.Request.EnvNames = []string{"PATH"} }},
		{"path", func(a *PolicyAction) { a.Request.Paths = []string{"/workspace/other"} }},
		{"cwd", func(a *PolicyAction) { a.Request.Cwd = "/workspace/other" }},
	}
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, a), SourceClientSelected)
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			changed := a
			variant.change(&changed)
			assertAllow(t, g.AuthorizeAction(context.Background(), policy, changed), SourceClientSelected)
			assertAllow(t, g.AuthorizeAction(context.Background(), policy, changed), SourceSessionGrant)
		})
	}
	cli := PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Kind: KindCLIExec, Tool: "cli", Boundary: policyBoundary, Binary: policyBinary(), BinaryPath: "/usr/bin/git", Argv: []string{"/usr/bin/git", "status"}, Cwd: "/workspace"}}
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, cli), SourceClientSelected)
	cli.Request.Argv = []string{"/usr/bin/git", "status", "--short"}
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, cli), SourceClientSelected)
	if want := int32(1 + len(variants) + 2); p.calls.Load() != want {
		t.Fatalf("prompts = %d, want %d", p.calls.Load(), want)
	}
}

func TestPolicyGateCardShowsAuthorityDimensions(t *testing.T) {
	a := policyAction(KindFSRead, "/workspace/file")
	a.Network, a.Credentials, a.Delegate = namedEffect("https://example.test"), namedEffect("provider"), true
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, policyInputs())
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, nil, GateOptions{})
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, a), SourceClientSelected)
	for _, want := range []string{"Network targets: \"https://example.test\"", "Credential scopes: \"provider\"", "Delegation: true"} {
		if !strings.Contains(p.cards[0].Body, want) {
			t.Fatalf("card omits %q", want)
		}
	}
}

func TestPolicyGateTemplateApprovalBindsFinalArgvAndID(t *testing.T) {
	in, action := templatePolicyInputs()
	alternate := cloneAuthority(in.Workflow)
	alternate.Rules[0].Command.TemplateID = "git-status:v2"
	for _, scope := range []*Authority{&in.Operator.HardLimits, &in.Operator.Requestable, &in.Workflow} {
		scope.Rules = append(scope.Rules, alternate.Rules...)
	}
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	p := &fakePrompter{answer: choose(ScopeSession)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	g.buildCard = BuildCard
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, action), SourceClientSelected)
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, action), SourceSessionGrant)
	if !strings.Contains(p.cards[0].Body, `argv[2] = "--short"`) || !strings.Contains(p.cards[0].Body, `Command template: "git-status:v1"`) {
		t.Fatalf("approval omitted final command or template: %s", p.cards[0].Body)
	}
	action.Request.Argv = []string{"/usr/bin/git", "status", "--branch"}
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, action), SourceClientSelected)
	action.TemplateID = "git-status:v2"
	assertAllow(t, g.AuthorizeAction(context.Background(), policy, action), SourceClientSelected)
	if p.calls.Load() != 3 {
		t.Fatal("session approval widened argv or template")
	}
}

func TestPolicyGateAmbientOnlyOffersOnceAndUnknownNeverPrompts(t *testing.T) {
	in := policyInputs()
	for _, bound := range []*Authority{&in.Operator.HardLimits, &in.Operator.Requestable, &in.Workflow} {
		for i := range bound.Rules {
			bound.Rules[i].Network, bound.Rules[i].Credentials = EffectScope{Level: EffectAmbient}, EffectScope{Level: EffectAmbient}
		}
	}
	policy := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	action := policyAction(KindFSRead, "/workspace/file")
	action.Network, action.Credentials = EffectScope{Level: EffectAmbient}, EffectScope{Level: EffectAmbient}
	p := &fakePrompter{answer: choose(ScopeOnce)}
	g := newTestGate(p, NewSessionGrants(10, nil), GateOptions{})
	for i := 0; i < 2; i++ {
		assertAllow(t, g.AuthorizeAction(context.Background(), policy, action), SourceClientSelected)
	}
	for _, opts := range p.opts {
		for _, opt := range opts {
			if opt.Scope == ScopeSession {
				t.Fatal("ambient session approval offered")
			}
		}
	}
	for _, want := range []string{"Network targets: ambient access to any network target", "Credential scopes: ambient access to credentials available to the process", "Approval scope: one action only"} {
		if !strings.Contains(p.cards[0].Body, want) {
			t.Fatalf("ambient card omitted %q: %s", want, p.cards[0].Body)
		}
	}
	for _, scope := range []EffectScope{{}, {Level: EffectUnknown}} {
		action.Network = scope
		assertDeny(t, g.AuthorizeAction(context.Background(), policy, action), SourcePolicy)
		if _, err := policyActionCard(Card{}, action); err == nil {
			t.Fatal("unknown effects displayable for approval")
		}
	}
	if p.calls.Load() != 2 {
		t.Fatal("unknown effects prompted")
	}
}
