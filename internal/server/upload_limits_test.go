package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadZip_RejectsTooManyEntries(t *testing.T) {
	cs := &ConfigStore{tempDir: t.TempDir(), entries: map[string]string{}}
	files := map[string][]byte{"config.yaml": []byte("name: x\n")}
	for i := 0; i < 1001; i++ {
		files[fmt.Sprintf("agents/a%d.yaml", i)] = []byte("x")
	}
	if _, err := cs.UploadZip(zipOf(t, files)); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("want entry-count error, got %v", err)
	}
}

func TestUploadZip_RejectsTooLargeTotal(t *testing.T) {
	cs := &ConfigStore{tempDir: t.TempDir(), entries: map[string]string{}}
	files := map[string][]byte{"config.yaml": []byte("name: x\n")}
	chunk := bytes.Repeat([]byte("a"), 2<<20) // compresses to almost nothing
	for i := 0; i < 26; i++ {
		files[fmt.Sprintf("prompts/p%d.md", i)] = chunk
	}
	if _, err := cs.UploadZip(zipOf(t, files)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("want total-size error, got %v", err)
	}
	if entries, _ := os.ReadDir(cs.tempDir); len(entries) != 0 {
		t.Errorf("partial extraction left behind: %d entries", len(entries))
	}
}

func TestCheckPrivateDir_RefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "rakitsu-configs")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(link); err == nil {
		t.Fatal("symlinked temp dir was accepted")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	if err := checkPrivateDir(dir); err != nil {
		t.Fatalf("own dir refused: %v", err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %o, want 700", fi.Mode().Perm())
	}
}
