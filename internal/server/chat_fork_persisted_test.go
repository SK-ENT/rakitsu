package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// PR D-5.5 — fork on a past (non-live) session. These tests exercise the
// disk-fallback path: resolvePersistedSession (the load-and-resolve core)
// and forkPersistedOptions (the past-session mirror of forkOptionsLocked).

// forkTestConfigYAML is a minimal valid config whose provider api_key is the
// literal "[REDACTED]" mask — what a YAML persisted by Redacted() looks like.
// Fork-on-past must de-redact it from request env_vars.
const forkTestConfigYAML = `name: fork-test
version: "1.0"
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: "[REDACTED]"
agents:
  - name: a1
    role: worker
    system_prompt: |
      You are a test agent.
`

// newForkTestManager builds a ChatManager backed by a temp-dir SessionStore
// (HOME is redirected so ~/.rakitsu/sessions is isolated per test) and a
// ConfigStore with forkTestConfigYAML registered inline. buildFunc is nil —
// resolvePersistedSession / forkPersistedOptions never touch it.
func newForkTestManager(t *testing.T) (*ChatManager, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.rakitsu/sessions
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	cs, err := NewConfigStore(nil)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	entry, err := cs.Inline(forkTestConfigYAML)
	if err != nil {
		t.Fatalf("Inline config: %v", err)
	}
	return NewChatManager(nil, cs, ss, nil), entry.ID
}

// persistForkFixture writes a .chat.json envelope for sessionID: a linear
// tree t1 -> t2 (each with one assistant entry, t2 active) plus a dead
// sibling t2-dead off t1. configID is stamped into the envelope.
func persistForkFixture(t *testing.T, m *ChatManager, sessionID, configID string) {
	t.Helper()
	tr := turntree.New[transcriptEntry]()
	if _, err := tr.AppendTurn("t1", "first question", ""); err != nil {
		t.Fatal(err)
	}
	_ = tr.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "first answer"})
	_ = tr.Finalize("t1", turntree.StatusComplete)
	if _, err := tr.AppendTurn("t2", "second question", "t1"); err != nil {
		t.Fatal(err)
	}
	_ = tr.AppendEntry("t2", transcriptEntry{Kind: "assistant", Text: "second answer"})
	_ = tr.Finalize("t2", turntree.StatusComplete)
	_, _ = tr.AddSibling("t2-dead", "t2", "wrong question")
	_, _ = tr.SwitchToSibling("t2-dead", -1) // t2 active again

	sess := &ChatSession{ID: sessionID, configID: configID, tree: tr}
	sess.mu.Lock()
	raw, err := sess.marshalChatTreeLocked()
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := m.sessionStore.SaveChatTree(sessionID, raw); err != nil {
		t.Fatalf("SaveChatTree: %v", err)
	}
}

// persistJSONLFixture writes a session with NO .chat.json — only a JSONL
// event log + a sessions.json index entry carrying the inline config YAML.
// This is the CLI-run / pre-PR-B shape that exercises the reconstruction
// fallback inside resolvePersistedSession.
func persistJSONLFixture(t *testing.T, sessionID string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".rakitsu", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Index entry — GetSession must return a non-nil meta so the JSONL
	// fallback can recover the inline config YAML.
	meta := store.SessionMeta{
		ID:         sessionID,
		ConfigYAML: forkTestConfigYAML,
		Workdir:    "/tmp/fork-workdir",
		StartTime:  time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
	}
	idx, err := json.MarshalIndent([]store.SessionMeta{meta}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), idx, 0o644); err != nil {
		t.Fatal(err)
	}

	// JSONL: line 1 is the meta, then two CHAT_TURN_START/END pairs.
	f, err := os.Create(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t0 := meta.StartTime
	enc := json.NewEncoder(f)
	if err := enc.Encode(meta); err != nil {
		t.Fatal(err)
	}
	events := []telemetry.AgentEvent{
		{EventType: telemetry.EventChatTurnStart, Timestamp: t0,
			Payload: mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 1, Text: "first question"})},
		{EventType: telemetry.EventChatTurnEnd, Timestamp: t0.Add(time.Second),
			Payload: mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 1, Final: "first answer"})},
		{EventType: telemetry.EventChatTurnStart, Timestamp: t0.Add(2 * time.Second),
			Payload: mustPayload(t, telemetry.ChatTurnStartPayload{Turn: 2, Text: "second question"})},
		{EventType: telemetry.EventChatTurnEnd, Timestamp: t0.Add(3 * time.Second),
			Payload: mustPayload(t, telemetry.ChatTurnEndPayload{Turn: 2, Final: "second answer"})},
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolvePersistedSessionFromChatJSON(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	persistForkFixture(t, m, "chat-past-1", cfgID)

	tree, _, resolvedID, _, err := m.resolvePersistedSession("chat-past-1", "", nil)
	if err != nil {
		t.Fatalf("resolvePersistedSession: %v", err)
	}
	if resolvedID != cfgID {
		t.Errorf("resolvedConfigID = %q, want %q (recovered from .chat.json envelope)", resolvedID, cfgID)
	}
	if _, ok := tree.Get("t2-dead"); !ok {
		t.Error("loaded tree dropped the dead sibling — the full tree should load")
	}
	if leaf := tree.ActiveLeaf(); leaf == nil || leaf.ID != "t2" {
		t.Errorf("loaded active leaf = %+v, want t2", leaf)
	}
}

