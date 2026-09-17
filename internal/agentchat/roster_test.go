package agentchat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

func newTestRoster(cfg config.AgentChatConfig) *Roster {
	return New(cfg, nil, nil)
}

func TestRosterListsAllThreeSources(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddHost("Coordinator", "gemini-3.5-flash-lite", "litellm")
	r.AddConfig("Reviewer", "gpt-4o-mini", "openai")
	r.MarkRunning("Researcher-1", KindSpawned, "gpt-4o-mini", "openai")

	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List() returned %d entries, want 3: %+v", len(got), got)
	}
	byName := map[string]Entry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if e := byName["Coordinator"]; e.Kind != KindHost || e.Status != StatusIdle {
		t.Errorf("host entry = %+v", e)
	}
	if e := byName["Reviewer"]; e.Kind != KindConfig || e.Status != StatusIdle {
		t.Errorf("config entry = %+v", e)
	}
	if e := byName["Researcher-1"]; e.Kind != KindSpawned || e.Status != StatusRunning {
		t.Errorf("spawned entry = %+v", e)
	}
}

func TestListPreservesInsertionOrder(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddHost("Host", "m", "p")
	r.AddConfig("B", "m", "p")
	r.AddConfig("A", "m", "p")
	want := []string{"Host", "B", "A"}
	got := r.List()
	for i, e := range got {
		if e.Name != want[i] {
			t.Fatalf("List()[%d] = %q, want %q (order must be insertion, not alphabetical)", i, e.Name, want[i])
		}
	}
}

func TestRecordTranscriptFlipsRunningToDone(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	r.RecordTranscript("Researcher-1", []llm.Message{
		llm.NewTextMessage("user", "go"),
		llm.NewTextMessage("assistant", "done"),
	}, nil)
	e := r.List()[0]
	if e.Status != StatusDone {
		t.Errorf("status = %q, want %q", e.Status, StatusDone)
	}
	if e.Turns != 1 {
		t.Errorf("Turns = %d, want 1 (one user message in the transcript)", e.Turns)
	}
}

func TestLastReply(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	if _, ok := r.LastReply("Researcher-1"); ok {
		t.Error("LastReply should report false before any assistant message")
	}
	r.RecordTranscript("Researcher-1", []llm.Message{
		llm.NewTextMessage("user", "go"),
		llm.NewTextMessage("assistant", "the README only"),
		llm.NewTextMessage("user", "and?"),
		llm.NewTextMessage("assistant", "nothing else"),
	}, nil)
	reply, ok := r.LastReply("Researcher-1")
	if !ok || reply != "nothing else" {
		t.Errorf("LastReply = (%q, %v), want (nothing else, true)", reply, ok)
	}
	if _, ok := r.LastReply("nobody"); ok {
		t.Error("LastReply for an unknown agent should report false")
	}
}

func TestRecordTranscriptForUnknownAgentCreatesSpawnedEntry(t *testing.T) {
	// The transcript sink can fire for a child whose MarkRunning was never
	// called (e.g. a build path that skipped it). Recording must still list
	// the agent rather than dropping its context on the floor.
	r := newTestRoster(config.AgentChatConfig{})
	r.RecordTranscript("Ghost-1", []llm.Message{llm.NewTextMessage("assistant", "hi")}, nil)
	got := r.List()
	if len(got) != 1 || got[0].Name != "Ghost-1" || got[0].Kind != KindSpawned {
		t.Fatalf("List() = %+v, want one spawned Ghost-1 entry", got)
	}
}

// TestAddHostPromotesExistingConfigEntry covers the single-agent-config
// default path: BuildRunner calls AddConfig for every cfg.Agents entry
// before runInteractive calls AddHost with the same name (no orchestrator ⇒
// the host's name IS the sole config agent's name). Without the promotion,
// the entry stays KindConfig forever — no "return to main conversation" in
// the picker, and Send would fork a second instance of the host with an
// empty transcript.
func TestAddHostPromotesExistingConfigEntry(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddConfig("Assistant", "gpt-4o-mini", "openai")
	r.AddHost("Assistant", "interactive", "")

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() = %+v, want exactly one entry", got)
	}
	if got[0].Kind != KindHost {
		t.Errorf("Kind = %q, want %q (AddHost must promote an existing config entry)", got[0].Kind, KindHost)
	}
}

// TestAddConfigCannotDowngradeHost covers the reverse registration order —
// AddHost first, AddConfig second — to guard against BuildRunner/runInteractive
// ever being reordered relative to each other.
func TestAddConfigCannotDowngradeHost(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddHost("Assistant", "interactive", "")
	r.AddConfig("Assistant", "gpt-4o-mini", "openai")

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() = %+v, want exactly one entry", got)
	}
	if got[0].Kind != KindHost {
		t.Errorf("Kind = %q, want %q (AddConfig must not downgrade an existing host entry)", got[0].Kind, KindHost)
	}
}

