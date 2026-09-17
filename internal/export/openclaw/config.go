// Package openclaw generates OpenClaw agent configurations from Rakitsu configs.
package openclaw

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// --- OpenClaw config types (matching real OpenClaw schema) ---

// Config is the root openclaw.json structure.
type Config struct {
	Gateway *Gateway `json:"gateway,omitempty"`
	Models  *Models  `json:"models,omitempty"`
	Agents  *Agents  `json:"agents"`
}

type Gateway struct {
	Auth *GatewayAuth `json:"auth,omitempty"`
}

type GatewayAuth struct {
	Mode string `json:"mode"`
}

type Models struct {
	Providers map[string]*Provider `json:"providers,omitempty"`
}

type Provider struct {
	BaseURL string  `json:"baseUrl,omitempty"`
	APIKey  string  `json:"apiKey,omitempty"`
	API     string  `json:"api,omitempty"`
	Models  []Model `json:"models,omitempty"`
}

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type Agents struct {
	Defaults *AgentDefaults `json:"defaults,omitempty"`
	List     []Agent        `json:"list"`
}

type AgentDefaults struct {
	Model    *DefaultModel `json:"model,omitempty"`
	Thinking *Thinking     `json:"thinking,omitempty"`
	Tools    *ToolsConfig  `json:"tools,omitempty"`
}

type DefaultModel struct {
	Primary   string   `json:"primary"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

type Thinking struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level,omitempty"`
}

type Agent struct {
	ID       string       `json:"id"`
	Name     string       `json:"name,omitempty"`
	Default  bool         `json:"default,omitempty"`
	Model    string       `json:"model,omitempty"`
	ModelCfg *ModelConfig `json:"model_config,omitempty"`
	Thinking *Thinking    `json:"thinking,omitempty"`
	Tools    *ToolsConfig `json:"tools,omitempty"`
}

type ModelConfig struct {
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
}

