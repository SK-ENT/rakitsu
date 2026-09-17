package shared

import "github.com/SK-ENT/rakitsu/internal/config"

// ResolveProvider returns the provider name and definition for an agent.
// Falls back to settings.default_provider if agent has no provider set.
func ResolveProvider(agent config.AgentDefinition, cfg *config.Config) (name string, pDef config.ProviderDefinition) {
	name = agent.Provider
	if name == "" {
		name = cfg.Settings.DefaultProvider
	}
	if name == "" {
		name = "openai"
	}
	pDef = cfg.Settings.Providers[name]
	if pDef.Type == "" {
		pDef.Type = name
	}
	return name, pDef
}

// ResolveModel returns the model string for an agent.
// Falls back to settings.defaults.model.
func ResolveModel(agent config.AgentDefinition, cfg *config.Config) string {
	if agent.Model != "" {
		return agent.Model
	}
	return cfg.Settings.Defaults.Model
}

// QualifiedModel returns "provider/model" format used by OpenClaw.
func QualifiedModel(providerName, model string) string {
	return providerName + "/" + model
}

// ToolsByName returns a map of tool name -> definition for quick lookup.
func ToolsByName(cfg *config.Config) map[string]config.ToolDefinition {
	m := make(map[string]config.ToolDefinition, len(cfg.Tools))
	for _, t := range cfg.Tools {
		m[t.Name] = t
	}
	return m
}