func TestResolvePersistedSessionJSONLFallback(t *testing.T) {
	m, _ := newForkTestManager(t)
	persistJSONLFixture(t, "chat-jsonl-1")

	tree, _, resolvedID, workdir, err := m.resolvePersistedSession("chat-jsonl-1", "", nil)
	if err != nil {
		t.Fatalf("resolvePersistedSession (JSONL fallback): %v", err)
	}
	// No .chat.json envelope and no caller config id — the inline YAML on
	// SessionMeta must be re-registered to yield a usable config id.
	if resolvedID == "" {
		t.Error("resolvedConfigID empty — inline config_yaml was not re-registered")
	}
	if workdir != "/tmp/fork-workdir" {
		t.Errorf("workdir = %q, want /tmp/fork-workdir (from SessionMeta)", workdir)
	}
	path := tree.ActivePath()
	if len(path) != 2 {
		t.Fatalf("reconstructed tree active path len = %d, want 2", len(path))
	}
	if path[0].UserText != "first question" || path[1].UserText != "second question" {
		t.Errorf("reconstructed turns = [%q, %q], want [first question, second question]",
			path[0].UserText, path[1].UserText)
	}
}

func TestResolvePersistedSessionUnknownIDErrors(t *testing.T) {
	m, _ := newForkTestManager(t)
	if _, _, _, _, err := m.resolvePersistedSession("does-not-exist", "", nil); err == nil {
		t.Fatal("resolvePersistedSession on an unknown id should error")
	}
}

func TestForkPersistedOptionsActivePath(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	persistForkFixture(t, m, "chat-past-2", cfgID)

	opts, err := m.forkPersistedOptions(
		"chat-past-2", ForkActivePath, "",
		map[string]string{"OPENAI_API_KEY": "real-secret"},
	)
	if err != nil {
		t.Fatalf("forkPersistedOptions: %v", err)
	}
	// A fork is a fresh session: no ResumeID, but seeded with a tree.
	if opts.ResumeID != "" {
		t.Errorf("fork opts.ResumeID = %q, want empty (a fork is a new session)", opts.ResumeID)
	}
	if opts.ResumeTree == nil {
		t.Fatal("fork opts.ResumeTree is nil")
	}
	// Path mode drops the dead sibling, keeps the active leaf.
	if _, ok := opts.ResumeTree.Get("t2-dead"); ok {
		t.Error("path-mode fork kept the dead sibling t2-dead")
	}
	if _, ok := opts.ResumeTree.Get("t2"); !ok {
		t.Error("fork tree missing the active leaf t2")
	}
	// Config resolved, and the redacted api key de-redacted from env_vars.
	if opts.Cfg == nil {
		t.Fatal("fork opts.Cfg is nil")
	}
	if opts.ConfigID != cfgID {
		t.Errorf("fork opts.ConfigID = %q, want %q", opts.ConfigID, cfgID)
	}
	if p, ok := opts.Cfg.Settings.Providers["openai"]; !ok || p.APIKey != "real-secret" {
		t.Errorf("fork cfg openai api_key = %q, want de-redacted 'real-secret'", p.APIKey)
	}
}

func TestForkPersistedOptionsSubtreeKeepsSiblings(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	persistForkFixture(t, m, "chat-past-3", cfgID)

	opts, err := m.forkPersistedOptions("chat-past-3", ForkSubtree, "", nil)
	if err != nil {
		t.Fatalf("forkPersistedOptions (subtree): %v", err)
	}
	if _, ok := opts.ResumeTree.Get("t2-dead"); !ok {
		t.Error("subtree-mode fork dropped the sibling t2-dead")
	}
}

func TestForkPersistedOptionsOffPathTruncateErrors(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	persistForkFixture(t, m, "chat-past-4", cfgID)

	// t2-dead is not on the active path — path-mode truncate must reject it
	// so a stale id can't silently fork a dead branch.
	_, err := m.forkPersistedOptions("chat-past-4", ForkActivePath, "t2-dead", nil)
	if err == nil {
		t.Fatal("path-mode fork truncating at an off-path node should error")
	}
	if !errors.Is(err, turntree.ErrUnknownNode) {
		t.Errorf("want error wrapping turntree.ErrUnknownNode, got %v", err)
	}
}
