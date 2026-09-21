package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestResolveFileRef_ExplicitPrefix(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("Hello from file"), 0644)

	field := "file:prompt.md"
	if err := resolveFileRef(dir, &field); err != nil {
		t.Fatal(err)
	}
	if field != "Hello from file" {
		t.Errorf("expected 'Hello from file', got %q", field)
	}
}

func TestResolveFileRef_ExplicitPrefix_MissingFile(t *testing.T) {
	dir := t.TempDir()
	field := "file:missing.md"
	err := resolveFileRef(dir, &field)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestResolveFileRef_AutoDetect(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("Auto detected"), 0644)

	field := "prompt.md"
	if err := resolveFileRef(dir, &field); err != nil {
		t.Fatal(err)
	}
	if field != "Auto detected" {
		t.Errorf("expected 'Auto detected', got %q", field)
	}
}

func TestResolveFileRef_AutoDetect_MissingFile(t *testing.T) {
	dir := t.TempDir()
	field := "missing.md"
	if err := resolveFileRef(dir, &field); err != nil {
		t.Fatal(err)
	}
	if field != "missing.md" {
		t.Errorf("expected literal 'missing.md', got %q", field)
	}
}

func TestResolveFileRef_MultilineNotAutoDetected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("file content"), 0644)

	field := "line1\nline2 prompt.md"
	if err := resolveFileRef(dir, &field); err != nil {
		t.Fatal(err)
	}
	if field != "line1\nline2 prompt.md" {
		t.Errorf("multi-line should be kept as literal, got %q", field)
	}
}

func TestResolveFileRef_InlineString(t *testing.T) {
	dir := t.TempDir()
	field := "You are a helpful assistant"
	if err := resolveFileRef(dir, &field); err != nil {
		t.Fatal(err)
	}
	if field != "You are a helpful assistant" {
		t.Errorf("plain string should be kept, got %q", field)
	}
}

func TestSplitFrontMatter(t *testing.T) {
	data := []byte("---\nname: test\nrole: worker\n---\nBody content here")
	fm, body, err := splitFrontMatter(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(fm) != "name: test\nrole: worker" {
		t.Errorf("unexpected front matter: %q", fm)
	}
	if string(body) != "Body content here" {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestSplitFrontMatter_NoFrontMatter(t *testing.T) {
	data := []byte("No front matter here")
	_, _, err := splitFrontMatter(data)
	if err == nil {
		t.Fatal("expected error for missing front matter")
	}
}

func TestParseMarkdownAgent(t *testing.T) {
	data := []byte(`---
name: "Test Agent"
role: "worker"
provider: "openai"
model: "gpt-4o"
tools:
  - "read_file"
---

You are a test agent. Do testing things.
`)
	agent, err := parseMarkdownAgent(data)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "Test Agent" {
		t.Errorf("expected name 'Test Agent', got %q", agent.Name)
	}
	if agent.Role != "worker" {
		t.Errorf("expected role 'worker', got %q", agent.Role)
	}
	if agent.SystemPrompt != "You are a test agent. Do testing things." {
		t.Errorf("unexpected system_prompt: %q", agent.SystemPrompt)
	}
	if len(agent.Tools) != 1 || agent.Tools[0] != "read_file" {
		t.Errorf("unexpected tools: %v", agent.Tools)
	}
}

func TestParseMarkdownSkill(t *testing.T) {
	data := []byte(`---
name: "review"
description: "Code review skill"
tools:
  - "read_file"
---

Review the code for bugs and issues.
`)
	skill, err := parseMarkdownSkill(data)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "review" {
		t.Errorf("expected name 'review', got %q", skill.Name)
	}
	if skill.PromptTemplate != "Review the code for bugs and issues." {
		t.Errorf("unexpected prompt_template: %q", skill.PromptTemplate)
	}
}

func TestDiscoverAgents(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentDir, 0755)

	// Write a YAML agent
	yamlAgent := `name: "YAML Agent"
role: "worker"
provider: "openai"
model: "gpt-4o"
system_prompt: "I am a YAML agent"
`
	os.WriteFile(filepath.Join(agentDir, "yaml-agent.yaml"), []byte(yamlAgent), 0644)

	// Write a markdown agent
	mdAgent := `---
name: "MD Agent"
role: "worker"
provider: "openai"
model: "gpt-4o"
---

I am a markdown agent.
`
	os.WriteFile(filepath.Join(agentDir, "md-agent.md"), []byte(mdAgent), 0644)

	cfg := &Config{}
	if err := cfg.discoverAgents(agentDir); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents))
	}

	// Check both were loaded
	names := map[string]bool{}
	for _, a := range cfg.Agents {
		names[a.Name] = true
	}
	if !names["YAML Agent"] {
		t.Error("YAML Agent not found")
	}
	if !names["MD Agent"] {
		t.Error("MD Agent not found")
	}
}

