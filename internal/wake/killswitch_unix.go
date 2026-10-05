//go:build unix

package wake

import (
	"os"
	"syscall"
)

// openKill creates the file, refusing a symlink at the final path component.
func openKill(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}
