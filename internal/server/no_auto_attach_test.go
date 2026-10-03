package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Query text arriving over the hub/serve API must never trigger local file
// reads (auto-attach is CLI-only).
func TestServerDoesNotAutoAttachQueryFiles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "ScanQueryForAttachments") || strings.Contains(string(b), "autoAttachFromQuery") {
			t.Errorf("%s references query auto-attach; serve must require explicit attach", f)
		}
	}
}
