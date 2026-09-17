package fs

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// ============================================================
// Helpers
// ============================================================

func newFSTool(op string, allowedPaths []string) *Tool {
	return NewTool(&config.ToolDefinition{
		Name:         "test-fs",
		Description:  "test",
		Operation:    op,
		AllowedPaths: allowedPaths,
	})
}

// writeTemp creates a temp file with content and returns its path.
func writeTemp(t *testing.T, dir, content string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "fstool-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, err = f.WriteString(content)
	f.Close()
	if err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	return f.Name()
}

// ============================================================
// isPathAllowed — traversal guard
// ============================================================

func TestIsPathAllowed_ExactDir(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("read", []string{dir})
	if !tool.isPathAllowed(dir) {
		t.Errorf("exact dir %q should be allowed", dir)
	}
}

func TestIsPathAllowed_FileUnderDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "file.txt")
	tool := newFSTool("read", []string{dir})
	if !tool.isPathAllowed(file) {
		t.Errorf("file under allowed dir should be allowed")
	}
}

func TestIsPathAllowed_SiblingDir_Denied(t *testing.T) {
	parent := t.TempDir()
	allowed := filepath.Join(parent, "allowed")
	sibling := filepath.Join(parent, "secret")
	os.MkdirAll(allowed, 0755)
	os.MkdirAll(sibling, 0755)

	tool := newFSTool("read", []string{allowed})
	if tool.isPathAllowed(sibling) {
		t.Error("sibling directory should not be allowed")
	}
}

func TestIsPathAllowed_TraversalAttack_Denied(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("read", []string{dir})

	// Attempt to escape via ../
	traversal := filepath.Join(dir, "..", "secret")
	if tool.isPathAllowed(traversal) {
		t.Error("path traversal via ../ should be denied")
	}
}

func TestIsPathAllowed_DefaultDot_AllowsCWD(t *testing.T) {
	tool := newFSTool("read", nil) // defaults to "."
	cwd, _ := os.Getwd()
	if !tool.isPathAllowed(cwd) {
		t.Error("current working directory should be allowed under default '.' policy")
	}
}

func TestIsPathAllowed_RootAllowedPath_AllowsSubpath(t *testing.T) {
	// Regression: realAllowed=="/" plus an unconditional separator append
	// produced "//", which no real path has a prefix of — every subpath
	// was incorrectly denied under an allowed_paths: ["/"] config.
	tool := newFSTool("read", []string{"/"})
	dir := t.TempDir() // some real absolute path guaranteed to exist
	if !tool.isPathAllowed(dir) {
		t.Errorf("path %q should be allowed under allowed_paths [\"/\"]", dir)
	}
}

// ============================================================
// Read operation
// ============================================================

func TestRead_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "hello world")
	tool := newFSTool("read", []string{dir})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected 'hello world' in output, got %q", out)
	}
}

// TestRead_LargeFile_TruncatedAtCap: readFile used to ioutil.ReadFile the
// whole file into memory regardless
// of size, so a huge or special file was a memory + context-blowout DoS.
// A file bigger than the configured max_output_bytes cap must be
// truncated, not fully buffered and returned whole.
func TestRead_LargeFile_TruncatedAtCap(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, strings.Repeat("x", 100))
	tool := NewTool(&config.ToolDefinition{
		Name:         "test-fs",
		Operation:    "read",
		AllowedPaths: []string{dir},
		Sandbox: &config.SandboxConfig{
			ResourceLimits: config.ResourceLimits{MaxOutputBytes: 10},
		},
	})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	content, marker, found := strings.Cut(out, "\n...")
	if !found {
		t.Fatalf("expected a truncation marker, got: %q", out)
	}
	if content != strings.Repeat("x", 10) {
		t.Fatalf("expected exactly 10 bytes of file content before the marker, got %q", content)
	}
	if !strings.Contains(marker, "truncated") {
		t.Fatalf("expected 'truncated' in the marker, got: %q", marker)
	}
}

