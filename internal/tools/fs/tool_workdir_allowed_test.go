package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// Regression for the --workdir / allowed_paths mismatch: relative
// allowed_paths entries must resolve against the tool's working_dir (what
// `rakitsu run --workdir` sets), not the process cwd. Before the fix, a
// config with allowed_paths: ["."] denied every file under the workdir
// whenever rakitsu was launched from elsewhere.
func TestIsPathAllowed_RelativeAllowedResolvesAgainstWorkingDir(t *testing.T) {
	workdir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "RUN.md"), []byte("kind: web\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Launch "from" a different directory than the workdir.
	orig, _ := os.Getwd()
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	tool := NewTool(&config.ToolDefinition{
		Name:         "fs_read",
		Operation:    "read",
		AllowedPaths: []string{"."},
		WorkingDir:   workdir,
	})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": "RUN.md"})
	if err != nil {
		t.Fatalf("read of a file inside the workdir must be allowed, got: %v", err)
	}
	if out != "kind: web\n" {
		t.Fatalf("unexpected content %q", out)
	}

	// And a file outside the workdir (in the launch cwd) must still be denied.
	if err := os.WriteFile(filepath.Join(elsewhere, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"path": filepath.Join(elsewhere, "secret.txt")}); err == nil {
		t.Fatal("file outside the workdir must be denied")
	}
}

// Without working_dir the old behaviour is preserved: "." is the process cwd.
func TestIsPathAllowed_RelativeAllowedWithoutWorkingDirUsesCwd(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	tool := NewTool(&config.ToolDefinition{Name: "fs_read", Operation: "read", AllowedPaths: []string{"."}})
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"path": "a.txt"}); err != nil {
		t.Fatalf("read inside cwd must be allowed when no working_dir is set, got: %v", err)
	}
}
