package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// tgDecodeError decodes an ErrorPayload (helper local to package agent; the
// agent_test package has its own decoders).
func tgDecodeError(t *testing.T, raw json.RawMessage) telemetry.ErrorPayload {
	t.Helper()
	var p telemetry.ErrorPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode ErrorPayload: %v", err)
	}
	return p
}

func tgCountErrorType(t *testing.T, events []telemetry.AgentEvent, errorType string) int {
	t.Helper()
	n := 0
	for _, ev := range events {
		if ev.EventType == telemetry.EventError && tgDecodeError(t, ev.Payload).ErrorType == errorType {
			n++
		}
	}
	return n
}

// tgAgentEnd returns the AGENT_END status and how many AGENT_END events fired.
func tgAgentEnd(t *testing.T, events []telemetry.AgentEvent) (string, int) {
	t.Helper()
	var status string
	n := 0
	for _, ev := range events {
		if ev.EventType == telemetry.EventAgentEnd {
			var p telemetry.AgentEndPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode AgentEndPayload: %v", err)
			}
			status = p.Status
			n++
		}
	}
	return status, n
}

// TestTurnGuard_NoDelegation_RetryRecovers: a force_delegation turn that first
// answers directly (zero invoke_config calls) gets one corrective retry; when
// the retry actually calls invoke_config, the turn succeeds with no marker.
func TestTurnGuard_NoDelegation_RetryRecovers(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	invoke := newMockTool("invoke_config", "delegated answer")
	provider := newSequenceProvider(
		stopResponse("Sure, here's the answer from my own knowledge."),                     // iter0: no delegation
		toolCallResponse("", tc("invoke_config", map[string]interface{}{"task": "do it"})), // iter1: delegates
		stopResponse("Here is the relayed answer."),                                        // iter2: clean answer
	)
	a := newE2EAgent("ChatHost", provider, bus, invoke)
	a.SetTurnGuard(NewTurnGuard(TurnGuardConfig{ForceDelegation: true}))

	var result string
	var err error
	evs := collectEvents(bus, func() { result, err = a.Run(context.Background(), "what is X?") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.HasPrefix(result, markerNoDelegation) {
		t.Errorf("recovered turn should carry no marker, got: %q", result)
	}
	if result != "Here is the relayed answer." {
		t.Errorf("result = %q, want the clean relayed answer", result)
	}
	if invoke.callCount() != 1 {
		t.Errorf("invoke_config called %d times, want 1", invoke.callCount())
	}
	if got := tgCountErrorType(t, evs, "turn_guard_retry"); got != 1 {
		t.Errorf("turn_guard_retry events = %d, want 1", got)
	}
	if got := tgCountErrorType(t, evs, "turn_guard_unrecovered"); got != 0 {
		t.Errorf("turn_guard_unrecovered events = %d, want 0", got)
	}
	if status, n := tgAgentEnd(t, evs); status != "success" || n != 1 {
		t.Errorf("AGENT_END status=%q count=%d, want success/1", status, n)
	}
}

// TestTurnGuard_NoDelegation_RetryFails: when the model answers directly on
// both the original turn and the single retry, the breach is surfaced with a
// [NO-DELEGATION] marker + no_delegation status, exactly one retry, and one
// AGENT_END (no infinite loop).
func TestTurnGuard_NoDelegation_RetryFails(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	invoke := newMockTool("invoke_config", "delegated answer")
	provider := newSequenceProvider(
		stopResponse("Direct answer one."),
		stopResponse("Direct answer two, still no delegation."),
	)
	a := newE2EAgent("ChatHost", provider, bus, invoke)
	a.SetTurnGuard(NewTurnGuard(TurnGuardConfig{ForceDelegation: true}))

	var result string
	var err error
	evs := collectEvents(bus, func() { result, err = a.Run(context.Background(), "what is X?") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(result, markerNoDelegation) {
		t.Errorf("unrecovered turn should carry %s marker, got: %q", markerNoDelegation, result)
	}
	if invoke.callCount() != 0 {
		t.Errorf("invoke_config should never have been called, got %d", invoke.callCount())
	}
	if provider.callCount() != 2 {
		t.Errorf("provider called %d times, want 2 (original + one retry)", provider.callCount())
	}
	if got := tgCountErrorType(t, evs, "turn_guard_retry"); got != 1 {
		t.Errorf("turn_guard_retry events = %d, want 1", got)
	}
	if got := tgCountErrorType(t, evs, "turn_guard_unrecovered"); got != 1 {
		t.Errorf("turn_guard_unrecovered events = %d, want 1", got)
	}
	if status, n := tgAgentEnd(t, evs); status != statusNoDelegation || n != 1 {
		t.Errorf("AGENT_END status=%q count=%d, want %s/1", status, n, statusNoDelegation)
	}
}

// TestTurnGuard_FabricatedStore_RetryFails: a turn that claims a memory write
// with zero MEMORY_WRITE gets one retry then a [UNVERIFIED CLAIM] marker.
func TestTurnGuard_FabricatedStore_RetryFails(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	memAdd := newMockTool("memory_add", "stored")
	provider := newSequenceProvider(
		stopResponse("Done — I've saved that as `note-1`."),
		stopResponse("Done — I've saved that as `note-1`."),
	)
	a := newE2EAgent("memwriter", provider, bus, memAdd)
	a.SetTurnGuard(NewTurnGuard(TurnGuardConfig{CheckStorageClaims: true}))

	var result string
	var err error
	evs := collectEvents(bus, func() { result, err = a.Run(context.Background(), "remember my timezone is JST") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(result, markerUnverified) {
		t.Errorf("fabricated store should carry %s marker, got: %q", markerUnverified, result)
	}
	if memAdd.callCount() != 0 {
		t.Errorf("memory_add should never have been called, got %d", memAdd.callCount())
	}
	if status, _ := tgAgentEnd(t, evs); status != statusUnverified {
		t.Errorf("AGENT_END status=%q, want %s", status, statusUnverified)
	}
}

// TestTurnGuard_RealWrite_NotFlagged: the same storage claim is accepted when
// a real memory_add ran that turn — proving the ground-truth cross-check.
func TestTurnGuard_RealWrite_NotFlagged(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	memAdd := newMockTool("memory_add", "stored")
	provider := newSequenceProvider(
		toolCallResponse("", tc("memory_add", map[string]interface{}{"content": "tz=JST"})),
		stopResponse("Done — I've saved that as `note-1`."),
	)
	a := newE2EAgent("memwriter", provider, bus, memAdd)
	a.SetTurnGuard(NewTurnGuard(TurnGuardConfig{CheckStorageClaims: true}))

	var result string
	var err error
	evs := collectEvents(bus, func() { result, err = a.Run(context.Background(), "remember my timezone is JST") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.HasPrefix(result, markerUnverified) {
		t.Errorf("real write should NOT be flagged, got: %q", result)
	}
	if memAdd.callCount() != 1 {
		t.Errorf("memory_add called %d times, want 1", memAdd.callCount())
	}
	if status, _ := tgAgentEnd(t, evs); status != "success" {
		t.Errorf("AGENT_END status=%q, want success", status)
	}
}

// TestTurnGuard_Disabled_NoBehaviorChange: with no SetTurnGuard call, a turn
// that would trip every check returns byte-identical to today.
func TestTurnGuard_Disabled_NoBehaviorChange(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(stopResponse("I've stored that for you."))
	a := newE2EAgent("plain", provider, bus)

	result, err := a.Run(context.Background(), "remember X")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "I've stored that for you." {
		t.Errorf("disabled guard must not alter the answer, got: %q", result)
	}
}

// TestTurnGuard_LastIteration_NoSilentBypass: at maxIterations=1 a no-delegation
// turn has no room to retry; the marker path must still run (not fall through
// to the bottom max-iterations exit), and AGENT_END fires exactly once.
func TestTurnGuard_LastIteration_NoSilentBypass(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	invoke := newMockTool("invoke_config", "delegated answer")
	provider := newSequenceProvider(stopResponse("Direct answer, no delegation."))
	a := newE2EAgentWithSettings("ChatHost", provider, bus,
		&config.AgentSettings{MaxIterations: 1}, invoke)
	a.SetTurnGuard(NewTurnGuard(TurnGuardConfig{ForceDelegation: true}))

	var result string
	var err error
	evs := collectEvents(bus, func() { result, err = a.Run(context.Background(), "what is X?") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(result, markerNoDelegation) {
		t.Errorf("last-iteration breach should still be marked, got: %q", result)
	}
	if provider.callCount() != 1 {
		t.Errorf("provider called %d times, want 1 (no retry possible)", provider.callCount())
	}
	if got := tgCountErrorType(t, evs, "turn_guard_retry"); got != 0 {
		t.Errorf("turn_guard_retry events = %d, want 0 (no iterations left)", got)
	}
	if status, n := tgAgentEnd(t, evs); status != statusNoDelegation || n != 1 {
		t.Errorf("AGENT_END status=%q count=%d, want %s/1", status, n, statusNoDelegation)
	}
}
