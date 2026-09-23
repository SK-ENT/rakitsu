//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Session files hold config snapshots and tool output, so they must be
// private to the owner — not readable by other local users.
func TestSessionStore_PrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	// An existing, too-open directory from an older version gets tightened.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := newSessionStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("sessions dir mode = %o, want 700", fi.Mode().Perm())
	}

	id := startTestSession(t, s)
	s.EndSession(SessionSuccess)
	if err := s.SaveChatTree(id, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no files written")
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group/other access", e.Name(), fi.Mode().Perm())
		}
	}
}
