package server

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestApplyWorkdirToTools_GlobalTools(t *testing.T) {
	cfg := &config.Config{
		Tools: []config.ToolDefinition{
			{Name: "a", Type: "cli"},
			{Name: "b", Type: "fs", WorkingDir: "/explicit"},
			{Name: "c", Type: "fs", Sandbox: &config.SandboxConfig{AllowedPaths: []string{"/already/allowed"}}},
		},
	}
	applyWorkdirToTools(cfg, "/tmp/work")

	// Tool a had no sandbox or workdir — both get filled.
	if cfg.Tools[0].WorkingDir != "/tmp/work" {
		t.Errorf("a.WorkingDir = %q, want /tmp/work", cfg.Tools[0].WorkingDir)
	}
	if cfg.Tools[0].Sandbox == nil || len(cfg.Tools[0].Sandbox.AllowedPaths) != 1 || cfg.Tools[0].Sandbox.AllowedPaths[0] != "/tmp/work" {
		t.Errorf("a.Sandbox.AllowedPaths not injected: %+v", cfg.Tools[0].Sandbox)
	}
	// Tool b had explicit WorkingDir — must be preserved.
	if cfg.Tools[1].WorkingDir != "/explicit" {
		t.Errorf("b.WorkingDir overridden: got %q, want /explicit", cfg.Tools[1].WorkingDir)
	}
	// Tool c had explicit allowlist — must be preserved.
	if cfg.Tools[2].Sandbox.AllowedPaths[0] != "/already/allowed" {
		t.Errorf("c allowlist overridden: %+v", cfg.Tools[2].Sandbox.AllowedPaths)
	}
}

func TestApplyWorkdirToTools_InlineOnAgents(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentDefinition{
			{
				Name: "Worker",
				ToolsInline: []config.ToolDefinition{
					{Name: "inline-a", Type: "cli"},
					{Name: "inline-b", Type: "fs", WorkingDir: "/pinned"},
				},
			},
		},
	}
	applyWorkdirToTools(cfg, "/tmp/work")

	if cfg.Agents[0].ToolsInline[0].WorkingDir != "/tmp/work" {
		t.Errorf("inline-a WorkingDir not injected: %q", cfg.Agents[0].ToolsInline[0].WorkingDir)
	}
	if cfg.Agents[0].ToolsInline[0].Sandbox == nil ||
		len(cfg.Agents[0].ToolsInline[0].Sandbox.AllowedPaths) != 1 ||
		cfg.Agents[0].ToolsInline[0].Sandbox.AllowedPaths[0] != "/tmp/work" {
		t.Errorf("inline-a Sandbox not injected: %+v", cfg.Agents[0].ToolsInline[0].Sandbox)
	}
	if cfg.Agents[0].ToolsInline[1].WorkingDir != "/pinned" {
		t.Errorf("inline-b explicit WorkingDir overridden")
	}
}

func TestApplyWorkdirToTools_EmptyWorkdirIsNoop(t *testing.T) {
	cfg := &config.Config{
		Tools: []config.ToolDefinition{{Name: "a", Type: "cli"}},
	}
	applyWorkdirToTools(cfg, "")
	if cfg.Tools[0].WorkingDir != "" {
		t.Errorf("empty workdir should not mutate cfg; WorkingDir=%q", cfg.Tools[0].WorkingDir)
	}
	if cfg.Tools[0].Sandbox != nil {
		t.Errorf("empty workdir should not create Sandbox: %+v", cfg.Tools[0].Sandbox)
	}
}

func TestApplyWorkdirToTools_NilCfgIsNoop(t *testing.T) {
	// Must not panic.
	applyWorkdirToTools(nil, "/tmp/work")
}
