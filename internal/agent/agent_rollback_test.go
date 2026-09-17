package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// rollbackCaptureEvents subscribes to bus and returns a snapshot getter plus a
// stop func. Call stop() before reading the snapshot so all in-flight events
// have been delivered.
func rollbackCaptureEvents(bus *telemetry.EventBus) (get func() []telemetry.AgentEvent, stop func()) {
	ch := bus.Subscribe()
	var mu sync.Mutex
	var events []telemetry.AgentEvent
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()
	stop = func() { bus.Unsubscribe(ch); <-done }
	get = func() []telemetry.AgentEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]telemetry.AgentEvent, len(events))
		copy(out, events)
		return out
	}
	return get, stop
}

func decodeRollbackPayload(t *testing.T, raw json.RawMessage) telemetry.RollbackPayload {
	t.Helper()
	var p telemetry.RollbackPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode RollbackPayload: %v", err)
	}
	return p
}

// TestRollback_DeadEndRecovery covers the core recovery case: when every tool call in an
// iteration fails, the agent rewinds that iteration out of its conversation
// history — so the error never pollutes the LLM context — and retries from a
// clean checkpoint. The next iteration commits a clean answer.
func TestRollback_DeadEndRecovery(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	failTool := newMockTool("broken", "")
	failTool.err = errors.New("connection refused")

	provider := newSequenceProvider(
		toolCallResponse("I'll use the broken tool", tc("broken", map[string]interface{}{"query": "x"})),
		stopResponse("here is the final answer"),
	)
	settings := &config.AgentSettings{Rollback: config.RollbackConfig{Enabled: true}}
	a := newE2EAgentWithSettings("worker", provider, bus, settings, failTool)

	get, stop := rollbackCaptureEvents(bus)
	result, err := a.Run(context.Background(), "do the task")
	stop()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "final answer") {
		t.Errorf("expected recovered final answer, got %q", result)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected 2 LLM calls (1 dead-end + 1 recovery), got %d", provider.callCount())
	}

	rollbacks := filterEvents(get(), telemetry.EventRollback)
	if len(rollbacks) != 1 {
		t.Fatalf("expected 1 ROLLBACK event, got %d", len(rollbacks))
	}
	p := decodeRollbackPayload(t, rollbacks[0].Payload)
	if p.Trigger != "tool_error" {
		t.Errorf("Trigger = %q, want tool_error", p.Trigger)
	}
	if p.Iteration != 0 || p.RollbackNum != 1 {
		t.Errorf("Iteration/RollbackNum = %d/%d, want 0/1", p.Iteration, p.RollbackNum)
	}
	if p.MessagesPruned < 2 {
		t.Errorf("MessagesPruned = %d, want >=2 (assistant tool-call + tool result)", p.MessagesPruned)
	}

	// The post-rollback LLM call must see a CLEAN context: the failed tool
	// call and its error output were pruned; only the self-correction
	// directive remains.
	recovery := provider.getCall(1)
	for _, m := range recovery.History {
		if m.Role == "tool" {
			t.Errorf("post-rollback history still contains a tool message: %+v", m)
		}
		if strings.Contains(m.AsText(), "connection refused") {
			t.Errorf("post-rollback history still contains the tool error: %q", m.AsText())
		}
	}
	sawDirective := false
	for _, m := range recovery.History {
		if strings.Contains(m.AsText(), "[SELF-CORRECTION]") {
			sawDirective = true
		}
	}
	if !sawDirective {
		t.Error("post-rollback history should contain the [SELF-CORRECTION] directive")
	}
}

// TestRollback_LoopGuard verifies rollbacks are capped at MaxRollbacks: a
// persistently-failing path cannot rewind forever — once the cap is hit the
// loop proceeds normally and terminates at max_iterations.
func TestRollback_LoopGuard(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	failTool := newMockTool("broken", "")
	failTool.err = errors.New("always fails")

	// Every iteration calls the broken tool. Provide ample responses so the
	// sequenceProvider never panics before max_iterations halts the loop.
	calls := make([]llm.GenerateResult, 12)
	for i := range calls {
		calls[i] = toolCallResponse("retry", tc("broken", map[string]interface{}{"query": "x"}))
	}
	provider := newSequenceProvider(calls...)

	settings := &config.AgentSettings{
		MaxIterations: 6,
		Rollback:      config.RollbackConfig{Enabled: true, MaxRollbacks: 2},
	}
	a := newE2EAgentWithSettings("worker", provider, bus, settings, failTool)

	get, stop := rollbackCaptureEvents(bus)
	_, err := a.Run(context.Background(), "do the task")
	stop()

	if err != nil {
		t.Fatalf("Run should terminate gracefully at max_iterations, got: %v", err)
	}
	if n := len(filterEvents(get(), telemetry.EventRollback)); n != 2 {
		t.Fatalf("expected exactly 2 ROLLBACK events (MaxRollbacks=2), got %d", n)
	}
}

