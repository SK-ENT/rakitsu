package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/permission"
)

func enablePolicyModes(t *testing.T) {
	t.Helper()
	ask, semi, auto := askModeEnabled, semiAutoModeEnabled, autoModeEnabled
	askModeEnabled, semiAutoModeEnabled, autoModeEnabled = true, true, true
	t.Cleanup(func() { askModeEnabled, semiAutoModeEnabled, autoModeEnabled = ask, semi, auto })
}

func TestPermissions_FocusStrictLoading(t *testing.T) {
	enablePolicyModes(t)
	cases := []struct {
		name, body, wantErr string
		focus               permission.Focus
		mode                permission.Mode
	}{
		{name: "legacy absent", focus: permission.FocusCoding, mode: permission.ModeLegacy},
		{name: "legacy explicit", body: "  permissions:\n    mode: legacy\n", focus: permission.FocusCoding, mode: permission.ModeLegacy},
		{name: "secure opt in defaults ask", body: "  permissions: {}\n", focus: permission.FocusCoding, mode: permission.ModeAsk},
		{name: "YAML merged permissions", body: "  <<: {permissions: {mode: auto}}\n", focus: permission.FocusCoding, mode: permission.ModeAuto},
		{name: "YAML alias permissions", body: "  policy: &policy {mode: ask}\n  permissions: *policy\n", focus: permission.FocusCoding, mode: permission.ModeAsk},
		{name: "YAML merged unknown key", body: "  permissions:\n    <<: {mode: ask, bogus: true}\n", wantErr: "unknown key"},
		{name: "secure timeout defaults ask", body: "  permissions:\n    ask_timeout_seconds: 20\n", focus: permission.FocusCoding, mode: permission.ModeAsk},
		{name: "coding", body: "  focus:\n    mode: coding\n", focus: permission.FocusCoding, mode: permission.ModeLegacy},
		{name: "plan", body: "  focus:\n    mode: plan\n", focus: permission.FocusPlan, mode: permission.ModeLegacy},
		{name: "design", body: "  focus:\n    mode: design\n", focus: permission.FocusDesign, mode: permission.ModeLegacy},
		{name: "ask", body: "  permissions:\n    mode: ask\n", focus: permission.FocusCoding, mode: permission.ModeAsk},
		{name: "semi auto", body: "  permissions:\n    mode: semi_auto\n", focus: permission.FocusCoding, mode: permission.ModeSemiAuto},
		{name: "auto default opt in", body: "  permissions:\n    mode: auto\n", focus: permission.FocusCoding, mode: permission.ModeAuto},
		{name: "auto listed opt in", body: "  permissions:\n    mode: ask\n    available_modes: [ask, auto]\n", focus: permission.FocusCoding, mode: permission.ModeAsk},
		{name: "invalid mode", body: "  permissions:\n    mode: Auto\n", wantErr: "unknown value"},
		{name: "invalid focus", body: "  focus:\n    mode: think\n", wantErr: "focus"},
		{name: "mode boolean", body: "  permissions:\n    mode: true\n", wantErr: "must be a non-empty string"},
		{name: "mode null", body: "  permissions:\n    mode: null\n", wantErr: "must be a non-empty string"},
		{name: "mode empty", body: "  permissions:\n    mode: ''\n", wantErr: "must be a non-empty string"},
		{name: "mode number", body: "  permissions:\n    mode: 3\n", wantErr: "must be a non-empty string"},
		{name: "focus null", body: "  focus: null\n", wantErr: "must be a mapping"},
		{name: "permissions null", body: "  permissions: null\n", wantErr: "must be a mapping"},
		{name: "permissions sequence", body: "  permissions: [ask]\n", wantErr: "must be a mapping"},
		{name: "unknown focus key", body: "  focus:\n    prompt: hello\n", wantErr: "unknown key"},
		{name: "unapproved authority key", body: "  permissions:\n    authority: {}\n", wantErr: "unknown key"},
		{name: "unknown available mode", body: "  permissions:\n    mode: ask\n    available_modes: [ask, unlimited]\n", wantErr: "invalid or duplicate"},
		{name: "duplicate available mode", body: "  permissions:\n    mode: ask\n    available_modes: [ask, ask]\n", wantErr: "duplicate"},
		{name: "unavailable default", body: "  permissions:\n    mode: ask\n    available_modes: [auto]\n", wantErr: "defaults must be available"},
		{name: "legacy downgrade selector", body: "  permissions:\n    mode: ask\n    available_modes: [ask, legacy]\n", wantErr: "cannot be mixed"},
		{name: "legacy secure selector", body: "  permissions:\n    mode: legacy\n    available_modes: [legacy, auto]\n", wantErr: "cannot be mixed"},
		{name: "duplicate focus", body: "  focus:\n    available_modes: [coding, coding]\n", wantErr: "duplicate"},
		{name: "unavailable focus", body: "  focus:\n    mode: design\n    available_modes: [coding]\n", wantErr: "defaults must be available"},
		{name: "empty availability", body: "  permissions:\n    mode: ask\n    available_modes: []\n", wantErr: "must be a sequence"},
		{name: "scalar availability", body: "  permissions:\n    mode: ask\n    available_modes: auto\n", wantErr: "must be a sequence"},
		{name: "number availability", body: "  permissions:\n    mode: ask\n    available_modes: [ask, 9]\n", wantErr: "values must be strings"},
		{name: "timeout coercion", body: "  permissions:\n    mode: ask\n    ask_timeout_seconds: '30'\n", wantErr: "must be an integer"},
		{name: "env passthrough coercion", body: "  permissions:\n    mode: ask\n    env_passthrough: PATH\n", wantErr: "must be a sequence"},
		{name: "environment expression", body: "  permissions:\n    mode: '${MY_POLICY}'\n", wantErr: "unknown value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := loadPerms(t, tc.body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err := c.Settings.PolicyConfig().ResolveSelection(permission.Selection{})
			if err != nil || s.Focus != tc.focus || s.Mode != tc.mode {
				t.Fatalf("selection %+v error %v", s, err)
			}
		})
	}
}

