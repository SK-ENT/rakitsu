package permission

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func policyConfig(mode Mode) PolicyConfig {
	return PolicyConfig{DefaultMode: mode, AvailableModes: []Mode{ModeAsk, ModeSemiAuto, ModeAuto}, AvailableFocus: []Focus{FocusCoding, FocusPlan, FocusDesign}}
}

var policyBoundary = Boundary{Type: "host", Label: "host execution"}

func policyBinary() FileID {
	fi, err := os.Stat("/usr/bin/git")
	if err != nil {
		panic(err)
	}
	return NewFileID(fi)
}

func policyScope(roots ...string) Authority {
	var a Authority
	for _, kind := range []Kind{KindFSRead, KindFSWrite, KindCLIExec} {
		rule := AuthorityRule{Kind: kind, Tool: "fs", Boundary: policyBoundary, Roots: append([]string(nil), roots...), EnvNames: []string{"LANG"}, Network: namedEffect("https://example.test"), Credentials: namedEffect("provider"), Delegation: true}
		if kind == KindCLIExec {
			rule.Tool = "cli"
			rule.Command = &CommandAuthority{Argv: []string{"/usr/bin/git", "status"}, Binary: policyBinary()}
		}
		a.Rules = append(a.Rules, rule)
	}
	return a
}
func policyInputs() PolicyInputs {
	a := policyScope("/workspace")
	return PolicyInputs{Operator: OperatorPolicy{HardLimits: a, Requestable: a, AllowChildGrants: true}, Workflow: a, RunID: "run", PolicyRevision: "v1", Now: time.Now()}
}
func policyAction(kind Kind, path string) PolicyAction {
	return PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Boundary: policyBoundary, Kind: kind, Tool: "fs", Paths: []string{path}}}
}
func mustPolicy(t *testing.T, c PolicyConfig, s Selection, parent *EffectivePolicy, in PolicyInputs) EffectivePolicy {
	t.Helper()
	p, err := ResolvePolicy(c, s, parent, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPolicySelection(t *testing.T) {
	cases := []struct {
		name string
		c    PolicyConfig
		s    Selection
		want Selection
		bad  bool
	}{
		{name: "defaults legacy coding", want: Selection{FocusCoding, ModeLegacy}},
		{name: "secure default ask", c: PolicyConfig{DefaultMode: ModeAsk}, want: Selection{FocusCoding, ModeAsk}},
		{name: "auto unavailable without operator opt in", c: PolicyConfig{DefaultMode: ModeAsk}, s: Selection{Mode: ModeAuto}, bad: true},
		{name: "default auto opt in", c: PolicyConfig{DefaultMode: ModeAuto}, want: Selection{FocusCoding, ModeAuto}},
		{name: "listed auto operator opt in", c: PolicyConfig{DefaultMode: ModeAsk, AvailableModes: []Mode{ModeAsk, ModeAuto}}, s: Selection{Mode: ModeAuto}, want: Selection{FocusCoding, ModeAuto}},
		{name: "focus unavailable", c: PolicyConfig{}, s: Selection{Focus: FocusPlan}, bad: true},
		{name: "focus available", c: PolicyConfig{AvailableFocus: []Focus{FocusCoding, FocusPlan}}, s: Selection{Focus: FocusPlan}, want: Selection{FocusPlan, ModeLegacy}},
		{name: "bad focus", s: Selection{Focus: "unlimited"}, bad: true},
		{name: "bad mode", s: Selection{Mode: "unlimited"}, bad: true},
		{name: "duplicate mode", c: PolicyConfig{DefaultMode: ModeAsk, AvailableModes: []Mode{ModeAsk, ModeAsk}}, bad: true},
		{name: "duplicate focus", c: PolicyConfig{AvailableFocus: []Focus{FocusCoding, FocusCoding}}, bad: true},
		{name: "default absent from choices", c: PolicyConfig{DefaultMode: ModeAsk, AvailableModes: []Mode{ModeAuto}}, bad: true},
		{name: "legacy mixed with secure", c: PolicyConfig{DefaultMode: ModeAsk, AvailableModes: []Mode{ModeAsk, ModeLegacy}}, bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.c.ResolveSelection(tc.s)
			if tc.bad {
				if err == nil {
					t.Fatalf("accepted %+v", s)
				}
				return
			}
			if err != nil || s != tc.want {
				t.Fatalf("got %+v %v want %+v", s, err, tc.want)
			}
		})
	}
}

func TestPolicyModeDecisionTable(t *testing.T) {
	cases := []struct {
		name                string
		mode                Mode
		pre, routine, human bool
		risk                Risk
		want                Handling
	}{
		{"ask no grant", ModeAsk, false, false, false, RiskNone, HandlingAsk},
		{"ask preauthorized still asks", ModeAsk, true, false, false, RiskNone, HandlingAsk},
		{"ask routine still asks", ModeAsk, false, true, false, RiskNone, HandlingAsk},
		{"ask human grant", ModeAsk, false, false, true, RiskNone, HandlingAllow},
		{"semi routine", ModeSemiAuto, false, true, false, RiskNone, HandlingAllow},
		{"semi ambiguous", ModeSemiAuto, false, false, false, RiskNone, HandlingAsk},
		{"semi preauthorized unclassified", ModeSemiAuto, true, false, false, RiskNone, HandlingAsk},
		{"semi risky routine asks", ModeSemiAuto, false, true, false, RiskDestructive, HandlingAsk},
		{"semi unknown risk asks", ModeSemiAuto, false, true, false, Risk("unknown"), HandlingAsk},
		{"auto no grant", ModeAuto, false, false, false, RiskNone, HandlingDeny},
		{"auto routine not preauthorized", ModeAuto, false, true, false, RiskNone, HandlingDeny},
		{"auto explicit grant", ModeAuto, true, false, false, RiskNone, HandlingAllow},
		{"auto human grant", ModeAuto, false, false, true, RiskNone, HandlingAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := policyInputs()
			a := policyAction(KindFSWrite, "/workspace/a")
			a.Request.Risk = tc.risk
			if tc.pre {
				in.Preauthorized = []Authority{in.Workflow}
			}
			if tc.routine {
				in.Routine = []Authority{in.Workflow}
			}
			if tc.human {
				in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: in.Workflow}}
			}
			p := mustPolicy(t, policyConfig(tc.mode), Selection{}, nil, in)
			if got := p.Handling(a); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestPolicySelectionDoesNotWidenAuthority(t *testing.T) {
	in := policyInputs()
	in.Operator.HardLimits = policyScope("/workspace/safe")
	in.Workflow = policyScope("/workspace")
	in.Preauthorized = []Authority{in.Workflow}
	in.Routine = []Authority{in.Workflow}
	var authority Authority
	for _, mode := range []Mode{ModeAsk, ModeSemiAuto, ModeAuto} {
		p := mustPolicy(t, policyConfig(mode), Selection{Focus: FocusDesign}, nil, in)
		if authority.Rules == nil {
			authority = p.Authority()
		} else if !reflect.DeepEqual(authority, p.Authority()) {
			t.Fatal("mode changed authority")
		}
		for _, a := range []PolicyAction{policyAction(KindFSWrite, "/workspace/outside"), policyAction(KindFSWrite, "/workspace/safe/../outside"), policyAction(KindFSWrite, "/workspace/safe-other/a"), {Request: Request{Boundary: policyBoundary, Kind: KindFSRead, Tool: "unlisted", Paths: []string{"/workspace/safe/a"}}}, {Request: Request{Boundary: policyBoundary, Kind: Kind("unknown"), Tool: "fs", Paths: []string{"/workspace/safe/a"}}}} {
			if got := p.Handling(a); got != HandlingDeny {
				t.Fatalf("%s escaped hard policy: %s", mode, got)
			}
		}
	}
}

func TestPolicyChildCeilingAndApprovalConstraints(t *testing.T) {
	in := policyInputs()
	in.Workflow = policyScope("/workspace/parent")
	in.Preauthorized = []Authority{in.Workflow}
	in.Routine = []Authority{in.Workflow}
	parent := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	for _, mode := range []Mode{ModeAsk, ModeSemiAuto, ModeAuto} {
		childIn := policyInputs()
		childIn.Preauthorized = []Authority{childIn.Workflow}
		childIn.Routine = []Authority{childIn.Workflow}
		child := mustPolicy(t, policyConfig(mode), Selection{}, &parent, childIn)
		if got := child.Handling(policyAction(KindFSWrite, "/workspace/other/a")); got != HandlingDeny {
			t.Fatalf("%s widened parent ceiling", mode)
		}
		if got := child.Handling(policyAction(KindFSWrite, "/workspace/parent/a")); got != HandlingAsk {
			t.Fatalf("%s lowered parent's Ask requirement: %s", mode, got)
		}
	}
	if _, err := ResolvePolicy(PolicyConfig{}, Selection{}, &parent, policyInputs()); err == nil {
		t.Fatal("child downgraded secure parent to legacy")
	}
}

func TestPolicyHumanChildOnlyGrant(t *testing.T) {
	in := policyInputs()
	in.Workflow = policyScope("/workspace/parent")
	parent := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	original := parent.Authority()
	action := policyAction(KindFSWrite, "/workspace/child/a")
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded", true: "exact"}[exact], func(t *testing.T) {
			childIn := policyInputs()
			childIn.RunID = "child"
			grant := ApprovedGrant{RunID: "child", PolicyRevision: "v1", ExpiresAt: childIn.Now.Add(time.Minute), Authority: policyScope("/workspace/child")}
			if exact {
				grant.Exact = &action
			}
			childIn.HumanGrants = []ApprovedGrant{grant}
			child := mustPolicy(t, policyConfig(ModeAsk), Selection{}, &parent, childIn)
			if child.Handling(action) != HandlingAllow {
				t.Fatal("operator-permitted human child grant denied")
			}
			if parent.Handling(action) != HandlingDeny || !reflect.DeepEqual(original, parent.Authority()) {
				t.Fatal("child grant mutated parent")
			}
			siblingIn := childIn
			siblingIn.RunID = "sibling"
			sibling := mustPolicy(t, policyConfig(ModeAsk), Selection{}, &parent, siblingIn)
			if sibling.Handling(action) != HandlingDeny {
				t.Fatal("child grant authorized sibling")
			}
			if child.Handling(policyAction(KindFSWrite, "/workspace/child/b")) != map[bool]Handling{false: HandlingAllow, true: HandlingDeny}[exact] {
				t.Fatal("exact or bounded scope not preserved")
			}
			denied := childIn
			denied.Operator.AllowChildGrants = false
			if p := mustPolicy(t, policyConfig(ModeAsk), Selection{}, &parent, denied); p.Handling(action) != HandlingDeny {
				t.Fatal("operator child-grant prohibition bypassed")
			}
			denied = childIn
			denied.Operator.HardLimits = policyScope("/workspace/parent")
			if p := mustPolicy(t, policyConfig(ModeAsk), Selection{}, &parent, denied); p.Handling(action) != HandlingDeny {
				t.Fatal("human grant bypassed hard limit")
			}
		})
	}
}

func TestPolicyGrantsFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PolicyInputs, *PolicyAction)
	}{
		{"expired", func(in *PolicyInputs, _ *PolicyAction) { in.HumanGrants[0].ExpiresAt = in.Now }},
		{"wrong run", func(in *PolicyInputs, _ *PolicyAction) { in.HumanGrants[0].RunID = "sibling" }},
		{"wrong revision", func(in *PolicyInputs, _ *PolicyAction) { in.HumanGrants[0].PolicyRevision = "old" }},
		{"no current time", func(in *PolicyInputs, _ *PolicyAction) { in.Now = time.Time{} }},
		{"no current revision", func(in *PolicyInputs, _ *PolicyAction) { in.PolicyRevision = "" }},
		{"hard denial", func(in *PolicyInputs, _ *PolicyAction) { in.Operator.HardDenials = []Kind{KindFSWrite} }},
		{"bounded risky grant", func(_ *PolicyInputs, a *PolicyAction) { a.Request.Risk = RiskDestructive }},
		{"bounded persistence grant", func(_ *PolicyInputs, a *PolicyAction) { a.Request.Reasons = []Reason{ReasonPersistencePoint} }},
		{"network outside envelope", func(_ *PolicyInputs, a *PolicyAction) { a.Network = namedEffect("https://unlisted.test") }},
		{"credential outside envelope", func(_ *PolicyInputs, a *PolicyAction) { a.Credentials = namedEffect("unlisted") }},
		{"delegation denied", func(in *PolicyInputs, a *PolicyAction) {
			for i := range in.Operator.HardLimits.Rules {
				in.Operator.HardLimits.Rules[i].Delegation = false
			}
			a.Delegate = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := policyInputs()
			in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: in.Workflow}}
			a := policyAction(KindFSWrite, "/workspace/a")
			tc.mutate(&in, &a)
			p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
			if got := p.Handling(a); got != HandlingDeny {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func TestPolicyPlanInheritedAndImmutable(t *testing.T) {
	in := policyInputs()
	in.Preauthorized = []Authority{in.Workflow}
	parent := mustPolicy(t, policyConfig(ModeAuto), Selection{Focus: FocusPlan}, nil, in)
	child := mustPolicy(t, policyConfig(ModeAuto), Selection{Focus: FocusDesign}, &parent, in)
	in.Workflow.Rules[0].Roots[0] = "/other"
	in.Operator.HardLimits.Rules[0].Tool = "other"
	in.Preauthorized[0].Rules[2].Command.Argv[0] = "/other/bin"
	returned := child.Authority()
	returned.Rules[0].Roots[0] = "/other"
	if child.Handling(policyAction(KindFSRead, "/workspace/a")) != HandlingAllow {
		t.Fatal("input/getter mutation changed policy")
	}
	for _, a := range []PolicyAction{policyAction(KindFSWrite, "/workspace/a"), {Request: Request{Boundary: policyBoundary, Kind: KindCLIExec, Tool: "cli", BinaryPath: "/usr/bin/git", Binary: policyBinary(), Argv: []string{"/usr/bin/git", "status"}, Cwd: "/workspace"}}, {Request: Request{Boundary: policyBoundary, Kind: KindFSRead, Tool: "fs", Paths: []string{"/workspace/a"}}, Delegate: true}} {
		if got := child.Handling(a); got != HandlingDeny {
			t.Fatal("child design bypassed inherited Plan")
		}
	}
	if !child.ReadOnly() {
		t.Fatal("child lost inherited Plan restriction")
	}
	var zero EffectivePolicy
	if zero.Handling(policyAction(KindFSRead, "/workspace/a")) != HandlingDeny {
		t.Fatal("zero policy allowed action")
	}
}

func TestPolicyCommandsAreExact(t *testing.T) {
	in := policyInputs()
	in.Preauthorized = []Authority{in.Workflow}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	cases := []struct {
		argv []string
		cwd  string
		want Handling
	}{
		{[]string{"/usr/bin/git", "status"}, "/workspace", HandlingAllow},
		{[]string{"/usr/bin/git", "status", "--short"}, "/workspace", HandlingDeny},
		{[]string{"/usr/bin/git", "push"}, "/workspace", HandlingDeny},
		{[]string{"/usr/bin/git", "status"}, "/elsewhere", HandlingDeny},
	}
	for _, tc := range cases {
		a := PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Boundary: policyBoundary, Kind: KindCLIExec, Tool: "cli", BinaryPath: "/usr/bin/git", Binary: policyBinary(), Argv: tc.argv, Cwd: tc.cwd}}
		if got := p.Handling(a); got != tc.want {
			t.Fatalf("%v %s => %s", tc.argv, tc.cwd, got)
		}
	}
}

