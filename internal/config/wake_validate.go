// Wake-up timer config validation.
package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var wakeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var wakeTaskName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var wakeParamName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// WakeEnumValue is the only charset an enum value may use (no spaces, quotes or fence characters).
var WakeEnumValue = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (w *WakeConfig) ApplyDefaults() {
	if !w.Enabled {
		return
	}
	def := func(p *int, v int) {
		if *p == 0 {
			*p = v
		}
	}
	w.Alerts.ApplyDefaults()
	def(&w.IntervalSeconds, 60)
	def(&w.MinIntervalSeconds, 30)
	def(&w.MaxIntervalSeconds, 600)
	def(&w.MaxTurnsPerHour, 6)
	def(&w.TurnTimeoutSeconds, 180)
	def(&w.HeartbeatStaleSeconds, 180)
	def(&w.ConsecutiveAlarms, 2)
	if w.BackoffFactor == 0 {
		w.BackoffFactor = 1.5
	}
	if w.JitterPercent == 0 {
		w.JitterPercent = 10
	}
	if w.KillSwitchFile == "" {
		w.KillSwitchFile = "~/.rakitsu/wake/STOP"
	}
	if len(w.Allow.Configs) > 0 {
		def(&w.MaxTasksPerHour, 10)
		def(&w.MaxConcurrentTasks, 3)
		def(&w.TaskTimeoutSeconds, 300)
		for i := range w.Allow.Configs {
			if w.Allow.Configs[i].Name == "" {
				base := filepath.Base(w.Allow.Configs[i].Path)
				w.Allow.Configs[i].Name = strings.TrimSuffix(base, filepath.Ext(base))
			}
		}
	}
	for i := range w.Checks {
		if w.Checks[i].TimeoutSeconds == 0 {
			w.Checks[i].TimeoutSeconds = 10
		}
		if w.Checks[i].Type == "http_status" && w.Checks[i].Expect == 0 {
			w.Checks[i].Expect = 200
		}
	}
}

func (c *Config) validateWake(add func(field, message string)) {
	w := c.Settings.Wake
	if !w.Enabled {
		return
	}
	const p = "settings.wake."
	if w.MinIntervalSeconds < 10 {
		add(p+"min_interval_seconds", "must be >= 10")
	}
	if w.IntervalSeconds < w.MinIntervalSeconds {
		add(p+"interval_seconds", "must be >= min_interval_seconds")
	}
	if w.MaxIntervalSeconds < w.IntervalSeconds {
		add(p+"max_interval_seconds", "must be >= interval_seconds")
	}
	if w.HeartbeatStaleSeconds < 15 {
		add(p+"heartbeat_stale_seconds", "must be >= 15 (the heartbeat is written every heartbeat_stale_seconds/3)")
	}
	if w.BackoffFactor < 1.0 || w.BackoffFactor > 4.0 {
		add(p+"backoff_factor", "must be in [1.0, 4.0]")
	}
	if w.JitterPercent < 0 || w.JitterPercent > 50 {
		add(p+"jitter_percent", "must be in [0, 50]")
	}
	if w.MaxTurnsPerHour < 1 || w.MaxTurnsPerHour > 60 {
		add(p+"max_turns_per_hour", "must be in [1, 60]")
	}
	if len(w.Checks) == 0 {
		add(p+"checks", "at least one check is required when wake is enabled")
	}
	seen := map[string]bool{}
	for i, ch := range w.Checks {
		f := fmt.Sprintf("%schecks[%d].", p, i)
		if ch.Name == "" || seen[ch.Name] {
			add(f+"name", "must be non-empty and unique")
		}
		seen[ch.Name] = true
		switch ch.Type {
		case "file_mtime", "file_contains":
			c.validateWakePath(add, f+"path", ch.Path)
		case "http_status", "http_json":
			u, err := url.Parse(ch.URL)
			if err == nil && u.User != nil {
				add(f+"url", "must not contain credentials (userinfo)")
			} else if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add(f+"url", "must be an http or https URL")
			} else if !wakeHostAllowed(w.Allow.URLHosts, u.Host) {
				add(f+"url", fmt.Sprintf("host %q is not in settings.wake.allow.url_hosts", u.Host))
			}
			if ch.Type == "http_json" && strings.TrimSpace(ch.Field) == "" {
				add(f+"field", "required for http_json (dotted path to a number)")
			}
		default:
			add(f+"type", fmt.Sprintf("unknown check type %q: must be file_mtime, file_contains, http_status or http_json", ch.Type))
		}
	}
	if w.Judge.Enabled && strings.TrimSpace(w.Judge.Model) == "" {
		add(p+"judge.model", "required when judge.enabled is true")
	}
	for i, e := range w.SecretEnv {
		if !wakeEnvName.MatchString(e) {
			add(fmt.Sprintf("%ssecret_env[%d]", p, i), "must be an environment variable NAME (no values)")
		}
	}
	c.validateWakeTasks(add)
	refuse := func(field string, t ToolDefinition) {
		switch t.Type {
		case "cli", "fs", "mcp_server", "a2a":
			add(field, fmt.Sprintf("tool type %q is refused while settings.wake.enabled is true (unattended turns, no approval prompt)", t.Type))
		}
	}
	for i, t := range c.Tools {
		refuse(fmt.Sprintf("tools[%d]", i), t)
	}
	for ai, a := range c.Agents {
		for ti, t := range a.ToolsInline {
			refuse(fmt.Sprintf("agents[%d].tools_inline[%d]", ai, ti), t)
		}
	}
}

