package nemoclaw_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/nemoclaw"
	"github.com/SK-ENT/rakitsu/internal/export/openclaw"
	"gopkg.in/yaml.v3"
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
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Agents:   []string{"Researcher", "Developer"},
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{Name: "research", Agent: "Researcher"},
					{Name: "develop", Agent: "Developer"},
				},
			},
		},
	}
}

// --- NemoClaw exporter tests ---

func TestExport_SingleAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := singleAgentConfig()

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Default export: no blueprint.yaml
	for _, name := range []string{"openclaw.json", "sandbox-policy.yaml", "AGENT.md", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing file: %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "blueprint.yaml")); err == nil {
		t.Error("blueprint.yaml should NOT be in default export")
	}

	// Verify AGENT.md
	agentMD, _ := os.ReadFile(filepath.Join(dir, "AGENT.md"))
	if string(agentMD) != "You are a helpful assistant.\n" {
		t.Errorf("AGENT.md = %q, want system prompt", string(agentMD))
	}

	// Verify openclaw.json uses correct schema
	ocData, _ := os.ReadFile(filepath.Join(dir, "openclaw.json"))
	var oc openclaw.Config
	if err := json.Unmarshal(ocData, &oc); err != nil {
		t.Fatalf("Invalid openclaw.json: %v", err)
	}
	if oc.Models == nil || oc.Models.Providers == nil {
		t.Fatal("providers should be a map")
	}
}

func TestExport_WithBlueprint(t *testing.T) {
	dir := t.TempDir()
	cfg := singleAgentConfig()

	exp := &nemoclaw.Exporter{}
	exp.SetOptions(nemoclaw.Options{WithBlueprint: true})
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "blueprint.yaml")); err != nil {
		t.Fatal("blueprint.yaml missing when --with-blueprint set")
	}

	data, err := os.ReadFile(filepath.Join(dir, "blueprint.yaml"))
	if err != nil {
		t.Fatalf("Cannot read blueprint.yaml: %v", err)
	}
	var bp nemoclaw.Blueprint
	if err := yaml.Unmarshal(data, &bp); err != nil {
		t.Fatalf("Invalid YAML: %v", err)
	}
	if bp.Version != "1.0" {
		t.Errorf("blueprint version = %q, want %q", bp.Version, "1.0")
	}
	if len(bp.InferenceProfiles) != 1 {
		t.Errorf("inference profiles = %d, want 1", len(bp.InferenceProfiles))
	}
}

// Regression: buildBlueprint used to write a provider's api_key verbatim
// into blueprint.yaml's api_key_env field whenever it wasn't a ${VAR}
// reference — a field name that implies "this is an env var name," not a
// value, in a file explicitly meant for NVIDIA catalog contributions.
func TestExport_WithBlueprint_RejectsLiteralAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfg := singleAgentConfig()
	cfg.Settings.Providers["anthropic"] = config.ProviderDefinition{
		Type:   "anthropic",
		APIKey: "sk-ant-literal-secret-not-a-var-ref",
	}

	exp := &nemoclaw.Exporter{}
	exp.SetOptions(nemoclaw.Options{WithBlueprint: true})
	err := exp.Export(cfg, "", dir)
	if err == nil {
		t.Fatal("expected an error when a provider's api_key is a literal value, not a ${VAR} reference")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "blueprint.yaml")); statErr == nil {
		t.Error("blueprint.yaml should not exist after a rejected export — the literal secret must not reach disk")
	}
}

func TestExport_MultiAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	for _, name := range []string{"agent-researcher", "agent-developer"} {
		agentDir := filepath.Join(dir, name)
		info, err := os.Stat(agentDir)
		if err != nil {
			t.Fatalf("missing agent dir: %s", name)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", name)
		}
		for _, f := range []string{"openclaw.json", "sandbox-policy.yaml"} {
			if _, err := os.Stat(filepath.Join(agentDir, f)); err != nil {
				t.Errorf("missing %s/%s", name, f)
			}
		}
		if _, err := os.Stat(filepath.Join(agentDir, "blueprint.yaml")); err == nil {
			t.Errorf("%s/blueprint.yaml should not exist in default export", name)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Error("missing orchestrator README.md")
	}

	readmeData, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	readme := string(readmeData)
	if !strings.Contains(readme, "Pipeline") {
		t.Error("README should mention Pipeline strategy")
	}
}

