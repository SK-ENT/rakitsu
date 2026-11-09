//go:build !unix && !windows

package main

import "sync"

var acpProfileMu sync.Mutex

// Platforms without a supported file lock can only serialize within a process.
func lockACPProfileFile(string) (func(), error) {
	acpProfileMu.Lock()
	return acpProfileMu.Unlock, nil
}
