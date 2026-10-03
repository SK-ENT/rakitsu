package config

import (
	"strings"
	"testing"
)

func collect(c *Config) []string {
	var out []string
	c.validateMonitors(func(f, m string) { out = append(out, f+" "+m) })
	c.validateWakeAlerts(func(f, m string) { out = append(out, f+" "+m) })
	return out
}

func TestMonitorsValidate(t *testing.T) {
	ok := &Config{Monitors: []MonitorConfig{{ID: "web-prod", Config: "a.yaml", EnvRefs: []string{"PD_TOKEN"}}, {ID: "db", Config: "b.yaml"}}}
	if e := collect(ok); len(e) != 0 {
		t.Fatalf("unexpected: %v", e)
	}
	if ok.Monitors[0].SessionID() != "monitor-web-prod" {
		t.Fatal("session id")
	}
	cases := map[string]*Config{
		"duplicate":   {Monitors: []MonitorConfig{{ID: "a", Config: "x"}, {ID: "a", Config: "y"}}},
		"id":          {Monitors: []MonitorConfig{{ID: "Bad_ID", Config: "x"}}},
		"id2":         {Monitors: []MonitorConfig{{ID: strings.Repeat("a", 41), Config: "x"}}},
		"config":      {Monitors: []MonitorConfig{{ID: "a"}}},
		"environment": {Monitors: []MonitorConfig{{ID: "a", Config: "x", EnvRefs: []string{"TOKEN=abc"}}}},
	}
	for name, c := range cases {
		if e := collect(c); len(e) == 0 {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestHealthzDefaults(t *testing.T) {
	var h HealthzConfig
	if !h.FailOnStoppedOrDefault() || h.StartGraceOrDefault() != 120 || h.AllowRemote {
		t.Fatal("bad defaults")
	}
	f := false
	h = HealthzConfig{FailOnStopped: &f, StartGraceSeconds: 5}
	if h.FailOnStoppedOrDefault() || h.StartGraceOrDefault() != 5 {
		t.Fatal("overrides ignored")
	}
}

func validAlerts() WakeAlerts {
	a := WakeAlerts{Sinks: []AlertSink{{Name: "ops", Type: "webhook", URLEnv: "ALERT_URL", AllowHosts: []string{"hooks.example.com"}}}}
	a.ApplyDefaults()
	return a
}

func TestAlertsDefaultsAndValid(t *testing.T) {
	a := validAlerts()
	if a.RatePerHour != 20 || a.Retry.MaxAttempts != 3 || a.Retry.BackoffSeconds != 5 || a.Sinks[0].MinSeverity != "warn" {
		t.Fatalf("defaults: %+v", a)
	}
	if e := collect(&Config{Settings: Settings{Wake: WakeConfig{Alerts: a}}}); len(e) != 0 {
		t.Fatalf("unexpected: %v", e)
	}
}

func TestAlertsInvalid(t *testing.T) {
	mut := map[string]func(*WakeAlerts){
		"type":     func(a *WakeAlerts) { a.Sinks[0].Type = "smtp" },
		"url_env":  func(a *WakeAlerts) { a.Sinks[0].URLEnv = "https://x.example.com/hook" },
		"no host":  func(a *WakeAlerts) { a.Sinks[0].AllowHosts = nil },
		"scheme":   func(a *WakeAlerts) { a.Sinks[0].AllowHosts = []string{"https://hooks.example.com"} },
		"wildcard": func(a *WakeAlerts) { a.Sinks[0].AllowHosts = []string{"*.example.com"} },
		"port":     func(a *WakeAlerts) { a.Sinks[0].AllowHosts = []string{"a.example.com:8080"} },
		"severity": func(a *WakeAlerts) { a.Sinks[0].MinSeverity = "loud" },
		"attempts": func(a *WakeAlerts) { a.Retry.MaxAttempts = 9 },
		"dup":      func(a *WakeAlerts) { a.Sinks = append(a.Sinks, a.Sinks[0]) },
	}
	for name, f := range mut {
		a := validAlerts()
		f(&a)
		if e := collect(&Config{Settings: Settings{Wake: WakeConfig{Alerts: a}}}); len(e) == 0 {
			t.Errorf("%s: expected error", name)
		}
	}
}
