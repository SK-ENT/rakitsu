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

// unlock releases the lock taken by tryLock.
func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
