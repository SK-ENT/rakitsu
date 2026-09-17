package server

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// TestHistoryFromTranscript verifies the web chat reconstructs prior turns
// as an llm.Message list — user/assistant text only, empties and
// non-dialogue kinds (reasoning/tool/system) dropped. This is the
// multi-turn memory the web chat gained in Phase 0; the assertion is
// unchanged across the PR A refactor onto the turn-tree, which is the
// equivalence guarantee for linear (no-branch) sessions.
func TestHistoryFromTranscript(t *testing.T) {
	// Equivalent of the prior flat transcript:
	//   user "what is 2+2"      → turn 1's UserText
	//   reasoning, tool, asst "4"→ turn 1's Entries
	//   user "and times 3"      → turn 2's UserText
	//   asst ""                 → turn 2's Entries (interrupted/empty — dropped)
	//   user "still there?"     → turn 3's UserText (no assistant yet)
	tree := turntree.New[transcriptEntry]()
	if _, err := tree.AppendTurn("t1", "what is 2+2", ""); err != nil {
		t.Fatal(err)
	}
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "reasoning", Text: "the user wants arithmetic"})
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "tool", ToolName: "calc"})
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "4"})
	if _, err := tree.AppendTurn("t2", "and times 3", "t1"); err != nil {
		t.Fatal(err)
	}
	_ = tree.AppendEntry("t2", transcriptEntry{Kind: "assistant", Text: ""}) // empty — dropped
	if _, err := tree.AppendTurn("t3", "still there?", "t2"); err != nil {
		t.Fatal(err)
	}

	s := &ChatSession{tree: tree}
	h := s.historyFromTranscript()

	if len(h) != 4 {
		t.Fatalf("want 4 messages (3 user + 1 assistant), got %d: %+v", len(h), h)
	}
	want := []llm.Message{
		llm.NewTextMessage("user", "what is 2+2"),
		llm.NewTextMessage("assistant", "4"),
		llm.NewTextMessage("user", "and times 3"),
		llm.NewTextMessage("user", "still there?"),
	}
	for i, w := range want {
		if h[i].Role != w.Role || h[i].AsText() != w.AsText() {
			t.Errorf("h[%d] = %+v, want %+v", i, h[i], w)
		}
	}
}

// TestSnapshotTranscriptDerivesUserEntries verifies that the wire-facing
// flat transcript (used in the `attached` replay frame) is derived from
// the active path: each turn yields a synthetic "user" entry from
// Node.UserText, then that node's Entries in order. This is the contract
// existing WS clients depend on.
func TestSnapshotTranscriptDerivesUserEntries(t *testing.T) {
	tree := turntree.New[transcriptEntry]()
	_, _ = tree.AppendTurn("t1", "hi", "")
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "tool", ToolName: "echo"})
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "hello"})
	_, _ = tree.AppendTurn("t2", "again?", "t1")
	_ = tree.AppendEntry("t2", transcriptEntry{Kind: "assistant", Text: "yes"})

	s := &ChatSession{tree: tree}
	flat := s.snapshotTranscript()

	wantKinds := []string{"user", "tool", "assistant", "user", "assistant"}
	if len(flat) != len(wantKinds) {
		t.Fatalf("flat len = %d, want %d: %+v", len(flat), len(wantKinds), flat)
	}
	for i, k := range wantKinds {
		if flat[i].Kind != k {
			t.Errorf("flat[%d].Kind = %q, want %q", i, flat[i].Kind, k)
		}
	}
	if flat[0].Text != "hi" || flat[3].Text != "again?" {
		t.Errorf("user texts not threaded through: flat[0]=%q flat[3]=%q", flat[0].Text, flat[3].Text)
	}
	if flat[2].Text != "hello" || flat[4].Text != "yes" {
		t.Errorf("assistant texts not threaded through: flat[2]=%q flat[4]=%q", flat[2].Text, flat[4].Text)
	}
	if flat[0].Timestamp == "" || flat[3].Timestamp == "" {
		t.Errorf("synthetic user entries should carry Node.Created as timestamp")
	}
}

// TestSnapshotTranscriptOnlyActivePath verifies branch-tree behavior: a
// non-active sibling's entries do NOT appear in the snapshot. This is the
// invariant the inline UI relies on — switching siblings instantly
// re-renders the conversation without re-running anything.
func TestSnapshotTranscriptOnlyActivePath(t *testing.T) {
	tree := turntree.New[transcriptEntry]()
	_, _ = tree.AppendTurn("t1", "first", "")
	_ = tree.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "ok"})
	_, _ = tree.AppendTurn("t2a", "branch-a", "t1")
	_ = tree.AppendEntry("t2a", transcriptEntry{Kind: "assistant", Text: "answer-a"})
	// Edit creates sibling t2b under t1; active flips to t2b.
	_, _ = tree.AddSibling("t2b", "t2a", "branch-b")
	_ = tree.AppendEntry("t2b", transcriptEntry{Kind: "assistant", Text: "answer-b"})

	s := &ChatSession{tree: tree}
	flat := s.snapshotTranscript()
	for _, e := range flat {
		if e.Text == "answer-a" || e.Text == "branch-a" {
			t.Errorf("dead-branch entry leaked into active snapshot: %+v", e)
		}
	}
	// Switching back to t2a brings the original answer back, no regen.
	if _, err := tree.SwitchToSibling("t2b", -1); err != nil {
		t.Fatal(err)
	}
	flat = s.snapshotTranscript()
	found := false
	for _, e := range flat {
		if e.Text == "answer-a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("after switching back to t2a, answer-a not in snapshot: %+v", flat)
	}
}

// TestComposeHistoryQuery verifies the orchestrator-path text prefix: empty
// history passes the query through untouched; otherwise prior turns are
// rendered as a labelled transcript ahead of the current question.
func TestComposeHistoryQuery(t *testing.T) {
	if got := composeHistoryQuery(nil, "just this"); got != "just this" {
		t.Errorf("empty history should return the query unchanged, got %q", got)
	}

	h := []llm.Message{
		llm.NewTextMessage("user", "first question"),
		llm.NewTextMessage("assistant", "first answer"),
	}
	got := composeHistoryQuery(h, "follow-up")
	for _, want := range []string{
		"Previous conversation:",
		"User: first question",
		"Assistant: first answer",
		"Current question:",
		"follow-up",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("composed query missing %q:\n%s", want, got)
		}
	}
}

// TestComposeHistoryQueryTruncatesLongAnswers keeps the prefix bounded so a
// huge prior answer can't blow the context window.
func TestComposeHistoryQueryTruncatesLongAnswers(t *testing.T) {
	h := []llm.Message{
		llm.NewTextMessage("user", "q"),
		llm.NewTextMessage("assistant", strings.Repeat("x", 5000)),
	}
	got := composeHistoryQuery(h, "next")
	if !strings.Contains(got, "...[truncated]") {
		t.Error("a 5000-char assistant answer should be truncated in the prefix")
	}
	if len(got) > 3000 {
		t.Errorf("composed prefix should stay bounded, got %d chars", len(got))
	}
}
