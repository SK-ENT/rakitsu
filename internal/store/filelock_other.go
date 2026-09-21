//go:build !unix

package store

// lockFile is a no-op on non-unix build targets: there's no flock
// equivalent wired up here, so cross-process serialization of sessions.json
// updates falls back to best-effort (the unique-tmp-filename fix in
// writeIndex still prevents torn-file corruption even without this lock;
// only the lost-update race is unmitigated on these platforms).
func lockFile(_ string, fn func() error) error {
	return fn()
}
