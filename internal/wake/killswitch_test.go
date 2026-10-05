package wake

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateKillSwitchTildeAndAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := CreateKillSwitch("~/x/KILL"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "x", "KILL")); err != nil {
		t.Fatalf("expanded file missing: %v", err)
	}
	abs := filepath.Join(t.TempDir(), "A")
	if err := CreateKillSwitch(abs); err != nil {
		t.Fatal(err)
	}
	if err := CreateKillSwitch(abs); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatal(err)
	}
}

func TestCreateKillSwitchRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no O_NOFOLLOW on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "STOP")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := CreateKillSwitch(link); err == nil {
		t.Fatal("expected symlink refusal")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("symlink target must not be created")
	}
}
