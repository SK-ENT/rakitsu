package config

import (
	"strings"
	"testing"
)

func okWake() WakeConfig {
	w := WakeConfig{
		Enabled: true,
		Allow:   WakeAllow{Paths: []string{"./logs"}, URLHosts: []string{"localhost:8080"}},
		Checks: []WakeCheck{
			{Name: "log", Type: "file_mtime", Path: "./logs/app.log", MaxAgeSeconds: 300},
			{Name: "api", Type: "http_status", URL: "http://localhost:8080/health", Expect: 200},
		},
	}
	w.ApplyDefaults()
	return w
}

func wakeErrs(c *Config) []string {
	var out []string
	c.validateWake(func(f, m string) { out = append(out, f+": "+m) })
	return out
}

func TestWakeApplyDefaults(t *testing.T) {
	w := okWake()
	if w.IntervalSeconds != 60 || w.BackoffFactor != 1.5 || w.MinIntervalSeconds != 30 ||
		w.MaxIntervalSeconds != 600 || w.JitterPercent != 10 || w.MaxTurnsPerHour != 6 ||
		w.TurnTimeoutSeconds != 180 || w.HeartbeatStaleSeconds != 180 || w.ConsecutiveAlarms != 2 {
		t.Fatalf("defaults wrong: %+v", w)
	}
	if w.KillSwitchFile != "~/.rakitsu/wake/STOP" {
		t.Fatalf("kill switch default: %q", w.KillSwitchFile)
	}
}

func TestWakeDisabledSkipsValidation(t *testing.T) {
	c := &Config{Tools: []ToolDefinition{{Name: "sh", Type: "cli"}}}
	c.Settings.Wake = WakeConfig{Enabled: false, Checks: nil}
	if errs := wakeErrs(c); len(errs) != 0 {
		t.Fatalf("disabled wake must not validate: %v", errs)
	}
}

func TestWakeValidValid(t *testing.T) {
	c := &Config{}
	c.Settings.Wake = okWake()
	if errs := wakeErrs(c); len(errs) != 0 {
		t.Fatalf("unexpected: %v", errs)
	}
}

func TestWakeValidationTable(t *testing.T) {
	mut := func(f func(w *WakeConfig)) *Config {
		c := &Config{}
		w := okWake()
		f(&w)
		c.Settings.Wake = w
		return c
	}
	f64 := func(v float64) *float64 { return &v }
	_ = f64
	cases := []struct {
		name string
		cfg  *Config
		want string // substring of "field: message"
	}{
		{"min interval too low", mut(func(w *WakeConfig) { w.MinIntervalSeconds = 5 }), "min_interval_seconds"},
		{"interval below min", mut(func(w *WakeConfig) { w.IntervalSeconds = 10 }), "interval_seconds"},
		{"max below interval", mut(func(w *WakeConfig) { w.MaxIntervalSeconds = 40 }), "max_interval_seconds"},
		{"backoff too high", mut(func(w *WakeConfig) { w.BackoffFactor = 5 }), "backoff_factor"},
		{"backoff too low", mut(func(w *WakeConfig) { w.BackoffFactor = 0.5 }), "backoff_factor"},
		{"jitter too high", mut(func(w *WakeConfig) { w.JitterPercent = 60 }), "jitter_percent"},
		{"cap too high", mut(func(w *WakeConfig) { w.MaxTurnsPerHour = 61 }), "max_turns_per_hour"},
		{"no checks", mut(func(w *WakeConfig) { w.Checks = nil }), "checks"},
		{"dup names", mut(func(w *WakeConfig) { w.Checks[1].Name = "log" }), "checks[1].name"},
		{"unknown type", mut(func(w *WakeConfig) { w.Checks[0].Type = "shell" }), "checks[0].type"},
		{"url not allowlisted", mut(func(w *WakeConfig) { w.Checks[1].URL = "http://evil.example/x" }), "checks[1].url"},
		{"url bad scheme", mut(func(w *WakeConfig) { w.Checks[1].URL = "file:///etc/passwd" }), "checks[1].url"},
		{"path dotdot", mut(func(w *WakeConfig) { w.Checks[0].Path = "./logs/../../etc/passwd" }), "checks[0].path"},
		{"path not allowlisted", mut(func(w *WakeConfig) { w.Checks[0].Path = "./other/x.log" }), "checks[0].path"},
		{"absolute path outside", mut(func(w *WakeConfig) { w.Checks[0].Path = "/etc/passwd" }), "checks[0].path"},
		{"judge without model", mut(func(w *WakeConfig) { w.Judge = WakeJudge{Enabled: true} }), "judge.model"},
		{"secret_env with value", mut(func(w *WakeConfig) { w.SecretEnv = []string{"KEY=abc"} }), "secret_env[0]"},
		{"secret_env bad name", mut(func(w *WakeConfig) { w.SecretEnv = []string{"1BAD"} }), "secret_env[0]"},
		{"http_json needs field", mut(func(w *WakeConfig) {
			w.Checks[1] = WakeCheck{Name: "j", Type: "http_json", URL: "http://localhost:8080/j"}
		}), "checks[1].field"},
		{"global cli tool", func() *Config {
			c := mut(func(*WakeConfig) {})
			c.Tools = []ToolDefinition{{Name: "sh", Type: "cli"}}
			return c
		}(), "tools[0]"},
		{"global fs tool", func() *Config {
			c := mut(func(*WakeConfig) {})
			c.Tools = []ToolDefinition{{Name: "f", Type: "fs"}}
			return c
		}(), "tools[0]"},
		{"inline mcp tool", func() *Config {
			c := mut(func(*WakeConfig) {})
			c.Agents = []AgentDefinition{{Name: "a", ToolsInline: []ToolDefinition{{Name: "m", Type: "mcp_server"}}}}
			return c
		}(), "agents[0].tools_inline[0]"},
		{"inline a2a tool", func() *Config {
			c := mut(func(*WakeConfig) {})
			c.Agents = []AgentDefinition{{Name: "a", ToolsInline: []ToolDefinition{{Name: "x", Type: "a2a"}}}}
			return c
		}(), "agents[0].tools_inline[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := wakeErrs(tc.cfg)
			if len(errs) == 0 {
				t.Fatalf("expected error containing %q, got none", tc.want)
			}
			joined := strings.Join(errs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, joined)
			}
		})
	}
}

func TestWakeHeartbeatStaleTooSmallRejected(t *testing.T) {
	c := &Config{}
	w := okWake()
	w.HeartbeatStaleSeconds = 10
	c.Settings.Wake = w
	errs := wakeErrs(c)
	if len(errs) != 1 || !strings.Contains(errs[0], "heartbeat_stale_seconds") {
		t.Fatalf("want one heartbeat_stale_seconds error, got %v", errs)
	}
}