func TestPolicyRejectsMalformedAuthority(t *testing.T) {
	for _, mutate := range []func(*AuthorityRule){
		func(r *AuthorityRule) { r.Roots = []string{"relative"} },
		func(r *AuthorityRule) { r.Roots = []string{"/workspace/../other"} },
		func(r *AuthorityRule) { r.Kind = Kind("unknown") },
		func(r *AuthorityRule) { r.Command.Argv[0] = "git" },
		func(r *AuthorityRule) { r.Tool = "" },
		func(r *AuthorityRule) { r.Roots = nil },
		func(r *AuthorityRule) { r.Command = nil },
		func(r *AuthorityRule) { r.Kind = KindFSRead },
	} {
		a := Authority{Rules: []AuthorityRule{policyScope("/workspace").Rules[2]}}
		mutate(&a.Rules[0])
		in := policyInputs()
		in.Operator.HardLimits = a
		if _, err := ResolvePolicy(policyConfig(ModeAsk), Selection{}, nil, in); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
}

func TestPolicyGrantExpiryRevalidatedAtAction(t *testing.T) {
	in := policyInputs()
	expiry := in.Now.Add(time.Minute)
	in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: expiry, Authority: in.Workflow}}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	a := policyAction(KindFSWrite, "/workspace/a")
	if p.HandlingAt(a, in.Now) != HandlingAllow || p.HandlingAt(a, expiry) != HandlingDeny {
		t.Fatal("grant expiry was cached at resolution time")
	}
}

