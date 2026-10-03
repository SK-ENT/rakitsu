// Alert sink config.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

// WakeAlerts configures server-side alert delivery. The agent can only queue
// a message through the alert tool; URL and host come from here.
type WakeAlerts struct {
	Sinks       []AlertSink `mapstructure:"sinks" yaml:"sinks,omitempty"`
	RatePerHour int         `mapstructure:"rate_per_hour" yaml:"rate_per_hour,omitempty"` // per sink, default 20
	Retry       AlertRetry  `mapstructure:"retry" yaml:"retry,omitempty"`
}

type AlertSink struct {
	Name        string   `mapstructure:"name" yaml:"name"`
	Type        string   `mapstructure:"type" yaml:"type"`       // webhook | ntfy
	URLEnv      string   `mapstructure:"url_env" yaml:"url_env"` // env var NAME holding the URL
	AllowHosts  []string `mapstructure:"allow_hosts" yaml:"allow_hosts"`
	MinSeverity string   `mapstructure:"min_severity" yaml:"min_severity,omitempty"` // info | warn | critical; default warn
}

type AlertRetry struct {
	MaxAttempts    int `mapstructure:"max_attempts" yaml:"max_attempts,omitempty"`       // default 3, max 5
	BackoffSeconds int `mapstructure:"backoff_seconds" yaml:"backoff_seconds,omitempty"` // default 5, max 60
}

// ApplyDefaults fills unset alert fields.
func (a *WakeAlerts) ApplyDefaults() {
	if len(a.Sinks) == 0 {
		return
	}
	if a.RatePerHour == 0 {
		a.RatePerHour = 20
	}
	if a.Retry.MaxAttempts == 0 {
		a.Retry.MaxAttempts = 3
	}
	if a.Retry.BackoffSeconds == 0 {
		a.Retry.BackoffSeconds = 5
	}
	for i := range a.Sinks {
		if a.Sinks[i].MinSeverity == "" {
			a.Sinks[i].MinSeverity = "warn"
		}
	}
}

var alertHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func (c *Config) validateWakeAlerts(add func(field, message string)) {
	a := c.Settings.Wake.Alerts
	if len(a.Sinks) == 0 {
		return
	}
	const p = "settings.wake.alerts."
	if a.RatePerHour < 1 || a.RatePerHour > 600 {
		add(p+"rate_per_hour", "must be in [1, 600]")
	}
	if a.Retry.MaxAttempts < 1 || a.Retry.MaxAttempts > 5 {
		add(p+"retry.max_attempts", "must be in [1, 5]")
	}
	if a.Retry.BackoffSeconds < 1 || a.Retry.BackoffSeconds > 60 {
		add(p+"retry.backoff_seconds", "must be in [1, 60]")
	}
	names := map[string]bool{}
	for i, s := range a.Sinks {
		f := fmt.Sprintf("%ssinks[%d].", p, i)
		if s.Name == "" {
			add(f+"name", "is required")
		} else if names[s.Name] {
			add(f+"name", fmt.Sprintf("duplicate sink name %q", s.Name))
		}
		names[s.Name] = true
		if s.Type != "webhook" && s.Type != "ntfy" {
			add(f+"type", "must be webhook or ntfy")
		}
		if !wakeEnvName.MatchString(s.URLEnv) {
			add(f+"url_env", "must be an environment variable NAME (the URL itself never goes in YAML)")
		}
		if len(s.AllowHosts) == 0 {
			add(f+"allow_hosts", "at least one host is required")
		}
		for k, h := range s.AllowHosts {
			if h != strings.ToLower(h) || !alertHostRe.MatchString(h) {
				add(fmt.Sprintf("%sallow_hosts[%d]", f, k), "must be a bare lowercase hostname (no scheme, port, path or wildcard)")
			}
		}
		switch s.MinSeverity {
		case "info", "warn", "critical":
		default:
			add(f+"min_severity", "must be info, warn or critical")
		}
	}
}