func TestDiscoverAgents_InlinePrecedence(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentDir, 0755)

	yamlAgent := `name: "Existing Agent"
role: "worker"
system_prompt: "from file"
`
	os.WriteFile(filepath.Join(agentDir, "existing.yaml"), []byte(yamlAgent), 0644)

	cfg := &Config{
		Agents: []AgentDefinition{
			{Name: "Existing Agent", Role: "supervisor", SystemPrompt: "inline"},
		},
	}
	if err := cfg.discoverAgents(agentDir); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent (inline wins), got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].SystemPrompt != "inline" {
		t.Error("inline definition should take precedence")
	}
}

func TestDiscoverAgents_NoDirectory(t *testing.T) {
	cfg := &Config{}
	if err := cfg.discoverAgents("/nonexistent/agents"); err != nil {
		t.Fatal("missing directory should not be an error")
	}
}

// TestRedacted covers B33: resolved api_key values must not leak into
// session snapshots persisted at ~/.rakitsu/sessions/<id>.jsonl.
func TestRedacted(t *testing.T) {
	cfg := &Config{
		Name: "test",
		Settings: Settings{
			Providers: map[string]ProviderDefinition{
				"litellm": {
					Type:    "litellm",
					APIKey:  "sk-realkey-1234",
					BaseURL: "https://example.com/v1",
				},
				"openai-direct": {
					Type:   "openai",
					APIKey: "sk-example-key-5678",
				},
				"no-auth": {
					Type: "ollama",
				},
			},
			APIKeys: map[string]string{
				"openai":    "sk-legacy-abcd",
				"anthropic": "sk-ant-9999",
				"empty":     "",
			},
			BaseURLs: map[string]string{
				"litellm": "https://example.com/v1",
			},
		},
		Tools: []ToolDefinition{
			{Name: "delegate", Type: "a2a", APIKey: "a2a-secret-global"},
			{Name: "no-key", Type: "a2a"},
		},
		Agents: []AgentDefinition{
			{
				Name: "Coordinator",
				ToolsInline: []ToolDefinition{
					{Name: "delegate_inline", Type: "a2a", APIKey: "a2a-secret-inline"},
				},
			},
		},
	}

	got := Redacted(cfg)

	if got == nil {
		t.Fatal("Redacted must not return nil")
	}
	if got == cfg {
		t.Error("Redacted must return a deep copy, not the input pointer")
	}
	if cfg.Settings.Providers["litellm"].APIKey != "sk-realkey-1234" {
		t.Error("Redacted mutated caller's config (providers)")
	}
	if cfg.Settings.APIKeys["openai"] != "sk-legacy-abcd" {
		t.Error("Redacted mutated caller's config (api_keys)")
	}

	if got.Settings.Providers["litellm"].APIKey != "[REDACTED]" {
		t.Errorf("provider litellm APIKey not redacted: %q", got.Settings.Providers["litellm"].APIKey)
	}
	if got.Settings.Providers["openai-direct"].APIKey != "[REDACTED]" {
		t.Errorf("provider openai-direct APIKey not redacted: %q", got.Settings.Providers["openai-direct"].APIKey)
	}
	if got.Settings.Providers["no-auth"].APIKey != "" {
		t.Errorf("provider no-auth APIKey should stay empty, got %q", got.Settings.Providers["no-auth"].APIKey)
	}
	if got.Settings.APIKeys["openai"] != "[REDACTED]" {
		t.Errorf("api_keys[openai] not redacted: %q", got.Settings.APIKeys["openai"])
	}
	if got.Settings.APIKeys["anthropic"] != "[REDACTED]" {
		t.Errorf("api_keys[anthropic] not redacted: %q", got.Settings.APIKeys["anthropic"])
	}
	if got.Settings.APIKeys["empty"] != "" {
		t.Errorf("api_keys[empty] should stay empty, got %q", got.Settings.APIKeys["empty"])
	}

	if got.Name != "test" {
		t.Errorf("Name lost: %q", got.Name)
	}
	if got.Settings.Providers["litellm"].BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL should not be redacted: %q", got.Settings.Providers["litellm"].BaseURL)
	}
	if got.Settings.BaseURLs["litellm"] != "https://example.com/v1" {
		t.Errorf("base_urls map lost: %q", got.Settings.BaseURLs["litellm"])
	}

	if got.Tools[0].APIKey != "[REDACTED]" {
		t.Errorf("global a2a tool api_key not redacted: %q", got.Tools[0].APIKey)
	}
	if got.Tools[1].APIKey != "" {
		t.Errorf("tool with no api_key should stay empty, got %q", got.Tools[1].APIKey)
	}
	if got.Agents[0].ToolsInline[0].APIKey != "[REDACTED]" {
		t.Errorf("inline a2a tool api_key not redacted: %q", got.Agents[0].ToolsInline[0].APIKey)
	}
	if cfg.Tools[0].APIKey != "a2a-secret-global" {
		t.Error("Redacted mutated caller's config (tools)")
	}
	if cfg.Agents[0].ToolsInline[0].APIKey != "a2a-secret-inline" {
		t.Error("Redacted mutated caller's config (agent tools_inline)")
	}
}

