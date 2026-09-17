// Package agentchat implements directed agent chat: addressing one agent in
// a session directly, separate from the main conversation.
//
// The model is COLD RETENTION. Agents are destroyed on the normal schedule —
// a spawned child's tool registry is closed the moment its run returns, so no
// MCP subprocess outlives it. What survives is the child's transcript,
// captured through agent.SetTranscriptSink just before teardown. When the
// user wants to talk to that agent, it is rebuilt from its recorded
// definition and replayed with the retained history. Agents hold no
// conversation state of their own, which is what makes this possible.
//
// This package must not import package main. The rebuild step arrives as an
// injected Builder closure, the same pattern spawn.Factory uses.
package agentchat

import (
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Kind is where a roster entry came from.
type Kind string

const (
	// KindHost is the agent driving the main conversation. It is listed so
	// the picker can offer "return to the main conversation", but Send
	// rejects it — the host's history is owned by the chat surface.
	KindHost Kind = "host"
	// KindConfig is an agent declared in the YAML config.
	KindConfig Kind = "config"
	// KindSpawned is a runtime spawn_agent child.
	KindSpawned Kind = "spawned"
)

// Status is an entry's addressability at listing time.
type Status string

const (
	// StatusIdle means the agent exists but has never run.
	StatusIdle Status = "idle"
	// StatusDone means the agent finished and its transcript is retained.
	StatusDone Status = "done"
	// StatusFailed means the run ended in an error — a provider failure, a
	// cancelled context, or settings.spawn.timeout_seconds. Its transcript is
	// retained and it stays addressable: "what happened?" is exactly the
	// question a failed run raises, and the stored history is trimmed to a
	// replayable boundary before storage (see trimDanglingToolCalls). What
	// must never happen is showing it as done — the user would be told the
	// child finished when it did not.
	StatusFailed Status = "failed"
	// StatusIncomplete means the run stopped without failing and without
	// finishing — today, an agent that used up its max_iterations budget. It
	// is deliberately neither StatusDone nor StatusFailed: "done" would tell
	// the user the agent answered when it ran out of room, and "failed" would
	// invent an error that never happened. Its transcript is retained and it
	// stays addressable, like a failed run.
	StatusIncomplete Status = "incomplete"
	// StatusRunning means a run is in flight. Listed but not sendable —
	// live intervention is deferred to a separate design.
	StatusRunning Status = "running"
	// StatusEvicted means the transcript was dropped by the retention cap.
	// The entry survives so the agent never silently vanishes.
	StatusEvicted Status = "evicted"
)

// maxEntries bounds roster entries independent of the transcript cap. A very
// long session with heavy fan-out would otherwise accumulate one small Entry
// per child forever. Beyond this, the oldest entries are dropped whole and
// counted — see Roster.Dropped.
const maxEntries = 200

// retainNothingConfigValue is the AgentChatConfig.MaxRetained value that
// means "retain no transcripts". Mirrors config's own sentinel so tests and
// callers in this package don't reach into another package's unexported
// constant.
const retainNothingConfigValue = -1

// Entry is one addressable agent as shown to a user.
type Entry struct {
	Name     string
	Kind     Kind
	Status   Status
	Model    string
	Provider string
	// Turns is how many times the agent was addressed, counted from the full
	// conversation. It does not shrink when the stored transcript is truncated
	// for space, so it is not proof those turns are still retrievable — it
	// drops to 0 only when nothing is retained at all (StatusEvicted, or an
	// explicit ClearTranscript). A UI rendering this number should say
	// "N turns", not "N turns retained".
	Turns int
}

// entry is the internal record: the public Entry plus retained state.
type entry struct {
	Entry
	history  []llm.Message
	finished int64 // monotonic sequence at the last RecordTranscript; 0 = never
	inFlight bool
}

// Roster is the set of addressable agents for one process. Safe for
// concurrent use: the transcript sink fires from spawned-child goroutines
// while the chat surface reads the list.
type Roster struct {
	mu      sync.Mutex
	cfg     config.AgentChatConfig
	build   Builder
	bus     *telemetry.EventBus
	order   []string
	entries map[string]*entry
	seq     int64
	dropped int
	// originSeq numbers directed turns so each gets its own correlation id.
	originSeq int64
}

// New creates a Roster. build may be nil in tests that only exercise listing
// and retention; Send returns an error when it is. bus may be nil, in which
// case no telemetry is emitted.
func New(cfg config.AgentChatConfig, build Builder, bus *telemetry.EventBus) *Roster {
	return &Roster{
		cfg:     cfg,
		build:   build,
		bus:     bus,
		entries: make(map[string]*entry),
	}
}

// Enabled reports whether directed agent chat is on for this config.
func (r *Roster) Enabled() bool { return r.cfg.EffectiveEnabled() }

// AddHost registers the agent driving the main conversation. If name was
// already registered by AddConfig — the default in single-agent configs,
// where the host's name is just the sole config agent's name — the existing
// entry is promoted to KindHost rather than left KindConfig. An agent that
// also drives the main conversation IS the host; that is not ambiguous, and
// leaving it KindConfig would deny the user a way back to it once
// thread-switching lands (the picker only offers "return to main
// conversation" for KindHost) and let Send fork a second instance of it.
//
// model/provider fill Entry.Model/Provider only when currently empty, so
// this can never clobber a real value AddConfig already recorded — callers
// like runInteractive often only have a display label ("chat-host
// (gpt-4o-mini)"), not a model name, and an unknown value must stay empty
// rather than become a label.
func (r *Roster) AddHost(name, model, provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.ensureLocked(name, KindHost, model, provider)
	e.Kind = KindHost
	e.Status = StatusIdle
	if e.Model == "" && model != "" {
		e.Model = model
	}
	if e.Provider == "" && provider != "" {
		e.Provider = provider
	}
}

// AddConfig registers a YAML-declared agent. Config agents are addressable
// whether or not they have ever run; their thread starts empty. Never
// downgrades an entry AddHost already promoted — registration order between
// the two is not guaranteed, so the host status must survive either order.
func (r *Roster) AddConfig(name, model, provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.ensureLocked(name, KindConfig, model, provider)
	if e.Kind != KindHost {
		e.Kind = KindConfig
	}
	e.Status = StatusIdle
}

// MarkRunning registers an agent as in flight. Called when a spawn_agent
// child is built, so a running child appears in the picker — marked, and not
// sendable — rather than being invisible until it finishes.
func (r *Roster) MarkRunning(name string, kind Kind, model, provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.ensureLocked(name, kind, model, provider)
	e.Status = StatusRunning
	if model != "" {
		e.Model = model
	}
	if provider != "" {
		e.Provider = provider
	}
}

// ensureLocked returns the entry for name, creating it if absent. Caller
// holds r.mu.
func (r *Roster) ensureLocked(name string, kind Kind, model, provider string) *entry {
	if e, ok := r.entries[name]; ok {
		return e
	}
	e := &entry{Entry: Entry{
		Name:     name,
		Kind:     kind,
		Status:   StatusIdle,
		Model:    model,
		Provider: provider,
	}}
	r.entries[name] = e
	r.order = append(r.order, name)
	r.enforceEntryCapLocked()
	return e
}

// enforceEntryCapLocked drops the oldest evictable entries once the hard cap
// is exceeded, counting them so the surface can say how many. Caller holds
// r.mu. KindHost/KindConfig entries are never evicted: they register at
// session start and would otherwise sit at the front of insertion order —
// exactly what a naive oldest-first eviction removes first. Losing the host
// entry specifically denies the user a way back to the main conversation,
// the exact thing promoting an agent to KindHost exists to prevent (see
// AddHost's doc comment). Only KindSpawned entries are evictable; if none
// remain, the cap can't be enforced further and the loop stops rather than
// touching Host/Config.
func (r *Roster) enforceEntryCapLocked() {
	for len(r.order) > maxEntries {
		idx := -1
		for i, name := range r.order {
			if e, ok := r.entries[name]; ok && e.Kind == KindSpawned {
				idx = i
				break
			}
		}
		if idx == -1 {
			return
		}
		oldest := r.order[idx]
		r.order = append(r.order[:idx], r.order[idx+1:]...)
		delete(r.entries, oldest)
		r.dropped++
	}
}

// List returns every addressable agent in insertion order: the host first,
// then config agents as declared, then spawned children as they appeared.
// Insertion order beats alphabetical here — it reads as the shape of the run.
func (r *Roster) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.order))
	for _, name := range r.order {
		if e, ok := r.entries[name]; ok {
			ent := e.Entry
			// A directed turn in flight is a running agent. claim reserves the
			// slot with the unexported inFlight flag rather than writing
			// Status, so deriving it here is what makes the picker tell the
			// truth without a prior status to save and restore.
			if e.inFlight {
				ent.Status = StatusRunning
			}
			out = append(out, ent)
		}
	}
	return out
}

// Dropped returns how many entries the hard entry cap discarded entirely.
// The picker shows this so a truncated list never reads as a complete one.
func (r *Roster) Dropped() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// LastReply returns the most recent assistant message retained for an agent.
// The second result is false when the agent is unknown, has no retained
// transcript, or that transcript contains no assistant message.
func (r *Roster) LastReply(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		return "", false
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		if e.history[i].Role == "assistant" && e.history[i].AsText() != "" {
			return e.history[i].AsText(), true
		}
	}
	return "", false
}