func TestPolicyBoundaryAndEnvironmentCeilings(t *testing.T) {
	for _, dimension := range []string{"boundary", "environment"} {
		for _, ceiling := range []string{"hard", "requestable", "workflow", "parent"} {
			t.Run(dimension+"/"+ceiling, func(t *testing.T) {
				in := policyInputs()
				action := policyAction(KindFSRead, "/workspace/a")
				action.Request.EnvNames = []string{"LANG"}
				restrict := func(a *Authority) {
					for i := range a.Rules {
						if dimension == "boundary" {
							a.Rules[i].Boundary = Boundary{Type: "container", Label: "isolated"}
						} else {
							a.Rules[i].EnvNames = nil
						}
					}
				}
				var parent *EffectivePolicy
				switch ceiling {
				case "hard":
					restrict(&in.Operator.HardLimits)
				case "requestable":
					restrict(&in.Operator.Requestable)
				case "workflow":
					restrict(&in.Workflow)
				case "parent":
					parentIn := policyInputs()
					restrict(&parentIn.Workflow)
					p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, parentIn)
					parent = &p
				}
				in.Preauthorized = []Authority{policyScope("/workspace")}
				p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, parent, in)
				if got := p.Handling(action); got != HandlingDeny {
					t.Fatalf("escaped %s ceiling: %s", ceiling, got)
				}
			})
		}
	}
	in := policyInputs()
	in.Preauthorized = []Authority{in.Workflow}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Boundary = Boundary{} },
		func(r *Request) { r.Boundary.Label = "different host" },
		func(r *Request) { r.EnvNames = []string{"lang"} },
		func(r *Request) { r.EnvNames = []string{"LANG", "TOKEN"} },
		func(r *Request) { r.EnvNames = []string{"LANG=override"} },
	} {
		a := policyAction(KindFSRead, "/workspace/a")
		mutate(&a.Request)
		if got := p.Handling(a); got != HandlingDeny {
			t.Fatalf("unbounded action allowed: %s", got)
		}
	}
}

func TestPolicyGrantBoundaryAndEnvironmentScope(t *testing.T) {
	for _, source := range []string{"preauthorized", "routine", "human"} {
		for _, dimension := range []string{"boundary", "environment"} {
			t.Run(source+"/"+dimension, func(t *testing.T) {
				in := policyInputs()
				grant := policyScope("/workspace")
				for i := range grant.Rules {
					if dimension == "boundary" {
						grant.Rules[i].Boundary = Boundary{Type: "container", Label: "isolated"}
					} else {
						grant.Rules[i].EnvNames = nil
					}
				}
				mode, want := ModeAuto, HandlingDeny
				switch source {
				case "preauthorized":
					in.Preauthorized = []Authority{grant}
				case "routine":
					mode, want = ModeSemiAuto, HandlingAsk
					in.Routine = []Authority{grant}
				case "human":
					mode, want = ModeAsk, HandlingAsk
					in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: grant}}
				}
				action := policyAction(KindFSRead, "/workspace/a")
				action.Request.EnvNames = []string{"LANG"}
				p := mustPolicy(t, policyConfig(mode), Selection{}, nil, in)
				if got := p.Handling(action); got != want {
					t.Fatalf("grant widened %s: got %s want %s", dimension, got, want)
				}
			})
		}
	}
}

