package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// seedTree builds a small tree fixture: t1 (root) -> t2 (one child),
// each with one assistant entry, t2 active. The returned tree is hydrated
// and ready to attach to a ChatSession{tree: ...} stub for unit tests
// that don't need a live runner.
func seedTree(t *testing.T) *turntree.Tree[transcriptEntry] {
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
	return tr
}

// TestHandleGetTurnIncludesErrorText — regression: after a turn fails,
// GET /api/chat/{id}/turn/{turn_id} must expose the error text, not just a
// bare interrupted:true.
func TestHandleGetTurnIncludesErrorText(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "", errors.New("boom")
		},
		nil,
	))
	col := newCollector(sess)
	if err := sess.Submit("hi"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !col.waitFor(func(msgs []serverMsg) bool {
		for _, m := range msgs {
			if m.Type == "turn_done" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		t.Fatalf("turn never completed, got %+v", col.snapshot())
	}

	snap := sess.SnapshotTree()
	if len(snap.ActivePath) == 0 {
		t.Fatal("empty active path")
	}
	turnID := snap.ActivePath[len(snap.ActivePath)-1]

	s := newMsgServer(t, sess)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("%s/api/chat/%s/turn/%s", srv.URL, sess.ID, turnID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var node struct {
		Entries []transcriptEntry `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range node.Entries {
		if e.Kind == "assistant" {
			found = true
			if e.Err != "boom" {
				t.Errorf("assistant entry error = %q, want %q", e.Err, "boom")
			}
		}
	}
	if !found {
		t.Fatal("no assistant entry in turn node")
	}
}

func TestEditTurnCreatesSiblingAndActivates(t *testing.T) {
	sess := &ChatSession{tree: seedTree(t)}

	if err := sess.EditTurn("t2", "edited second question"); err != nil {
		t.Fatalf("EditTurn: %v", err)
	}

	// One sibling added under t1; it's the active leaf.
	t1, _ := sess.tree.Get("t1")
	if len(t1.Children) != 2 {
		t.Fatalf("t1.Children = %d, want 2 (t2 + new sibling)", len(t1.Children))
	}
	leaf := sess.tree.ActiveLeaf()
	if leaf == nil || leaf.UserText != "edited second question" {
		t.Fatalf("active leaf = %+v, want UserText='edited second question'", leaf)
	}
	if leaf.ID == "t2" {
		t.Fatal("new sibling has the same id as t2")
	}
	// RetryCtx must be nil for user-driven edits — load-bearing distinction.
	if leaf.RetryCtx != nil {
		t.Errorf("user-driven edit_turn populated RetryCtx: %+v", leaf.RetryCtx)
	}
}

func TestEditTurnRejectsEmptyText(t *testing.T) {
	sess := &ChatSession{tree: seedTree(t)}
	if err := sess.EditTurn("t2", "   "); err == nil {
		t.Fatal("EditTurn with whitespace-only text should error")
	}
	if err := sess.EditTurn("not-there", "anything"); err == nil {
		t.Fatal("EditTurn with unknown turn id should error")
	}
}

func TestRegenerateCreatesSameTextSibling(t *testing.T) {
	sess := &ChatSession{tree: seedTree(t)}

	if err := sess.RegenerateTurn("t2"); err != nil {
		t.Fatalf("RegenerateTurn: %v", err)
	}
	leaf := sess.tree.ActiveLeaf()
	if leaf == nil || leaf.UserText != "second question" {
		t.Fatalf("regenerated leaf UserText = %+v, want 'second question'", leaf)
	}
	if leaf.RetryCtx != nil {
		t.Errorf("user-driven regenerate populated RetryCtx: %+v", leaf.RetryCtx)
	}
}

func TestSwitchBranchCyclesDirections(t *testing.T) {
	// Build a 3-sibling fan-out: t1 -> {t2a, t2b, t2c}, t2c initially active.
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "root", "")
	_, _ = tr.AppendTurn("t2a", "a", "t1")
	_, _ = tr.AddSibling("t2b", "t2a", "b")
	_, _ = tr.AddSibling("t2c", "t2b", "c")
	sess := &ChatSession{tree: tr}

	if leaf := sess.tree.ActiveLeaf(); leaf.ID != "t2c" {
		t.Fatalf("setup: active leaf = %q, want t2c", leaf.ID)
	}

	// +1 from t2c wraps to t2a.
	if err := sess.SwitchBranch("t2c", +1); err != nil {
		t.Fatal(err)
	}
	if leaf := sess.tree.ActiveLeaf(); leaf.ID != "t2a" {
		t.Errorf("after +1 from t2c: leaf = %q, want t2a (wrap)", leaf.ID)
	}

	// -1 from t2a wraps to t2c.
	if err := sess.SwitchBranch("t2a", -1); err != nil {
		t.Fatal(err)
	}
	if leaf := sess.tree.ActiveLeaf(); leaf.ID != "t2c" {
		t.Errorf("after -1 from t2a: leaf = %q, want t2c (wrap)", leaf.ID)
	}
}

func TestSwitchBranchSingleSiblingIsNoop(t *testing.T) {
	sess := &ChatSession{tree: seedTree(t)} // t1 has only t2

	if err := sess.SwitchBranch("t2", +1); err != nil {
		t.Fatalf("SwitchBranch should be no-op on one-of-one, got: %v", err)
	}
	if leaf := sess.tree.ActiveLeaf(); leaf.ID != "t2" {
		t.Errorf("one-of-one switch moved leaf: now %q, want t2", leaf.ID)
	}
}

func TestSetActivePathWalksAncestors(t *testing.T) {
	// Build a two-level branch: t1 -> {t2a, t2b}; t2a -> t3a; t2b -> t3b.
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "root", "")
	_, _ = tr.AppendTurn("t2a", "a", "t1")
	_, _ = tr.AddSibling("t2b", "t2a", "b")
	_, _ = tr.AppendTurn("t3a", "a.deep", "t2a")
	_, _ = tr.AppendTurn("t3b", "b.deep", "t2b")
	sess := &ChatSession{tree: tr}

	// Initial active (constructed by-AppendTurn order) ends at t3b — the
	// last AppendTurn flipped ActiveChild at every visited level.
	if leaf := sess.tree.ActiveLeaf(); leaf.ID != "t3b" {
		t.Fatalf("setup: active leaf = %q, want t3b", leaf.ID)
	}

	// Make t3a active by setting the active path to it.
	if err := sess.SetActivePath("t3a"); err != nil {
		t.Fatal(err)
	}
	path := sess.tree.ActivePath()
	got := make([]string, 0, len(path))
	for _, n := range path {
		got = append(got, n.ID)
	}
	want := []string{"t1", "t2a", "t3a"}
	if !equalSlices(got, want) {
		t.Fatalf("ActivePath = %v, want %v", got, want)
	}
}

func TestForkActivePathIsIndependent(t *testing.T) {
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "first", "")
	_ = tr.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "ans1"})
	_, _ = tr.AppendTurn("t2", "second", "t1")
	_ = tr.AppendEntry("t2", transcriptEntry{Kind: "assistant", Text: "ans2"})
	// Add a dead sibling so we can verify path mode excludes it.
	_, _ = tr.AddSibling("t2-dead", "t2", "wrong question")
	_, _ = tr.SwitchToSibling("t2-dead", -1) // back to t2 active

	sess := &ChatSession{tree: tr}

	sess.mu.Lock()
	cloned, err := sess.copyTreeForkLocked(ForkActivePath, "")
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("fork (path): %v", err)
	}

	// Active path mode: only nodes on path → t1, t2. t2-dead must be gone.
	if _, ok := cloned.Get("t2-dead"); ok {
		t.Error("fork-path included dead sibling t2-dead")
	}
	if _, ok := cloned.Get("t1"); !ok {
		t.Error("fork-path missing t1")
	}
	if _, ok := cloned.Get("t2"); !ok {
		t.Error("fork-path missing t2")
	}

	// Independence: mutate original, fork is unaffected.
	_ = tr.AppendEntry("t2", transcriptEntry{Kind: "system", Text: "POST-FORK"})
	clonedT2, _ := cloned.Get("t2")
	for _, e := range clonedT2.Entries {
		if e.Text == "POST-FORK" {
			t.Error("fork-path leaked a mutation from the source after copy")
		}
	}

	// Independence the other way: mutate the fork, original is unaffected.
	_, _ = cloned.AppendTurn("t-fork-only", "added in fork", "t2")
	if _, ok := tr.Get("t-fork-only"); ok {
		t.Error("source tree saw a fork-only node after copy")
	}
}

func TestForkSubtreePreservesAllBranches(t *testing.T) {
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "first", "")
	_, _ = tr.AppendTurn("t2a", "second-a", "t1")
	_, _ = tr.AddSibling("t2b", "t2a", "second-b") // sibling
	_, _ = tr.SwitchToSibling("t2b", -1)            // back to t2a active

	sess := &ChatSession{tree: tr}

	sess.mu.Lock()
	cloned, err := sess.copyTreeForkLocked(ForkSubtree, "")
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("fork (subtree): %v", err)
	}

	// Subtree mode preserves both siblings.
	if _, ok := cloned.Get("t2a"); !ok {
		t.Error("fork-subtree missing t2a")
	}
	if _, ok := cloned.Get("t2b"); !ok {
		t.Error("fork-subtree missing t2b (sibling dropped)")
	}

	// Active state cloned too.
	if leaf := cloned.ActiveLeaf(); leaf == nil || leaf.ID != "t2a" {
		t.Errorf("cloned active leaf = %+v, want t2a", leaf)
	}
}

func TestForkRejectsUnknownMode(t *testing.T) {
	sess := &ChatSession{tree: seedTree(t)}
	sess.mu.Lock()
	_, err := sess.copyTreeForkLocked(ForkMode("garbage"), "")
	sess.mu.Unlock()
	if err == nil {
		t.Fatal("unknown fork mode should error")
	}
}

func TestForkPathRejectsOffPathTruncate(t *testing.T) {
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "first", "")
	_, _ = tr.AppendTurn("t2a", "a", "t1")
	_, _ = tr.AddSibling("t2b", "t2a", "b") // t2b now active
	sess := &ChatSession{tree: tr}

	// t2a is not on the active path (t2b is); truncating at t2a in path
	// mode must reject so the user can't accidentally fork a dead branch
	// via a stale id.
	sess.mu.Lock()
	_, err := sess.copyTreeForkLocked(ForkActivePath, "t2a")
	sess.mu.Unlock()
	if err == nil {
		t.Fatal("fork-path with off-active-path truncate should error")
	}
	if !errors.Is(err, turntree.ErrUnknownNode) {
		t.Errorf("want wraps ErrUnknownNode, got %v", err)
	}
}

func TestSnapshotTreeLockedShape(t *testing.T) {
	tr := seedTree(t)
	// Add a third level + a retry-ctx-annotated sibling so the snapshot
	// has something interesting to assert against.
	_, _ = tr.AppendTurn("t3", "third", "t2")
	_ = tr.AppendEntry("t3", transcriptEntry{Kind: "assistant", Text: "third answer"})
	_ = tr.AppendEntry("t3", transcriptEntry{Kind: "tool", ToolName: "noop"})
	_ = tr.Finalize("t3", turntree.StatusComplete)
	_, _ = tr.AddSibling("t3-retry", "t3", "third (retry)")
	tr.Nodes["t3-retry"].RetryCtx = &turntree.RetryContext{
		Source: "rollback", PriorTurnID: "t3", Reason: "test",
	}

	sess := &ChatSession{tree: tr}
	sess.mu.Lock()
	snap := sess.snapshotTreeLocked()
	sess.mu.Unlock()

	// Top-level shape.
	if len(snap.Nodes) != len(tr.Nodes) {
		t.Fatalf("snap.Nodes = %d, tree.Nodes = %d", len(snap.Nodes), len(tr.Nodes))
	}
	if !equalSlices(snap.Roots, tr.Roots) {
		t.Errorf("snap.Roots = %v, tree.Roots = %v", snap.Roots, tr.Roots)
	}

	// Find t3 in snap and verify EntryCount is exposed (not entry bodies).
	for _, n := range snap.Nodes {
		if n.ID == "t3" && n.EntryCount != 2 {
			t.Errorf("snap.t3.EntryCount = %d, want 2", n.EntryCount)
		}
		if n.ID == "t3-retry" {
			if n.RetryCtx == nil || n.RetryCtx.Source != "rollback" {
				t.Errorf("snap.t3-retry.RetryCtx = %+v, want {Source:rollback}", n.RetryCtx)
			}
		}
	}

	// ActivePath is precomputed root→leaf ids — fast UI path.
	if len(snap.ActivePath) == 0 {
		t.Error("snap.ActivePath empty; UI cannot highlight")
	}
}

func TestRequestTreeBroadcastsSnapshot(t *testing.T) {
	tr := seedTree(t)
	sess := &ChatSession{
		tree:    tr,
		clients: make(map[*chatClient]struct{}),
	}
	c := &chatClient{
		session: sess,
		sendCh:  make(chan serverMsg, 4),
		closed:  make(chan struct{}),
	}
	sess.attach(c)

	sess.RequestTree()

	select {
	case msg := <-c.sendCh:
		if msg.Type != "tree_snapshot" {
			t.Fatalf("got msg type %q, want tree_snapshot", msg.Type)
		}
		if msg.Tree == nil || len(msg.Tree.Nodes) == 0 {
			t.Fatal("tree_snapshot had no tree payload")
		}
	case <-time.After(time.Second):
		t.Fatal("no tree_snapshot received within 1s")
	}
}

// TestSnapshotTranscriptDecoratesBranchMetadata verifies PR D's wire
// addition: every transcript entry derived from the active path carries
// turn_id, and user entries additionally carry branch_index/branch_count
// so the inline UX can render the `‹n/m›` chip without a separate tree
// query. Persisted Node.Entries leave the branch fields blank (they're
// pure wire decoration, not state on disk).
func TestSnapshotTranscriptDecoratesBranchMetadata(t *testing.T) {
	// Tree: t1 (root) -> {t2a, t2b}; t2a active. So user "first" has
	// branch_count=1 (sole root) and user "second-a" has branch_count=2,
	// branch_index=0 (t2a is the first child of t1).
	tr := turntree.New[transcriptEntry]()
	_, _ = tr.AppendTurn("t1", "first", "")
	_ = tr.AppendEntry("t1", transcriptEntry{Kind: "assistant", Text: "first-ans"})
	_ = tr.Finalize("t1", turntree.StatusComplete)
	_, _ = tr.AppendTurn("t2a", "second-a", "t1")
	_ = tr.AppendEntry("t2a", transcriptEntry{Kind: "assistant", Text: "ans-a"})
	_ = tr.Finalize("t2a", turntree.StatusComplete)
	_, _ = tr.AddSibling("t2b", "t2a", "second-b") // creates sibling AND activates
	_ = tr.Finalize("t2b", turntree.StatusComplete)
	// Re-activate t2a so the active path is [t1, t2a].
	_, _ = tr.SwitchToSibling("t2b", -1)

	sess := &ChatSession{tree: tr}
	flat := sess.snapshotTranscript()

	// Find the two user entries and assert their branch metadata.
	users := []transcriptEntry{}
	for _, e := range flat {
		if e.Kind == "user" {
			users = append(users, e)
		}
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 user entries on active path, got %d: %+v", len(users), users)
	}

	// User "first" — only child at root level.
	if users[0].Text != "first" || users[0].TurnID != "t1" ||
		users[0].BranchIndex != 0 || users[0].BranchCount != 1 {
		t.Errorf("users[0] = %+v, want {Text:first, TurnID:t1, BranchIndex:0, BranchCount:1}", users[0])
	}

	// User "second-a" — first of two siblings under t1.
	if users[1].Text != "second-a" || users[1].TurnID != "t2a" ||
		users[1].BranchIndex != 0 || users[1].BranchCount != 2 {
		t.Errorf("users[1] = %+v, want {Text:second-a, TurnID:t2a, BranchIndex:0, BranchCount:2}", users[1])
	}

	// Non-user entries get turn_id but no branch_index/branch_count.
	for _, e := range flat {
		if e.Kind != "user" {
			if e.TurnID == "" {
				t.Errorf("non-user entry missing turn_id: %+v", e)
			}
			if e.BranchIndex != 0 || e.BranchCount != 0 {
				t.Errorf("non-user entry got branch fields it shouldn't: %+v", e)
			}
		}
	}
}

// TestSnapshotTranscriptRespectsCap pins down that snapshotTranscript's
// underlying decorateActivePathLocked already caps the flat transcript to
// the last transcriptCap entries (a review round flagged this as
// undocumented/missing — it's neither; the cap lives here, one call away
// from snapshotTranscript, and this test exists so a future refactor can't
// silently drop it without a test going red).
func TestSnapshotTranscriptRespectsCap(t *testing.T) {
	tr := turntree.New[transcriptEntry]()
	prev := ""
	turns := transcriptCap/2 + 10 // 2 entries/turn (user + assistant) comfortably exceeds the cap
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("t%d", i)
		if _, err := tr.AppendTurn(id, fmt.Sprintf("q%d", i), prev); err != nil {
			t.Fatalf("AppendTurn(%s): %v", id, err)
		}
		_ = tr.AppendEntry(id, transcriptEntry{Kind: "assistant", Text: fmt.Sprintf("a%d", i)})
		_ = tr.Finalize(id, turntree.StatusComplete)
		prev = id
	}

	sess := &ChatSession{tree: tr}
	flat := sess.snapshotTranscript()

	if len(flat) != transcriptCap {
		t.Fatalf("snapshotTranscript() returned %d entries with %d turns on the active path, want exactly %d (the cap)", len(flat), turns, transcriptCap)
	}
	// It's the TAIL that survives, not an arbitrary cap: the last turn's
	// entries must be present.
	last := fmt.Sprintf("t%d", turns-1)
	found := false
	for _, e := range flat {
		if e.TurnID == last {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("capped transcript is missing the most recent turn (%s) — cap should keep the tail, not the head", last)
	}
}

// equalSlices is a small helper to keep the assertions readable without
// pulling in reflect.DeepEqual for two []string values.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
