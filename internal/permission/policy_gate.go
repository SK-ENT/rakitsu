package permission

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// AuthorizeAction requires a Gate and a policy. Adapters supply the action's
// network, credential and delegation dimensions from the operation they will
// execute, never from model text. Authorize(Request) remains the legacy API.
func AuthorizeAction(ctx context.Context, policy EffectivePolicy, action PolicyAction) Verdict {
	if g, ok := GateFrom(ctx); ok {
		return g.AuthorizeAction(ctx, policy, action)
	}
	return denyVerdict(SourceNoChannel, "Permission denied: no runtime permission gate is available; the action was not run.")
}

// AuthorizeAction evaluates the runtime ceiling before looking at approvals.
// Secure session approvals use exact action keys, separate from legacy grants.
func (g *Gate) AuthorizeAction(ctx context.Context, policy EffectivePolicy, action PolicyAction) Verdict {
	if g == nil || g.checker == nil || g.grants == nil || g.buildCard == nil {
		return denyVerdict(SourceNoChannel, "Permission denied: no runtime permission gate is available; the action was not run.")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	action.Request = normalize(action.Request)
	action.Network.Names = sortedUnique(action.Network.Names)
	action.Credentials.Names = sortedUnique(action.Credentials.Names)
	req := action.Request
	if ctx.Err() != nil {
		return g.deny(req, SourceCancelled, msgCancelled)
	}
	handling := policy.Handling(action)
	if handling == HandlingDeny {
		return g.deny(req, SourcePolicy, "Permission denied: the action exceeds its runtime policy; the action was not run.")
	}
	if policy.Selection().Mode == ModeLegacy {
		return g.authorize(ctx, req)
	}
	switch handling {
	case HandlingAllow:
		v := Verdict{Decision: Allow, Source: SourcePolicy}
		g.emitDecided(req, v)
		return v
	case HandlingAsk:
		fp := policyActionFingerprint(action)
		key := GrantKey("")
		if policyActionSessionAllowed(action) {
			key = GrantKey(fp)
		}
		return g.prompt(ctx, req, key, fp, func(r Request) (Card, error) {
			// BuildCard deliberately rejects caller-supplied boundary labels.
			// Here the action has already matched a policy rule, which pins the
			// adapter boundary. Keep the broker-owned host warning and display
			// that pinned label separately, escaped by policyActionCard.
			if r.Boundary.Type != "host" {
				return Card{}, ErrNotDisplayable
			}
			display := cloneRequest(r)
			display.Boundary.Label = HostBoundaryLabel
			card, err := g.buildCard(display)
			if err != nil {
				return Card{}, err
			}
			return policyActionCard(card, action)
		}, func() bool { return policy.Handling(action) != HandlingDeny }, policyActionSessionAllowed(action))
	default:
		return g.deny(req, SourcePolicy, "Permission denied: unknown runtime policy decision; the action was not run.")
	}
}

func policyActionFingerprint(action PolicyAction) string {
	var b strings.Builder
	b.WriteString("policy-action-v2 ")
	b.WriteString(strconv.Quote(action.TemplateID))
	b.WriteByte(' ')
	b.WriteString(fingerprint(action.Request))
	b.WriteString(strconv.Quote(action.Request.Boundary.Label))
	for _, scope := range []EffectScope{action.Network, action.Credentials} {
		b.WriteString(strconv.Quote(string(scope.Level)))
		values := sortedUnique(scope.Names)
		b.WriteString(fmt.Sprintf(" %d[", len(values)))
		for _, value := range values {
			b.WriteString(strconv.Quote(value))
			b.WriteByte(' ')
		}
		b.WriteByte(']')
	}
	b.WriteString(" " + strconv.FormatBool(action.Delegate))
	return b.String()
}

// Keep the existing broker card and show every extra dimension it approves.
// Refuse overlong cards rather than hiding authority from the human.
func policyActionCard(card Card, action PolicyAction) (Card, error) {
	var extra strings.Builder
	if action.Request.Boundary.Label != "" && action.Request.Boundary.Label != HostBoundaryLabel {
		if len(action.Request.Boundary.Label) > maxElemBytes || action.Request.Boundary.Type != "host" {
			return Card{}, ErrNotDisplayable
		}
		label, _ := Escape(action.Request.Boundary.Label)
		extra.WriteString("\nPolicy boundary: \"" + label + "\"")
	}
	for i, scope := range []EffectScope{action.Network, action.Credentials} {
		if !knownEffect(scope) {
			return Card{}, ErrNotDisplayable
		}
		values := scope.Names
		if len(values) > maxEnvNames {
			return Card{}, ErrNotDisplayable
		}
		extra.WriteString([]string{"\nNetwork targets:", "\nCredential scopes:"}[i])
		switch scope.Level {
		case EffectNone:
			extra.WriteString(" none")
		case EffectAmbient:
			extra.WriteString([]string{" ambient access to any network target", " ambient access to credentials available to the process"}[i])
		}
		for _, value := range values {
			if len(value) > maxElemBytes {
				return Card{}, ErrNotDisplayable
			}
			escaped, _ := Escape(value)
			extra.WriteString(" \"" + escaped + "\"")
		}
	}
	if action.TemplateID != "" {
		if len(action.TemplateID) > maxElemBytes {
			return Card{}, ErrNotDisplayable
		}
		escaped, _ := Escape(action.TemplateID)
		extra.WriteString("\nCommand template: \"" + escaped + "\"")
	}
	if !policyActionSessionAllowed(action) {
		extra.WriteString("\nApproval scope: one action only")
	}
	extra.WriteString("\nDelegation: " + strconv.FormatBool(action.Delegate))
	if len(card.Body)+extra.Len() > maxCardBytes {
		return Card{}, ErrNotDisplayable
	}
	card.Body += extra.String()
	return card, nil
}

func policyActionSessionAllowed(a PolicyAction) bool {
	return SessionAllowed(a.Request) && routineEffect(a.Network) && routineEffect(a.Credentials)
}