// TestRead_MaxIntMaxOutputBytes_NoOverflow regression-guards a finding
// from code review: int64(maxBytes)+1 overflows to a
// negative number when max_output_bytes is set to math.MaxInt64, which
// made io.LimitReader return EOF immediately — a valid file read came
// back empty instead of applying the (effectively unlimited) cap.
func TestRead_MaxIntMaxOutputBytes_NoOverflow(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("z", 100)
	file := writeTemp(t, dir, content)
	tool := NewTool(&config.ToolDefinition{
		Name:         "test-fs",
		Operation:    "read",
		AllowedPaths: []string{dir},
		Sandbox: &config.SandboxConfig{
			ResourceLimits: config.ResourceLimits{MaxOutputBytes: math.MaxInt64},
		},
	})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if out != content {
		t.Fatalf("expected full content with max_output_bytes at MaxInt64, got %q", out)
	}
}

// TestRead_NegativeMaxOutputBytes_DisablesCap regression-guards that -1
// (the documented opt-out, mirroring cli's DefaultMaxOutputBytes) still
// returns the file whole rather than being misread as "use the default".
func TestRead_NegativeMaxOutputBytes_DisablesCap(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("y", 100)
	file := writeTemp(t, dir, content)
	tool := NewTool(&config.ToolDefinition{
		Name:         "test-fs",
		Operation:    "read",
		AllowedPaths: []string{dir},
		Sandbox: &config.SandboxConfig{
			ResourceLimits: config.ResourceLimits{MaxOutputBytes: -1},
		},
	})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if out != content {
		t.Fatalf("expected full content with cap disabled, got %q", out)
	}
}

func TestRead_MissingFile_Error(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("read", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": filepath.Join(dir, "nonexistent.txt")})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRead_DirectoryPath_Error(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("read", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": dir})
	if err == nil {
		t.Fatal("expected error when reading a directory")
	}
}

// ============================================================
// Write operation
// ============================================================

func TestWrite_NewFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	tool := newFSTool("write", []string{dir})

	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    dest,
		"content": "new content",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(out, "wrote") && !strings.Contains(out, "bytes") {
		t.Errorf("expected success message, got %q", out)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "new content" {
		t.Errorf("file content mismatch: %q", string(data))
	}
}

func TestWrite_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "old")
	tool := newFSTool("write", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    file,
		"content": "new",
	})
	if err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "new" {
		t.Errorf("expected 'new', got %q", string(data))
	}
}

func TestWrite_AppendMode(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "first")
	tool := newFSTool("write", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    file,
		"content": " second",
		"mode":    "append",
	})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "first second" {
		t.Errorf("expected 'first second', got %q", string(data))
	}
}

func TestWrite_UnknownMode_Error(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("write", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    filepath.Join(dir, "f.txt"),
		"content": "x",
		"mode":    "truncate", // not valid
	})
	if err == nil {
		t.Fatal("expected error for unknown write mode")
	}
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "a", "b", "c.txt")
	tool := newFSTool("write", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    dest,
		"content": "deep",
	})
	if err != nil {
		t.Fatalf("write to nested path failed: %v", err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "deep" {
		t.Errorf("expected 'deep', got %q", string(data))
	}
}

// ============================================================
// List operation
// ============================================================

func TestList_Directory(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "x")
	writeTemp(t, dir, "y")
	tool := newFSTool("list", []string{dir})

	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": dir})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if out == "" || strings.Contains(out, "No files") {
		t.Errorf("expected file listing, got %q", out)
	}
}

func TestList_PatternFilter(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "") // creates *.txt
	// create a .go file
	goFile := filepath.Join(dir, "main.go")
	os.WriteFile(goFile, []byte("package main"), 0644)

	tool := newFSTool("list", []string{dir})
	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    dir,
		"pattern": "*.go",
	})
	if err != nil {
		t.Fatalf("list with pattern failed: %v", err)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("expected main.go in listing, got %q", out)
	}
}

func TestList_FilePath_Error(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "x")
	tool := newFSTool("list", []string{dir})

	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err == nil {
		t.Fatal("expected error when listing a file (not a dir)")
	}
}

// ============================================================
// Search operation
// ============================================================

func TestSearch_ContentMatch(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "needle in haystack")
	tool := newFSTool("search", []string{dir})

	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    dir,
		"pattern": "needle",
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if strings.Contains(out, "No files") {
		t.Errorf("expected match, got: %q", out)
	}
}

func TestSearch_NoMatch_EmptyResult(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "no match here")
	tool := newFSTool("search", []string{dir})

	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    dir,
		"pattern": "xyzzy-not-found",
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if !strings.Contains(out, "No files") {
		t.Errorf("expected 'No files' message, got %q", out)
	}
}