// Regression: exportMulti used to build each agent's directory name via
// shared.SanitizeID with no collision check. Config validation only rejects
// exact-string duplicate agent names, but SanitizeID normalizes differently
// -- "Code Reviewer" and "Code-Reviewer" both sanitize to "code-reviewer" --
// so the second agent's exportSingle call would silently overwrite the
// first agent's already-written files in the same directory.
func TestExport_MultiAgent_RejectsSanitizedNameCollision(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()
	cfg.Agents[0].Name = "Code Reviewer"
	cfg.Agents[1].Name = "Code-Reviewer"

	exp := &nemoclaw.Exporter{}
	err := exp.Export(cfg, "", dir)
	if err == nil {
		t.Fatal("expected an error when two agent names sanitize to the same directory name")
	}

	// The first agent legitimately wrote its files before the collision was
	// detected on the second — that's expected. What must not happen is the
	// second agent's export silently succeeding and overwriting them with
	// its own system prompt/config.
	agentMD := filepath.Join(dir, "agent-code-reviewer", "AGENT.md")
	data, readErr := os.ReadFile(agentMD)
	if readErr != nil {
		t.Fatalf("first agent's AGENT.md should exist (it exported before the collision was caught): %v", readErr)
	}
	if !strings.Contains(string(data), cfg.Agents[0].SystemPrompt) {
		t.Errorf("AGENT.md should still hold the FIRST agent's system prompt, not have been overwritten: %s", data)
	}
}

func TestExport_ReAct_DeploysSupervisorAsOwnAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()
	cfg.Orchestrator = &config.OrchestratorConfig{
		Name:         "Supervisor",
		Strategy:     "ReAct",
		Provider:     "openai",
		Model:        "gpt-4o",
		SystemPrompt: "You are the supervisor.",
		Agents:       []string{"Researcher", "Developer"},
	}

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "agent-supervisor", "openclaw.json")); err != nil {
		t.Errorf("expected the supervisor's own openclaw.json to be exported: %v", err)
	}

	compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "agent-supervisor:") {
		t.Error("docker-compose.yaml should include a service for the supervisor")
	}

	script, err := os.ReadFile(filepath.Join(dir, "orchestrate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "agent-supervisor") {
		t.Error("orchestrate.sh's setup-commands reminder should mention the supervisor's own sandbox")
	}
}

func TestExport_ReAct_SupervisorNameCollidesWithAgent_Errors(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig()
	cfg.Orchestrator = &config.OrchestratorConfig{
		Name:     "Researcher", // collides with the existing worker agent's directory
		Strategy: "ReAct",
		Agents:   []string{"Researcher", "Developer"},
	}

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err == nil {
		t.Fatal("expected an error when the supervisor's name collides with an existing agent's directory")
	}
}

func TestExport_Pipeline_DoesNotDeploySupervisorAsAgent(t *testing.T) {
	dir := t.TempDir()
	cfg := multiAgentConfig() // Strategy: "Pipeline"

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "agent-lead")); err == nil {
		t.Error("Pipeline strategy should not export the orchestrator as its own agent directory")
	}
}

// --- Policy tests ---

