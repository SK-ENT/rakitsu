//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockACPProfileFile takes an exclusive lock on path via LockFileEx (created if
// missing) and returns the function that releases it. Blocks until the lock
// is free, so two rakitsu processes sharing one profile serialize setup
// on Windows the same way flock does on unix.
func lockACPProfileFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, nil
}