// TestLoad_A2AToolAPIKeyExpandsEnvVar covers issue #22 item 1: the a2a tool
// type had no credential field at all, so an a2a peer gated by
// RAKITSU_API_TOKEN was unreachable from any config. api_key needs the same
// ${VAR} expansion provider api_key values already get — both for a
// globally-declared tool and one declared inline on an agent, since
// buildAgentToolRegistry (runtime.go) constructs a2a tools from either.
func TestLoad_A2AToolAPIKeyExpandsEnvVar(t *testing.T) {
	t.Setenv("TEST_A2A_TOKEN", "resolved-secret-abc")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yamlContent := `
name: a2a-expand-test
version: "1.0"
tools:
  - name: delegate
    type: a2a
    url: http://peer.example/a2a
    agent: Researcher
    api_key: ${TEST_A2A_TOKEN}
agents:
  - name: Coordinator
    role: worker
    tools_inline:
      - name: delegate_inline
        type: a2a
        url: http://peer2.example/a2a
        agent: Researcher
        api_key: ${TEST_A2A_TOKEN}
`
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tool := cfg.GetTool("delegate")
	if tool == nil {
		t.Fatal("global tool \"delegate\" not found")
	}
	if tool.APIKey != "resolved-secret-abc" {
		t.Errorf("global tool api_key = %q, want the expanded env var value", tool.APIKey)
	}

	if len(cfg.Agents) != 1 || len(cfg.Agents[0].ToolsInline) != 1 {
		t.Fatalf("expected one agent with one inline tool, got agents=%d", len(cfg.Agents))
	}
	if got := cfg.Agents[0].ToolsInline[0].APIKey; got != "resolved-secret-abc" {
		t.Errorf("inline tool api_key = %q, want the expanded env var value", got)
	}
}

