package server

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/store"
)

// TestPersistTreeRaw_SkipsStaleGeneration regression-guards: persistTree
// (turn-finalize path) and broadcastTreeMutationLocked (branch-op path)
// each independently snapshot the tree under s.mu, then write to disk after
// releasing it — nothing serialized the two writes against each other. A
// branch op racing a turn finalize could have the OLDER snapshot's write
// land on disk after the NEWER one's, silently reverting state below what
// was actually last true in memory. Tested deterministically against the
// low-level write path directly (not via racing goroutines, which can't
// reliably reproduce which write "wins" — see the DetachDebugger fix in
// round 4 for the same lesson): call the newer generation's write first,
// then the (now-stale) older generation's write, and confirm the older one
// is dropped rather than overwriting what's already on disk.
func TestPersistTreeRaw_SkipsStaleGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	sess := &ChatSession{ID: "gen-test", sessionStore: ss}

	// The newer snapshot's write lands first...
	sess.persistTreeRaw([]byte("NEW-CONTENT"), 2)
	// ...then a stale, older snapshot's write arrives after — as would
	// happen if its disk I/O simply took longer to complete.
	sess.persistTreeRaw([]byte("STALE-CONTENT"), 1)

	raw, err := ss.LoadChatTree("gen-test")
	if err != nil {
		t.Fatalf("LoadChatTree: %v", err)
	}
	if strings.Contains(string(raw), "STALE-CONTENT") {
		t.Fatalf("a stale (older-generation) write overwrote the newer one already on disk: %s", raw)
	}
	if !strings.Contains(string(raw), "NEW-CONTENT") {
		t.Fatalf("expected the newer generation's content on disk, got: %s", raw)
	}
}
