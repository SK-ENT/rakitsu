//go:build unix

package wake

import (
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive advisory lock on f.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// verifyLockPath checks that path still refers to the file f holds locked. If the
// lock file was deleted and recreated, a second process could lock the new file.
func verifyLockPath(path string, f *os.File) error {
	pi, err := os.Stat(path)
	if err != nil {
		return ErrLockReplaced
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pi, fi) {
		return ErrLockReplaced
	}
	return nil
}

// unlock releases the lock taken by tryLock.
func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
