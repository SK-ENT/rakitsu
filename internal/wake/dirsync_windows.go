//go:build windows

package wake

var syncDirectory = func(string) error { return nil }
