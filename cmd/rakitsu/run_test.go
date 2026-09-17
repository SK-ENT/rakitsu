package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func withInteractiveFlag(t *testing.T, v bool) {
	t.Helper()
	prev := interactiveFlag
	interactiveFlag = v
	t.Cleanup(func() { interactiveFlag = prev })
}

func TestRunArgs_ZeroArgsRequiresInteractive(t *testing.T) {
	withInteractiveFlag(t, true)
	if err := runCmd.Args(runCmd, nil); err != nil {
		t.Errorf("zero args with --interactive: got error %v, want nil", err)
	}
}

func TestRunArgs_ZeroArgsWithoutInteractiveStillFails(t *testing.T) {
	withInteractiveFlag(t, false)
	if err := runCmd.Args(runCmd, nil); err == nil {
		t.Error("zero args without --interactive: got nil error, want an error")
	}
}

func TestRunArgs_ExistingMultiArgBehaviorUnaffected(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		withInteractiveFlag(t, interactive)
		if err := runCmd.Args(runCmd, []string{"agent.yaml"}); err != nil {
			t.Errorf("interactive=%v, 1 arg: got error %v, want nil", interactive, err)
		}
		if err := runCmd.Args(runCmd, []string{"agent.yaml", "query"}); err != nil {
			t.Errorf("interactive=%v, 2 args: got error %v, want nil", interactive, err)
		}
	}
}

func TestEnsureDefaultConfig_CreatesOnceAndPreservesEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "default-agent.yaml")

	if err := ensureDefaultConfig(path); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist after first call: %v", err)
	}

	marker := "\n# user edit marker\n"
	if err := appendToFile(path, marker); err != nil {
		t.Fatalf("appending marker: %v", err)
	}

	if err := ensureDefaultConfig(path); err != nil {
		t.Fatalf("second call: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if !strings.Contains(string(content), "user edit marker") {
		t.Error("second call overwrote the file — user edit was lost")
	}
}

func TestEnsureDefaultConfig_IncludesReadOnlyFsTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default-agent.yaml")

	if err := ensureDefaultConfig(path); err != nil {
		t.Fatalf("ensureDefaultConfig: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading generated config: %v", err)
	}

	wantTools := map[string]bool{"list_files": false, "read_file": false, "search_files": false}
	for _, tool := range cfg.Tools {
		if _, ok := wantTools[tool.Name]; ok {
			wantTools[tool.Name] = true
			if tool.Type != "fs" {
				t.Errorf("tool %q: got type %q, want fs", tool.Name, tool.Type)
			}
		}
	}
	for name, found := range wantTools {
		if !found {
			t.Errorf("expected tool %q not found in generated config", name)
		}
	}

	if len(cfg.Agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(cfg.Agents))
	}
	agent := cfg.Agents[0]
	if agent.Settings == nil {
		t.Fatal("agent.Settings is nil, want max_iterations set")
	}
	if agent.Settings.MaxIterations != 6 {
		t.Errorf("got max_iterations %d, want 6", agent.Settings.MaxIterations)
	}
}

func TestDefaultConfigPath_UnderHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("defaultConfigPath: %v", err)
	}
	want := filepath.Join(dir, ".rakitsu", "default-agent.yaml")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
