package config

import (
	"strings"
	"testing"
)

func taskWake() WakeConfig {
	w := okWake()
	w.Allow.Configs = []WakeAllowConfig{{
		Name: "alert-handler", Path: "tasks/alert.yaml", Task: "Handle the alert.",
		Params: map[string]WakeTaskParam{
			"severity": {Type: "enum", Enum: []string{"low", "high"}},
			"file":     {Type: "path"},
			"count":    {Type: "int", Max: 50},
		},
	}}
	w.ApplyDefaults()
	return w
}

func TestWakeTaskDefaults(t *testing.T) {
	w := taskWake()
	if w.MaxTasksPerHour != 10 || w.MaxConcurrentTasks != 3 || w.TaskTimeoutSeconds != 300 || !w.CancelOnStopEnabled() {
		t.Fatalf("defaults wrong: %+v", w)
	}
	f := false
	w.CancelOnStop = &f
	if w.CancelOnStopEnabled() {
		t.Fatal("explicit false must win")
	}
}

func TestWakeTaskNoAllowlistNoDefaults(t *testing.T) {
	w := okWake()
	if w.MaxTasksPerHour != 0 || w.MaxConcurrentTasks != 0 {
		t.Fatalf("task caps must stay unset without an allowlist: %+v", w)
	}
}

func TestWakeTaskValidate(t *testing.T) {
	good := &Config{}
	good.Settings.Wake = taskWake()
	if errs := wakeErrs(good); len(errs) != 0 {
		t.Fatalf("valid task allowlist rejected: %v", errs)
	}
	cases := map[string]func(*WakeConfig){
		"must be relative to the workdir": func(w *WakeConfig) { w.Allow.Configs[0].Path = "/etc/x.yaml" },
		"must not contain '..'":           func(w *WakeConfig) { w.Allow.Configs[0].Path = "../x.yaml" },
		"path: required":                  func(w *WakeConfig) { w.Allow.Configs[0].Path = "" },
		"must match [A-Za-z0-9_-]+":       func(w *WakeConfig) { w.Allow.Configs[0].Name = "bad name!" },
		"unique":                          func(w *WakeConfig) { w.Allow.Configs = append(w.Allow.Configs, w.Allow.Configs[0]) },
		"task: required":                  func(w *WakeConfig) { w.Allow.Configs[0].Task = "" },
		"enum requires values":            func(w *WakeConfig) { w.Allow.Configs[0].Params["severity"] = WakeTaskParam{Type: "enum"} },
		"unknown param type":              func(w *WakeConfig) { w.Allow.Configs[0].Params["file"] = WakeTaskParam{Type: "string"} },
		"int requires max":                func(w *WakeConfig) { w.Allow.Configs[0].Params["count"] = WakeTaskParam{Type: "int"} },
		"param name must match":           func(w *WakeConfig) { w.Allow.Configs[0].Params["Bad-Key"] = WakeTaskParam{Type: "path"} },
		"max_tasks_per_hour":              func(w *WakeConfig) { w.MaxTasksPerHour = 1000 },
		"max_concurrent_tasks":            func(w *WakeConfig) { w.MaxConcurrentTasks = 99 },
		"task_timeout_seconds":            func(w *WakeConfig) { w.TaskTimeoutSeconds = 100000 },
		"enum value": func(w *WakeConfig) {
			w.Allow.Configs[0].Params["severity"] = WakeTaskParam{Type: "enum", Enum: []string{"a b"}}
		},
	}
	for want, mut := range cases {
		c := &Config{}
		c.Settings.Wake = taskWake()
		mut(&c.Settings.Wake)
		errs := strings.Join(wakeErrs(c), "\n")
		if !strings.Contains(errs, want) {
			t.Errorf("want error containing %q, got %q", want, errs)
		}
	}
}

func TestWakeTaskParamsLowercaseOnly(t *testing.T) {
	// viper lowercases map keys; uppercase names would silently never match.
	c := &Config{}
	c.Settings.Wake = taskWake()
	c.Settings.Wake.Allow.Configs[0].Params["Severity"] = WakeTaskParam{Type: "path"}
	if errs := wakeErrs(c); len(errs) == 0 {
		t.Fatal("uppercase param name must be rejected")
	}
}
