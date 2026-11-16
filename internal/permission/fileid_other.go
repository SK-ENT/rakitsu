//go:build !unix

package permission

import "os"

// fileIdentity has no portable dev:ino here; size and mtime still bind the
// grant key, and adapters compare identities with os.SameFile.
func fileIdentity(os.FileInfo) string { return "" }
