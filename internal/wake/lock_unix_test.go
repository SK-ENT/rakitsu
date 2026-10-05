//go:build unix

package wake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockRefusesReplacedLockFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tryLock(f); err != nil {
		t.Fatal(err)
	}
	if err := verifyLockPath(p, f); err != nil {
		t.Fatalf("same inode must pass: %v", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	g, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if err := verifyLockPath(p, f); !errors.Is(err, ErrLockReplaced) {
		t.Fatalf("replaced file must be refused, got %v", err)
	}
	// missing path is also a replacement
	_ = os.Remove(p)
	if err := verifyLockPath(p, f); !errors.Is(err, ErrLockReplaced) {
		t.Fatalf("missing file must be refused, got %v", err)
	}
}

func TestNewRefusesWhenLockFileReplacedBeforeCheck(t *testing.T) {
	dir := t.TempDir()
	lp := filepath.Join(dir, "s1.lock")
	testHookAfterLock = func() { _ = os.Remove(lp); _ = os.WriteFile(lp, nil, 0o600) }
	defer func() { testHookAfterLock = nil }()
	_, err := New(Deps{Cfg: baseCfg(dir), SessionID: "s1", Dir: dir, Checks: (&fakeProbe{}).fn})
	if !errors.Is(err, ErrLockReplaced) {
		t.Fatalf("want ErrLockReplaced, got %v", err)
	}
}

func TestTickStopsWhenLockReplacedMidRun(t *testing.T) {
	for _, mode := range []string{"replaced", "removed"} {
		t.Run(mode, func(t *testing.T) {
			e, clk, dir := newEngine(t, nil, &fakeProbe{})
			n := 0
			if r := e.Tick(context.Background(), accept(&n)); r.Killed || r.LockLost {
				t.Fatalf("intact lock must keep ticking: %+v", r)
			}
			lp := filepath.Join(dir, "s1.lock")
			_ = os.Remove(lp)
			if mode == "replaced" {
				_ = os.WriteFile(lp, nil, 0o600)
			}
			clk.Advance(time.Minute)
			r := e.Tick(context.Background(), accept(&n))
			if !r.Killed || !r.LockLost || !e.Stopped() || n != 0 {
				t.Fatalf("lock loss must stop the loop: %+v stopped=%v", r, e.Stopped())
			}
			found := false
			for _, rec := range readAudit(t, dir) {
				if rec["event"] == "lock_replaced" {
					found = true
				}
			}
			if !found {
				t.Fatal("no lock_replaced audit line")
			}
			if err := e.Resume(); !errors.Is(err, ErrLockReplaced) || !e.Stopped() {
				t.Fatalf("resume must be refused: %v", err)
			}
		})
	}
}
