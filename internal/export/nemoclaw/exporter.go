package nemoclaw

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/core"
	"github.com/SK-ENT/rakitsu/internal/export/openclaw"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// Options configures exporter behavior.
type Options struct {
	// WithBlueprint emits blueprint.yaml (only needed for NVIDIA catalog contributions).
	WithBlueprint bool
}

// Exporter generates NemoClaw deployment files:
// - openclaw.json      (agent config — verified against OpenClaw)
// - sandbox-policy.yaml (OpenShell security policies — verified on DGX Spark)
// - README.md          (deployment instructions)
//
// With WithBlueprint option also emits:
// - blueprint.yaml     (for contributing to NVIDIA's blueprint catalog)
//
// For multi-agent configs, generates one subdirectory per agent.
type Exporter struct {
	Opts Options
}

// SetOptions configures the exporter.
func (e *Exporter) SetOptions(opts Options) {
	e.Opts = opts
}

func (e *Exporter) Export(cfg *config.Config, configPath, outputDir string) error {
	if err := shared.RestoreEnvRefs(cfg, configPath); err != nil {
		return fmt.Errorf("restore env refs: %w", err)
	}
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("no agents defined in config")
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("cannot create output dir: %w", err)
	}

	if len(cfg.Agents) == 1 {
		return e.exportSingle(cfg, cfg.Agents[0], outputDir)
	}
	return e.exportMulti(cfg, outputDir)
}

// exportSingle generates files for a single-agent config.
func (e *Exporter) exportSingle(cfg *config.Config, agent config.AgentDefinition, dir string) error {
	// openclaw.json — single agent config
	singleCfg := SingleAgentConfig(cfg, agent)
	oc, err := openclaw.BuildConfig(singleCfg)
	if err != nil {
		return fmt.Errorf("build openclaw config: %w", err)
	}
	if err := shared.WriteJSON(filepath.Join(dir, "openclaw.json"), oc); err != nil {
		return err
	}

	// blueprint.yaml — only when explicitly requested (for catalog contributions)
	if e.Opts.WithBlueprint {
		bp, err := buildBlueprint(cfg, agent)
		if err != nil {
			return fmt.Errorf("build blueprint: %w", err)
		}
		if err := shared.WriteYAML(filepath.Join(dir, "blueprint.yaml"), bp); err != nil {
			return err
		}
	}

	// sandbox-policy.yaml
	policy, err := BuildPolicy(singleCfg)
	if err != nil {
		return fmt.Errorf("build policy: %w", err)
	}
	if err := shared.WriteYAML(filepath.Join(dir, "sandbox-policy.yaml"), policy); err != nil {
		return err
	}

	// AGENT.md — system prompt
	if agent.SystemPrompt != "" {
		if err := shared.WriteFile(filepath.Join(dir, "AGENT.md"), agent.SystemPrompt+"\n"); err != nil {
			return err
		}
	}

	// README.md
	readme := singleReadme(cfg.Name, agent.Name)
	return shared.WriteFile(filepath.Join(dir, "README.md"), readme)
}

