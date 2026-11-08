package wake

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteAtomicSyncsContainingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is not supported by the Windows filesystem API")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	called := false
	originalSyncDirectory := syncDirectory
	syncDirectory = func(path string) error {
		called = true
		return originalSyncDirectory(path)
	}
	t.Cleanup(func() { syncDirectory = originalSyncDirectory })
	if err := writeAtomic(path, []byte(`{"version":1}`)); err != nil {
		t.Fatalf("writeAtomic() error = %v", err)
	}
	if !called {
		t.Fatal("writeAtomic() did not sync its containing directory")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(got) != `{"version":1}` {
		t.Fatalf("persisted data = %q", got)
	}
}

func TestNewSyncsNewDirectoryEntries(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "new-parent")
	dir := filepath.Join(parent, "wake")
	originalSync := syncDirectory
	var synced []string
	syncDirectory = func(path string) error {
		synced = append(synced, path)
		return originalSync(path)
	}
	t.Cleanup(func() { syncDirectory = originalSync })

	e, err := New(Deps{Cfg: baseCfg(dir), Dir: dir, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if len(synced) < 2 || synced[len(synced)-2] != root || synced[len(synced)-1] != parent {
		t.Fatalf("new directory entries were not synced in creation order: %v", synced)
	}
}

func TestWriteAtomicReportsDirectorySyncFailureAfterRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	syncErr := errors.New("injected directory sync failure")
	originalSync := syncDirectory
	syncDirectory = func(gotDir string) error {
		if gotDir != dir {
			t.Fatalf("sync directory = %q, want %q", gotDir, dir)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "new state" {
			t.Fatalf("target at directory sync = %q, %v", got, err)
		}
		return syncErr
	}
	t.Cleanup(func() { syncDirectory = originalSync })

	if err := writeAtomic(path, []byte("new state")); !errors.Is(err, syncErr) {
		t.Fatalf("writeAtomic error = %v, want %v", err, syncErr)
	}
}
