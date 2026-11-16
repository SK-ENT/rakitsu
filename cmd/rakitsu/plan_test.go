package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/permission"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	fstool "github.com/SK-ENT/rakitsu/internal/tools/fs"
)

func TestPlanRuntimeRejectsUnsafeConfigBeforeBuildingTools(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{Focus: config.FocusConfig{Mode: permission.FocusPlan}},
		Agents:   []config.AgentDefinition{{Name: "planner"}},
		Tools:    []config.ToolDefinition{{Name: "exec", Type: "cli"}},
	}
	ctx := context.Background()
	bus := telemetry.NewEventBus(16)
	if _, err := BuildRunner(ctx, cfg, bus, BuildOptions{}); err == nil {
		t.Fatal("chat and CLI runtime accepted Plan with command execution")
	}
	if _, err := executeConfigConv(ctx, cfg, bus, nil, "plan", nil, nil); err == nil {
		t.Fatal("ACP and server runtime accepted Plan with command execution")
	}
}

func TestPlanRegistryCannotExecuteWrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "canary.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Settings: config.Settings{Focus: config.FocusConfig{Mode: permission.FocusPlan}},
		Tools: []config.ToolDefinition{
			{Name: "read", Type: "fs", Operation: "read", AllowedPaths: []string{dir}},
			{Name: "write", Type: "fs", Operation: "write", AllowedPaths: []string{dir}},
			{Name: "external", Type: "mcp_server"},
		},
	}
	reg := createToolRegistry(context.Background(), cfg)
	defer reg.CloseAll()
	reg.RegisterTool(fstool.NewTool(&cfg.Tools[1]))
	args := map[string]any{"path": file, "content": "changed"}
	if out, err := reg.ExecuteToolCall(context.Background(), tools.ToolCall{Name: "read", Arguments: args}); err != nil || out != "original" {
		t.Fatalf("read = %q, %v", out, err)
	}
	if _, err := reg.ExecuteToolCall(context.Background(), tools.ToolCall{Name: "write", Arguments: args}); err == nil {
		t.Fatal("write dispatched")
	}
	if reg.GetTool("external") != nil {
		t.Fatal("external tool registered")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "original" {
		t.Fatalf("canary = %q, %v", data, err)
	}
}
