//go:build !windows

package server

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir fails unless dir is a real directory (not a symlink)
// owned by the current user, and then restricts it to 0700.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a plain directory; refusing to use it", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user; refusing to use it", dir)
	}
	return os.Chmod(dir, 0o700)
}
