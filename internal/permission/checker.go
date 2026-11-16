package permission

// checker decides from a request and the current grants alone. It is pure:
// no I/O, no clock, no globals. It may return decisionAsk, which the Gate
// resolves; that value never leaves the package.
type checker interface {
	check(req Request, grants grantView) Verdict
}

// defaultChecker rules:
//   - unknown Kind -> Deny (policy);
//   - no reasons and no risk -> Allow (policy);
//   - a session grant for KeyFor(req) -> Allow (session_grant); KeyFor is
//     empty, so never granted, for risky, persistence-point and
//     identity-less cli requests;
//   - otherwise Ask.
type defaultChecker struct{}

func (defaultChecker) check(req Request, grants grantView) Verdict {
	if !req.Kind.known() {
		return Verdict{Decision: Deny, Source: SourcePolicy,
			Message: "Permission denied: unknown action kind; the action was not run."}
	}
	if len(req.Reasons) == 0 && req.Risk == RiskNone {
		return Verdict{Decision: Allow, Scope: ScopeNone, Source: SourcePolicy}
	}
	if grants != nil {
		if k := KeyFor(req); k != "" && grants.Has(k) {
			return Verdict{Decision: Allow, Scope: ScopeSession, Source: SourceSessionGrant}
		}
	}
	return Verdict{Decision: decisionAsk}
}
