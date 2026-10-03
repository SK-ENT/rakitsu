// Wake-up timer config. Opt-in.
package config

type WakeConfig struct {
	Enabled               bool        `mapstructure:"enabled" yaml:"enabled,omitempty"`
	IntervalSeconds       int         `mapstructure:"interval_seconds" yaml:"interval_seconds,omitempty"`
	BackoffFactor         float64     `mapstructure:"backoff_factor" yaml:"backoff_factor,omitempty"`
	MinIntervalSeconds    int         `mapstructure:"min_interval_seconds" yaml:"min_interval_seconds,omitempty"`
	MaxIntervalSeconds    int         `mapstructure:"max_interval_seconds" yaml:"max_interval_seconds,omitempty"`
	JitterPercent         int         `mapstructure:"jitter_percent" yaml:"jitter_percent,omitempty"`
	MaxTurnsPerHour       int         `mapstructure:"max_turns_per_hour" yaml:"max_turns_per_hour,omitempty"`
	TurnTimeoutSeconds    int         `mapstructure:"turn_timeout_seconds" yaml:"turn_timeout_seconds,omitempty"`
	KillSwitchFile        string      `mapstructure:"kill_switch_file" yaml:"kill_switch_file,omitempty"`
	HeartbeatStaleSeconds int         `mapstructure:"heartbeat_stale_seconds" yaml:"heartbeat_stale_seconds,omitempty"`
	ConsecutiveAlarms     int         `mapstructure:"consecutive_alarms" yaml:"consecutive_alarms,omitempty"` // http checks only; default 2
	Allow                 WakeAllow   `mapstructure:"allow" yaml:"allow,omitempty"`
	SecretEnv             []string    `mapstructure:"secret_env" yaml:"secret_env,omitempty"`
	Alerts                WakeAlerts  `mapstructure:"alerts" yaml:"alerts,omitempty"`
	Checks                []WakeCheck `mapstructure:"checks" yaml:"checks,omitempty"`
	Judge                 WakeJudge   `mapstructure:"judge" yaml:"judge,omitempty"`

	// start_task (wake trigger).
	// Active only when allow.configs is non-empty. Defaults: 10 / 3 / 300s / cancel on stop.
	MaxTasksPerHour    int   `mapstructure:"max_tasks_per_hour" yaml:"max_tasks_per_hour,omitempty"`     // rolling hour, persisted
	MaxConcurrentTasks int   `mapstructure:"max_concurrent_tasks" yaml:"max_concurrent_tasks,omitempty"` // overflow is refused, not queued
	TaskTimeoutSeconds int   `mapstructure:"task_timeout_seconds" yaml:"task_timeout_seconds,omitempty"` // never unbounded
	CancelOnStop       *bool `mapstructure:"cancel_on_stop" yaml:"cancel_on_stop,omitempty"`             // default true
}

// CancelOnStopEnabled reports whether stop / kill-switch cancels running started tasks (default true).
func (w WakeConfig) CancelOnStopEnabled() bool { return w.CancelOnStop == nil || *w.CancelOnStop }

type WakeAllow struct {
	Paths    []string `mapstructure:"paths" yaml:"paths,omitempty"`
	URLHosts []string `mapstructure:"url_hosts" yaml:"url_hosts,omitempty"`
	// Configs lists the task configs start_task may launch. Nothing else can be started.
	Configs []WakeAllowConfig `mapstructure:"configs" yaml:"configs,omitempty"`
}

// WakeAllowConfig is one launchable task config plus its FIXED argument template.
// Task is the fixed instruction text; Params are the only values the model may
// supply, each constrained to an enum, a safe relative path, or a bounded int.
type WakeAllowConfig struct {
	Name   string                   `mapstructure:"name" yaml:"name,omitempty"` // default: file base name without extension
	Path   string                   `mapstructure:"path" yaml:"path"`           // relative to the workdir
	Task   string                   `mapstructure:"task" yaml:"task"`
	Params map[string]WakeTaskParam `mapstructure:"params" yaml:"params,omitempty"`
}

// WakeTaskParam constrains one start_task argument. Type: enum | path | int.
type WakeTaskParam struct {
	Type string   `mapstructure:"type" yaml:"type"`
	Enum []string `mapstructure:"enum" yaml:"enum,omitempty"`
	Max  int      `mapstructure:"max" yaml:"max,omitempty"` // int: inclusive upper bound (min is 0)
}

type WakeCheck struct {
	Name           string       `mapstructure:"name" yaml:"name"`
	Type           string       `mapstructure:"type" yaml:"type"` // file_mtime | file_contains | http_status | http_json
	Path           string       `mapstructure:"path" yaml:"path,omitempty"`
	MaxAgeSeconds  int          `mapstructure:"max_age_seconds" yaml:"max_age_seconds,omitempty"`
	Contains       string       `mapstructure:"contains" yaml:"contains,omitempty"`
	URL            string       `mapstructure:"url" yaml:"url,omitempty"`
	Expect         int          `mapstructure:"expect" yaml:"expect,omitempty"`
	Field          string       `mapstructure:"field" yaml:"field,omitempty"`
	AlarmIf        *WakeAlarmIf `mapstructure:"alarm_if" yaml:"alarm_if,omitempty"`
	LabelBand      *WakeBand    `mapstructure:"label_band" yaml:"label_band,omitempty"` // http_json only
	TimeoutSeconds int          `mapstructure:"timeout_seconds" yaml:"timeout_seconds,omitempty"`
}

type WakeAlarmIf struct {
	Below         *float64 `mapstructure:"below" yaml:"below,omitempty"`
	Above         *float64 `mapstructure:"above" yaml:"above,omitempty"`
	ChangePercent *float64 `mapstructure:"change_percent" yaml:"change_percent,omitempty"`
	WindowSeconds int      `mapstructure:"window_seconds" yaml:"window_seconds,omitempty"`
}

// WakeBand is the bull/bear label rule with hysteresis: label "up" when value > Upper,
// "down" when value < Lower; leaves "up" only when value < Exit, leaves "down" only
// when value > -Exit; otherwise "neutral". A label flip is a `changed` result.
type WakeBand struct {
	Upper float64 `mapstructure:"upper" yaml:"upper"`
	Lower float64 `mapstructure:"lower" yaml:"lower"`
	Exit  float64 `mapstructure:"exit" yaml:"exit"`
}

type WakeJudge struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled,omitempty"`
	Model   string `mapstructure:"model" yaml:"model,omitempty"`
}
