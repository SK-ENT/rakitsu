package main

import (
	"strings"
	"testing"
)

// The monitor commands and endpoints must be discoverable from --help.
func TestHelpMentionsMonitorSurface(t *testing.T) {
	if !strings.Contains(rootCmd.Long, "rakitsu healthcheck") {
		t.Error("root help does not mention healthcheck")
	}
	for _, want := range []string{"/healthz", "/wake/status"} {
		if !strings.Contains(serveCmd.Long, want) {
			t.Errorf("serve help does not mention %s", want)
		}
	}
}
