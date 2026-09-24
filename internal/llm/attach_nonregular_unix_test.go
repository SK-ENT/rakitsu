//go:build unix

package llm

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// a named pipe or device reports size 0, so the old size check passed
// and os.ReadFile then blocked forever (FIFO) or read without end
// (/dev/zero). Only regular files may be loaded.
func TestLoadImageAttachment_RejectsNonRegularFiles(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "shot.png")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	for _, path := range []string{fifo, "/dev/zero"} {
		done := make(chan error, 1)
		go func() {
			_, err := LoadImageAttachment(path)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("%s: err = %v, want a not-a-regular-file error", path, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: LoadImageAttachment hung instead of rejecting a non-regular file", path)
		}
	}
}