func TestPermissions_AllSecureModesDisabled(t *testing.T) {
	for _, mode := range []string{"ask", "semi_auto", "auto"} {
		t.Run(mode, func(t *testing.T) {
			_, err := loadPerms(t, "  permissions:\n    mode: "+mode+"\n")
			if err == nil || !strings.Contains(err.Error(), "not enabled in this build") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPermissions_EnvCannotSetAnyPolicy(t *testing.T) {
	for _, key := range []string{"SETTINGS_FOCUS", "SETTINGS_FOCUS_MODE", "SETTINGS_FOCUS_AVAILABLE_MODES", "SETTINGS_PERMISSIONS", "SETTINGS_PERMISSIONS_MODE", "SETTINGS_PERMISSIONS_AVAILABLE_MODES", "SETTINGS_PERMISSIONS_ASK_TIMEOUT_SECONDS", "SETTINGS_PERMISSIONS_MAX_PROMPTS_PER_MINUTE", "SETTINGS_PERMISSIONS_ENV_PASSTHROUGH", "SETTINGS_PERMISSIONS_TRUSTED_PATH"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			_, err := loadPerms(t, "")
			if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPermissions_LoadWithEnvCannotSetPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(permsBase), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"SETTINGS_PERMISSIONS_MODE", "SETTINGS_FOCUS_MODE", "SETTINGS_PERMISSIONS_AVAILABLE_MODES"} {
		_, err := LoadWithEnv(path, map[string]string{key: ""})
		if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
			t.Fatalf("%s: %v", key, err)
		}
	}
}

func TestPermissions_AvailabilityNeverGrantsAuthority(t *testing.T) {
	enablePolicyModes(t)
	c, err := loadPerms(t, "  focus:\n    available_modes: [coding, plan, design]\n  permissions:\n    mode: ask\n    available_modes: [ask, semi_auto, auto]\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []permission.Mode{permission.ModeAsk, permission.ModeSemiAuto, permission.ModeAuto} {
		p, err := ResolvePolicy(c.Settings, permission.Selection{Focus: permission.FocusDesign, Mode: mode}, nil, permission.PolicyInputs{})
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Handling(permission.PolicyAction{Request: permission.Request{Kind: permission.KindFSWrite, Tool: "fs", Paths: []string{"/workspace/a"}}}); got != permission.HandlingDeny {
			t.Fatalf("%s manufactured authority: %s", mode, got)
		}
	}
}

func TestPermissions_ResolverCannotBypassBuildGate(t *testing.T) {
	for _, mode := range []permission.Mode{permission.ModeAsk, permission.ModeSemiAuto, permission.ModeAuto} {
		_, err := ResolvePolicy(Settings{Permissions: PermissionsConfig{Mode: mode}}, permission.Selection{}, nil, permission.PolicyInputs{})
		if err == nil || !strings.Contains(err.Error(), "not enabled in this build") {
			t.Fatalf("%s bypassed gate: %v", mode, err)
		}
	}
}

func TestPermissions_CaseInsensitivePolicyDuplicates(t *testing.T) {
	enablePolicyModes(t)
	cases := []struct{ name, body, field string }{
		{"settings", "Settings:\n  permissions: {mode: auto}\n", "settings"},
		{"permissions", "  permissions: {mode: ask}\n  Permissions: {mode: auto}\n", "settings.permissions"},
		{"focus", "  focus: {mode: coding}\n  Focus: {mode: plan}\n", "settings.focus"},
		{"permission fields", "  permissions: {mode: ask, Mode: auto}\n", "settings.permissions.mode"},
		{"focus fields", "  focus: {mode: coding, Mode: plan}\n", "settings.focus.mode"},
		{"merged permissions", "  <<: {permissions: {mode: ask}}\n  Permissions: {mode: auto}\n", "settings.permissions"},
		{"merged settings", "Settings: {permissions: {mode: auto}}\n<<: {settings: {permissions: {mode: ask}}}\n", "settings"},
		{"alias fields", "  policy: &policy {mode: ask, Mode: auto}\n  permissions: *policy\n", "settings.permissions.mode"},
		{"merge list", "  <<: [{focus: {mode: coding}}, {Focus: {mode: plan}}]\n", "settings.focus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPerms(t, tc.body)
			want := tc.field + ": duplicate policy key (case-insensitive)"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q got %v", want, err)
			}
		})
	}
}

func TestPermissions_EnvironmentPolicyNamespaceAndAncestors(t *testing.T) {
	for _, key := range []string{"SETTINGS", "SETTINGS_PERMISSIONS_FUTURE_FIELD", "SETTINGS_FOCUS_FUTURE_FIELD", "SETTINGS.PERMISSIONS.MODE", "settings_permissions_mode"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "auto")
			_, err := loadPerms(t, "  permissions: {mode: legacy}\n")
			if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
				t.Fatalf("got %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(permsBase), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"SETTINGS", "SETTINGS_PERMISSIONS_FUTURE_FIELD", "SETTINGS.FOCUS.MODE"} {
		_, err := LoadWithEnv(path, map[string]string{key: ""})
		if err == nil || !strings.Contains(err.Error(), "cannot come from the environment") {
			t.Fatalf("%s: %v", key, err)
		}
	}
}

func TestPlanRejectsUnmediatedCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"write", func(c *Config) { c.Tools[0].Operation = "write" }},
		{"cli", func(c *Config) { c.Tools[0].Type = "cli" }},
		{"mcp", func(c *Config) { c.Agents[0].ToolsInline = []ToolDefinition{{Type: "mcp_server"}} }},
		{"a2a", func(c *Config) { c.Tools[0].Type = "a2a" }},
		{"jev", func(c *Config) { c.Tools[0].Type = "jev" }},
		{"spawn", func(c *Config) { c.Settings.Spawn.Enabled = true }},
		{"memory", func(c *Config) { c.Settings.Memory.Enabled = true }},
		{"session messaging", func(c *Config) { c.Settings.SessionMsg.Enabled = true }},
		{"wake", func(c *Config) { c.Settings.Wake.Enabled = true }},
		{"orchestrator", func(c *Config) { c.Orchestrator = &OrchestratorConfig{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Settings: Settings{Focus: FocusConfig{Mode: permission.FocusPlan}},
				Tools:  []ToolDefinition{{Name: "read_file", Type: "fs", Operation: "read"}},
				Agents: []AgentDefinition{{Name: "reader"}},
			}
			if err := c.ValidatePlan(); err != nil {
				t.Fatal(err)
			}
			tc.change(c)
			if err := c.ValidatePlan(); err == nil || !strings.Contains(err.Error(), "plan") {
				t.Fatalf("unsafe capability accepted: %v", err)
			}
		})
	}
}
