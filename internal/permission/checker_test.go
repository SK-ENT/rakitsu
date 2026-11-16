package permission

import "testing"

type mapView map[GrantKey]bool

func (m mapView) Has(k GrantKey) bool { return m[k] }

func TestChecker_CleanAllows(t *testing.T) {
	r := cliReq(t)
	r.Reasons = nil
	v := defaultChecker{}.check(r, mapView{})
	if v.Decision != Allow || v.Source != SourcePolicy {
		t.Fatalf("clean request: got %+v", v)
	}
}

func TestChecker_ReasonAsks(t *testing.T) {
	v := defaultChecker{}.check(cliReq(t), mapView{})
	if v.Decision != decisionAsk {
		t.Fatalf("got %+v, want ask", v)
	}
}

func TestChecker_RiskWithoutReasonStillAsks(t *testing.T) {
	r := cliReq(t)
	r.Reasons = nil
	r.Risk = RiskInterpreter
	if v := (defaultChecker{}).check(r, mapView{}); v.Decision != decisionAsk {
		t.Fatalf("risky request without reasons must ask, got %+v", v)
	}
}

func TestChecker_GrantAllows(t *testing.T) {
	r := cliReq(t)
	v := defaultChecker{}.check(r, mapView{KeyFor(r): true})
	if v.Decision != Allow || v.Source != SourceSessionGrant || v.Scope != ScopeSession {
		t.Fatalf("granted request: got %+v", v)
	}
}

func TestChecker_GrantNeverCoversRiskyOrPersistence(t *testing.T) {
	r := cliReq(t)
	key := KeyFor(r)
	risky := r
	risky.Risk = RiskShell
	pp := r
	pp.Reasons = append([]Reason{}, ReasonPersistencePoint)
	// Even a view that claims every key (including "") is granted must not
	// allow these.
	all := allView{}
	for _, req := range []Request{risky, pp} {
		if v := (defaultChecker{}).check(req, all); v.Decision == Allow {
			t.Fatalf("grant covered a non-grantable request %+v (key %q)", req, key)
		}
	}
}

func TestChecker_UnknownKindDenies(t *testing.T) {
	r := cliReq(t)
	r.Kind = "net_connect"
	if v := (defaultChecker{}).check(r, mapView{}); v.Decision != Deny {
		t.Fatalf("unknown kind must deny, got %+v", v)
	}
	r.Reasons = nil
	if v := (defaultChecker{}).check(r, mapView{}); v.Decision != Deny {
		t.Fatalf("unknown kind must deny even when clean, got %+v", v)
	}
}

type allView struct{}

func (allView) Has(GrantKey) bool { return true }
