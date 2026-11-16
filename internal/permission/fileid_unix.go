//go:build unix

package permission

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity returns "dev:ino" for a captured FileInfo, or "".
func fileIdentity(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}
