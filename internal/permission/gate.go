package permission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

const defaultAskTimeout = 120 * time.Second

// GateOptions configures a Gate.
type GateOptions struct {
	SessionID string        // copied into telemetry events
	Timeout   time.Duration // how long to wait for an answer; <= 0 uses 120s
}

// Gate turns Ask into a human decision through a Prompter. It is safe for
// concurrent use; prompts for one SessionGrants are serialized.
type Gate struct {
	checker   checker
	prompter  Prompter
	grants    *SessionGrants
	bus       *telemetry.EventBus
	sessionID string
	timeout   time.Duration
	buildCard func(Request) (Card, error) // BuildCard; unexported so callers cannot swap it
}

// NewGate returns a Gate. A nil Prompter denies every Ask ("no_channel");
// nil grants get a fresh in-memory set.
func NewGate(p Prompter, g *SessionGrants, opts GateOptions) *Gate {
	if g == nil {
		g = NewSessionGrants(0, nil)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultAskTimeout
	}
	return &Gate{
		checker:   defaultChecker{},
		prompter:  p,
		grants:    g,
		sessionID: opts.SessionID,
		timeout:   timeout,
		buildCard: BuildCard,
	}
}

// WithBus returns a shallow copy that emits telemetry to bus. The copy
// shares grants and the prompt lock with the original.
func (g *Gate) WithBus(bus *telemetry.EventBus) *Gate {
	if g == nil {
		return nil
	}
	c := *g
	c.bus = bus
	return &c
}

