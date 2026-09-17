package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// TestMarshalChatTreeRoundTrip exercises the .chat.json envelope: a session
// with a few turns + branches serializes, parses back through
// LoadChatTreeFile, and yields a tree that walks the same active path.
func TestMarshalChatTreeRoundTrip(t *testing.T) {
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "hello", "")
	_ = tr.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "hi there"})
	_ = tr.Finalize("t1", turntree.StatusComplete)
	_, _ = tr.AppendTurn("t2a", "and now?", "t1")
	_ = tr.AppendEntry("t2a", transcriptEntry{Kind: "assistant", Text: "answer A"})
	_ = tr.Finalize("t2a", turntree.StatusComplete)
	// Edit-style sibling that becomes active.
	_, _ = tr.AddSibling("t2b", "t2a", "actually, different question")
	_ = tr.AppendEntry("t2b", transcriptEntry{Kind: "assistant", Text: "answer B"})
	_ = tr.Finalize("t2b", turntree.StatusComplete)

	sess := &ChatSession{
		ID:         "chat-fixture",
		configID:   "cfg-1",
		agentName:  "a1",
		modelLabel: "fake-model",
		created:    time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC),
		tree:       tr,
	}

	sess.mu.Lock()
	raw, err := sess.marshalChatTreeLocked()
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("marshalChatTreeLocked: %v", err)
	}

	// Envelope fields land where we expect — guard against silent rename.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	for _, k := range []string{"schema_version", "session_id", "config_id", "agent_name", "model", "created", "tree"} {
		if _, ok := probe[k]; !ok {
			t.Errorf("envelope missing field %q", k)
		}
	}

	loaded, err := LoadChatTreeFile(raw)
	if err != nil {
		t.Fatalf("LoadChatTreeFile: %v", err)
	}
	if loaded.SchemaVersion != chatTreeSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", loaded.SchemaVersion, chatTreeSchemaVersion)
	}
	if loaded.SessionID != "chat-fixture" || loaded.ConfigID != "cfg-1" || loaded.Model != "fake-model" {
		t.Errorf("envelope identity not preserved: %+v", loaded)
	}
	if !loaded.Created.Equal(sess.created) {
		t.Errorf("Created not preserved: got %v, want %v", loaded.Created, sess.created)
	}

	// The active path is still root → t2b after round-trip.
	path := loaded.Tree.ActivePath()
	if len(path) != 2 || path[0].ID != "t1" || path[1].ID != "t2b" {
		var ids []string
		for _, n := range path {
			ids = append(ids, n.ID)
		}
		t.Fatalf("loaded active path = %v, want [t1 t2b]", ids)
	}
	if len(loaded.Tree.Nodes["t2a"].Entries) == 0 || loaded.Tree.Nodes["t2a"].Entries[0].Text != "answer A" {
		t.Errorf("dead-branch t2a content lost on round-trip")
	}
}

// TestLoadChatTreeFileRejectsFutureSchema verifies that a blob from a
// newer schema_version fails loudly rather than silently dropping unknown
// fields and possibly corrupting state on resume.
func TestLoadChatTreeFileRejectsFutureSchema(t *testing.T) {
	raw := []byte(`{"schema_version":99,"session_id":"x","tree":{"nodes":{}}}`)
	_, err := LoadChatTreeFile(raw)
	if err == nil {
		t.Fatal("expected error for schema_version=99")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error should mention schema_version: %v", err)
	}
}

// TestLoadChatTreeFileNilTreeYieldsEmpty handles the edge case of a file
// written before the first turn — the tree key is absent. Loader returns a
// usable empty tree instead of leaving Tree nil.
func TestLoadChatTreeFileNilTreeYieldsEmpty(t *testing.T) {
	raw := []byte(`{"schema_version":1,"session_id":"x"}`)
	f, err := LoadChatTreeFile(raw)
	if err != nil {
		t.Fatalf("LoadChatTreeFile: %v", err)
	}
	if f.Tree == nil {
		t.Fatal("expected non-nil empty Tree")
	}
	if path := f.Tree.ActivePath(); len(path) != 0 {
		t.Errorf("empty tree has non-empty active path: %v", path)
	}
}

// TestStartChatSessionResumeSeedsTree verifies that ResumeID + ResumeTree
// + ResumeCreated together rehydrate a ChatSession byte-for-byte
// equivalent to its persisted form — the contract PR B promises to PR C
// and the resume HTTP flow.
func TestStartChatSessionResumeSeedsTree(t *testing.T) {
	// Build a tree as if a prior session had completed two turns.
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("p1", "first question", "")
	_ = tr.AppendEntry("p1", transcriptEntry{Kind: "assistant", Text: "first answer"})
	_ = tr.Finalize("p1", turntree.StatusComplete)
	_, _ = tr.AppendTurn("p2", "second question", "p1")
	_ = tr.AppendEntry("p2", transcriptEntry{Kind: "assistant", Text: "second answer"})
	_ = tr.Finalize("p2", turntree.StatusComplete)

	resumed := "chat-resume-fixture"
	originalCreated := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	cfg := &config.Config{
		Name:   "test",
		Agents: []config.AgentDefinition{{Name: "a1"}},
	}
	sess, err := StartChatSession(context.Background(), ChatSessionOptions{
		ConfigID:      "cfg-1",
		Cfg:           cfg,
		BuildFunc:     makeFakeChatBuildFunc("a1", nil, nil),
		ResumeID:      resumed,
		ResumeTree:    tr,
		ResumeCreated: originalCreated,
	})
	if err != nil {
		t.Fatalf("StartChatSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	if sess.ID != resumed {
		t.Errorf("resumed session.ID = %q, want %q", sess.ID, resumed)
	}
	if !sess.created.Equal(originalCreated) {
		t.Errorf("resumed session.created = %v, want %v", sess.created, originalCreated)
	}
	// historyFromTranscript walks the rehydrated tree's active path.
	hist := sess.historyFromTranscript()
	if len(hist) != 4 {
		t.Fatalf("resumed history len = %d, want 4 (2 user + 2 assistant): %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[0].AsText() != "first question" {
		t.Errorf("hist[0] = %+v", hist[0])
	}
	if hist[3].Role != "assistant" || hist[3].AsText() != "second answer" {
		t.Errorf("hist[3] = %+v", hist[3])
	}
}
