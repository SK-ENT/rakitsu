package server

import "github.com/SK-ENT/rakitsu/internal/config"

// applyWorkdirToTools injects workdir into every tool's sandbox allowlist and
// WorkingDir — both global `cfg.Tools` and agent-level `ToolsInline`. Only
// fills fields that are empty/unset so explicit YAML wins over the runtime
// workdir.
//
// Used by both one-shot runs (AgentRunner) and chat sessions (ChatManager) so
// the two paths agree on how tools are sandboxed when a browser workspace is
// selected. No-op when workdir is empty.
func applyWorkdirToTools(cfg *config.Config, workdir string) {
	if workdir == "" || cfg == nil {
		return
	}
	for i := range cfg.Tools {
		if cfg.Tools[i].Sandbox == nil {
			cfg.Tools[i].Sandbox = &config.SandboxConfig{}
		}
		if len(cfg.Tools[i].Sandbox.AllowedPaths) == 0 {
			cfg.Tools[i].Sandbox.AllowedPaths = []string{workdir}
		}
		if cfg.Tools[i].WorkingDir == "" {
			cfg.Tools[i].WorkingDir = workdir
		}
	}
	for i := range cfg.Agents {
		for j := range cfg.Agents[i].ToolsInline {
			if cfg.Agents[i].ToolsInline[j].Sandbox == nil {
				cfg.Agents[i].ToolsInline[j].Sandbox = &config.SandboxConfig{}
			}
			if len(cfg.Agents[i].ToolsInline[j].Sandbox.AllowedPaths) == 0 {
				cfg.Agents[i].ToolsInline[j].Sandbox.AllowedPaths = []string{workdir}
			}
			if cfg.Agents[i].ToolsInline[j].WorkingDir == "" {
				cfg.Agents[i].ToolsInline[j].WorkingDir = workdir
			}
		}
	}
}