func TestPolicyCommandExecutableIdentity(t *testing.T) {
	path := t.TempDir() + "/command"
	if err := os.WriteFile(path, []byte("original"), 0700); err != nil {
		t.Fatal(err)
	}
	capture := func() FileID {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return NewFileID(fi)
	}
	original := capture()
	scope := policyScope("/workspace")
	scope.Rules[2].Command = &CommandAuthority{Argv: []string{path, "status"}, Binary: original}
	in := policyInputs()
	in.Operator.HardLimits, in.Operator.Requestable, in.Workflow = scope, scope, scope
	in.Preauthorized = []Authority{scope}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	action := PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Kind: KindCLIExec, Tool: "cli", Boundary: policyBoundary, BinaryPath: path, Binary: capture(), Argv: []string{path, "status"}, Cwd: "/workspace"}}
	if got := p.Handling(action); got != HandlingAllow {
		t.Fatalf("same file denied: %s", got)
	}
	action.Request.Binary = FileID{}
	if got := p.Handling(action); got != HandlingDeny {
		t.Fatalf("missing identity allowed: %s", got)
	}
	if err := os.WriteFile(path, []byte("changed size and contents"), 0700); err != nil {
		t.Fatal(err)
	}
	action.Request.Binary = capture()
	if !original.SameFile(action.Request.Binary) {
		t.Fatal("expected unchanged inode")
	}
	if got := p.Handling(action); got != HandlingDeny {
		t.Fatalf("mutated executable allowed: %s", got)
	}
	replacement := path + ".replacement"
	if err := os.WriteFile(replacement, []byte("original"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	action.Request.Binary = capture()
	if original.SameFile(action.Request.Binary) {
		t.Fatal("expected replacement inode")
	}
	if got := p.Handling(action); got != HandlingDeny {
		t.Fatalf("replacement executable allowed: %s", got)
	}
	changed := cloneAuthority(scope)
	changed.Rules[2].Command = &CommandAuthority{Argv: []string{path, "status"}, Binary: action.Request.Binary}
	in.Workflow = changed
	in.Preauthorized = []Authority{changed}
	p = mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	if got := p.Handling(action); got != HandlingDeny {
		t.Fatalf("intersection dropped identity: %s", got)
	}
}

func TestPolicyNewAuthorityFieldsAreValidatedAndCopied(t *testing.T) {
	for _, mutate := range []func(*AuthorityRule){
		func(r *AuthorityRule) { r.Boundary = Boundary{Type: "host"} },
		func(r *AuthorityRule) { r.Boundary = Boundary{Label: "host execution"} },
		func(r *AuthorityRule) { r.EnvNames = []string{"BAD=VALUE"} },
		func(r *AuthorityRule) { r.Command.Binary = FileID{} },
		func(r *AuthorityRule) { r.Network = namedEffect("") },
		func(r *AuthorityRule) { r.Credentials = namedEffect("") },
	} {
		scope := Authority{Rules: []AuthorityRule{policyScope("/workspace").Rules[2]}}
		mutate(&scope.Rules[0])
		in := policyInputs()
		in.Workflow = scope
		if _, err := ResolvePolicy(policyConfig(ModeAuto), Selection{}, nil, in); err == nil {
			t.Fatalf("accepted malformed scope: %+v", scope)
		}
	}
	in := policyInputs()
	in.Preauthorized = []Authority{in.Workflow}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	in.Workflow.Rules[0].Boundary = Boundary{Type: "other", Label: "other"}
	in.Workflow.Rules[0].EnvNames[0] = "TOKEN"
	exported := p.Authority()
	exported.Rules[0].Boundary = Boundary{}
	exported.Rules[0].EnvNames[0] = "TOKEN"
	exported.Rules[2].Command.Argv[0] = "/different"
	action := policyAction(KindFSRead, "/workspace/a")
	action.Request.EnvNames = []string{"LANG"}
	if got := p.Handling(action); got != HandlingAllow {
		t.Fatalf("mutation affected policy: %s", got)
	}
}

func TestPolicyExactGrantBindsBoundaryLabel(t *testing.T) {
	in := policyInputs()
	other := Boundary{Type: "host", Label: "other host"}
	for _, a := range []*Authority{&in.Operator.HardLimits, &in.Operator.Requestable, &in.Workflow} {
		alternatives := cloneAuthority(*a)
		for i := range alternatives.Rules {
			alternatives.Rules[i].Boundary = other
		}
		a.Rules = append(a.Rules, alternatives.Rules...)
	}
	exact := policyAction(KindFSWrite, "/workspace/a")
	in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: in.Workflow, Exact: &exact}}
	p := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	if got := p.Handling(exact); got != HandlingAllow {
		t.Fatalf("exact grant denied: %s", got)
	}
	changed := exact
	changed.Request.Boundary = other
	if got := p.Handling(changed); got != HandlingAsk {
		t.Fatalf("exact boundary scope widened: %s", got)
	}
}

