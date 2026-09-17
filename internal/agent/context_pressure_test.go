package agent

import (
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// newPressureMonitor creates a ContextMonitor with "auto" strategy and custom pressure thresholds.
func newPressureMonitor(budgetThreshold, retrievalThreshold float64) *ContextMonitor {
	return NewContextMonitor(config.ContextConfig{
		Strategy:                  "auto",
		WindowSize:                10,
		KeepRecent:                2,
		AutoFullThreshold:         1000, // very high — ensure pressure wins in pressure tests
		AutoCompressThreshold:     2000,
		ContextBudgetThreshold:    budgetThreshold,
		ContextRetrievalThreshold: retrievalThreshold,
	})
}

func TestPressure_BelowAllThresholds(t *testing.T) {
	cm := newPressureMonitor(0.75, 0.90)
	sl := &StepLog{Query: "q"}

	// Set pressure below both thresholds
	cm.SetTokenPressure(5000, 10000) // 50%

	cm.BuildHistory(sl, makeMessages(5))
	if cm.lastApplied != "full" {
		t.Errorf("lastApplied = %q, want %q", cm.lastApplied, "full")
	}
	if got := cm.Pressure(); got < 0.49 || got > 0.51 {
		t.Errorf("Pressure() = %f, want ~0.50", got)
	}
}

func TestPressure_AtBudgetThreshold(t *testing.T) {
	cm := newPressureMonitor(0.75, 0.90)
	sl := &StepLog{Query: "q"}

	// Exactly at budget threshold
	cm.SetTokenPressure(7500, 10000) // 75%

	cm.BuildHistory(sl, makeMessages(5))
	if cm.lastApplied != "sliding_window" {
		t.Errorf("lastApplied = %q, want %q", cm.lastApplied, "sliding_window")
	}
}

func TestPressure_AboveRetrievalThreshold(t *testing.T) {
	cm := newPressureMonitor(0.75, 0.90)
	sl := &StepLog{Query: "q"}

	// Above retrieval threshold → step_log
	cm.SetTokenPressure(9200, 10000) // 92%

	cm.BuildHistory(sl, makeMessages(5))
	if cm.lastApplied != "step_log" {
		t.Errorf("lastApplied = %q, want %q", cm.lastApplied, "step_log")
	}
}

func TestPressure_WinsOverMessageCount(t *testing.T) {
	// Message count thresholds are set very high; only 5 messages in history.
	// But token pressure is above budgetThreshold → should escalate to sliding_window.
	cm := newPressureMonitor(0.75, 0.90)
	sl := &StepLog{Query: "q"}

	cm.SetTokenPressure(8000, 10000) // 80% — above budget threshold

	cm.BuildHistory(sl, makeMessages(5)) // only 5 messages (would be "full" by message count)
	if cm.lastApplied != "sliding_window" {
		t.Errorf("lastApplied = %q, want %q (pressure should win over msg count)", cm.lastApplied, "sliding_window")
	}
}

func TestPressure_ZeroFallsBackToMessageCount(t *testing.T) {
	// No SetTokenPressure called → pressure == 0 → fall back to message count.
	cm := NewContextMonitor(config.ContextConfig{
		Strategy:              "auto",
		WindowSize:            10,
		KeepRecent:            2,
		AutoFullThreshold:     30,
		AutoCompressThreshold: 60,
	})
	sl := &StepLog{Query: "q"}

	// 35 messages: above autoFullThreshold(30) → sliding_window
	cm.BuildHistory(sl, makeMessages(35))
	if cm.lastApplied != "sliding_window" {
		t.Errorf("lastApplied = %q, want %q (msg count fallback)", cm.lastApplied, "sliding_window")
	}
}

func TestPressure_Concurrent(t *testing.T) {
	cm := newPressureMonitor(0.75, 0.90)
	sl := &StepLog{Query: "q"}
	history := makeMessages(5)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cm.SetTokenPressure(n*100, 10000)
			cm.BuildHistory(sl, history)
			_ = cm.Pressure()
		}(i)
	}
	wg.Wait()
	// No race detector errors = pass
}
