package agent

import (
	"fmt"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// makeMessages returns a slice of alternating user/assistant messages.
func makeMessages(n int) []llm.Message {
	msgs := make([]llm.Message, n)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = llm.NewTextMessage("user", fmt.Sprintf("user msg %d", i))
		} else {
			msgs[i] = llm.NewTextMessage("assistant", fmt.Sprintf("assistant msg %d", i))
		}
	}
	return msgs
}

// newAutoMonitor creates a ContextMonitor with "auto" strategy and given thresholds.
func newAutoMonitor(fullThreshold, compressThreshold int) *ContextMonitor {
	return NewContextMonitor(config.ContextConfig{
		Strategy:              "auto",
		WindowSize:            10,
		KeepRecent:            2,
		AutoFullThreshold:     fullThreshold,
		AutoCompressThreshold: compressThreshold,
	})
}

// --- Unit Tests ---

func TestBuildAuto_BelowFullThreshold_ReturnsRawHistory(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	history := makeMessages(20) // below 30
	sl := &StepLog{Query: "q"}

	result, _ := cm.BuildHistory(sl, history)
	if len(result) != len(history) {
		t.Errorf("expected full history (%d), got %d", len(history), len(result))
	}
	if cm.lastApplied != "full" {
		t.Errorf("lastApplied = %q, want %q", cm.lastApplied, "full")
	}
}

func TestBuildAuto_AtFullThreshold_UsesSlidingWindow(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	history := makeMessages(30) // exactly 30, should switch to sliding_window
	sl := &StepLog{Query: "q"}

	result, _ := cm.BuildHistory(sl, history)
	if len(result) >= len(history) {
		t.Errorf("expected sliding_window to reduce history (got %d, input %d)", len(result), len(history))
	}
	if cm.lastApplied != "sliding_window" {
		t.Errorf("lastApplied = %q, want %q", cm.lastApplied, "sliding_window")
	}
}

func TestBuildAuto_BetweenThresholds_UsesSlidingWindow(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	history := makeMessages(45) // between 30 and 60
	sl := &StepLog{Query: "q"}

	cm.BuildHistory(sl, history)
	if cm.lastApplied != "sliding_window" {
		t.Errorf("lastApplied = %q, want sliding_window", cm.lastApplied)
	}
}

func TestBuildAuto_AtCompressThreshold_UsesStepLog(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	history := makeMessages(60)
	sl := &StepLog{Query: "q", Steps: []Step{{Thought: "step1"}}}

	cm.BuildHistory(sl, history)
	if cm.lastApplied != "step_log" {
		t.Errorf("lastApplied = %q, want step_log", cm.lastApplied)
	}
}

func TestBuildAuto_AboveCompressThreshold_UsesStepLog(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	history := makeMessages(100) // above 60
	sl := &StepLog{Query: "q", Steps: []Step{{Thought: "step1"}}}

	result, _ := cm.BuildHistory(sl, history)
	if cm.lastApplied != "step_log" {
		t.Errorf("lastApplied = %q, want step_log", cm.lastApplied)
	}
	// step_log result should not be the raw history
	if len(result) == len(history) {
		t.Error("expected step_log to produce fewer messages than raw history")
	}
}

func TestAppliedStrategy_NonAuto_ReturnsStrategy(t *testing.T) {
	for _, strat := range []string{"full", "sliding_window", "step_log"} {
		cm := NewContextMonitor(config.ContextConfig{Strategy: strat, WindowSize: 10, KeepRecent: 2})
		if got := cm.AppliedStrategy(); got != strat {
			t.Errorf("strategy=%q: AppliedStrategy() = %q, want %q", strat, got, strat)
		}
	}
}

func TestAppliedStrategy_Auto_ReturnsLastApplied(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	// Before first BuildHistory, lastApplied is ""
	if got := cm.AppliedStrategy(); got != "" {
		t.Errorf("before first call, AppliedStrategy() = %q, want empty", got)
	}

	sl := &StepLog{Query: "q"}
	cm.BuildHistory(sl, makeMessages(5))
	if got := cm.AppliedStrategy(); got != "full" {
		t.Errorf("AppliedStrategy() = %q, want full", got)
	}

	cm.BuildHistory(sl, makeMessages(40))
	if got := cm.AppliedStrategy(); got != "sliding_window" {
		t.Errorf("AppliedStrategy() = %q, want sliding_window", got)
	}
}

func TestNeedsStepLog_Auto_ReturnsTrue(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	if !cm.NeedsStepLog() {
		t.Error("NeedsStepLog() should be true for auto strategy")
	}
}

func TestNeedsStepLog_Full_ReturnsFalse(t *testing.T) {
	cm := NewContextMonitor(config.ContextConfig{Strategy: "full"})
	if cm.NeedsStepLog() {
		t.Error("NeedsStepLog() should be false for full strategy")
	}
}

func TestEscalationDetected_FullToSlidingWindow(t *testing.T) {
	cm := newAutoMonitor(10, 30)
	sl := &StepLog{Query: "q"}

	cm.BuildHistory(sl, makeMessages(5))
	prev := cm.AppliedStrategy()
	if prev != "full" {
		t.Fatalf("expected full before escalation, got %q", prev)
	}

	cm.BuildHistory(sl, makeMessages(15))
	next := cm.AppliedStrategy()
	if next != "sliding_window" {
		t.Fatalf("expected sliding_window after escalation, got %q", next)
	}
	if prev == next {
		t.Error("strategy should have changed")
	}
}

