package nemoclaw

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// Blueprint is the NemoClaw blueprint.yaml structure.
type Blueprint struct {
	Version           string             `yaml:"version"`
	MinOpenShellVer   string             `yaml:"min_openshell_version"`
	MinOpenClawVer    string             `yaml:"min_openclaw_version"`
	Image             string             `yaml:"image"`
	InferenceProfiles []InferenceProfile `yaml:"inference_profiles"`
	Policy            string             `yaml:"policy"`
}

type InferenceProfile struct {
	Name     string                 `yaml:"name"`
	Provider string                 `yaml:"provider"`
	Config   map[string]interface{} `yaml:"config,omitempty"`
}

// buildBlueprint returns an error if a provider's api_key isn't a ${VAR}
// reference — blueprint.yaml is explicitly meant for NVIDIA catalog
// contributions (see Exporter's doc comment), so writing a literal secret
// into its api_key_env field (a name that implies "this is an env var name",
// not a value) would leak it into a file meant to be published.
func buildBlueprint(cfg *config.Config, agent config.AgentDefinition) (*Blueprint, error) {
	bp := &Blueprint{
		Version:         "1.0",
		MinOpenShellVer: "0.1.0",
		MinOpenClawVer:  "0.5.0",
		Image:           "ghcr.io/nvidia/openshell-community/sandboxes/openclaw:latest",
		Policy:          "sandbox-policy.yaml",
	}

	// Inference profiles from providers
	for name, pDef := range cfg.Settings.Providers {
		pType := pDef.Type
		if pType == "" {
			pType = name
		}
		profile := InferenceProfile{
			Name:     name,
			Provider: pType,
		}
		if pDef.BaseURL != "" {
			profile.Config = map[string]interface{}{
				"base_url": pDef.BaseURL,
			}
		}
		if pDef.APIKey != "" {
			if !shared.IsEnvRef(pDef.APIKey) {
				return nil, fmt.Errorf("provider %q: api_key must be an ${VAR} reference to include it in blueprint.yaml, got a literal value", name)
			}
			if profile.Config == nil {
				profile.Config = map[string]interface{}{}
			}
			profile.Config["api_key_env"] = shared.EnvVarName(pDef.APIKey)
		}
		bp.InferenceProfiles = append(bp.InferenceProfiles, profile)
	}

	return bp, nil
}