func TestPolicyRulesKeepCompleteActionScopes(t *testing.T) {
	read := cloneAuthorityRule(policyScope("/workspace/a").Rules[0])
	write := cloneAuthorityRule(policyScope("/workspace/b").Rules[1])
	host := cloneAuthorityRule(policyScope("/workspace/a").Rules[2])
	container := cloneAuthorityRule(policyScope("/workspace/b").Rules[2])
	container.Boundary = Boundary{Type: "container", Label: "isolated"}
	container.Command.Argv = append(container.Command.Argv, "--short")
	otherTool := cloneAuthorityRule(read)
	otherTool.Tool = "other-fs"
	read.EnvNames, read.Network, read.Credentials, read.Delegation = nil, EffectScope{Level: EffectNone}, EffectScope{Level: EffectNone}, false
	secondWrite := cloneAuthorityRule(write)
	secondWrite.Roots = []string{"/workspace/c"}
	scope := Authority{Rules: []AuthorityRule{read, write, host, container, otherTool, secondWrite}}
	in := policyInputs()
	in.Operator.HardLimits, in.Operator.Requestable, in.Workflow = scope, scope, scope
	in.Preauthorized = []Authority{scope}
	p := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	cli := PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Kind: KindCLIExec, Tool: "cli", Boundary: policyBoundary, BinaryPath: "/usr/bin/git", Binary: policyBinary(), Argv: []string{"/usr/bin/git", "status"}, Cwd: "/workspace/a"}}
	cases := []struct {
		name   string
		action PolicyAction
		want   Handling
	}{
		{"read own root", policyAction(KindFSRead, "/workspace/a/file"), HandlingAllow},
		{"write own root", policyAction(KindFSWrite, "/workspace/b/file"), HandlingAllow},
		{"write cannot borrow read root", policyAction(KindFSWrite, "/workspace/a/file"), HandlingDeny},
		{"host command", cli, HandlingAllow},
		{"paths cannot span rules", PolicyAction{Network: EffectScope{Level: EffectNone}, Credentials: EffectScope{Level: EffectNone}, Request: Request{Kind: KindFSWrite, Tool: "fs", Boundary: policyBoundary, Paths: []string{"/workspace/b/file", "/workspace/c/file"}}}, HandlingDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Handling(tc.action); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	for _, mutate := range []func(*PolicyAction){
		func(a *PolicyAction) { a.Request.Cwd = "/workspace/b" },
		func(a *PolicyAction) { a.Request.Argv = container.Command.Argv },
		func(a *PolicyAction) { a.Request.Boundary = container.Boundary },
		func(a *PolicyAction) { a.Request.Paths = []string{"/workspace/b/file"} },
	} {
		action := cli
		mutate(&action)
		if got := p.Handling(action); got != HandlingDeny {
			t.Fatalf("command borrowed another rule's scope: %s", got)
		}
	}
	cli.Request.Argv, cli.Request.Boundary, cli.Request.Cwd = container.Command.Argv, container.Boundary, "/workspace/b"
	if got := p.Handling(cli); got != HandlingAllow {
		t.Fatalf("complete container command denied: %s", got)
	}
	for _, mutate := range []func(*PolicyAction){
		func(a *PolicyAction) { a.Request.EnvNames = []string{"LANG"} },
		func(a *PolicyAction) { a.Network = namedEffect("https://example.test") },
		func(a *PolicyAction) { a.Credentials = namedEffect("provider") },
		func(a *PolicyAction) { a.Delegate = true },
	} {
		action := policyAction(KindFSRead, "/workspace/a/file")
		mutate(&action)
		if got := p.Handling(action); got != HandlingDeny {
			t.Fatalf("tool borrowed another tool's capability: %s", got)
		}
		action.Request.Tool = "other-fs"
		if got := p.Handling(action); got != HandlingAllow {
			t.Fatalf("capability denied to its own tool: %s", got)
		}
	}
	if coversAuthority(Authority{}, policyAction(KindFSRead, "/workspace/a/file")) {
		t.Fatal("empty authority covered an action")
	}
}

func TestPolicyRuleIntersectionAndChildScope(t *testing.T) {
	wide := cloneAuthorityRule(policyScope("/workspace").Rules[1])
	wide.EnvNames = []string{"LANG", "PATH"}
	wide.Network = namedEffect("https://example.test", "https://other.test")
	wide.Credentials = namedEffect("provider", "other-provider")
	narrow := cloneAuthorityRule(wide)
	narrow.Roots = []string{"/workspace/a"}
	narrow.EnvNames, narrow.Network, narrow.Credentials, narrow.Delegation = []string{"LANG"}, namedEffect("https://example.test"), namedEffect("provider"), false
	other := cloneAuthorityRule(wide)
	other.Roots = []string{"/workspace/b"}
	other.EnvNames, other.Network, other.Credentials = []string{"PATH"}, namedEffect("https://other.test"), namedEffect("other-provider")
	ceiling := Authority{Rules: []AuthorityRule{wide}}
	parentScope := Authority{Rules: []AuthorityRule{narrow, other}}
	if got := intersectAuthority(ceiling, parentScope); !reflect.DeepEqual(got, parentScope) {
		t.Fatalf("pairwise intersection lost narrower scopes: %+v", got)
	}
	in := policyInputs()
	in.Operator.HardLimits, in.Operator.Requestable, in.Workflow = ceiling, ceiling, parentScope
	in.Preauthorized = []Authority{ceiling}
	parent := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	original := parent.Authority()
	childIn := policyInputs()
	childRule := cloneAuthorityRule(wide)
	childRule.Roots = []string{"/workspace/a/child"}
	childIn.Operator.HardLimits, childIn.Operator.Requestable = ceiling, ceiling
	childIn.Workflow = Authority{Rules: []AuthorityRule{childRule}}
	childIn.Preauthorized = []Authority{ceiling}
	child := mustPolicy(t, policyConfig(ModeAuto), Selection{}, &parent, childIn)
	siblingIn := childIn
	siblingIn.Workflow = parentScope
	sibling := mustPolicy(t, policyConfig(ModeAuto), Selection{}, &parent, siblingIn)
	expected := cloneAuthorityRule(narrow)
	expected.Roots = []string{"/workspace/a/child"}
	if got := child.Authority(); !reflect.DeepEqual(got, Authority{Rules: []AuthorityRule{expected}}) {
		t.Fatalf("child widened parent rule: %+v", got)
	}
	action := policyAction(KindFSWrite, "/workspace/a/child/file")
	action.Request.EnvNames, action.Network, action.Credentials = []string{"LANG"}, namedEffect("https://example.test"), namedEffect("provider")
	if child.Handling(action) != HandlingAllow {
		t.Fatal("narrow complete child action denied")
	}
	action.Request.EnvNames = []string{"PATH"}
	if child.Handling(action) != HandlingDeny {
		t.Fatal("child borrowed sibling root's environment")
	}
	if child.Handling(policyAction(KindFSWrite, "/workspace/a/outside")) != HandlingDeny || sibling.Handling(policyAction(KindFSWrite, "/workspace/b/file")) != HandlingAllow {
		t.Fatal("child scope affected sibling")
	}
	if !reflect.DeepEqual(original, parent.Authority()) {
		t.Fatal("intersection mutated parent")
	}
}

func namedEffect(names ...string) EffectScope {
	return EffectScope{Level: EffectNamed, Names: names}
}

func TestPolicyEffectScopes(t *testing.T) {
	none := EffectScope{Level: EffectNone}
	ambient := EffectScope{Level: EffectAmbient}
	for _, dimension := range []string{"network", "credentials"} {
		t.Run(dimension, func(t *testing.T) {
			setAction := func(a *PolicyAction, scope EffectScope) {
				if dimension == "network" {
					a.Network = scope
				} else {
					a.Credentials = scope
				}
			}
			setRule := func(r *AuthorityRule, scope EffectScope) {
				if dimension == "network" {
					r.Network = scope
				} else {
					r.Credentials = scope
				}
			}
			for _, mode := range []Mode{ModeAsk, ModeSemiAuto, ModeAuto} {
				in := policyInputs()
				for _, bound := range []*Authority{&in.Operator.HardLimits, &in.Operator.Requestable, &in.Workflow} {
					for i := range bound.Rules {
						setRule(&bound.Rules[i], ambient)
					}
				}
				in.Preauthorized, in.Routine = []Authority{in.Workflow}, []Authority{in.Workflow}
				action := policyAction(KindFSRead, "/workspace/file")
				setAction(&action, ambient)
				policy := mustPolicy(t, policyConfig(mode), Selection{}, nil, in)
				want := HandlingAsk
				if mode == ModeAuto {
					want = HandlingAllow
				}
				if got := policy.Handling(action); got != want {
					t.Fatalf("ambient %s: %s", mode, got)
				}
				for _, unknown := range []EffectScope{{}, {Level: EffectUnknown}, {Level: EffectNamed}, {Level: EffectLevel("unrecognized")}} {
					setAction(&action, unknown)
					exact := action
					in.HumanGrants = []ApprovedGrant{{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: in.Workflow, Exact: &exact}}
					policy = mustPolicy(t, policyConfig(mode), Selection{}, nil, in)
					if got := policy.Handling(action); got != HandlingDeny {
						t.Fatalf("unknown %s scope allowed in %s: %s", dimension, mode, got)
					}
				}
			}
			in := policyInputs()
			for _, bound := range []*Authority{&in.Operator.HardLimits, &in.Operator.Requestable, &in.Workflow} {
				for i := range bound.Rules {
					setRule(&bound.Rules[i], none)
				}
			}
			in.Preauthorized = []Authority{in.Workflow}
			policy := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
			action := policyAction(KindFSRead, "/workspace/file")
			if got := policy.Handling(action); got != HandlingAllow {
				t.Fatalf("explicit none denied: %s", got)
			}
			setAction(&action, ambient)
			if got := policy.Handling(action); got != HandlingDeny {
				t.Fatalf("none covered ambient: %s", got)
			}
		})
	}
	for _, tc := range []struct{ a, b, want EffectScope }{
		{namedEffect("a", "b"), namedEffect("b", "c"), namedEffect("b")},
		{namedEffect("a"), namedEffect("b"), none},
		{ambient, namedEffect("a"), namedEffect("a")},
		{none, ambient, none},
		{EffectScope{}, ambient, EffectScope{Level: EffectUnknown}},
	} {
		if got := intersectEffectScope(tc.a, tc.b); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("intersection %+v %+v = %+v, want %+v", tc.a, tc.b, got, tc.want)
		}
	}
	if coversEffectScope(namedEffect("a"), namedEffect("a", "b")) || coversEffectScope(none, namedEffect("a")) || coversEffectScope(ambient, EffectScope{}) {
		t.Fatal("effect ceiling widened")
	}
}

