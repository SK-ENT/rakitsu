package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
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

	wantTools := map[string]bool{"list_files": false, "read_file": false, "search_files": false, "read_image": false}
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
	// No fixed step cap in the default chat, so a run with no
	// --timeout can take as many steps as the task needs.
	if agent.Settings != nil && agent.Settings.MaxIterations != 0 {
		t.Errorf("got max_iterations %d, want unset", agent.Settings.MaxIterations)
	}
	// Vision left unset = auto — images reach a vision model, and a
	// text-only model (the llama3.1 default) falls back to a note.
	if agent.Vision != nil {
		t.Errorf("vision = %v, want unset (auto)", *agent.Vision)
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

// TestRegisterToolDefs_RegistersEachSupportedType is a regression test for
// a bug where runAgent's single-agent/no-orchestrator path used
// to skip ToolsInline registration entirely (a duplicated, independently
// hand-rolled switch elsewhere had the real logic, but this path never
// called it). registerToolDefs is now the one shared implementation all
// four call sites use — this test exercises it directly against the
// non-network tool types (cli, fs, jev) so it runs fast and offline; the
// mcp_server/a2a cases are covered by their own package-level tests and by
// the live CI verification already run for examples/jev/06-fact-checked-review.
func TestRegisterToolDefs_RegistersEachSupportedType(t *testing.T) {
	defs := []config.ToolDefinition{
		{Name: "run-command", Type: "cli", Command: "echo hi"},
		{Name: "read-file", Type: "fs", Operation: "read"},
		{Name: "jev", Type: "jev"},
	}

	registry := tools.NewToolRegistry()
	registerToolDefs(context.Background(), registry, nil, defs)

	for _, name := range []string{"run-command", "read-file", "jev"} {
		if registry.GetTool(name) == nil {
			t.Errorf("expected tool %q to be registered, got nil", name)
		}
	}
	if got, want := len(registry.GetAllTools()), 3; got != want {
		t.Errorf("got %d registered tools, want %d", got, want)
	}
}

// TestRegisterToolDefs_EmptyDefsRegistersNothing guards the other
// direction: an agent with no tools_inline (or an unset ToolsInline field)
// must not panic or register anything, which matters since every call
// site now shares this one function.
func TestRegisterToolDefs_EmptyDefsRegistersNothing(t *testing.T) {
	registry := tools.NewToolRegistry()
	registerToolDefs(context.Background(), registry, nil, nil)
	if got := len(registry.GetAllTools()); got != 0 {
		t.Errorf("got %d registered tools, want 0", got)
	}
}
