//go:build !windows

package wake

import "os"

var syncDirectory = syncDirectoryImpl

func syncDirectoryImpl(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
