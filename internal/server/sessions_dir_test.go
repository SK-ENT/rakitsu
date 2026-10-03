package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/store"
)

// With a store at a custom dir, a chat session started through the manager
// must write only there, never under HOME's default .rakitsu/sessions.
func TestChatManager_CustomSessionsDir_DefaultDirUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(t.TempDir(), "inst-a", "sessions")
	ss, err := store.NewSessionStoreAt(custom)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := NewConfigStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := cs.Inline(forkTestConfigYAML)
	if err != nil {
		t.Fatal(err)
	}
	m := NewChatManager(nil, cs, ss, slowFakeChatBuildFunc("a1", 0))

	sess, err := m.Start(context.Background(), entry.ID, "", nil, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	sess.Close()

	files, _ := filepath.Glob(filepath.Join(custom, "*.jsonl"))
	if len(files) == 0 {
		ents, _ := os.ReadDir(custom)
		t.Fatalf("no session file in custom dir; contents: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(home, ".rakitsu", "sessions")); !os.IsNotExist(err) {
		t.Fatalf("default sessions dir must not exist, stat err = %v", err)
	}
}