// TestLoad_JevToolAPIKeyExpandsEnvVar mirrors
// TestLoad_A2AToolAPIKeyExpandsEnvVar: the jev tool type follows the same
// api_key convention (config-expanded ${VAR}) as a2a, for both a
// globally-declared tool and one declared inline on an agent.
func TestLoad_JevToolAPIKeyExpandsEnvVar(t *testing.T) {
	t.Setenv("TEST_JEV_KEY", "resolved-jev-secret")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yamlContent := `
name: jev-expand-test
version: "1.0"
tools:
  - name: jev
    type: jev
    api_key: ${TEST_JEV_KEY}
agents:
  - name: Coordinator
    role: worker
    tools_inline:
      - name: jev_inline
        type: jev
        api_key: ${TEST_JEV_KEY}
`
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tool := cfg.GetTool("jev")
	if tool == nil {
		t.Fatal("global tool \"jev\" not found")
	}
	if tool.APIKey != "resolved-jev-secret" {
		t.Errorf("global tool api_key = %q, want the expanded env var value", tool.APIKey)
	}

	if len(cfg.Agents) != 1 || len(cfg.Agents[0].ToolsInline) != 1 {
		t.Fatalf("expected one agent with one inline tool, got agents=%d", len(cfg.Agents))
	}
	if got := cfg.Agents[0].ToolsInline[0].APIKey; got != "resolved-jev-secret" {
		t.Errorf("inline tool api_key = %q, want the expanded env var value", got)
	}
}

func TestLoad_MCPServerToolExpandsEnvVars(t *testing.T) {
	t.Setenv("TEST_KG_MCP_URL", "http://kg.example:8111/mcp")
	t.Setenv("TEST_MCP_COMMAND", "test-mcp-server")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yamlContent := `
name: mcp-expand-test
version: "1.0"
tools:
  - name: kg
    type: mcp_server
    transport: http
    url: ${TEST_KG_MCP_URL}
  - name: browser
    type: mcp_server
    transport: stdio
    command: ${TEST_MCP_COMMAND}
    args: ["${TEST_KG_MCP_URL}"]
agents:
  - name: Coordinator
    role: worker
`
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	kg := cfg.GetTool("kg")
	if kg == nil {
		t.Fatal("tool \"kg\" not found")
	}
	if kg.URL != "http://kg.example:8111/mcp" {
		t.Errorf("kg.URL = %q, want the expanded env var value", kg.URL)
	}

	browser := cfg.GetTool("browser")
	if browser == nil {
		t.Fatal("tool \"browser\" not found")
	}
	if browser.Command != "test-mcp-server" {
		t.Errorf("browser.Command = %q, want the expanded env var value", browser.Command)
	}
	if len(browser.Args) != 1 || browser.Args[0] != "http://kg.example:8111/mcp" {
		t.Errorf("browser.Args = %v, want expanded env var in args[0]", browser.Args)
	}
}

func TestRedacted_Nil(t *testing.T) {
	got := Redacted(nil)
	if got == nil {
		t.Fatal("Redacted(nil) must return a non-nil zero Config, got nil")
	}
	if got.Name != "" || got.Settings.Providers != nil {
		t.Errorf("Redacted(nil) must return a zero Config, got %+v", got)
	}
}

