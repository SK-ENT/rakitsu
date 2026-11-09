//go:build unix

package main

import (
	"os"
	"syscall"
)

// lockACPProfileFile takes an exclusive advisory lock on path (created if missing,
// mode 0600) and returns the function that releases it. Blocks until the
// lock is free. Advisory locks are per open file description, so two
// callers in one process exclude each other as well as two processes.
func lockACPProfileFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
