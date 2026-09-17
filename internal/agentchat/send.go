package agentchat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Sentinel errors. Every rejection says what actually happened; none of them
// fail silently.
var (
	// ErrDisabled means settings.agent_chat.enabled is false.
	ErrDisabled = errors.New("directed agent chat is disabled (settings.agent_chat.enabled: false)")
	// ErrUnknownAgent means no roster entry matches the name. The returned
	// error wraps this and names the valid agents.
	ErrUnknownAgent = errors.New("unknown agent")
	// ErrHostAgent means the target is the host. The host's conversation is
	// the main thread, owned by the chat surface, not by this package.
	ErrHostAgent = errors.New("that is the host agent — its conversation is the main thread")
	// ErrStillRunning means the agent has a run in flight. Sending to a
	// running subagent is deferred to a separate design.
	ErrStillRunning = errors.New("agent is still running — you can talk to it once it finishes")
	// ErrBusy means this agent already has a directed-chat request in flight.
	ErrBusy = errors.New("agent is busy with another message")
	// ErrNoBuilder means the roster was constructed without a Builder, so no
	// agent can be rebuilt.
	ErrNoBuilder = errors.New("directed agent chat is not wired in this build")
)

// HistoryRunner is the slice of *agent.Agent that a directed chat needs.
// agent.Runner declares only Run(ctx, query), which cannot carry a retained
// conversation, so this package declares its own narrower contract.
//
// Contract: on a successful return, the runner must already have invoked the
// function registered via SetTranscriptSink with the full settled
// transcript. Send relies on that to capture the exact conversation,
// including any tool calls, rather than composing one by hand. This is
// enforced by convention, not by the type system — a runner that returns
// success without firing the sink leaves Send with only a degraded
// reconstruction of what happened (see the fallback in Send).
type HistoryRunner interface {
	RunWithHistory(ctx context.Context, query string, history []llm.Message) (string, error)
	SetTranscriptSink(fn func(history []llm.Message, runErr error))
	GetName() string
}

// Builder constructs a runnable agent by roster name. cleanup must be safe to
// call exactly once and is always called after the agent finishes.
//
// origin is this turn's correlation id. The rebuilt agent must emit its
// telemetry through a bus tagged with it (telemetry.EventBus.WithOrigin), so
// that a consumer can tell this side chat's events from the main run's. Agent
// name cannot carry that: the main run may be delegating to an agent of the
// same name at the same moment, and filtering on the name then blinds the main
// transcript. A builder that ignores origin is not wrong, only unfilterable.
type Builder func(ctx context.Context, name, origin string) (HistoryRunner, func(), error)

