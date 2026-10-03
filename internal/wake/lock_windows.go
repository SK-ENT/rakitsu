//go:build windows

package wake

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes a non-blocking exclusive lock on the first byte of f via
// LockFileEx. Like flock, it excludes other handles in this process too.
func tryLock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
}

// unlock releases the lock taken by tryLock.
func unlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
