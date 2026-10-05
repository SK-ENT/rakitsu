package wake

import (
	"os"
	"path/filepath"
)

// ExpandPath expands a leading "~" exactly as the engine does when it reads
// the kill-switch file.
func ExpandPath(p string) string { return expandPath(p) }

// CreateKillSwitch creates the kill-switch file at the engine's expanded path
// (parent dir 0700, file 0600, symlink at the final component refused where
// supported). Creating an existing file is not an error.
func CreateKillSwitch(configured string) error {
	path := expandPath(configured)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := openKill(path)
	if err != nil {
		return err
	}
	return f.Close()
}