func templatePolicyInputs() (PolicyInputs, PolicyAction) {
	in := policyInputs()
	rule := cloneAuthorityRule(in.Workflow.Rules[2])
	rule.Boundary = Boundary{Type: "host", Label: HostBoundaryLabel}
	rule.Command = &CommandAuthority{Executable: "/usr/bin/git", Binary: policyBinary(), TemplateID: "git-status:v1"}
	rule.Network, rule.Credentials = EffectScope{Level: EffectNone}, EffectScope{Level: EffectNone}
	scope := Authority{Rules: []AuthorityRule{rule}}
	in.Operator.HardLimits, in.Operator.Requestable, in.Workflow = scope, scope, scope
	action := PolicyAction{TemplateID: rule.Command.TemplateID, Network: rule.Network, Credentials: rule.Credentials, Request: Request{Kind: KindCLIExec, Tool: "cli", Boundary: rule.Boundary, BinaryPath: "/usr/bin/git", Binary: policyBinary(), Argv: []string{"/usr/bin/git", "status", "--short"}, Cwd: "/workspace"}}
	return in, action
}

func TestPolicyCommandTemplateCeilingAndExactGrants(t *testing.T) {
	in, action := templatePolicyInputs()
	ask := mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	if got := ask.Handling(action); got != HandlingAsk {
		t.Fatalf("template ceiling denied rendered argv: %s", got)
	}
	for _, mutate := range []func(*PolicyAction){
		func(a *PolicyAction) { a.TemplateID = "" },
		func(a *PolicyAction) { a.TemplateID = "git-status:v2" },
		func(a *PolicyAction) { a.Request.Binary = FileID{} },
		func(a *PolicyAction) {
			a.Request.BinaryPath = "/bin/sh"
			a.Request.Argv = []string{"/bin/sh", "-c", "pwd"}
		},
	} {
		changed := action
		mutate(&changed)
		if got := ask.Handling(changed); got != HandlingDeny {
			t.Fatalf("template identity widened: %s", got)
		}
	}
	in.Routine = []Authority{in.Workflow}
	semi := mustPolicy(t, policyConfig(ModeSemiAuto), Selection{}, nil, in)
	if got := semi.Handling(action); got != HandlingAsk {
		t.Fatalf("template treated as exact routine authority: %s", got)
	}
	auto := mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	if got := auto.Handling(action); got != HandlingDeny {
		t.Fatalf("template implicitly preauthorized: %s", got)
	}
	in.Preauthorized = []Authority{in.Workflow}
	if _, err := ResolvePolicy(policyConfig(ModeAuto), Selection{}, nil, in); err == nil {
		t.Fatal("template-only preauthorization accepted")
	}
	exact := cloneAuthority(in.Workflow)
	exact.Rules[0].Command.Argv = append([]string(nil), action.Request.Argv...)
	in.Preauthorized = []Authority{exact}
	auto = mustPolicy(t, policyConfig(ModeAuto), Selection{}, nil, in)
	if got := auto.Handling(action); got != HandlingAllow {
		t.Fatalf("exact argv preauthorization denied: %s", got)
	}
	changed := action
	changed.Request.Argv = []string{"/usr/bin/git", "status", "--branch"}
	if got := auto.Handling(changed); got != HandlingDeny {
		t.Fatalf("exact preauthorization widened argv: %s", got)
	}
	if got := intersectAuthority(in.Workflow, exact); !reflect.DeepEqual(got, exact) {
		t.Fatalf("template/exact intersection: %+v", got)
	}
	in.Preauthorized = nil
	grant := ApprovedGrant{RunID: in.RunID, PolicyRevision: in.PolicyRevision, ExpiresAt: in.Now.Add(time.Minute), Authority: in.Workflow}
	in.HumanGrants = []ApprovedGrant{grant}
	ask = mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	if got := ask.Handling(action); got != HandlingAsk {
		t.Fatalf("human template blanket grant allowed argv: %s", got)
	}
	in.HumanGrants[0].Exact = &action
	ask = mustPolicy(t, policyConfig(ModeAsk), Selection{}, nil, in)
	if got := ask.Handling(action); got != HandlingAllow {
		t.Fatalf("exact human action denied: %s", got)
	}
	if got := ask.Handling(changed); got != HandlingAsk {
		t.Fatalf("human action widened argv: %s", got)
	}
}