// TestRollback_ConfigDefaults verifies NewAgent fills the MaxRollbacks and
// Triggers defaults, and that rollback stays disabled when unconfigured.
func TestRollback_ConfigDefaults(t *testing.T) {
	bus := telemetry.NewEventBus(16)

	enabled := newE2EAgentWithSettings("w", newSequenceProvider(), bus,
		&config.AgentSettings{Rollback: config.RollbackConfig{Enabled: true}})
	if enabled.rollback.MaxRollbacks != 3 {
		t.Errorf("default MaxRollbacks = %d, want 3", enabled.rollback.MaxRollbacks)
	}
	if len(enabled.rollback.Triggers) != 1 || enabled.rollback.Triggers[0] != "tool_error" {
		t.Errorf("default Triggers = %v, want [tool_error]", enabled.rollback.Triggers)
	}

	noSettings := newE2EAgent("w", newSequenceProvider(), bus)
	if noSettings.rollback.Enabled {
		t.Error("rollback must be disabled when no settings are provided")
	}
}

// TestRollback_DisabledKeepsErrorInContext is the regression guard: with
// rollback off, a failed tool call stays in history (existing behavior) and
// no ROLLBACK event is emitted.
func TestRollback_DisabledKeepsErrorInContext(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	failTool := newMockTool("broken", "")
	failTool.err = errors.New("connection refused")

	provider := newSequenceProvider(
		toolCallResponse("use broken", tc("broken", map[string]interface{}{"query": "x"})),
		stopResponse("answer anyway"),
	)
	a := newE2EAgent("worker", provider, bus, failTool) // no settings → rollback disabled

	get, stop := rollbackCaptureEvents(bus)
	_, err := a.Run(context.Background(), "do the task")
	stop()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(filterEvents(get(), telemetry.EventRollback)); n != 0 {
		t.Fatalf("expected 0 ROLLBACK events with rollback disabled, got %d", n)
	}
	recovery := provider.getCall(1)
	sawToolErr := false
	for _, m := range recovery.History {
		if strings.Contains(m.AsText(), "connection refused") {
			sawToolErr = true
		}
	}
	if !sawToolErr {
		t.Error("with rollback disabled the tool error should remain in history")
	}
}

// TestRollback_LLMSelfJudge verifies the opt-in secondary trigger: when no
// deterministic trigger fires (the tool call succeeded), an enabled LLM
// self-judge that returns a DEAD_END verdict still triggers a rollback.
func TestRollback_LLMSelfJudge(t *testing.T) {
	bus := telemetry.NewEventBus(256)

	// A tool that SUCCEEDS — so the deterministic tool_error trigger stays quiet.
	okTool := newMockTool("worktool", "ok, did something")

	provider := newSequenceProvider(
		// iter 0: a successful tool call.
		toolCallResponse("using worktool", tc("worktool", map[string]interface{}{"query": "x"})),
		// the LLM self-judge call for iter 0 — returns a dead-end verdict.
		stopResponse("VERDICT: DEAD_END — the step did not advance the task"),
		// iter 1 after rollback: a clean final answer.
		stopResponse("here is the final answer"),
	)
	settings := &config.AgentSettings{
		Rollback: config.RollbackConfig{Enabled: true, LLMSelfJudge: true},
	}
	a := newE2EAgentWithSettings("worker", provider, bus, settings, okTool)

	get, stop := rollbackCaptureEvents(bus)
	result, err := a.Run(context.Background(), "do the task")
	stop()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "final answer") {
		t.Errorf("expected recovered final answer, got %q", result)
	}
	rollbacks := filterEvents(get(), telemetry.EventRollback)
	if len(rollbacks) != 1 {
		t.Fatalf("expected 1 ROLLBACK event from the LLM self-judge, got %d", len(rollbacks))
	}
	p := decodeRollbackPayload(t, rollbacks[0].Payload)
	if p.Trigger != "llm_judge" {
		t.Errorf("Trigger = %q, want llm_judge", p.Trigger)
	}
}

// TestRollback_HotReload verifies the agent re-reads its RollbackConfig from
// the source YAML on each Run when SetConfigReloadPath is set, so a config
// edit takes effect without a restart.
func TestRollback_HotReload(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	path := filepath.Join(t.TempDir(), "cfg.yaml")

	writeCfg := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	writeCfg("name: t\nagents:\n  - name: worker\n    settings:\n      rollback:\n        enabled: true\n        max_rollbacks: 5\n")

	a := newE2EAgent("worker", newSequenceProvider(), bus)
	if a.rollback.Enabled {
		t.Fatal("agent built without settings should start with rollback disabled")
	}

	a.SetConfigReloadPath(path)
	a.reloadRollback()
	if !a.rollback.Enabled || a.rollback.MaxRollbacks != 5 {
		t.Fatalf("after reload: Enabled=%v MaxRollbacks=%d, want true/5", a.rollback.Enabled, a.rollback.MaxRollbacks)
	}

	// Edit the file — disable rollback — and reload again.
	writeCfg("name: t\nagents:\n  - name: worker\n    settings:\n      rollback:\n        enabled: false\n")
	a.reloadRollback()
	if a.rollback.Enabled {
		t.Error("after the second reload rollback should be disabled")
	}
}
