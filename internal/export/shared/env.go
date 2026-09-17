package shared

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/config"
	"gopkg.in/yaml.v3"
)

// EnvVarPattern matches ${VAR_NAME} references.
var EnvVarPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// EnvVarNamePattern validates that a string is a valid environment variable name.
var EnvVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsEnvRef returns true if the string is an env var reference like ${VAR}.
func IsEnvRef(s string) bool {
	return EnvVarPattern.MatchString(s)
}

// EnvVarName extracts the env var name from "${VAR}" format, or returns as-is.
// Validates that the extracted name contains only valid env var characters
// to prevent injection of shell metacharacters into exported configs.
func EnvVarName(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		name := s[2 : len(s)-1]
		if EnvVarNamePattern.MatchString(name) {
			return name
		}
		return s // invalid name, return original string
	}
	return s
}

// RawProviderEnvVars reads the original YAML to extract un-expanded ${VAR} references
// for api_key and base_url fields. Returns map[providerName]ProviderDefinition with raw values.
// Returns an error if configPath is set but the file cannot be read or parsed,
// since silent failure here would leak resolved API keys into exported files.
func RawProviderEnvVars(configPath string) (map[string]config.ProviderDefinition, error) {
	if configPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config for env var restoration: %w", err)
	}
	var raw struct {
		Settings struct {
			Providers map[string]config.ProviderDefinition `yaml:"providers"`
		} `yaml:"settings"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config for env var restoration: %w", err)
	}
	return raw.Settings.Providers, nil
}

// RestoreEnvRefs replaces resolved values with original ${VAR} references from raw YAML.
// configPath is passed through to RawProviderEnvVars; errors are propagated to prevent
// silent secret leakage in exported files.
func RestoreEnvRefs(cfg *config.Config, configPath string) error {
	rawProviders, err := RawProviderEnvVars(configPath)
	if err != nil {
		return err
	}
	if rawProviders == nil {
		return nil
	}
	for name, rawPD := range rawProviders {
		// Viper always lowercases provider map keys (see findProvider in
		// internal/config/config.go), but this function's own yaml.Unmarshal
		// above preserves the raw file's literal casing. Try the exact key
		// first, then fall back to lowercase, so a provider block like
		// "DGX:" in the source YAML still matches cfg.Settings.Providers'
		// "dgx" key instead of silently skipping restoration.
		key := name
		pd, ok := cfg.Settings.Providers[key]
		if !ok {
			key = strings.ToLower(name)
			pd, ok = cfg.Settings.Providers[key]
			if !ok {
				continue
			}
		}
		if IsEnvRef(rawPD.APIKey) {
			pd.APIKey = rawPD.APIKey
		}
		if IsEnvRef(rawPD.BaseURL) {
			pd.BaseURL = rawPD.BaseURL
		}
		cfg.Settings.Providers[key] = pd
	}
	return nil
}