func TestPolicy_FilesystemPaths(t *testing.T) {
	cfg := multiAgentConfig()
	singleCfg := nemoclaw.SingleAgentConfig(cfg, cfg.Agents[0])
	policy, err := nemoclaw.BuildPolicy(singleCfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	found := false
	for _, p := range policy.FilesystemPolicy.ReadWrite {
		if p == "/workspace" {
			found = true
			break
		}
	}
	if !found {
		t.Error("policy should include /workspace in read_write from tool allowed_paths")
	}
}

func TestPolicy_NetworkPolicies(t *testing.T) {
	cfg := multiAgentConfig()
	singleCfg := nemoclaw.SingleAgentConfig(cfg, cfg.Agents[0])
	policy, err := nemoclaw.BuildPolicy(singleCfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	if len(policy.NetworkPolicies) < 2 {
		t.Fatalf("expected at least 2 network policies, got %d", len(policy.NetworkPolicies))
	}

	inferencePolicy, ok := policy.NetworkPolicies["inference"]
	if !ok {
		t.Fatal("missing inference network policy")
	}
	if len(inferencePolicy.Endpoints) < 1 {
		t.Errorf("inference endpoints = %d, want at least 1", len(inferencePolicy.Endpoints))
	}

	if _, ok := policy.NetworkPolicies["openclaw_api"]; !ok {
		t.Error("missing openclaw_api network policy")
	}
}

// Regression: SingleAgentConfig used to copy Settings.Providers verbatim,
// so every provider declared anywhere in a multi-agent config leaked a
// network-egress endpoint into every single agent's exported policy, even
// providers that agent never uses. multiAgentConfig() declares both
// "openai" and "anthropic", but both its agents only use "openai".
func TestPolicy_NetworkPolicies_FiltersToAgentsOwnProvider(t *testing.T) {
	cfg := multiAgentConfig()
	singleCfg := nemoclaw.SingleAgentConfig(cfg, cfg.Agents[0])

	if _, ok := singleCfg.Settings.Providers["anthropic"]; ok {
		t.Error("anthropic should be filtered out — this agent's Provider is \"openai\"")
	}
	if _, ok := singleCfg.Settings.Providers["openai"]; !ok {
		t.Error("openai (this agent's actual provider) should be present")
	}

	policy, err := nemoclaw.BuildPolicy(singleCfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	inferencePolicy := policy.NetworkPolicies["inference"]
	if inferencePolicy == nil {
		t.Fatal("missing inference network policy")
	}
	for _, ep := range inferencePolicy.Endpoints {
		if ep.Host == "api.anthropic.com" {
			t.Error("agent's sandbox-policy.yaml grants network access to a provider it never uses (anthropic)")
		}
	}
}

func TestPolicy_NonStandardPort(t *testing.T) {
	cfg := &config.Config{
		Name: "Port Test",
		Settings: config.Settings{
			DefaultProvider: "custom",
			Providers: map[string]config.ProviderDefinition{
				"custom": {
					Type:    "litellm",
					BaseURL: "https://llm.example.com:4443/v1",
					APIKey:  "dummy",
				},
			},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker", Provider: "custom", Model: "test"},
		},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	inferencePolicy := policy.NetworkPolicies["inference"]
	if inferencePolicy == nil || len(inferencePolicy.Endpoints) == 0 {
		t.Fatal("missing inference endpoints")
	}
	ep := inferencePolicy.Endpoints[0]
	if ep.Port != 4443 {
		t.Errorf("port = %d, want 4443", ep.Port)
	}
	if ep.Host != "llm.example.com" {
		t.Errorf("host = %q, want llm.example.com", ep.Host)
	}
}

func TestPolicy_EnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_LLM_URL", "https://inference.example.com:4443/v1")

	cfg := &config.Config{
		Name: "Env Test",
		Settings: config.Settings{
			DefaultProvider: "custom",
			Providers: map[string]config.ProviderDefinition{
				"custom": {
					Type:    "litellm",
					BaseURL: "${TEST_LLM_URL}",
					APIKey:  "${TEST_API_KEY}",
				},
			},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker", Provider: "custom", Model: "test-model"},
		},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	inferencePolicy, ok := policy.NetworkPolicies["inference"]
	if !ok {
		t.Fatal("missing inference policy")
	}

	if len(inferencePolicy.Endpoints) == 0 {
		t.Fatal("no endpoints in inference policy")
	}

	host := inferencePolicy.Endpoints[0].Host
	if host == "${TEST_LLM_URL}" {
		t.Errorf("host = %q — env var was not expanded (OpenShell would reject this)", host)
	}
	if host != "inference.example.com" {
		t.Errorf("host = %q, want %q", host, "inference.example.com")
	}
}

func TestPolicy_EmptyHostSkipped(t *testing.T) {
	cfg := &config.Config{
		Name: "Empty Host Test",
		Settings: config.Settings{
			DefaultProvider: "custom",
			Providers: map[string]config.ProviderDefinition{
				"custom": {
					Type:   "unknown-provider-type",
					APIKey: "dummy",
				},
			},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker", Provider: "custom", Model: "test"},
		},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if _, ok := policy.NetworkPolicies["inference"]; ok {
		t.Error("inference policy should not exist for unknown provider with no BaseURL")
	}
}

func TestPolicy_ToolURLPort(t *testing.T) {
	cfg := &config.Config{
		Name: "Tool Port Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {Type: "openai", APIKey: "dummy"},
			},
		},
		Tools: []config.ToolDefinition{
			{
				Name: "my-mcp",
				Type: "mcp_server",
				URL:  "http://localhost:3000/api",
			},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker"},
		},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	toolPolicy, ok := policy.NetworkPolicies["tool_my-mcp"]
	if !ok {
		t.Fatal("missing tool_my-mcp network policy")
	}
	ep := toolPolicy.Endpoints[0]
	if ep.Port != 3000 {
		t.Errorf("port = %d, want 3000", ep.Port)
	}
	if ep.Host != "localhost" {
		t.Errorf("host = %q, want localhost", ep.Host)
	}
	if ep.TLS != "" {
		t.Errorf("TLS = %q, want empty for http:// URL", ep.TLS)
	}
}

// Regression: an mcp_server tool URL with no explicit port always defaulted
// to 443 regardless of scheme, while the TLS flag two lines away was
// correctly scheme-aware -- an ordinary plain-HTTP MCP endpoint (port 80 by
// convention) got an inconsistent "port: 443, tls: ”" policy entry.
func TestPolicy_ToolURLPort_HTTPNoPortDefaultsTo80(t *testing.T) {
	cfg := &config.Config{
		Name: "Tool Port Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "dummy"}},
		},
		Tools: []config.ToolDefinition{
			{Name: "internal-mcp", Type: "mcp_server", URL: "http://internal-mcp.local/mcp"},
		},
		Agents: []config.AgentDefinition{{Name: "Bot", Role: "worker"}},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	toolPolicy, ok := policy.NetworkPolicies["tool_internal-mcp"]
	if !ok {
		t.Fatal("missing tool_internal-mcp network policy")
	}
	ep := toolPolicy.Endpoints[0]
	if ep.Port != 80 {
		t.Errorf("port = %d, want 80 for a plain-HTTP URL with no explicit port", ep.Port)
	}
	if ep.TLS != "" {
		t.Errorf("TLS = %q, want empty — inconsistent with port 80 otherwise", ep.TLS)
	}
}

// Regression: providerEndpointForType defaulted a no-port http:// baseURL to
// port 80, but never reset TLS from its "terminate" initial value, producing
// an internally inconsistent "port: 80, tls: terminate" policy entry.
func TestPolicy_ProviderEndpoint_HTTPNoPort_TLSReset(t *testing.T) {
	cfg := &config.Config{
		Name: "Provider TLS Test",
		Settings: config.Settings{
			DefaultProvider: "custom",
			Providers: map[string]config.ProviderDefinition{
				"custom": {Type: "litellm", BaseURL: "http://internal-llm.local"},
			},
		},
		Agents: []config.AgentDefinition{{Name: "Bot", Role: "worker", Provider: "custom", Model: "test"}},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	inferencePolicy := policy.NetworkPolicies["inference"]
	if inferencePolicy == nil || len(inferencePolicy.Endpoints) == 0 {
		t.Fatal("missing inference endpoint")
	}
	ep := inferencePolicy.Endpoints[0]
	if ep.Port != 80 {
		t.Errorf("port = %d, want 80", ep.Port)
	}
	if ep.TLS != "" {
		t.Errorf("TLS = %q, want empty — port 80 with tls:terminate is internally inconsistent", ep.TLS)
	}
}

// Regression: TLS was only reset to "" in the no-explicit-port branch, so a
// plaintext base URL WITH an explicit port (e.g. a local dev proxy on 8080)
// kept the default tls: terminate — mislabeling a plaintext endpoint as
// TLS-terminated in the generated sandbox policy.
func TestPolicy_ProviderEndpoint_HTTPExplicitPort_TLSReset(t *testing.T) {
	cfg := &config.Config{
		Name: "Provider TLS Explicit Port Test",
		Settings: config.Settings{
			DefaultProvider: "custom",
			Providers: map[string]config.ProviderDefinition{
				"custom": {Type: "litellm", BaseURL: "http://internal-llm.local:8080"},
			},
		},
		Agents: []config.AgentDefinition{{Name: "Bot", Role: "worker", Provider: "custom", Model: "test"}},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	inferencePolicy := policy.NetworkPolicies["inference"]
	if inferencePolicy == nil || len(inferencePolicy.Endpoints) == 0 {
		t.Fatal("missing inference endpoint")
	}
	ep := inferencePolicy.Endpoints[0]
	if ep.Port != 8080 {
		t.Errorf("port = %d, want 8080 (the explicit port)", ep.Port)
	}
	if ep.TLS != "" {
		t.Errorf("TLS = %q, want empty — plaintext http:// with an explicit port must not claim tls: terminate", ep.TLS)
	}
}

// Regression: two tools whose names sanitize to the same key used to
// silently overwrite each other's network policy entry in the returned
// map, with no error or warning -- the earlier tool's egress endpoint just
// disappears from the generated policy.
func TestPolicy_BuildNetworkPolicies_RejectsToolKeyCollision(t *testing.T) {
	cfg := &config.Config{
		Name: "Collision Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "dummy"}},
		},
		Tools: []config.ToolDefinition{
			{Name: "My MCP", Type: "mcp_server", URL: "http://a.example.com:8080/mcp"},
			{Name: "My-MCP", Type: "mcp_server", URL: "http://b.example.com:8080/mcp"},
		},
		Agents: []config.AgentDefinition{{Name: "Bot", Role: "worker"}},
	}

	if _, err := nemoclaw.BuildPolicy(cfg); err == nil {
		t.Fatal("expected an error when two tool names sanitize to the same network-policy key")
	}
}

func TestPolicy_PathCanonicalization(t *testing.T) {
	cfg := &config.Config{
		Name: "Path Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {Type: "openai", APIKey: "dummy"},
			},
		},
		Tools: []config.ToolDefinition{
			{
				Name:         "reader",
				Type:         "fs",
				AllowedPaths: []string{"/workspace/../etc", "/tmp//data"},
			},
		},
		Agents: []config.AgentDefinition{
			{Name: "Bot", Role: "worker"},
		},
	}

	policy, err := nemoclaw.BuildPolicy(cfg)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	for _, p := range policy.FilesystemPolicy.ReadWrite {
		if p == "/workspace/../etc" {
			t.Error("path /workspace/../etc was not canonicalized (should be /etc)")
		}
		if p == "/tmp//data" {
			t.Error("path /tmp//data was not canonicalized (should be /tmp/data)")
		}
	}
	foundEtc := false
	foundTmpData := false
	for _, p := range policy.FilesystemPolicy.ReadWrite {
		if p == "/etc" {
			foundEtc = true
		}
		if p == "/tmp/data" {
			foundTmpData = true
		}
	}
	if !foundEtc {
		t.Error("cleaned path /etc not found in policy")
	}
	if !foundTmpData {
		t.Error("cleaned path /tmp/data not found in policy")
	}
}

func TestRestoreEnvRefs_PropagatesError(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{
			Providers: map[string]config.ProviderDefinition{
				"openai": {APIKey: "sk-resolved-key"},
			},
		},
	}
	// Export with invalid config path should fail
	exp := &nemoclaw.Exporter{}
	err := exp.Export(cfg, "/nonexistent/path/config.yaml", t.TempDir())
	if err == nil {
		t.Fatal("expected error from Export with nonexistent config path, got nil")
	}
}

// --- OpenShellTarget tests ---

func TestOpenShellTarget_RunAgentCase(t *testing.T) {
	ot := &nemoclaw.OpenShellTarget{}
	cas := ot.RunAgentCase()
	if !strings.Contains(cas, "openshell sandbox upload") {
		t.Error("OpenShellTarget should use openshell sandbox upload")
	}
	if !strings.Contains(cas, "openshell sandbox exec --name") {
		t.Error("OpenShellTarget should use openshell sandbox exec --name to run a command non-interactively")
	}
	if !strings.Contains(cas, "openshell sandbox download") {
		t.Error("OpenShellTarget should use openshell sandbox download")
	}
	if !strings.Contains(cas, "openclaw agent") {
		t.Error("OpenShellTarget should use openclaw agent command")
	}
}

// Regression: `openshell sandbox connect <name> -- <cmd>` was assumed to run a
// command non-interactively, matching this file's own header comment. Verified
// against a live sandbox (real OpenShell CLI, not a mock) that `connect` rejects
// a trailing command — it's interactive-only. `exec --name <name> -- <cmd>` is
// the actual command-execution subcommand. Confirmed end-to-end: a real
// two-step pipeline exported by Rakitsu completed successfully against live
// NemoClaw sandboxes and a real inference backend once this was fixed.
func TestOpenShellTarget_DoesNotUseConnectForCommandExecution(t *testing.T) {
	ot := &nemoclaw.OpenShellTarget{}
	for _, cas := range []string{ot.RunAgentCase(), ot.DelegateCase()} {
		if strings.Contains(cas, "sandbox connect") {
			t.Errorf("OpenShellTarget must not use sandbox connect to run a command (interactive-only, rejects a trailing command): %s", cas)
		}
	}
}

func TestOpenShellTarget_NoHallucinatedCommands(t *testing.T) {
	ot := &nemoclaw.OpenShellTarget{}
	outputs := []string{
		ot.RunAgentCase(),
		ot.SetupCommands([]string{"agent-a", "agent-b"}),
		ot.DelegateCase(),
	}
	for _, output := range outputs {
		if strings.Contains(output, "nemoclaw run") || strings.Contains(output, "nemoclaw start") {
			t.Errorf("OpenShellTarget contains hallucinated NemoClaw command: %s", output)
		}
	}
}

func TestOpenShellTarget_SetupCommands(t *testing.T) {
	ot := &nemoclaw.OpenShellTarget{}
	setup := ot.SetupCommands([]string{"researcher", "writer"})
	if !strings.Contains(setup, "nemoclaw onboard --non-interactive") {
		t.Error("setup should reference nemoclaw onboard --non-interactive")
	}
	if !strings.Contains(setup, "nemoclaw list") {
		t.Error("setup should verify sandboxes with nemoclaw list")
	}
}

func TestOpenShellTarget_DelegateCase(t *testing.T) {
	ot := &nemoclaw.OpenShellTarget{}
	del := ot.DelegateCase()
	if !strings.Contains(del, "openshell sandbox upload") {
		t.Error("delegate should upload via openshell")
	}
	if !strings.Contains(del, "openshell sandbox exec --name") {
		t.Error("delegate should run the command via openshell sandbox exec --name")
	}
	if !strings.Contains(del, "openshell sandbox download") {
		t.Error("delegate should download via openshell")
	}
}

// Regression: RunAgentCase/DelegateCase used to hardcode /sandbox/input.txt
// and /sandbox/output.txt for every invocation, so two steps sharing an
// agent's sandbox in the same parallel level would race on the same two
// remote files. This actually runs the generated fragment against a fake
// "openshell" binary (not just a substring check) to confirm the remote
// paths really are derived from the step-scoped local filenames, and that
// openshell's own stdout no longer leaks into the script's stdout.
func TestOpenShellTarget_RunAgentCase_UsesStepScopedRemotePaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "openshell.log")

	// A fake "openshell" that logs its invocation and, like the real CLI per
	// the reviewer's finding, prints unrelated noise to its own stdout.
	fakeOpenshell := "#!/bin/sh\n" +
		"echo \"openshell $*\" >> " + shellQuoteForTest(logFile) + "\n" +
		"echo \"SENTINEL_OPENSHELL_STDOUT_NOISE\"\n"
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	openshellPath := filepath.Join(binDir, "openshell")
	if err := os.WriteFile(openshellPath, []byte(fakeOpenshell), 0755); err != nil {
		t.Fatal(err)
	}

	inputFile := filepath.Join(dir, "input-research.txt")
	outputFile := filepath.Join(dir, "output-research.txt")
	if err := os.WriteFile(inputFile, []byte("task text"), 0644); err != nil {
		t.Fatal(err)
	}

	ot := &nemoclaw.OpenShellTarget{}
	harness := "#!/usr/bin/env bash\nset -euo pipefail\n" +
		"name=" + shellQuoteForTest("researcher") + "\n" +
		"input_file=" + shellQuoteForTest(inputFile) + "\n" +
		"output_file=" + shellQuoteForTest(outputFile) + "\n" +
		ot.RunAgentCase() + "\n"

	scriptPath := filepath.Join(dir, "harness.sh")
	if err := os.WriteFile(scriptPath, []byte(harness), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness script failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "SENTINEL_OPENSHELL_STDOUT_NOISE") {
		t.Error("openshell's own stdout leaked into the script's stdout — the >/dev/null redirects aren't working")
	}

	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("fake openshell was never invoked: %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "/sandbox/input-research.txt") {
		t.Errorf("upload/exec/download should use a step-scoped remote path (/sandbox/input-research.txt), got:\n%s", log)
	}
	if !strings.Contains(log, "/sandbox/output-research.txt") {
		t.Errorf("upload/exec/download should use a step-scoped remote path (/sandbox/output-research.txt), got:\n%s", log)
	}
	if strings.Contains(log, "/sandbox/input.txt") || strings.Contains(log, "/sandbox/output.txt") {
		t.Errorf("fixed, non-unique remote path is still in use — two steps sharing an agent's sandbox would race:\n%s", log)
	}
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