// ============================================================
// Execute — path security via PathNotAllowedError
// ============================================================

func TestExecute_PathNotAllowed(t *testing.T) {
	allowed := t.TempDir()
	secret := t.TempDir() // separate temp dir — not in allowed paths
	tool := newFSTool("read", []string{allowed})

	file := writeTemp(t, secret, "secret")
	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
	if err == nil {
		t.Fatal("expected error for path outside allowed paths")
	}
	var pe *PathNotAllowedError
	if !errors.As(err, &pe) {
		t.Errorf("expected *PathNotAllowedError, got %T: %v", err, err)
	}
}

func TestExecute_UnknownOperation(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("upload", []string{dir}) // not a valid operation

	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": dir})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestExecute_MissingPathArg_FallsBackToFirstAllowedPath(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "fallback content")
	tool := newFSTool("read", []string{file})

	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("expected the missing path to fall back to allowedPaths[0], got error: %v", err)
	}
	if out != "fallback content" {
		t.Errorf("want %q, got %q", "fallback content", out)
	}
}

func TestExecute_MissingPathArg_NoAllowedPathsIsError(t *testing.T) {
	// NewTool always defaults an empty AllowedPaths to ["."], so this
	// branch is unreachable through the public constructor today — built
	// directly to exercise Execute's own defensive check instead of
	// asserting on NewTool's default (that's covered separately by
	// TestIsPathAllowed_DefaultDot_AllowsCWD).
	tool := &Tool{operation: "read"}

	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected an error when 'path' is missing and no allowed paths are configured to fall back to")
	}
}

// ============================================================
// Stress — concurrent reads
// ============================================================

func TestStress_ConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, dir, "shared content")
	tool := newFSTool("read", []string{dir})

	const N = 50
	var wg sync.WaitGroup
	var successes atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := tool.Execute(context.Background(), map[string]interface{}{"path": file})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != N {
		t.Errorf("expected %d successful reads, got %d", N, got)
	}
}

func TestStress_ConcurrentPathChecks(t *testing.T) {
	dir := t.TempDir()
	tool := newFSTool("read", []string{dir})

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				tool.isPathAllowed(dir)
			} else {
				tool.isPathAllowed("/etc/passwd")
			}
		}(i)
	}
	wg.Wait()
}

// ============================================================
// working-dir resolution — SK-ENT/rakitsu#28
// ============================================================

// TestWrite_FencedAllowedPath_ResolvesAgainstCwd locks down the #28 fix:
// with allowed_paths fencing a subdirectory and no explicit working_dir, a
// relative path that already names the fenced directory resolves against the
// process cwd — the old allowedPaths[0] default doubled it into
// workspace/workspace/… via writeFile's MkdirAll.
func TestWrite_FencedAllowedPath_ResolvesAgainstCwd(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("workspace", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	tool := newFSTool("write", []string{"./workspace/"})
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    "./workspace/file.txt",
		"content": "hi",
	}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat("workspace/file.txt"); err != nil {
		t.Errorf("expected file at cwd-relative path: %v", err)
	}
	if _, err := os.Stat("workspace/workspace"); err == nil {
		t.Error("doubled workspace/workspace path was created")
	}
}

// TestWrite_FencedAllowedPath_BareRelativeDenied: without the old fallback a
// bare filename resolves outside the fence and must fail loudly instead of
// being silently relocated into it.
func TestWrite_FencedAllowedPath_BareRelativeDenied(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("workspace", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	tool := newFSTool("write", []string{"./workspace/"})
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    "file.txt",
		"content": "hi",
	})
	var pErr *PathNotAllowedError
	if !errors.As(err, &pErr) {
		t.Errorf("expected *PathNotAllowedError, got %T: %v", err, err)
	}
}

// TestWrite_ExplicitWorkingDir_StillJoins: an explicit working_dir keeps its
// meaning — relative paths join onto it.
func TestWrite_ExplicitWorkingDir_StillJoins(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("workspace", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	tool := NewTool(&config.ToolDefinition{
		Name:         "test-fs",
		Description:  "test",
		Operation:    "write",
		AllowedPaths: []string{"./workspace/"},
		WorkingDir:   "./workspace/",
	})
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    "file.txt",
		"content": "hi",
	}); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat("workspace/file.txt"); err != nil {
		t.Errorf("expected file under working_dir: %v", err)
	}
}

