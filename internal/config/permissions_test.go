package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const permsBase = `name: t
agents:
  - name: A
    provider: openai
    model: m
    system_prompt: hi
settings:
  default_provider: openai
`

func loadPerms(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(permsBase+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestPermissions_LoadTable(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"absent is legacy", ``, ""},
		{"explicit legacy", "  permissions:\n    mode: legacy\n", ""},
		{"unknown mode", "  permissions:\n    mode: strict\n", `settings.permissions.mode: unknown value "strict"`},
		{"ask reserved", "  permissions:\n    mode: ask\n", `not enabled in this build`},
		{"unknown key", "  permissions:\n    mode: legacy\n    timeout: 5\n", `unknown key "timeout"`},
		{"env passthrough refuses control token", "  permissions:\n    mode: legacy\n    env_passthrough: [RAKITSU_API_TOKEN]\n", "RAKITSU_"},
		{"env passthrough refuses PATH", "  permissions:\n    mode: legacy\n    env_passthrough: [PATH]\n", "PATH"},
		{"env passthrough bad name", "  permissions:\n    mode: legacy\n    env_passthrough: ['A-B']\n", "env_passthrough"},
		{"relative trusted path", "  permissions:\n    mode: legacy\n    trusted_path: [bin]\n", "absolute"},
		{"timeout too low", "  permissions:\n    mode: legacy\n    ask_timeout_seconds: 5\n", "ask_timeout_seconds"},
		{"timeout too high", "  permissions:\n    mode: legacy\n    ask_timeout_seconds: 601\n", "ask_timeout_seconds"},
		{"prompts too high", "  permissions:\n    mode: legacy\n    max_prompts_per_minute: 61\n", "max_prompts_per_minute"},
		{"valid full", "  permissions:\n    mode: legacy\n    ask_timeout_seconds: 30\n    max_prompts_per_minute: 5\n    env_passthrough: [GOPATH]\n    trusted_path: [/usr/bin]\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPerms(t, tc.yaml)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestPermissions_AbsentIsZeroValue(t *testing.T) {
	c, err := loadPerms(t, ``)
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings.Permissions.Mode != "" || c.Settings.Permissions.Ask() {
		t.Fatalf("absent block must be legacy: %+v", c.Settings.Permissions)
	}
}

func TestPermissions_Defaults(t *testing.T) {
	var p PermissionsConfig
	if p.EffectiveAskTimeout() != 120*time.Second || p.EffectiveMaxPromptsPerMinute() != 10 {
		t.Fatalf("defaults wrong: %v %d", p.EffectiveAskTimeout(), p.EffectiveMaxPromptsPerMinute())
	}
	p = PermissionsConfig{AskTimeoutSeconds: 30, MaxPromptsPerMinute: 3}
	if p.EffectiveAskTimeout() != 30*time.Second || p.EffectiveMaxPromptsPerMinute() != 3 {
		t.Fatalf("explicit wrong")
	}
}

func TestPermissions_EnvCannotOverrideMode(t *testing.T) {
	t.Setenv("SETTINGS_PERMISSIONS_MODE", "legacy")
	_, err := loadPerms(t, "  permissions:\n    mode: ask\n")
	if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
		t.Fatalf("got %v", err)
	}
}

func TestPermissions_EnvCannotSetModeWhenAbsent(t *testing.T) {
	t.Setenv("SETTINGS_PERMISSIONS_MODE", "ask")
	_, err := loadPerms(t, ``)
	if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
		t.Fatalf("got %v", err)
	}
}

func TestPermissions_SourceFiles(t *testing.T) {
	c, err := loadPerms(t, ``)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SourceFiles) == 0 || !filepath.IsAbs(c.SourceFiles[0]) || filepath.Base(c.SourceFiles[0]) != "c.yaml" {
		t.Fatalf("SourceFiles = %v", c.SourceFiles)
	}
}

// askCfg builds a Config for validatePermissions with ask forced on.
func askErrs(t *testing.T, c *Config) []string {
	t.Helper()
	old := askModeEnabled
	askModeEnabled = true
	defer func() { askModeEnabled = old }()
	c.Settings.Permissions.Mode = "ask"
	var out []string
	c.validatePermissions(func(f, m string) { out = append(out, f+": "+m) })
	return out
}

func TestPermissions_AskBlockedFeatures(t *testing.T) {
	t.Run("mcp_server refused", func(t *testing.T) {
		e := askErrs(t, &Config{Tools: []ToolDefinition{{Name: "m", Type: "mcp_server"}}})
		if len(e) != 1 || !strings.Contains(e[0], `type mcp_server) is not available under settings.permissions.mode: ask`) {
			t.Fatalf("%v", e)
		}
	})
	t.Run("inline a2a refused", func(t *testing.T) {
		e := askErrs(t, &Config{Agents: []AgentDefinition{{Name: "A", ToolsInline: []ToolDefinition{{Name: "x", Type: "a2a"}}}}})
		if len(e) != 1 || !strings.Contains(e[0], "type a2a") {
			t.Fatalf("%v", e)
		}
	})
	t.Run("jev refused", func(t *testing.T) {
		e := askErrs(t, &Config{Tools: []ToolDefinition{{Name: "j", Type: "jev"}}})
		if len(e) != 1 {
			t.Fatalf("%v", e)
		}
	})
	t.Run("memory refused", func(t *testing.T) {
		c := &Config{}
		c.Settings.Memory.Enabled = true
		if e := askErrs(t, c); len(e) != 1 || !strings.Contains(e[0], "settings.memory.enabled") {
			t.Fatalf("%v", e)
		}
	})
	t.Run("session_msg wake monitors refused", func(t *testing.T) {
		c := &Config{Monitors: []MonitorConfig{{ID: "m"}}}
		c.Settings.SessionMsg.Enabled = true
		c.Settings.Wake.Enabled = true
		if e := askErrs(t, c); len(e) != 3 {
			t.Fatalf("%v", e)
		}
	})
	t.Run("docker cli allowed", func(t *testing.T) {
		c := &Config{Tools: []ToolDefinition{{Name: "c", Type: "cli", Sandbox: &SandboxConfig{Type: "docker"}}}}
		if e := askErrs(t, c); len(e) != 0 {
			t.Fatalf("%v", e)
		}
	})
	t.Run("ask accepted when enabled", func(t *testing.T) {
		if e := askErrs(t, &Config{}); len(e) != 0 {
			t.Fatalf("%v", e)
		}
	})
}

func TestPermissions_AllExamplesStillLoad(t *testing.T) {
	var files []string
	for _, root := range []string{"../../examples", "../../test/configs"} {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ".yaml") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no example configs found")
	}
	for _, f := range files {
		if _, err := Load(f); err != nil && strings.Contains(err.Error(), "settings.permissions") {
			t.Errorf("%s: %v", f, err)
		}
	}
}
