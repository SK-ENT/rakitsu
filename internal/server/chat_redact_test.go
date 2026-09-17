package server

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// Resume loads a persisted YAML whose api_key fields were redacted before
// write. The mask must be replaced by per-request env_vars or the LLM
// client 401s. See [[gotcha:rakitsu-redaction-breaks-resume-2026-05-21]].
func TestApplyRedactedKeyOverrides_RestoresProviderKeys(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{
			Providers: map[string]config.ProviderDefinition{
				"litellm":       {Type: "openai", APIKey: "[REDACTED]", BaseURL: "https://example.com/v1"},
				"openai-direct": {Type: "openai", APIKey: "[REDACTED]"},
				"no-auth":       {Type: "ollama", APIKey: ""},
				"real":          {Type: "anthropic", APIKey: "sk-real-already"},
			},
			APIKeys: map[string]string{
				"openai":    "[REDACTED]",
				"anthropic": "sk-still-here",
			},
		},
	}
	envVars := map[string]string{
		"LITELLM_API_KEY":       "sk-litellm-1234",
		"OPENAI_DIRECT_API_KEY": "sk-openai-direct-5678",
		"OPENAI_API_KEY":        "sk-openai-flat",
		"UNUSED_API_KEY":        "sk-noise",
	}

	applyRedactedKeyOverrides(cfg, envVars)

	if got := cfg.Settings.Providers["litellm"].APIKey; got != "sk-litellm-1234" {
		t.Errorf("litellm: want sk-litellm-1234, got %q", got)
	}
	if got := cfg.Settings.Providers["openai-direct"].APIKey; got != "sk-openai-direct-5678" {
		t.Errorf("openai-direct (hyphen normalization): want sk-openai-direct-5678, got %q", got)
	}
	if got := cfg.Settings.Providers["no-auth"].APIKey; got != "" {
		t.Errorf("no-auth: empty key should stay empty, got %q", got)
	}
	if got := cfg.Settings.Providers["real"].APIKey; got != "sk-real-already" {
		t.Errorf("real: non-redacted key must not be touched, got %q", got)
	}
	if got := cfg.Settings.APIKeys["openai"]; got != "sk-openai-flat" {
		t.Errorf("flat openai: want sk-openai-flat, got %q", got)
	}
	if got := cfg.Settings.APIKeys["anthropic"]; got != "sk-still-here" {
		t.Errorf("flat anthropic: non-redacted key must not be touched, got %q", got)
	}
}

func TestApplyRedactedKeyOverrides_NoMatchLeavesMask(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{
			Providers: map[string]config.ProviderDefinition{
				"litellm": {Type: "openai", APIKey: "[REDACTED]"},
			},
		},
	}
	// envVars present but missing the LITELLM_API_KEY the loaded config needs.
	applyRedactedKeyOverrides(cfg, map[string]string{"SOMETHING_ELSE": "x"})
	if got := cfg.Settings.Providers["litellm"].APIKey; got != "[REDACTED]" {
		t.Errorf("missing override: mask should remain (so the downstream LLM error is loud), got %q", got)
	}
}

func TestApplyRedactedKeyOverrides_NilSafe(t *testing.T) {
	// Must not panic on either nil cfg or empty envVars (Builder upload path
	// of a fresh non-redacted config calls this unconditionally too).
	applyRedactedKeyOverrides(nil, map[string]string{"X": "y"})
	cfg := &config.Config{Settings: config.Settings{Providers: map[string]config.ProviderDefinition{
		"litellm": {APIKey: "[REDACTED]"},
	}}}
	applyRedactedKeyOverrides(cfg, nil)
	if got := cfg.Settings.Providers["litellm"].APIKey; got != "[REDACTED]" {
		t.Errorf("empty envVars: mask should remain, got %q", got)
	}
}
