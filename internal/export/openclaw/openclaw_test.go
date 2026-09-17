package openclaw_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/openclaw"
)

func singleAgentConfig() *config.Config {
	return &config.Config{
		Name: "Chat Bot",
		Settings: config.Settings{
			DefaultProvider: "anthropic",
			Providers: map[string]config.ProviderDefinition{
				"anthropic": {
					Type:   "anthropic",
					APIKey: "${ANTHROPIC_API_KEY}",
				},
			},
			Defaults: config.DefaultSettings{
				Model: "claude-sonnet-4-6",
			},
		},
		Agents: []config.AgentDefinition{
			{
				Name:         "Assistant",
				Role:         "worker",
				Provider:     "anthropic",
				Model:        "claude-sonnet-4-6",
				SystemPrompt: "You are a helpful assistant.",
			},
		},
	}
}

func multiAgentConfig() *config.Config {
	return &config.Config{
		Name: "Test Project",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {
					Type:   "openai",
					APIKey: "${OPENAI_API_KEY}",
				},
				"anthropic": {
					Type:   "anthropic",
					APIKey: "${ANTHROPIC_API_KEY}",
				},
			},
			Defaults: config.DefaultSettings{
				Model:       "gpt-4o-mini",
				Temperature: 0.7,
				MaxTokens:   4096,
			},
		},
		Tools: []config.ToolDefinition{
			{Name: "read_file", Type: "fs", Description: "Read a file", AllowedPaths: []string{"/workspace"}},
			{Name: "run_command", Type: "cli", Description: "Run a shell command"},
			{Name: "write_file", Type: "fs", Description: "Write a file"},
		},
		Agents: []config.AgentDefinition{
			{
				Name:         "Researcher",
				Role:         "worker",
				Provider:     "openai",
				Model:        "gpt-4o",
				SystemPrompt: "You are a research assistant.",
				Tools:        []string{"read_file"},
			},
			{
				Name:         "Developer",
				Role:         "worker",
				Provider:     "openai",
				Model:        "gpt-4o-mini",
				SystemPrompt: "You are a software developer.",
				Tools:        []string{"read_file", "write_file", "run_command"},
				ModelConfig: &config.ModelConfig{
					Temperature: 0.3,
					MaxTokens:   8192,
				},
			},
		},
	}
}

func TestExport_SingleAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := singleAgentConfig()

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	if err != nil {
		t.Fatalf("Cannot read openclaw.json: %v", err)
	}

	var oc openclaw.Config
	if err := json.Unmarshal(data, &oc); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if len(oc.Agents.List) != 1 {
		t.Fatalf("agents.list len = %d, want 1", len(oc.Agents.List))
	}
	if oc.Agents.List[0].Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("agent model = %q, want %q", oc.Agents.List[0].Model, "anthropic/claude-sonnet-4-6")
	}
	if oc.Agents.Defaults == nil || oc.Agents.Defaults.Model == nil {
		t.Fatal("agents.defaults.model is nil")
	}
	if oc.Agents.Defaults.Model.Primary != "anthropic/claude-sonnet-4-6" {
		t.Errorf("primary model = %q, want %q", oc.Agents.Defaults.Model.Primary, "anthropic/claude-sonnet-4-6")
	}
	if oc.Models == nil || oc.Models.Providers == nil {
		t.Fatal("models.providers is nil")
	}
	if _, ok := oc.Models.Providers["anthropic"]; !ok {
		t.Error("missing anthropic provider in models.providers map")
	}
}

func TestExport_MultiAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	if err != nil {
		t.Fatalf("Cannot read openclaw.json: %v", err)
	}

	var oc openclaw.Config
	if err := json.Unmarshal(data, &oc); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if len(oc.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(oc.Agents.List))
	}
	if oc.Agents.List[0].Name != "Researcher" {
		t.Errorf("first agent = %q, want %q", oc.Agents.List[0].Name, "Researcher")
	}

	devTools := oc.Agents.List[1].Tools
	if devTools == nil {
		t.Fatal("developer tools is nil")
	}
	if len(devTools.Allow) != 2 {
		t.Errorf("developer tools.allow len = %d, want 2 (group:fs, exec)", len(devTools.Allow))
	}

	if len(oc.Models.Providers) != 2 {
		t.Errorf("providers count = %d, want 2", len(oc.Models.Providers))
	}

	if op, ok := oc.Models.Providers["openai"]; ok {
		if op.API != "openai-completions" {
			t.Errorf("openai api = %q, want %q", op.API, "openai-completions")
		}
	}

	if ap, ok := oc.Models.Providers["anthropic"]; ok {
		if ap.API != "" {
			t.Errorf("anthropic api = %q, want empty", ap.API)
		}
	}
}