// TestAddHostDoesNotClobberModelFromConfig covers Finding 2: runInteractive
// passes a display label ("interactive", "chat-host (gpt-4o-mini)"), not a
// model name, in the single-agent case. AddHost must not let that label
// overwrite the real model AddConfig already recorded.
func TestAddHostDoesNotClobberModelFromConfig(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddConfig("Assistant", "gpt-4o-mini", "openai")
	r.AddHost("Assistant", "interactive", "")

	got := r.List()[0]
	if got.Model != "gpt-4o-mini" || got.Provider != "openai" {
		t.Errorf("Model/Provider = %q/%q, want gpt-4o-mini/openai (AddHost must not clobber a value AddConfig recorded)", got.Model, got.Provider)
	}
}

// TestAddHostLeavesUnknownModelEmpty: when no model is known, AddHost must
// not fabricate one — Entry.Model should stay empty rather than take on a
// display label.
func TestAddHostLeavesUnknownModelEmpty(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddHost("Assistant", "", "")

	got := r.List()[0]
	if got.Model != "" || got.Provider != "" {
		t.Errorf("Model/Provider = %q/%q, want empty (AddHost must not fabricate a value)", got.Model, got.Provider)
	}
}

func TestEnabledMirrorsConfig(t *testing.T) {
	off := false
	if newTestRoster(config.AgentChatConfig{Enabled: &off}).Enabled() {
		t.Error("Enabled() should be false when config disables it")
	}
	if !newTestRoster(config.AgentChatConfig{}).Enabled() {
		t.Error("Enabled() should default to true")
	}
}

// TestRecordTranscriptMarksFailedRunFailed is the I1 repro: RecordTranscript
// set StatusDone unconditionally and the sink fires on every exit path, so a
// child that hit settings.spawn.timeout_seconds or a provider error was shown
// to the user as "done — N turns". The user was told the child finished; it
// did not.
func TestRecordTranscriptMarksFailedRunFailed(t *testing.T) {
	r := New(config.AgentChatConfig{}, nil, nil)
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	r.RecordTranscript("Researcher-1", []llm.Message{
		llm.NewTextMessage("user", "go"),
		llm.NewTextMessage("assistant", "partial"),
	}, context.DeadlineExceeded)

	got := r.List()[0]
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q — a timed-out child must not read as done", got.Status, StatusFailed)
	}
}

// TestFailedAgentIsStillSendable pins the deliberate choice that a failed
// child stays addressable: "what happened?" is exactly the question a failed
// run raises, and its stored transcript is trimmed to a replayable boundary.
func TestFailedAgentIsStillSendable(t *testing.T) {
	r := New(config.AgentChatConfig{}, builderFor(&fakeAgent{name: "Researcher-1"}, nil), nil)
	r.MarkRunning("Researcher-1", KindSpawned, "m", "p")
	r.RecordTranscript("Researcher-1", []llm.Message{llm.NewTextMessage("user", "go")}, errors.New("provider exploded"))

	if _, _, err := r.claim("Researcher-1"); err != nil {
		t.Errorf("claim on a failed agent = %v, want nil — a failed child must stay addressable", err)
	}
}

// TestEntryCapNeverEvictsHostOrConfig regression-guards: enforceEntryCapLocked
// evicted unconditionally from the front of r.order once past maxEntries,
// with no exemption for KindHost/KindConfig. Since AddHost/AddConfig
// register at session start, they sit at the front of insertion order —
// exactly what gets evicted first. A long-running session that spawns more
// than maxEntries children over its lifetime would lose the Host entry
// itself, denying the user a way back to the main conversation (the reason
// an agent gets promoted to KindHost in the first place — see AddHost's own
// doc comment).
func TestEntryCapNeverEvictsHostOrConfig(t *testing.T) {
	r := newTestRoster(config.AgentChatConfig{})
	r.AddHost("Coordinator", "m", "p")
	r.AddConfig("Reviewer", "m", "p")

	for i := 0; i < maxEntries+50; i++ {
		r.MarkRunning(fmt.Sprintf("spawned-%d", i), KindSpawned, "m", "p")
	}

	got := r.List()
	byName := map[string]Entry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if _, ok := byName["Coordinator"]; !ok {
		t.Error("Host entry was evicted once the roster hit its cap — should never happen")
	}
	if _, ok := byName["Reviewer"]; !ok {
		t.Error("Config entry was evicted once the roster hit its cap — should never happen")
	}
	if len(got) > maxEntries {
		t.Errorf("List() returned %d entries, want at most %d — cap should still be enforced overall", len(got), maxEntries)
	}
}
