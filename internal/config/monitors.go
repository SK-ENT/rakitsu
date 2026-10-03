// Monitor autostart config.
package config

import (
	"fmt"
	"regexp"
)

// MonitorConfig is one autostarted wake session. The session id is
// "monitor-<ID>". EnvRefs are variable NAMES only; values are read from the
// process environment at start and never stored.
type MonitorConfig struct {
	ID      string   `mapstructure:"id" yaml:"id"`
	Config  string   `mapstructure:"config" yaml:"config"`
	Workdir string   `mapstructure:"workdir" yaml:"workdir,omitempty"`
	EnvRefs []string `mapstructure:"env_refs" yaml:"env_refs,omitempty"`
}

// SessionID returns the stable chat session id for this monitor.
func (m MonitorConfig) SessionID() string { return "monitor-" + m.ID }

// HealthzConfig tunes the strict /healthz route and the monitor start grace.
type HealthzConfig struct {
	StartGraceSeconds int   `mapstructure:"monitors_start_grace_seconds" yaml:"monitors_start_grace_seconds,omitempty"` // default 120
	FailOnStopped     *bool `mapstructure:"healthz_fail_on_stopped" yaml:"healthz_fail_on_stopped,omitempty"`           // default true
	AllowRemote       bool  `mapstructure:"healthz_allow_remote" yaml:"healthz_allow_remote,omitempty"`                 // default false: loopback callers only
}

// FailOnStoppedOrDefault returns the effective healthz_fail_on_stopped (default true).
func (h HealthzConfig) FailOnStoppedOrDefault() bool {
	return h.FailOnStopped == nil || *h.FailOnStopped
}

// StartGraceOrDefault returns the effective start grace in seconds (default 120).
func (h HealthzConfig) StartGraceOrDefault() int {
	if h.StartGraceSeconds <= 0 {
		return 120
	}
	return h.StartGraceSeconds
}

var monitorIDRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// validateMonitors checks the monitors block. Any error here means the whole
// block must be rejected by the caller (the server still boots, no monitors).
func (c *Config) validateMonitors(add func(field, message string)) {
	seen := map[string]int{}
	for i, m := range c.Monitors {
		f := fmt.Sprintf("monitors[%d].", i)
		switch {
		case !monitorIDRe.MatchString(m.ID):
			add(f+"id", "must match [a-z0-9-], 1-40 chars")
		default:
			if j, dup := seen[m.ID]; dup {
				add(f+"id", fmt.Sprintf("duplicate id %q (also monitors[%d])", m.ID, j))
			}
			seen[m.ID] = i
		}
		if m.Config == "" {
			add(f+"config", "is required")
		}
		for k, e := range m.EnvRefs {
			if !wakeEnvName.MatchString(e) {
				add(fmt.Sprintf("%senv_refs[%d]", f, k), "must be an environment variable NAME, not a value")
			}
		}
	}
	if c.Healthz.StartGraceSeconds < 0 {
		add("healthz.monitors_start_grace_seconds", "must be >= 0")
	}
}