func TestGetDefaultModel(t *testing.T) {
	cfg := &Config{
		Settings: Settings{
			Providers: map[string]ProviderDefinition{
				"gemini-fast": {
					Type:         "openai",
					BaseURL:      "https://litellm.example.com/v1",
					DefaultModel: "gemini-3.1-flash-lite",
				},
				"no-default": {
					Type: "openai",
				},
			},
		},
	}

	cases := []struct {
		name     string
		provider string
		want     string
	}{
		{"declared default_model", "gemini-fast", "gemini-3.1-flash-lite"},
		{"provider exists, no default_model set", "no-default", ""},
		{"unknown provider name", "missing", ""},
		{"empty provider name", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.GetDefaultModel(tc.provider); got != tc.want {
				t.Errorf("GetDefaultModel(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}

func TestAgentChatConfigDefaults(t *testing.T) {
	enabled := true
	disabled := false
	cases := []struct {
		name          string
		cfg           AgentChatConfig
		wantEnabled   bool
		wantRetained  int
		wantMaxTBytes int
	}{
		{"zero value enables with defaults", AgentChatConfig{}, true, 20, 262144},
		{"explicit false disables", AgentChatConfig{Enabled: &disabled}, false, 20, 262144},
		{"explicit true enables", AgentChatConfig{Enabled: &enabled}, true, 20, 262144},
		{"explicit sizes win", AgentChatConfig{MaxRetained: 5, MaxTranscriptBytes: 1024}, true, 5, 1024},
		{"negative sizes fall back to defaults", AgentChatConfig{MaxRetained: -2, MaxTranscriptBytes: -2}, true, 20, 262144},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.EffectiveEnabled(); got != tc.wantEnabled {
				t.Errorf("EffectiveEnabled() = %v, want %v", got, tc.wantEnabled)
			}
			if got := tc.cfg.EffectiveMaxRetained(); got != tc.wantRetained {
				t.Errorf("EffectiveMaxRetained() = %d, want %d", got, tc.wantRetained)
			}
			if got := tc.cfg.EffectiveMaxTranscriptBytes(); got != tc.wantMaxTBytes {
				t.Errorf("EffectiveMaxTranscriptBytes() = %d, want %d", got, tc.wantMaxTBytes)
			}
		})
	}
}

func TestAgentChatConfigZeroMaxRetainedIsHonored(t *testing.T) {
	// max_retained: 0 must mean "retain nothing", not "use the default".
	// It is expressed by an explicit sentinel, not by the zero value —
	// see EffectiveMaxRetained's doc comment.
	cfg := AgentChatConfig{MaxRetained: -0}
	if got := cfg.EffectiveMaxRetained(); got != 20 {
		t.Errorf("literal zero should still default; got %d", got)
	}
	off := AgentChatConfig{MaxRetained: retainNothing}
	if got := off.EffectiveMaxRetained(); got != 0 {
		t.Errorf("retainNothing sentinel should yield 0; got %d", got)
	}
}

func TestResolveFileReferences_ReflectionPromptErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Agents: []AgentDefinition{
			{
				Name: "agent1",
				Settings: &AgentSettings{
					Reflection: ReflectionConfig{Prompt: "file:missing-reflect.md"},
				},
			},
		},
	}
	if err := cfg.resolveFileReferences(dir); err == nil {
		t.Fatal("expected error for missing reflection.prompt file, got nil")
	}
}

func TestResolveFileReferences_GroundCheckPromptErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Agents: []AgentDefinition{
			{
				Name: "agent1",
				Settings: &AgentSettings{
					GroundCheck: GroundCheckConfig{Prompt: "file:missing-groundcheck.md"},
				},
			},
		},
	}
	if err := cfg.resolveFileReferences(dir); err == nil {
		t.Fatal("expected error for missing ground_check.prompt file, got nil")
	}
}

func TestResolveFileReferences_FinalSynthesisPromptErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Workflows: []WorkflowDefinition{
			{
				Name:           "wf1",
				FinalSynthesis: &SynthesisConfig{Prompt: "file:missing-synthesis.md"},
			},
		},
	}
	if err := cfg.resolveFileReferences(dir); err == nil {
		t.Fatal("expected error for missing final_synthesis.prompt file, got nil")
	}
}