// Authorize decides a request. Only Allow or Deny leave this function.
func (g *Gate) Authorize(ctx context.Context, req Request) Verdict {
	if g == nil || g.checker == nil || g.grants == nil || g.buildCard == nil {
		// nil or zero-value Gate (not built by NewGate): same as no Gate.
		return noGateVerdict(req)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req = normalize(req)
	v := g.authorize(ctx, req)
	if v.Decision != Allow {
		if v.Source == "" {
			v = denyVerdict(SourcePolicy, "Permission denied; the action was not run.")
		}
		v.Decision, v.Scope = Deny, ScopeNone
	}
	return v
}

// normalize copies every slice (so a caller cannot change the request
// while it waits) and replaces the ID with a broker-generated one.
func normalize(req Request) Request {
	req.ParentChain = append([]string(nil), req.ParentChain...)
	req.Argv = append([]string(nil), req.Argv...)
	req.Paths = sortedUnique(req.Paths)
	req.EnvNames = append([]string(nil), req.EnvNames...)
	req.Reasons = append([]Reason(nil), req.Reasons...)
	req.ID = randomID() // "" on failure: Authorize then denies
	return req
}

func (g *Gate) authorize(ctx context.Context, req Request) Verdict {
	v := g.checker.check(req, g.grants)
	switch v.Decision {
	case Allow:
		if v.Source != SourcePolicy {
			g.emitDecided(req, v)
		}
		return v
	case decisionAsk:
	default:
		return g.deny(req, v.Source, v.Message)
	}
	return g.prompt(ctx, req, KeyFor(req), fingerprint(req), g.buildCard, nil)
}

func (g *Gate) prompt(ctx context.Context, req Request, key GrantKey, fp string, buildCard func(Request) (Card, error), revalidate func() bool, sessionAllowed ...bool) Verdict {
	if req.ID == "" {
		return g.deny(req, SourcePolicy, "Permission denied: could not create a request ID; the action was not run.")
	}

	// One prompt at a time per session; give up if the turn ends.
	if !g.grants.lock(ctx) {
		return g.deny(req, SourceCancelled, msgCancelled)
	}
	defer g.grants.unlock()

	// Re-check: a prompt we waited behind may have granted this.
	if revalidate != nil && !revalidate() {
		return g.deny(req, SourcePolicy, "Permission denied: the action no longer satisfies its runtime policy.")
	}
	if key != "" && g.grants.Has(key) {
		v := Verdict{Decision: Allow, Scope: ScopeSession, Source: SourceSessionGrant}
		g.emitDecided(req, v)
		return v
	}
	if g.grants.recentlyDenied(fp) {
		return g.deny(req, SourcePolicy,
			"Permission denied: the user just denied this exact action. Do not retry it; choose another approach or ask the user.")
	}
	if g.prompter == nil {
		return g.deny(req, SourceNoChannel, noChannelMessage(req, ""))
	}
	card, err := buildCard(req)
	if err != nil {
		return g.deny(req, SourceDisplayRefused,
			"Permission denied: the action cannot be shown in full for approval, so it was not run. Split the command or use the file tools.")
	}
	if !g.grants.takePromptSlot() {
		return g.deny(req, SourceRateLimited,
			"Permission denied: too many approval requests in the last minute, so this action was not run. Wait, or do the work in fewer actions.")
	}
	opts, err := optionsFor(req)
	if err != nil {
		return g.deny(req, SourcePolicy, "Permission denied: could not create approval options; the action was not run.")
	}
	if len(sessionAllowed) > 0 && !sessionAllowed[0] {
		filtered := opts[:0]
		for _, opt := range opts {
			if opt.Scope != ScopeSession {
				filtered = append(filtered, opt)
			}
		}
		opts = filtered
	}
	g.emitRequested(req, opts)

	pctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	ans, perr := g.prompter.Prompt(pctx, card, append([]Option(nil), opts...))

	// Context state wins over whatever the prompter returned: an answer
	// that arrives after the deadline or after cancellation is ignored.
	switch {
	case ctx.Err() != nil:
		return g.deny(req, SourceCancelled, msgCancelled)
	case pctx.Err() != nil:
		g.grants.recordDeny(fp)
		return g.deny(req, SourceTimeout, "Permission denied: no answer to the approval request in time, so the action was not run.")
	}
	if perr != nil {
		switch {
		case errors.Is(perr, ErrDisconnected):
			return g.deny(req, SourceDisconnected, "Permission denied: the approval channel disconnected, so the action was not run.")
		case errors.Is(perr, context.Canceled):
			return g.deny(req, SourceCancelled, msgCancelled)
		case errors.Is(perr, context.DeadlineExceeded):
			return g.deny(req, SourceTimeout, "Permission denied: no answer to the approval request in time, so the action was not run.")
		case errors.Is(perr, ErrNoChannel):
			return g.deny(req, SourceNoChannel, noChannelMessage(req, noChannelDetail(perr)))
		default:
			return g.deny(req, SourceNoChannel, noChannelMessage(req, ""))
		}
	}
	if ans.Cancelled {
		g.grants.recordDeny(fp)
		return g.deny(req, SourceCancelled, msgCancelled)
	}
	if revalidate != nil && !revalidate() {
		return g.deny(req, SourcePolicy, "Permission denied: the action no longer satisfies its runtime policy.")
	}
	var chosen *Option
	for i := range opts {
		if ans.OptionID != "" && opts[i].ID == ans.OptionID {
			chosen = &opts[i]
			break
		}
	}
	if chosen == nil {
		g.grants.recordDeny(fp)
		return g.deny(req, SourceUnknownOption, "Permission denied: the approval answer was not one of the offered options, so the action was not run.")
	}
	switch chosen.Scope {
	case ScopeOnce:
		v := Verdict{Decision: Allow, Scope: ScopeOnce, Source: SourceClientSelected}
		g.emitDecided(req, v)
		return v
	case ScopeSession:
		if key == "" { // not reachable: optionsFor offers session only when grantable
			return g.deny(req, SourceUnknownOption, "Permission denied: a session approval is not available for this action.")
		}
		g.grants.Add(key)
		v := Verdict{Decision: Allow, Scope: ScopeSession, Source: SourceClientSelected}
		g.emitDecided(req, v)
		return v
	default:
		g.grants.recordDeny(fp)
		return g.deny(req, SourceClientSelected,
			"Permission denied by the user. Do not retry the same action; choose another approach or ask the user.")
	}
}

const msgCancelled = "Permission denied: the approval request was cancelled, so the action was not run."

func denyVerdict(source, msg string) Verdict {
	return Verdict{Decision: Deny, Scope: ScopeNone, Source: source, Message: msg}
}

func (g *Gate) deny(req Request, source, msg string) Verdict {
	v := denyVerdict(source, msg)
	g.emitDecided(req, v)
	return v
}

func reasonList(req Request) string {
	if len(req.Reasons) == 0 && req.Risk != RiskNone {
		return string(ReasonRisky)
	}
	parts := make([]string, len(req.Reasons))
	for i, r := range req.Reasons {
		parts[i] = string(r)
	}
	return strings.Join(parts, ", ")
}

func noChannelMessage(req Request, detail string) string {
	msg := "Permission denied: this action needs approval (" + reasonList(req) +
		") but no approval channel is available, so it was not run. Stay within the configured commands and paths."
	if detail != "" {
		msg += " (" + detail + ")"
	}
	return msg
}

// noChannelDetail returns the DenyPrompter reason, if any.
func noChannelDetail(err error) string {
	s := err.Error()
	prefix := ErrNoChannel.Error() + ": "
	if rest, ok := strings.CutPrefix(s, prefix); ok {
		return rest
	}
	return ""
}

// optionsFor builds the offered options with fresh random IDs. Session is
// offered only when SessionAllowed.
func optionsFor(req Request) ([]Option, error) {
	scopes := []Scope{ScopeOnce}
	if SessionAllowed(req) && KeyFor(req) != "" {
		scopes = append(scopes, ScopeSession)
	}
	scopes = append(scopes, ScopeNone)
	names := map[Scope]string{
		ScopeOnce:    "Allow once",
		ScopeSession: "Allow for this session",
		ScopeNone:    "Deny",
	}
	opts := make([]Option, 0, len(scopes))
	for _, s := range scopes {
		id := randomID()
		if id == "" {
			return nil, errors.New("permission: random ID")
		}
		opts = append(opts, Option{ID: id, Name: names[s], Scope: s})
	}
	return opts, nil
}

// randomID returns 128 random bits as hex, or "" if the system random
// source fails.
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func (g *Gate) publish(req Request, t telemetry.EventType, payload any) {
	if g.bus == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	g.bus.Publish(telemetry.AgentEvent{
		ID:        randomID(),
		Timestamp: time.Now(),
		AgentName: req.Agent,
		EventType: t,
		// Redact at the source too, so in-process subscribers never see
		// raw argv secrets.
		Payload:   telemetry.RedactEventPayload(t, raw),
		SessionID: g.sessionID,
	})
}

func (g *Gate) emitRequested(req Request, opts []Option) {
	names := make([]string, len(opts))
	for i, o := range opts {
		if o.Scope == ScopeNone {
			names[i] = "deny"
		} else {
			names[i] = o.Scope.String()
		}
	}
	reasons := make([]string, len(req.Reasons))
	for i, r := range req.Reasons {
		reasons[i] = string(r)
	}
	g.publish(req, telemetry.EventPermissionRequested, telemetry.PermissionRequestedPayload{
		RequestID:        req.ID,
		SessionID:        g.sessionID,
		Tool:             req.Tool,
		Kind:             string(req.Kind),
		Reasons:          reasons,
		Risk:             string(req.Risk),
		Binary:           req.BinaryPath,
		Argv:             req.Argv,
		Paths:            req.Paths,
		PersistencePoint: req.has(ReasonPersistencePoint),
		Boundary:         req.Boundary.Type,
		ParentChain:      req.ParentChain,
		Options:          names,
	})
}

func (g *Gate) emitDecided(req Request, v Verdict) {
	g.publish(req, telemetry.EventPermissionDecided, telemetry.PermissionDecidedPayload{
		RequestID: req.ID,
		Decision:  v.Decision.String(),
		Scope:     v.Scope.String(),
		Source:    v.Source,
	})
}
