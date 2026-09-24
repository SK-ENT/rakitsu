package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/spf13/cobra"
)

// newTimeoutTestCmd builds a fresh command bound to the same package-level flag
// vars as runCmd, so resolve* precedence can be exercised without the global
// runCmd state. Binding via pflag resets timeoutSeconds/idleTimeoutSeconds to
// their defaults (off / 0).
func newTimeoutTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "run"}
	c.Flags().IntVarP(&timeoutSeconds, "timeout", "t", defaultTimeoutSeconds, "")
	c.Flags().IntVar(&idleTimeoutSeconds, "idle-timeout", 0, "")
	return c
}

func TestContextWithOptionalTimeout(t *testing.T) {
	// Positive timeout -> a deadline is set.
	ctx, cancel := contextWithOptionalTimeout(context.Background(), 5)
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("positive timeout: expected a deadline, got none")
	}
	cancel()

	// Zero and negative -> no deadline (the "ignore timeout" escape hatch).
	for _, ts := range []int{0, -1} {
		ctx, cancel := contextWithOptionalTimeout(context.Background(), ts)
		if _, ok := ctx.Deadline(); ok {
			t.Fatalf("timeout=%d: expected no deadline, got one", ts)
		}
		cancel()
	}
}

// TestResolveTimeoutSeconds_WiresYAMLField is the regression guard for the dead
// settings.execution.timeout_seconds field: before this fix only --timeout was
// honored and the YAML value was silently ignored.
func TestResolveTimeoutSeconds_WiresYAMLField(t *testing.T) {
	tests := []struct {
		name      string
		yaml      int
		flagSet   string // "" = leave --timeout untouched
		wantValue int
	}{
		{"unset everywhere -> off", 0, "", 0},
		{"yaml override honored", 1800, "", 1800},
		{"yaml unlimited (negative)", -1, "", -1},
		{"flag wins over yaml", 1800, "600", 600},
		{"flag zero = unlimited", 0, "0", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newTimeoutTestCmd()
			cfg := &config.Config{}
			cfg.Settings.Execution.TimeoutSeconds = tc.yaml
			if tc.flagSet != "" {
				if err := cmd.Flags().Set("timeout", tc.flagSet); err != nil {
					t.Fatalf("set --timeout: %v", err)
				}
			}
			if got := resolveTimeoutSeconds(cmd, cfg); got != tc.wantValue {
				t.Fatalf("resolveTimeoutSeconds = %d, want %d", got, tc.wantValue)
			}
		})
	}
}

func TestResolveIdleSeconds_WiresYAMLField(t *testing.T) {
	tests := []struct {
		name    string
		yaml    int
		flagSet string
		want    int
	}{
		{"unset -> disabled", 0, "", 0},
		{"yaml override honored", 300, "", 300},
		{"flag wins over yaml", 300, "60", 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newTimeoutTestCmd()
			cfg := &config.Config{}
			cfg.Settings.Execution.IdleTimeoutSeconds = tc.yaml
			if tc.flagSet != "" {
				if err := cmd.Flags().Set("idle-timeout", tc.flagSet); err != nil {
					t.Fatalf("set --idle-timeout: %v", err)
				}
			}
			if got := resolveIdleSeconds(cmd, cfg); got != tc.want {
				t.Fatalf("resolveIdleSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool, within time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", within, msg)
}

func TestStartIdleWatchdog_FiresWhenIdle(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var fired atomic.Bool

	startIdleWatchdog(ctx, bus, 80*time.Millisecond, cancel, &fired)

	waitFor(t, fired.Load, 2*time.Second, "watchdog should fire when no events stream")
	// The watchdog cancels before it sets fired, so by here ctx must be done;
	// poll rather than check instantly to stay robust to goroutine scheduling.
	waitFor(t, func() bool { return ctx.Err() != nil }, time.Second, "ctx should be cancelled after the idle watchdog fires")
}

// TestStartIdleWatchdog_ResetByEvents proves the "auto-renew" behavior: a steady
// event stream keeps the timer from firing; it only fires once activity stops.
func TestStartIdleWatchdog_ResetByEvents(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var fired atomic.Bool

	startIdleWatchdog(ctx, bus, 150*time.Millisecond, cancel, &fired)

	// Heartbeat well under the 150ms idle window; the timer must keep resetting.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(30 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				bus.Emit("test", telemetry.EventTokenChunk, struct{}{})
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	if fired.Load() {
		t.Fatal("watchdog fired despite steady streaming activity")
	}

	// Go silent — the watchdog should now fire.
	close(stop)
	<-done
	waitFor(t, fired.Load, 2*time.Second, "watchdog should fire after activity stops")
}

func TestStartIdleWatchdog_DisabledWhenNonPositive(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var fired atomic.Bool

	startIdleWatchdog(ctx, bus, 0, cancel, &fired)

	time.Sleep(120 * time.Millisecond)
	if fired.Load() {
		t.Fatal("watchdog must be disabled when idle <= 0")
	}
	if ctx.Err() != nil {
		t.Fatal("ctx must not be cancelled when the watchdog is disabled")
	}
}

// TestResolveAgentIterations: an agent without its own
// max_iterations uses settings.execution.max_iterations; with neither set and
// the run timeout off it gets no cap (-1). An agent's explicit value is always
// kept, and with a timeout on and no global value the agent default (10)
// stays.
func TestResolveAgentIterations(t *testing.T) {
	newCfg := func(global int) *config.Config {
		cfg := &config.Config{Agents: []config.AgentDefinition{
			{Name: "no-settings"},
			{Name: "unset", Settings: &config.AgentSettings{}},
			{Name: "explicit", Settings: &config.AgentSettings{MaxIterations: 6}},
		}}
		cfg.Settings.Execution.MaxIterations = global
		return cfg
	}
	iters := func(cfg *config.Config) []int {
		out := make([]int, len(cfg.Agents))
		for i, a := range cfg.Agents {
			if a.Settings != nil {
				out[i] = a.Settings.MaxIterations
			}
		}
		return out
	}

	tests := []struct {
		name       string
		global     int
		timeoutSec int
		want       []int
	}{
		{"timeout off, no global -> no cap", 0, 0, []int{-1, -1, 6}},
		{"timeout negative, no global -> no cap", 0, -1, []int{-1, -1, 6}},
		{"timeout on, no global -> agent default", 0, 600, []int{0, 0, 6}},
		{"global cap wins over no-timeout", 5, 0, []int{5, 5, 6}},
		{"global cap with timeout on", 5, 600, []int{5, 5, 6}},
		{"global -1 = no cap even with timeout on", -1, 600, []int{-1, -1, 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newCfg(tc.global)
			resolveAgentIterations(cfg, tc.timeoutSec)
			got := iters(cfg)
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("max_iterations = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
