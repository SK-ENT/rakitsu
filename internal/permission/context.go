package permission

import "context"

type gateKey struct{}
type callKey struct{}
type policyKey struct{}

// WithGate attaches a Gate to ctx. Tool adapters find it with GateFrom.
func WithGate(ctx context.Context, g *Gate) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, gateKey{}, g)
}

// GateFrom returns the Gate attached to ctx. A nil Gate counts as absent.
func GateFrom(ctx context.Context) (*Gate, bool) {
	if ctx == nil {
		return nil, false
	}
	g, _ := ctx.Value(gateKey{}).(*Gate)
	return g, g != nil
}

// WithPolicy attaches the immutable effective policy for one runtime/session.
// The value contains no mutable grant state; approvals remain owned by Gate.
func WithPolicy(ctx context.Context, policy EffectivePolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, policyKey{}, policy)
}

// PolicyFrom returns the effective policy attached to this runtime context.
func PolicyFrom(ctx context.Context) (EffectivePolicy, bool) {
	if ctx == nil {
		return EffectivePolicy{}, false
	}
	policy, ok := ctx.Value(policyKey{}).(EffectivePolicy)
	return policy, ok
}

// Authorize is what tool adapters call. With a Gate in ctx it delegates to
// Gate.Authorize. Without one, a clean request is allowed and anything that
// would need approval is denied with source "no_channel". Only Allow or
// Deny is returned.
func Authorize(ctx context.Context, req Request) Verdict {
	if g, ok := GateFrom(ctx); ok {
		return g.Authorize(ctx, req)
	}
	return noGateVerdict(req)
}

func noGateVerdict(req Request) Verdict {
	v := defaultChecker{}.check(req, nil)
	if v.Decision == Allow {
		return v
	}
	if v.Decision == Deny {
		return v
	}
	return denyVerdict(SourceNoChannel, noChannelMessage(req, ""))
}

// CallInfo is the model-side context of the current tool call, for display
// and audit only.
type CallInfo struct {
	ToolCallID, Agent string
	ParentChain       []string
	Rationale         string // untrusted model text
}

// WithCall attaches call info to ctx.
func WithCall(ctx context.Context, ci CallInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ci.ParentChain = append([]string(nil), ci.ParentChain...)
	return context.WithValue(ctx, callKey{}, ci)
}

// CallFrom returns the call info in ctx, or the zero value.
func CallFrom(ctx context.Context) CallInfo {
	if ctx == nil {
		return CallInfo{}
	}
	ci, _ := ctx.Value(callKey{}).(CallInfo)
	ci.ParentChain = append([]string(nil), ci.ParentChain...)
	return ci
}
