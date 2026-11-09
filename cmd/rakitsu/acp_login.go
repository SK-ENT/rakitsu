package main

import (
	"github.com/SK-ENT/rakitsu/internal/config"
)

const acpKeyringService = "rakitsu-acp"

func acpDefaultConfig(key string) *config.Config {
	return &config.Config{
		Name:     "Rakitsu ACP",
		Settings: config.Settings{DefaultProvider: "openai", APIKeys: map[string]string{"openai": key}, BaseURLs: map[string]string{"openai": acpOpenAIBaseURL}},
		Agents: []config.AgentDefinition{{
			Name:         "rakitsu",
			Role:         "worker",
			Provider:     "openai",
			Model:        "gpt-5.6-luna",
			ModelConfig:  &config.ModelConfig{ReasoningEffort: "none"},
			SystemPrompt: "You are a helpful assistant.",
		}},
	}
}