type ToolsConfig struct {
	Profile string   `json:"profile,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

// --- Build logic ---

// BuildConfig constructs an OpenClaw config from a Rakitsu config.
func BuildConfig(cfg *config.Config) (*Config, error) {
	tools := shared.ToolsByName(cfg)

	oc := &Config{}
	oc.Models = buildModels(cfg)
	agents, err := buildAgents(cfg, tools)
	if err != nil {
		return nil, err
	}
	oc.Agents = agents

	// The global default model must actually appear in its provider's own
	// catalog. buildModels only populates a provider's models from models
	// resolved per-agent (plus that provider's own default_model) — if
	// every agent on the default provider overrides its own model, the
	// default emitted above can otherwise reference a model ID that
	// appears nowhere in models.providers[...].models.
	if oc.Agents.Defaults != nil && oc.Agents.Defaults.Model != nil {
		defProvider := cfg.Settings.DefaultProvider
		if defProvider == "" {
			defProvider = "openai"
		}
		if op, ok := oc.Models.Providers[defProvider]; ok {
			defModelID := cfg.Settings.Defaults.Model
			found := false
			for _, m := range op.Models {
				if m.ID == defModelID {
					found = true
					break
				}
			}
			if !found && defModelID != "" {
				op.Models = append(op.Models, Model{ID: defModelID, Name: defModelID})
			}
		}
	}

	return oc, nil
}

func buildModels(cfg *config.Config) *Models {
	m := &Models{
		Providers: make(map[string]*Provider),
	}

	for name, pDef := range cfg.Settings.Providers {
		pType := pDef.Type
		if pType == "" {
			pType = name
		}

		op := &Provider{
			APIKey: pDef.APIKey,
			API:    MapProviderAPI(pType),
		}
		if pDef.BaseURL != "" {
			op.BaseURL = pDef.BaseURL
		}

		// Collect models used by agents for this provider
		seen := map[string]bool{}
		for _, agent := range cfg.Agents {
			pName, _ := shared.ResolveProvider(agent, cfg)
			if pName == name {
				model := shared.ResolveModel(agent, cfg)
				if model != "" && !seen[model] {
					seen[model] = true
					op.Models = append(op.Models, Model{
						ID:   model,
						Name: model,
					})
				}
			}
		}
		// Also include default model
		if pDef.DefaultModel != "" && !seen[pDef.DefaultModel] {
			op.Models = append(op.Models, Model{ID: pDef.DefaultModel, Name: pDef.DefaultModel})
		}

		m.Providers[name] = op
	}

	return m
}

func buildAgents(cfg *config.Config, tools map[string]config.ToolDefinition) (*Agents, error) {
	agents := &Agents{}

	// Defaults
	defProvider := cfg.Settings.DefaultProvider
	if defProvider == "" {
		defProvider = "openai"
	}
	defModel := cfg.Settings.Defaults.Model
	if defModel != "" {
		agents.Defaults = &AgentDefaults{
			Model: &DefaultModel{
				Primary: shared.QualifiedModel(defProvider, defModel),
			},
		}
	}

	// Agent list. Config validation only rejects exact-string duplicate
	// agent.Name values; shared.SanitizeID is not collision-resistant
	// ("Data Analyst" and "DataAnalyst" both sanitize to "data-analyst"),
	// so two legitimately-distinct, validation-passing agents could
	// otherwise silently share an exported id — fail loudly instead
	// (same policy as the nemoclaw exporter's identical collision class).
	seenIDs := map[string]string{} // id -> original agent name
	for i, agent := range cfg.Agents {
		provName, _ := shared.ResolveProvider(agent, cfg)
		model := shared.ResolveModel(agent, cfg)

		id := shared.SanitizeID(agent.Name)
		if prev, ok := seenIDs[id]; ok {
			return nil, fmt.Errorf("agents %q and %q both sanitize to the same id %q — rename one", prev, agent.Name, id)
		}
		seenIDs[id] = agent.Name

		oa := Agent{
			ID:      id,
			Name:    agent.Name,
			Default: i == 0,
		}

		if model != "" {
			oa.Model = shared.QualifiedModel(provName, model)
		}

		// Model config
		if agent.ModelConfig != nil {
			oa.ModelCfg = &ModelConfig{
				Temperature: agent.ModelConfig.Temperature,
				MaxTokens:   agent.ModelConfig.MaxTokens,
				TopP:        agent.ModelConfig.TopP,
			}
		}

		// Tools
		if len(agent.Tools) > 0 {
			oa.Tools = mapTools(agent.Tools, tools)
		}

		// Reflection -> per-agent thinking. Agent has its own Thinking
		// field (unlike the earlier i==0-only shared Defaults.Thinking
		// special case this replaces), so every agent's own reflection
		// setting is represented, not just the first one's.
		if agent.Settings != nil && agent.Settings.Reflection.Enabled {
			oa.Thinking = &Thinking{
				Enabled: true,
				Level:   "medium",
			}
		}

		agents.List = append(agents.List, oa)
	}

	return agents, nil
}

// mapTools converts Rakitsu tool references to OpenClaw tool allow list.
//
// Known limitation: this collapses every fs tool to the blanket "group:fs"
// and every cli tool to a bare "exec", regardless of the source tool's
// actual scope (an fs tool's operation/allowedPaths restriction, or a cli
// tool's allowed_commands list) — a read-only, path-scoped fs tool and an
// unrestricted one both become the identical string. ToolsConfig{Profile,
// Allow, Deny} has no field to carry that narrower scope today; fixing this
// properly requires confirming OpenClaw's actual capability-string grammar,
// which isn't available from this repo alone. Not fixed here — guessing at
// an unverified string convention risks OpenClaw silently ignoring or
// misinterpreting it, which would be worse than the current, at-least-
// visible blanket grant.
func mapTools(agentTools []string, allTools map[string]config.ToolDefinition) *ToolsConfig {
	tc := &ToolsConfig{
		Profile: "coding",
	}

	for _, name := range agentTools {
		tool, ok := allTools[name]
		if !ok {
			tc.Allow = append(tc.Allow, name)
			continue
		}
		switch tool.Type {
		case "fs":
			tc.Allow = append(tc.Allow, "group:fs")
		case "cli":
			tc.Allow = append(tc.Allow, "exec")
		default:
			tc.Allow = append(tc.Allow, name)
		}
	}

	// Deduplicate
	tc.Allow = shared.Dedup(tc.Allow)
	return tc
}

// MapProviderAPI returns the OpenClaw api field for a provider type.
func MapProviderAPI(t string) string {
	switch t {
	case "anthropic":
		return "" // native, no api field needed
	case "gemini":
		return "" // native
	default:
		return "openai-completions" // openai, litellm, ollama, vllm
	}
}

// MapProviderType converts Rakitsu provider types to OpenClaw types.
func MapProviderType(t string) string {
	switch t {
	case "openai", "litellm", "ollama":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "gemini":
		return "google"
	default:
		return "openai"
	}
}
