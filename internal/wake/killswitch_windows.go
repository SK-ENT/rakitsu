//go:build windows

package wake

import "os"

func openKill(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
}