func TestEscalationDetected_SlidingWindowToStepLog(t *testing.T) {
	cm := newAutoMonitor(10, 20)
	sl := &StepLog{Query: "q", Steps: []Step{{Thought: "t"}}}

	cm.BuildHistory(sl, makeMessages(15)) // sliding_window
	cm.BuildHistory(sl, makeMessages(25)) // step_log
	if cm.AppliedStrategy() != "step_log" {
		t.Errorf("expected step_log, got %q", cm.AppliedStrategy())
	}
}

// TestDefaultThresholds verifies that zero-value config uses default 30/60 thresholds.
func TestDefaultThresholds(t *testing.T) {
	cm := NewContextMonitor(config.ContextConfig{Strategy: "auto", WindowSize: 10, KeepRecent: 2})
	if cm.autoFullThreshold != 30 {
		t.Errorf("default autoFullThreshold = %d, want 30", cm.autoFullThreshold)
	}
	if cm.autoCompressThreshold != 60 {
		t.Errorf("default autoCompressThreshold = %d, want 60", cm.autoCompressThreshold)
	}
}

// --- Stress Tests ---

// TestStress_MonotonicEscalation verifies that auto-strategy never de-escalates.
// 200 sequential BuildHistory calls on growing history.
func TestStress_MonotonicEscalation(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	sl := &StepLog{Query: "q", Steps: []Step{{Thought: "step"}}}

	stratOrder := map[string]int{"full": 0, "sliding_window": 1, "step_log": 2}
	maxSeen := 0

	for i := 1; i <= 200; i++ {
		history := makeMessages(i)
		cm.BuildHistory(sl, history)
		strat := cm.AppliedStrategy()
		rank, ok := stratOrder[strat]
		if !ok {
			t.Fatalf("iteration %d: unknown strategy %q", i, strat)
		}
		if rank < maxSeen {
			t.Errorf("iteration %d: strategy de-escalated from rank %d to %d (%q)", i, maxSeen, rank, strat)
		}
		if rank > maxSeen {
			maxSeen = rank
		}
	}
}

// TestStress_ConcurrentBuildHistory verifies no data race under concurrent BuildHistory calls.
func TestStress_ConcurrentBuildHistory(t *testing.T) {
	cm := newAutoMonitor(30, 60)
	sl := &StepLog{Query: "concurrent", Steps: []Step{{Thought: "step"}}}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		n := (i % 90) + 1 // vary message count: 1–90
		go func(msgCount int) {
			defer wg.Done()
			history := makeMessages(msgCount)
			_, _ = cm.BuildHistory(sl, history)
		}(n)
	}
	wg.Wait()
}

// TestStress_100IterationLoop simulates a realistic 100-iteration ReAct loop,
// verifying strategy escalation happens exactly at the right iteration boundaries
// and that the event-detection pattern (prevStrategy != newStrategy) fires correctly.
func TestStress_100IterationLoop(t *testing.T) {
	const fullThresh = 20
	const compressThresh = 40
	cm := newAutoMonitor(fullThresh, compressThresh)
	sl := &StepLog{Query: "q", Steps: []Step{{Thought: "step"}}}

	history := make([]llm.Message, 0, 100)
	escalations := 0
	var prevStrat string

	for i := 0; i < 100; i++ {
		// Add 2 messages per iteration (user + assistant — like a real ReAct turn)
		history = append(history,
			llm.NewTextMessage("user", fmt.Sprintf("user %d", i)),
			llm.NewTextMessage("assistant", fmt.Sprintf("asst %d", i)),
		)

		before := cm.AppliedStrategy()
		cm.BuildHistory(sl, history)
		after := cm.AppliedStrategy()

		if after != before && before != "" {
			escalations++
		}
		prevStrat = after
		_ = prevStrat
	}

	// At fullThresh=20 msgs, we escalate to sliding_window (~iteration 10)
	// At compressThresh=40 msgs, we escalate to step_log (~iteration 20)
	// So we expect exactly 2 escalations
	if escalations != 2 {
		t.Errorf("expected 2 escalations, got %d", escalations)
	}
}

// TestStress_ConcurrentMultipleMonitors verifies independent ContextMonitors
// don't interfere with each other under concurrent use.
func TestStress_ConcurrentMultipleMonitors(t *testing.T) {
	const numMonitors = 20
	const goroutinesPerMonitor = 5

	var wg sync.WaitGroup
	wg.Add(numMonitors * goroutinesPerMonitor)

	for m := 0; m < numMonitors; m++ {
		cm := newAutoMonitor(10, 20)
		sl := &StepLog{Query: "concurrent", Steps: []Step{{Thought: "step"}}}

		for g := 0; g < goroutinesPerMonitor; g++ {
			n := (g*3+m)%30 + 1
			go func(monitor *ContextMonitor, msgCount int) {
				defer wg.Done()
				history := makeMessages(msgCount)
				_, _ = monitor.BuildHistory(sl, history)
				_ = monitor.AppliedStrategy()
			}(cm, n)
		}
	}
	wg.Wait()
}
