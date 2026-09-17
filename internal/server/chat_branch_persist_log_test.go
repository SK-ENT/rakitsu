package server

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// TestBroadcastTreeMutationLocked_LogsPersistFailure regression-guards:
// broadcastTreeMutationLocked formatted the persist-failure message with
// fmt.Fprintf(io.Discard, ...) — a real io.Writer that throws the message
// away exactly like not writing it at all. A SaveChatTree failure (disk
// full, permission error, corrupted state) left the on-disk tree silently
// stale with no log line, metric, or any other trace explaining why a later
// resume/reload of the session shows outdated history.
func TestBroadcastTreeMutationLocked_LogsPersistFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ss, err := store.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	// Force every SaveChatTree call to fail: replace the sessions directory
	// with a regular file, so os.WriteFile's parent-dir lookup fails with
	// ENOTDIR regardless of whether the test runs as root (a chmod-based
	// permission trigger wouldn't be reliable under root).
	dir := filepath.Join(os.Getenv("HOME"), ".rakitsu", "sessions")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove sessions dir: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("replace sessions dir with a file: %v", err)
	}

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	tr := turntree.New[transcriptEntry]()
	if _, err := tr.AppendTurn("t1", "hi", ""); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	sess := &ChatSession{ID: "persist-fail-test", tree: tr, sessionStore: ss}
	sess.mu.Lock()
	sess.broadcastTreeMutationLocked()
	sess.mu.Unlock()

	got := buf.String()
	if !strings.Contains(got, "persist-fail-test") || !strings.Contains(got, "persist tree") {
		t.Errorf("expected the SaveChatTree failure to be logged (session id + \"persist tree\"), got log output: %q", got)
	}
}