func wakeHostAllowed(allowed []string, host string) bool {
	for _, a := range allowed {
		if a == host {
			return true
		}
	}
	return false
}

// validateWakePath refuses absolute-or-relative paths that contain "..", are
// not under one of allow.paths, or (when absolute) are not under it either.
// Symlink resolution and workdir confinement happen at run time in internal/wake.
func (c *Config) validateWakePath(add func(string, string), field, path string) {
	if path == "" {
		add(field, "required")
		return
	}
	if strings.Contains(path, "..") {
		add(field, "must not contain '..'")
		return
	}
	clean := filepath.Clean(path)
	for _, ap := range c.Settings.Wake.Allow.Paths {
		a := filepath.Clean(ap)
		if clean == a || strings.HasPrefix(clean, a+string(filepath.Separator)) {
			return
		}
	}
	add(field, "must be under settings.wake.allow.paths")
}

// validateWakeTasks checks the static shape of settings.wake.allow.configs and the
// task caps. File existence and parseability need the workdir and are checked when
// the session starts (internal/wake ResolveAllowlist), before the first tick.
func (c *Config) validateWakeTasks(add func(field, message string)) {
	w := c.Settings.Wake
	const p = "settings.wake."
	if len(w.Allow.Configs) == 0 {
		return
	}
	if w.MaxTasksPerHour < 1 || w.MaxTasksPerHour > 60 {
		add(p+"max_tasks_per_hour", "must be in [1, 60]")
	}
	if w.MaxConcurrentTasks < 1 || w.MaxConcurrentTasks > 10 {
		add(p+"max_concurrent_tasks", "must be in [1, 10]")
	}
	if w.TaskTimeoutSeconds < 10 || w.TaskTimeoutSeconds > 3600 {
		add(p+"task_timeout_seconds", "must be in [10, 3600]")
	}
	seen := map[string]bool{}
	for i, ac := range w.Allow.Configs {
		f := fmt.Sprintf("%sallow.configs[%d].", p, i)
		switch {
		case ac.Path == "":
			add(f+"path", "required")
		case filepath.IsAbs(ac.Path):
			add(f+"path", "must be relative to the workdir")
		case strings.Contains(ac.Path, ".."):
			add(f+"path", "must not contain '..'")
		}
		if !wakeTaskName.MatchString(ac.Name) {
			add(f+"name", "must match [A-Za-z0-9_-]+")
		}
		if seen[ac.Name] {
			add(f+"name", "must be unique")
		}
		seen[ac.Name] = true
		if strings.TrimSpace(ac.Task) == "" {
			add(f+"task", "required (fixed instruction text)")
		}
		for k, prm := range ac.Params {
			pf := fmt.Sprintf("%sparams.%s", f, k)
			if !wakeParamName.MatchString(k) {
				add(pf, "param name must match [a-z][a-z0-9_]*")
			}
			switch prm.Type {
			case "enum":
				if len(prm.Enum) == 0 {
					add(pf, "enum requires values")
				}
				for _, v := range prm.Enum {
					if !WakeEnumValue.MatchString(v) {
						add(pf, fmt.Sprintf("enum value %q must match [A-Za-z0-9_.-]{1,64}", v))
					}
				}
			case "path":
			case "int":
				if prm.Max < 1 {
					add(pf, "int requires max >= 1")
				}
			default:
				add(pf, fmt.Sprintf("unknown param type %q: must be enum, path or int", prm.Type))
			}
		}
	}
}
