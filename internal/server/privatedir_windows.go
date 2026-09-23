//go:build windows

package server

import (
	"fmt"
	"os"
)

// checkPrivateDir fails unless dir is a real directory, not a symlink.
// Windows temp dirs are per-user, so no ownership check is done.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a plain directory; refusing to use it", dir)
	}
	return nil
}