// Send delivers one message to one agent and returns its reply.
//
// The agent is rebuilt, replayed with its retained history, recorded, and
// destroyed — cleanup always runs. One request may be in flight per agent; a
// second concurrent Send to the same agent returns ErrBusy. Different agents
// run independently, which is what keeps thread switching fluid.
func (r *Roster) Send(ctx context.Context, agentName, message string) (string, error) {
	kind, history, err := r.claim(agentName)
	// Every event this turn produces — the pair below and everything the
	// rebuilt agent emits — carries the same origin, so the session JSONL can
	// be read back as one side chat rather than as events interleaved with a
	// main run that may be using the same agent name at the same time.
	origin := r.nextOrigin(agentName)
	bus := r.bus.WithOrigin(origin)
	// START is emitted for every outcome, including a rejection, and always
	// before the matching END. Emitting END alone on the rejection paths left
	// the session JSONL with an END nothing opened — the verified sequence for
	// one accepted then one rejected send was [START END END] — and that file
	// is advertised as the truthful record of side chats. claim resolves the
	// kind first so both halves of the pair carry the same label.
	label := kindLabel(kind)
	emitStart(bus, agentName, label, message)
	if err != nil {
		emitEnd(bus, agentName, label, "", 0, "rejected", err)
		return "", err
	}
	defer r.release(agentName)

	ag, cleanup, err := r.build(ctx, agentName, origin)
	if err != nil {
		err = fmt.Errorf("cannot rebuild %q: %w", agentName, err)
		emitEnd(bus, agentName, label, "", 0, "error", err)
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Capture the agent's own ReAct transcript rather than composing one by
	// hand: a side chat may call tools, and those calls are exactly what a
	// later "why did you do that?" needs to read.
	//
	// capturedErr is kept because the sink can report an outcome the return
	// value does not: agent.Agent hands back (partial answer, nil) when a run
	// stops on max_iterations. Recording that as "done" would tell the user
	// the agent answered. The hard-failure path below still discards its
	// partial transcript outright — a directed chat is a live conversation the
	// user is watching, an errored turn is reported to them on the spot, and
	// keeping the previous valid transcript beats replacing it with a
	// half-turn.
	var captured []llm.Message
	var capturedErr error
	ag.SetTranscriptSink(func(h []llm.Message, runErr error) {
		captured = append([]llm.Message(nil), h...)
		capturedErr = runErr
	})

	reply, err := ag.RunWithHistory(ctx, message, history)
	if err != nil {
		// If the sink already fired before the run failed, the transcript it
		// captured is deliberately discarded here, not recorded: a run that
		// died mid-flight can leave a dangling tool call with no matching
		// tool response, which is not a valid history to replay into the
		// next Send. Losing that partial transcript is the safe choice, not
		// an oversight.
		emitEnd(bus, agentName, label, "", 0, "error", err)
		return "", err
	}
	if captured == nil {
		// The sink didn't fire (see HistoryRunner's contract). Silently
		// keeping the previous transcript would drop the exchange that just
		// happened — the next Send would replay history missing this turn,
		// with nothing saying so. Compose a degraded-but-truthful record
		// instead, from what is actually known: the history that was
		// replayed, the user's message, and the reply the agent returned.
		// The only real loss is the run's own tool calls, not the substance
		// of the exchange.
		captured = append(append([]llm.Message(nil), history...),
			llm.NewTextMessage("user", message),
			llm.NewTextMessage("assistant", reply))
	}
	r.RecordTranscript(agentName, captured, capturedErr)
	emitEnd(bus, agentName, label, reply, 0, "success", nil)
	return reply, nil
}

// nextOrigin mints this turn's correlation id. Monotonic per Roster, and the
// agent name is included so a JSONL reader can see at a glance which side chat
// a tagged event belongs to without cross-referencing the START.
func (r *Roster) nextOrigin(agentName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.originSeq++
	return fmt.Sprintf("direct-%s-%d", agentName, r.originSeq)
}

// claim validates the request and reserves the agent's single in-flight slot,
// returning a copy of its retained history to replay.
func (r *Roster) claim(agentName string) (Kind, []llm.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.cfg.EffectiveEnabled() {
		return "", nil, ErrDisabled
	}
	if r.build == nil {
		return "", nil, ErrNoBuilder
	}
	e, ok := r.entries[agentName]
	if !ok {
		return "", nil, fmt.Errorf("%w %q — addressable agents: %s",
			ErrUnknownAgent, agentName, strings.Join(r.namesLocked(), ", "))
	}
	switch {
	case e.Kind == KindHost:
		return e.Kind, nil, ErrHostAgent
	case e.Status == StatusRunning:
		return e.Kind, nil, fmt.Errorf("%q: %w", agentName, ErrStillRunning)
	case e.inFlight:
		return e.Kind, nil, fmt.Errorf("%q: %w", agentName, ErrBusy)
	}
	e.inFlight = true
	return e.Kind, append([]llm.Message(nil), e.history...), nil
}

// kindLabel renders a roster Kind for telemetry. claim returns Kind("") when
// it cannot resolve one at all — disabled, no builder wired, or an
// unrecognized agent name — and this project's convention is that an unknown
// value renders as the literal string "unknown", never a blank or a guess.
func kindLabel(k Kind) string {
	if k == "" {
		return "unknown"
	}
	return string(k)
}

// release frees the agent's in-flight slot.
func (r *Roster) release(agentName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[agentName]; ok {
		e.inFlight = false
	}
}

// namesLocked lists addressable agent names in listing order. Caller holds mu.
func (r *Roster) namesLocked() []string {
	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if _, ok := r.entries[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// emitStart/emitEnd take the bus rather than reading r.bus so both halves of a
// turn's pair go out through that turn's origin-tagged view.
func emitStart(bus *telemetry.EventBus, agentName, kind, message string) {
	if bus == nil {
		return
	}
	bus.Emit(agentName, telemetry.EventDirectChatStart, telemetry.DirectChatStartPayload{
		Agent:   agentName,
		Kind:    kind,
		Message: message,
	})
}

func emitEnd(bus *telemetry.EventBus, agentName, kind, reply string, tokens int, status string, err error) {
	if bus == nil {
		return
	}
	payload := telemetry.DirectChatEndPayload{
		Agent:  agentName,
		Kind:   kind,
		Reply:  reply,
		Tokens: tokens,
		Status: status,
	}
	if err != nil {
		payload.Error = err.Error()
	}
	bus.Emit(agentName, telemetry.EventDirectChatEnd, payload)
}