func TestLoadWithEnv_ConcurrentCallsDoNotRace(t *testing.T) {
	// Regression: envOverrides was a bare package-level map with no
	// synchronization. Run under `go test -race` to catch the data race;
	// this also exercises that concurrent calls don't crash or deadlock.
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	os.WriteFile(path, []byte("name: race-test\nversion: \"1.0\"\n"), 0644)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := LoadWithEnv(path, map[string]string{"SOME_VAR": "val"})
			if err != nil {
				t.Errorf("LoadWithEnv failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestLoad_ConcurrentWithLoadWithEnvDoesNotRace(t *testing.T) {
	// Regression: lookupEnv read the package-level envOverrides without
	// holding envOverridesMu. LoadWithEnv took the lock around its own
	// critical section, but a plain Load call reaches lookupEnv too and
	// never took any lock — a real data race when the two run concurrently.
	// Run under `go test -race` to catch it.
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	os.WriteFile(path, []byte("name: race-test\nversion: \"1.0\"\n"), 0644)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				if _, err := Load(path); err != nil {
					t.Errorf("Load failed: %v", err)
				}
			} else {
				if _, err := LoadWithEnv(path, map[string]string{"SOME_VAR": "val"}); err != nil {
					t.Errorf("LoadWithEnv failed: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
}

// ============================================================
// EffectiveSystemPrompt / skills prompt_template merge:
// skills: was validated and exported but never actually affected an
// agent's behavior. These cover the fix directly.
// ============================================================

func TestEffectiveSystemPrompt_NoSkills_Unchanged(t *testing.T) {
	cfg := &Config{}
	def := &AgentDefinition{SystemPrompt: "You are a helpful agent."}
	if got := cfg.EffectiveSystemPrompt(def); got != "You are a helpful agent." {
		t.Errorf("EffectiveSystemPrompt() = %q, want unchanged", got)
	}
}

func TestEffectiveSystemPrompt_OneSkill_Appended(t *testing.T) {
	cfg := &Config{Skills: []SkillDefinition{
		{Name: "deep_analysis", PromptTemplate: "Perform a deep analysis:\n1. Scan all files"},
	}}
	def := &AgentDefinition{SystemPrompt: "You are a researcher.", Skills: []string{"deep_analysis"}}
	want := "You are a researcher.\n\nPerform a deep analysis:\n1. Scan all files"
	if got := cfg.EffectiveSystemPrompt(def); got != want {
		t.Errorf("EffectiveSystemPrompt() = %q, want %q", got, want)
	}
}

func TestEffectiveSystemPrompt_MultipleSkills_OrderPreserved(t *testing.T) {
	cfg := &Config{Skills: []SkillDefinition{
		{Name: "a", PromptTemplate: "A instructions"},
		{Name: "b", PromptTemplate: "B instructions"},
	}}
	def := &AgentDefinition{SystemPrompt: "base", Skills: []string{"b", "a"}}
	want := "base\n\nB instructions\n\nA instructions"
	if got := cfg.EffectiveSystemPrompt(def); got != want {
		t.Errorf("EffectiveSystemPrompt() = %q, want %q (declaration order: b then a)", got, want)
	}
}

func TestEffectiveSystemPrompt_UnknownSkill_SkippedNotCrash(t *testing.T) {
	cfg := &Config{}
	def := &AgentDefinition{SystemPrompt: "base", Skills: []string{"does_not_exist"}}
	if got := cfg.EffectiveSystemPrompt(def); got != "base" {
		t.Errorf("EffectiveSystemPrompt() = %q, want %q (unknown skill skipped, not a crash — Validate() reports it separately)", got, "base")
	}
}

func TestEffectiveSystemPrompt_EmptyPromptTemplate_Skipped(t *testing.T) {
	cfg := &Config{Skills: []SkillDefinition{{Name: "empty", PromptTemplate: "  "}}}
	def := &AgentDefinition{SystemPrompt: "base", Skills: []string{"empty"}}
	if got := cfg.EffectiveSystemPrompt(def); got != "base" {
		t.Errorf("EffectiveSystemPrompt() = %q, want %q (blank prompt_template contributes nothing)", got, "base")
	}
}

// TestLoad_MergesSkillPromptTemplateIntoAgentSystemPrompt is the regression
// test for the skills prompt_template merge fix, using the actual shape already shipping in
// examples/single/08-full-featured/config.yaml: an agent that declares
// skills: [deep_analysis] and expects that skill's procedural instructions
// to actually apply. Before the fix, Load() left SystemPrompt untouched by
// skills entirely.
func TestLoad_MergesSkillPromptTemplateIntoAgentSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yamlContent := `
name: skills-merge-test
version: "1.0"
skills:
  - name: deep_analysis
    description: Thorough multi-step analysis workflow
    tools: [read_file]
    prompt_template: |
      Perform a deep analysis:
      1. Scan all relevant files
      2. Identify patterns and anomalies
agents:
  - name: Researcher
    role: worker
    system_prompt: You are a researcher.
    skills: [deep_analysis]
`
	os.WriteFile(path, []byte(yamlContent), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	agent := cfg.GetAgent("Researcher")
	if agent == nil {
		t.Fatal("agent Researcher not found after Load")
	}
	if !strings.Contains(agent.SystemPrompt, "Perform a deep analysis") {
		t.Errorf("agent.SystemPrompt = %q, want it to contain the deep_analysis skill's prompt_template", agent.SystemPrompt)
	}
	if !strings.Contains(agent.SystemPrompt, "You are a researcher.") {
		t.Errorf("agent.SystemPrompt = %q, want it to still contain the agent's own system_prompt", agent.SystemPrompt)
	}
}
