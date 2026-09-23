//go:build unix

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, blocking advisory lock on path (created if
// missing) for the duration of fn, then releases it. This serializes the
// read-modify-write cycle in appendToIndex/updateIndex across processes —
// SessionStore's own sync.Mutex only ever protects goroutines within one
// process, so two separate rakitsu processes racing to update sessions.json
// (the documented rakitsu serve + rakitsu run workflow) need this too.
func lockFile(path string, fn func() error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:errcheck

	return fn()
}
