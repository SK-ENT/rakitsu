package main

import (
	"context"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/alert"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestBuildAgentToolRegistryWithAlert verifies send_alert is registered
// when AlertNotifier is provided in BuildOptions.
func TestBuildAgentToolRegistryWithAlert(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentDefinition{{
			Name:  "test-agent",
			Model: "gpt-4",
		}},
		Settings: config.Settings{
			Wake: config.WakeConfig{
				Enabled: true,
				Alerts: config.WakeAlerts{
					Sinks: []config.AlertSink{{
						Name:       "webhook",
						Type:       "webhook",
						URLEnv:     "TEST_ALERT_URL",
						AllowHosts: []string{"example.com"},
					}},
				},
			},
		},
	}

	eventBus := telemetry.NewEventBus(128)

	// Create a minimal notifier for testing
	notifier, err := alert.NewNotifier(cfg.Settings.Wake.Alerts, nil, alert.Options{
		Getenv: func(k string) string {
			if k == "TEST_ALERT_URL" {
				return "https://example.com/webhook"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("failed to create notifier: %v", err)
	}
	defer notifier.Close()

	// Build the runtime with alert notifier
	builder := &runtimeBuilder{
		ctx:      context.Background(),
		cfg:      cfg,
		eventBus: eventBus,
		opts: BuildOptions{
			AlertNotifier: notifier,
		},
		agents: make(map[string]*agent.Agent),
	}

	// Build agent registry at depth 0 (top-level)
	reg := builder.buildAgentToolRegistryDepth(&cfg.Agents[0], 0)

	// Verify send_alert tool is registered
	if reg.GetTool("send_alert") == nil {
		t.Error("expected send_alert tool to be registered")
	}
}

// TestBuildAgentToolRegistryWithoutAlert verifies send_alert is not registered
// when AlertNotifier is nil.
func TestBuildAgentToolRegistryWithoutAlert(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentDefinition{{
			Name:  "test-agent",
			Model: "gpt-4",
		}},
		Settings: config.Settings{
			Wake: config.WakeConfig{
				Enabled: true,
				// No alerts configured
			},
		},
	}

	eventBus := telemetry.NewEventBus(128)

	// Build the runtime without alert notifier
	builder := &runtimeBuilder{
		ctx:      context.Background(),
		cfg:      cfg,
		eventBus: eventBus,
		opts: BuildOptions{
			AlertNotifier: nil, // No notifier
		},
		agents: make(map[string]*agent.Agent),
	}

	// Build agent registry at depth 0 (top-level)
	reg := builder.buildAgentToolRegistryDepth(&cfg.Agents[0], 0)

	// Verify send_alert tool is NOT registered
	if reg.GetTool("send_alert") != nil {
		t.Error("expected send_alert tool to NOT be registered when AlertNotifier is nil")
	}
}

// TestBuildAgentToolRegistryAlertNotAtDepth1 verifies send_alert is not registered
// at depth > 0 (spawned agents should not have alerts).
func TestBuildAgentToolRegistryAlertNotAtDepth1(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentDefinition{{
			Name:  "test-agent",
			Model: "gpt-4",
		}},
		Settings: config.Settings{
			Wake: config.WakeConfig{
				Enabled: true,
				Alerts: config.WakeAlerts{
					Sinks: []config.AlertSink{{
						Name:       "webhook",
						Type:       "webhook",
						URLEnv:     "TEST_ALERT_URL",
						AllowHosts: []string{"example.com"},
					}},
				},
			},
		},
	}

	eventBus := telemetry.NewEventBus(128)

	// Create a minimal notifier for testing
	notifier, err := alert.NewNotifier(cfg.Settings.Wake.Alerts, nil, alert.Options{
		Getenv: func(k string) string {
			if k == "TEST_ALERT_URL" {
				return "https://example.com/webhook"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("failed to create notifier: %v", err)
	}
	defer notifier.Close()

	// Build the runtime with alert notifier
	builder := &runtimeBuilder{
		ctx:      context.Background(),
		cfg:      cfg,
		eventBus: eventBus,
		opts: BuildOptions{
			AlertNotifier: notifier,
		},
		agents: make(map[string]*agent.Agent),
	}

	// Build agent registry at depth 1 (spawned child)
	reg := builder.buildAgentToolRegistryDepth(&cfg.Agents[0], 1)

	// Verify send_alert tool is NOT registered at depth > 0
	if reg.GetTool("send_alert") != nil {
		t.Error("expected send_alert tool to NOT be registered at depth > 0")
	}
}

// TestAlertNotifierCreation verifies that alert notifier can be created
// with valid configuration.
func TestAlertNotifierCreation(t *testing.T) {
	alerts := config.WakeAlerts{
		Sinks: []config.AlertSink{{
			Name:       "webhook",
			Type:       "webhook",
			URLEnv:     "TEST_ALERT_URL",
			AllowHosts: []string{"example.com"},
		}},
	}

	notifier, err := alert.NewNotifier(alerts, nil, alert.Options{
		Getenv: func(k string) string {
			if k == "TEST_ALERT_URL" {
				return "https://example.com/webhook"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("expected notifier creation to succeed with valid config, got: %v", err)
	}
	defer notifier.Close()

	if notifier == nil {
		t.Fatal("expected notifier to be non-nil")
	}
}

// TestAlertNotifierCreationWithMissingEnv verifies that alert notifier
// creation gracefully handles missing environment variables.
func TestAlertNotifierCreationWithMissingEnv(t *testing.T) {
	alerts := config.WakeAlerts{
		Sinks: []config.AlertSink{{
			Name:       "webhook",
			Type:       "webhook",
			URLEnv:     "NONEXISTENT_ALERT_URL", // This env var won't be set
			AllowHosts: []string{"example.com"},
		}},
	}

	notifier, err := alert.NewNotifier(alerts, nil, alert.Options{
		Getenv: func(k string) string {
			return "" // Always return empty
		},
	})

	// NewNotifier returns an error when a required environment variable is missing
	if err == nil {
		t.Fatalf("expected notifier creation to fail when env var is missing")
	}
	if notifier != nil {
		notifier.Close()
	}
}