// ============================================================
// search — symlink escape
// ============================================================

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported here: %v", err)
	}
}

func searchFor(t *testing.T, tool *Tool, path, pattern string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    path,
		"pattern": pattern,
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	return out
}

// A symlink inside the allowed directory that points at a file outside it
// must not turn search into a content oracle for that outside file.
func TestSearch_SymlinkToOutsideFile_NotMatched(t *testing.T) {
	outside := t.TempDir()
	allowed := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("marker-outside-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outsideFile, filepath.Join(allowed, "link.txt"))

	tool := newFSTool("search", []string{allowed})
	out := searchFor(t, tool, allowed, "marker-outside-483")
	if !strings.Contains(out, "No files") {
		t.Errorf("search followed a symlink out of allowed_paths: %q", out)
	}
}

func TestSearch_SymlinkedDirOutside_NotDescended(t *testing.T) {
	outside := t.TempDir()
	allowed := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("marker-outside-dir-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, filepath.Join(allowed, "linkdir"))

	tool := newFSTool("search", []string{allowed})
	out := searchFor(t, tool, allowed, "marker-outside-dir-483")
	if !strings.Contains(out, "No files") {
		t.Errorf("search descended a symlinked directory out of allowed_paths: %q", out)
	}
}

// A symlink that resolves inside the allowed directory is still fine.
func TestSearch_SymlinkWithinAllowed_StillMatched(t *testing.T) {
	allowed := t.TempDir()
	target := filepath.Join(allowed, "real.txt")
	if err := os.WriteFile(target, []byte("marker-inside-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, target, filepath.Join(allowed, "link.txt"))

	tool := newFSTool("search", []string{allowed})
	out := searchFor(t, tool, allowed, "marker-inside-483")
	if strings.Contains(out, "No files") || !strings.Contains(out, "real.txt") {
		t.Errorf("in-bound symlink/target should still match, got %q", out)
	}
}

// A search path that is itself a symlink to a directory inside the fence is
// pinned by handle and matched against the directory reached through the
// root, so it still searches (and reports under the path as requested).
func TestSearch_SymlinkedBaseDirWithinAllowed_StillWorks(t *testing.T) {
	allowed := t.TempDir()
	real := filepath.Join(allowed, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "hit.txt"), []byte("marker-base-link-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "link")
	mustSymlink(t, real, link)

	tool := newFSTool("search", []string{allowed})
	out := searchFor(t, tool, link, "marker-base-link-483")
	if !strings.Contains(out, filepath.Join(link, "hit.txt")) {
		t.Errorf("symlinked in-bound search path should still search, got %q", out)
	}
}

// The search path may be a single file ("Directory or file to search" in
// the example configs); it is matched through the root like any other.
func TestSearch_SingleFilePath_StillWorks(t *testing.T) {
	allowed := t.TempDir()
	file := filepath.Join(allowed, "only.txt")
	if err := os.WriteFile(file, []byte("marker-single-483"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newFSTool("search", []string{allowed})
	if out := searchFor(t, tool, file, "marker-single-483"); !strings.Contains(out, file) {
		t.Errorf("single-file search should match the file itself, got %q", out)
	}
	if out := searchFor(t, tool, file, "absent-483"); !strings.Contains(out, "No files") {
		t.Errorf("single-file search should report no match, got %q", out)
	}
}

// Normal in-bound searches keep working, including a search rooted at a
// subdirectory of the allowed path and results reported relative to it.
func TestSearch_Subdirectory_StillFound(t *testing.T) {
	allowed := t.TempDir()
	sub := filepath.Join(allowed, "sub", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("needle-deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, "top.txt"), []byte("needle-deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newFSTool("search", []string{allowed})
	out := searchFor(t, tool, filepath.Join(allowed, "sub"), "needle-deep")
	if !strings.Contains(out, filepath.Join(allowed, "sub", "deeper", "deep.txt")) {
		t.Errorf("expected the nested file under the search root, got %q", out)
	}
	if strings.Contains(out, "top.txt") {
		t.Errorf("search rooted at a subdirectory must not report files above it: %q", out)
	}
}

func TestSearch_RelativeAllowedDot_StillWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle-rel"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	tool := newFSTool("search", nil) // defaults to ["."]
	out := searchFor(t, tool, ".", "needle-rel")
	if strings.Contains(out, "No files") {
		t.Errorf("relative allowed path search failed: %q", out)
	}
}