// exportMulti generates one subdirectory per agent, orchestration glue scripts,
// docker-compose.yaml, and an orchestrator README.
func (e *Exporter) exportMulti(cfg *config.Config, dir string) error {
	// Config validation only rejects exact-string duplicate agent names;
	// shared.SanitizeID is not collision-resistant ("Code Reviewer" and
	// "Code-Reviewer" both sanitize to "code-reviewer"). Without this check,
	// the second agent's exportSingle call would silently overwrite the
	// first agent's already-written files in the same directory.
	seenDirs := map[string]string{} // sanitized ID -> original agent name
	for _, agent := range cfg.Agents {
		id := shared.SanitizeID(agent.Name)
		if prev, ok := seenDirs[id]; ok {
			return fmt.Errorf("agents %q and %q both sanitize to the same directory name %q — rename one", prev, agent.Name, id)
		}
		seenDirs[id] = agent.Name

		agentDir := filepath.Join(dir, "agent-"+id)
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			return fmt.Errorf("cannot create agent dir: %w", err)
		}
		if err := e.exportSingle(cfg, agent, agentDir); err != nil {
			return fmt.Errorf("export agent %s: %w", agent.Name, err)
		}
	}

	// For every non-Pipeline strategy (ReAct/Hierarchical/PlanAndExecute),
	// the generated orchestrate.sh unconditionally emits a "connect to
	// supervisor" step, but cfg.Orchestrator is a separate entity from
	// cfg.Agents that nothing here otherwise deploys — export it as its own
	// agent directory too, so the sandbox/container the script assumes
	// exists actually gets created. core.SupervisorAgentName is the single
	// source of truth for the name so the script's "connect to X" text and
	// this directory can't drift apart.
	orchestrationAgents := cfg.Agents
	strategy := ""
	if cfg.Orchestrator != nil {
		strategy = cfg.Orchestrator.Strategy
	}
	if strategy != "Pipeline" && cfg.Orchestrator != nil {
		supervisorName := core.SupervisorAgentName(cfg)
		supervisorID := shared.SanitizeID(supervisorName)
		if prev, ok := seenDirs[supervisorID]; ok {
			return fmt.Errorf("orchestrator %q and agent %q both sanitize to the same directory name %q — rename one", supervisorName, prev, supervisorID)
		}
		seenDirs[supervisorID] = supervisorName

		supervisorAgent := config.AgentDefinition{
			Name:         supervisorName,
			Role:         cfg.Orchestrator.Role,
			Provider:     cfg.Orchestrator.Provider,
			Model:        cfg.Orchestrator.Model,
			ModelConfig:  cfg.Orchestrator.ModelConfig,
			SystemPrompt: cfg.Orchestrator.SystemPrompt,
		}
		supervisorDir := filepath.Join(dir, "agent-"+supervisorID)
		if err := os.MkdirAll(supervisorDir, 0755); err != nil {
			return fmt.Errorf("cannot create supervisor dir: %w", err)
		}
		if err := e.exportSingle(cfg, supervisorAgent, supervisorDir); err != nil {
			return fmt.Errorf("export supervisor agent: %w", err)
		}
		orchestrationAgents = append(append([]config.AgentDefinition{}, cfg.Agents...), supervisorAgent)
	}

	// Generate orchestration glue (orchestrate.sh, docker-compose.yaml, delegate.sh)
	// Uses RuntimeTarget interface — orchestration logic is target-agnostic.
	// Passes orchestrationAgents (workers + supervisor, when applicable) so
	// docker-compose.yaml and the openshell setup-commands reminder both
	// know about the supervisor's sandbox/container too.
	cfgForOrchestration := *cfg
	cfgForOrchestration.Agents = orchestrationAgents
	targets := []core.RuntimeTarget{&OpenShellTarget{}, &core.ComposeTarget{}}
	if err := core.GenerateOrchestration(&cfgForOrchestration, dir, targets); err != nil {
		return fmt.Errorf("generate orchestration: %w", err)
	}

	// Orchestrator README
	readme := multiReadme(&cfgForOrchestration)
	return shared.WriteFile(filepath.Join(dir, "README.md"), readme)
}

// SingleAgentConfig creates a Config with only one agent (preserves settings/tools).
func SingleAgentConfig(cfg *config.Config, agent config.AgentDefinition) *config.Config {
	// Filter tools to only those used by this agent
	usedTools := map[string]bool{}
	for _, t := range agent.Tools {
		usedTools[t] = true
	}
	var tools []config.ToolDefinition
	for _, t := range cfg.Tools {
		if usedTools[t.Name] {
			tools = append(tools, t)
		}
	}

	// Filter providers to only those this agent can actually reach: its
	// resolved primary provider plus any names in its fallback chain.
	// Without this, BuildPolicy (which iterates Settings.Providers
	// unfiltered) grants every provider declared anywhere in the whole
	// multi-agent config a network-egress endpoint in this one agent's
	// sandbox-policy.yaml, even providers it never uses.
	usedProviders := map[string]bool{}
	primaryName, _ := shared.ResolveProvider(agent, cfg)
	usedProviders[primaryName] = true
	for _, p := range agent.Providers {
		usedProviders[p.Name] = true
	}
	providers := map[string]config.ProviderDefinition{}
	for name, pDef := range cfg.Settings.Providers {
		if usedProviders[name] {
			providers[name] = pDef
		}
	}
	settings := cfg.Settings
	settings.Providers = providers

	return &config.Config{
		Name:     cfg.Name,
		Settings: settings,
		Tools:    tools,
		Agents:   []config.AgentDefinition{agent},
	}
}