// Regression: buildAgents unconditionally emitted agents.defaults.model.primary
// from the global default model, but buildModels only populates a provider's
// models array from models actually resolved per-agent. If every agent on the
// default provider has its own explicit override, the emitted default used to
// reference a model ID that appeared nowhere in the provider's own catalog.
func TestExport_DefaultModelAlwaysInProviderCatalog(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Name: "Dangling Default Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"}},
			Defaults:        config.DefaultSettings{Model: "gpt-4o-mini"}, // never used below
		},
		Agents: []config.AgentDefinition{
			{Name: "A", Role: "worker", Provider: "openai", Model: "gpt-4o"},
			{Name: "B", Role: "worker", Provider: "openai", Model: "gpt-4.1"},
		},
	}

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oc openclaw.Config
	if err := json.Unmarshal(data, &oc); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if oc.Agents.Defaults == nil || oc.Agents.Defaults.Model == nil {
		t.Fatal("agents.defaults.model is nil")
	}
	primary := oc.Agents.Defaults.Model.Primary
	op, ok := oc.Models.Providers["openai"]
	if !ok {
		t.Fatal("missing openai provider")
	}
	found := false
	for _, m := range op.Models {
		if "openai/"+m.ID == primary {
			found = true
		}
	}
	if !found {
		t.Errorf("defaults.model.primary = %q does not appear in models.providers.openai.models: %+v", primary, op.Models)
	}
}

// Regression: oa.ID = shared.SanitizeID(agent.Name) had no uniqueness check.
// Config validation only rejects exact-string duplicate names; SanitizeID is
// not collision-resistant ("Data Analyst" and "DataAnalyst" both sanitize to
// "data-analyst"), so two distinct, validation-passing agents could silently
// end up with the same exported id.
func TestExport_RejectsSanitizedIDCollision(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()
	cfg.Agents[0].Name = "Data Analyst"
	cfg.Agents[1].Name = "DataAnalyst"

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err == nil {
		t.Fatal("expected an error when two agent names sanitize to the same id")
	}
}

// Regression: agent.Settings.Reflection.Enabled was only ever mapped to the
// shared agents.Defaults.Thinking, and only for agent index 0 -- there was no
// per-agent field, so a later agent's own reflection setting was silently
// dropped.
func TestExport_PerAgentThinking(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()
	cfg.Agents[0].Settings = nil // index 0: reflection disabled
	cfg.Agents[1].Settings = &config.AgentSettings{Reflection: config.ReflectionConfig{Enabled: true}}

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oc openclaw.Config
	if err := json.Unmarshal(data, &oc); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if len(oc.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(oc.Agents.List))
	}
	if oc.Agents.List[0].Thinking != nil {
		t.Error("agent 0 (reflection disabled) should have no thinking field")
	}
	if oc.Agents.List[1].Thinking == nil || !oc.Agents.List[1].Thinking.Enabled {
		t.Error("agent 1's own reflection setting was dropped — thinking should be enabled")
	}
}

func TestExport_NoAgents(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Name: "Empty"}

	exp := &openclaw.Exporter{}
	err := exp.Export(cfg, "", dir)
	if err == nil {
		t.Fatal("expected error for empty agents, got nil")
	}
}

func TestExport_CustomProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Name: "Custom",
		Settings: config.Settings{
			DefaultProvider: "dgx",
			Providers: map[string]config.ProviderDefinition{
				"dgx": {
					Type:    "litellm",
					APIKey:  "${LITELLM_API_KEY}",
					BaseURL: "https://my-dgx:4443/v1",
				},
			},
			Defaults: config.DefaultSettings{Model: "vllm-nemotron-elastic-30b"},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker", Provider: "dgx", Model: "vllm-nemotron-elastic-30b"},
		},
	}

	exp := &openclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	var oc openclaw.Config
	json.Unmarshal(data, &oc)

	dgx := oc.Models.Providers["dgx"]
	if dgx == nil {
		t.Fatal("missing dgx provider")
	}
	if dgx.BaseURL != "https://my-dgx:4443/v1" {
		t.Errorf("baseUrl = %q", dgx.BaseURL)
	}
	if dgx.API != "openai-completions" {
		t.Errorf("api = %q, want openai-completions", dgx.API)
	}
	if len(dgx.Models) != 1 || dgx.Models[0].ID != "vllm-nemotron-elastic-30b" {
		t.Errorf("models = %v", dgx.Models)
	}
}

func TestMapProviderAPI(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"openai", "openai-completions"},
		{"litellm", "openai-completions"},
		{"ollama", "openai-completions"},
		{"anthropic", ""},
		{"gemini", ""},
	}
	for _, tt := range tests {
		got := openclaw.MapProviderAPI(tt.input)
		if got != tt.want {
			t.Errorf("MapProviderAPI(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
