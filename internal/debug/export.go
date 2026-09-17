package debug

import (
	"github.com/SK-ENT/rakitsu/internal/config"
	"gopkg.in/yaml.v3"
)

// ExportConfig creates a YAML configuration with parameter overrides applied.
// It deep-copies the original config and modifies matching agents' model_config.
func ExportConfig(original *config.Config, overrides map[string]*ParamOverride) ([]byte, error) {
	// Marshal and unmarshal to deep-copy
	data, err := yaml.Marshal(original)
	if err != nil {
		return nil, err
	}
	var copied config.Config
	if err := yaml.Unmarshal(data, &copied); err != nil {
		return nil, err
	}

	// Apply overrides to matching agents
	for i := range copied.Agents {
		agent := &copied.Agents[i]
		ovr, ok := overrides[agent.Name]
		if !ok {
			continue
		}

		if agent.ModelConfig == nil {
			agent.ModelConfig = &config.ModelConfig{}
		}
		if ovr.Temperature != nil {
			agent.ModelConfig.Temperature = *ovr.Temperature
		}
		if ovr.MaxTokens != nil {
			agent.ModelConfig.MaxTokens = *ovr.MaxTokens
		}
		if ovr.TopP != nil {
			agent.ModelConfig.TopP = *ovr.TopP
		}
		if ovr.Model != nil {
			agent.Model = *ovr.Model
		}
	}

	return yaml.Marshal(&copied)
}
