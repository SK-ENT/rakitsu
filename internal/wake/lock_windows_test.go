//go:build windows

package wake

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsTryLockExcludes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := tryLock(a); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := tryLock(b); err == nil {
		t.Fatal("second lock must fail while first is held")
	}
	unlock(a)
	if err := tryLock(b); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock(b)
}
