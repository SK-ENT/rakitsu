package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// markerTool returns a tool whose command writes a marker file into dir when
// it actually runs on the host, so a test can prove a refused command never
// executed rather than just that an error came back.
func markerTool(t *testing.T, dir string, sandbox *config.SandboxConfig) *Tool {
	t.Helper()
	return NewTool(&config.ToolDefinition{
		Name:       "write",
		Command:    "sh -c 'printf touched > marker'",
		WorkingDir: dir,
		Sandbox:    sandbox,
	})
}

func assertMarkerAbsent(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("command executed on the host despite being refused")
	}
}

func TestNewTool_DefaultSandboxIsLocalRestricted(t *testing.T) {
	for name, sandbox := range map[string]*config.SandboxConfig{
		"nil":        nil,
		"empty type": {},
	} {
		t.Run(name, func(t *testing.T) {
			tool := NewTool(&config.ToolDefinition{Name: "ls", Command: "ls", Sandbox: sandbox})
			if tool.sandbox.Type != "local_restricted" {
				t.Fatalf("default sandbox type = %q, want local_restricted", tool.sandbox.Type)
			}
		})
	}
}

// Regression: an unknown sandbox type (a typo like "dokcer") used to fall
// through the Execute switch into host execution — the least isolated mode.
func TestExecute_UnknownSandboxType_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	tool := markerTool(t, dir, &config.SandboxConfig{Type: "dokcer"})
	_, err := tool.Execute(context.Background(), nil)
	var se *SecurityError
	if !errors.As(err, &se) {
		t.Fatalf("want *SecurityError for unknown sandbox type, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "dokcer") {
		t.Fatalf("error should name the bad value: %v", err)
	}
	assertMarkerAbsent(t, dir)
}

// Sandbox settings that would silently lose their meaning must be refused
// at execution too, not only at config load — tools can be built directly.
func TestExecute_InvalidSandboxSettings_Refused(t *testing.T) {
	dir := t.TempDir()
	tool := markerTool(t, dir, &config.SandboxConfig{Type: "local_restricted", Image: "alpine"})
	_, err := tool.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("want invalid-sandbox error naming 'image', got %v", err)
	}
	assertMarkerAbsent(t, dir)
}

func TestExecute_MissingDocker_NoHostFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // no docker, no sh
	tool := markerTool(t, dir, &config.SandboxConfig{Type: "docker"})
	_, err := tool.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "docker") {
		t.Fatalf("want a docker-unavailable error, got %v", err)
	}
	assertMarkerAbsent(t, dir)
}

// An explicit working_dir on the tool is what gets mounted at /workspace,
// matching local mode where working_dir is the command's cwd. Previously the
// process cwd was always mounted regardless of working_dir.
func TestBuildDockerArgs_MountsExplicitWorkingDir(t *testing.T) {
	dir := t.TempDir()
	args := buildDockerArgs(&config.SandboxConfig{Type: "docker", MountWorkdir: true}, []string{"ls"}, dir)
	if !containsArgPair(args, "-v", func(v string) bool { return v == dir+":/workspace:ro" }) {
		t.Fatalf("want %s mounted read-only at /workspace, got %v", dir, args)
	}
}

func TestBuildDockerArgs_ResolvesRelativeWorkingDir(t *testing.T) {
	// A relative working_dir was passed straight through as the -v mount
	// source. Docker requires an absolute host path for a bind mount, so
	// "./project:/workspace" either failed to start the container or was
	// read as a named volume instead of the host directory local mode uses.
	t.Chdir(t.TempDir())
	rel := "project"
	if err := os.Mkdir(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	args := buildDockerArgs(&config.SandboxConfig{Type: "docker", MountWorkdir: true}, []string{"ls"}, rel)
	if !containsArgPair(args, "-v", func(v string) bool { return v == want+":/workspace:ro" }) {
		t.Fatalf("want %s (resolved absolute) mounted read-only at /workspace, got %v", want, args)
	}
}

func TestBuildDockerArgs_NoWorkingDirMountsCwd(t *testing.T) {
	cwd, _ := os.Getwd()
	args := buildDockerArgs(&config.SandboxConfig{Type: "docker", MountWorkdir: true}, []string{"ls"}, "")
	if !containsArgPair(args, "-v", func(v string) bool { return v == cwd+":/workspace:ro" }) {
		t.Fatalf("want process cwd mounted when working_dir is empty, got %v", args)
	}
}

func TestBuildDockerArgs_NamesContainer(t *testing.T) {
	args := buildDockerArgs(&config.SandboxConfig{Type: "docker"}, []string{"ls"}, "")
	if !containsArgPair(args, "--name", func(v string) bool { return strings.HasPrefix(v, "rakitsu-") }) {
		t.Fatalf("want a unique rakitsu-* container name for cleanup, got %v", args)
	}
}
