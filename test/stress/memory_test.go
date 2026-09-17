//go:build stress

package stress

import (
	"runtime"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestMemoryStability: 100 sequential event bus cycles, track goroutine/heap growth.
func TestMemoryStability(t *testing.T) {
	const (
		numCycles     = 100
		eventsPerCycle = 1_000
		numSubs       = 5
	)

	metrics := NewMetrics()

	// Baseline
	runtime.GC()
	var baseStats runtime.MemStats
	runtime.ReadMemStats(&baseStats)
	baseGoroutines := runtime.NumGoroutine()

	for cycle := 0; cycle < numCycles; cycle++ {
		eb := telemetry.NewEventBus(50)

		// Subscribe
		subs := make([]<-chan telemetry.AgentEvent, numSubs)
		for i := 0; i < numSubs; i++ {
			subs[i] = eb.Subscribe()
		}

		// Drain in background
		done := make(chan struct{})
		go func() {
			for _, ch := range subs {
				go func(c <-chan telemetry.AgentEvent) {
					for range c {
					}
				}(ch)
			}
			<-done
		}()

		// Publish
		for i := 0; i < eventsPerCycle; i++ {
			eb.Emit("agent", telemetry.EventAgentMessage, telemetry.AgentMessagePayload{Message: "mem"})
		}

		// Unsubscribe
		for _, ch := range subs {
			eb.Unsubscribe(ch)
		}
		close(done)

		// Snapshot every 10 cycles
		if cycle%10 == 0 {
			metrics.Snapshot()
		}
	}

	// Final measurement
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)
	finalGoroutines := runtime.NumGoroutine()

	heapGrowthMB := (float64(finalStats.HeapAlloc) - float64(baseStats.HeapAlloc)) / (1024 * 1024)
	goroutineGrowth := finalGoroutines - baseGoroutines

	t.Logf("After %d cycles:", numCycles)
	t.Logf("  Heap growth: %.2f MB (base: %.2f MB, final: %.2f MB)",
		heapGrowthMB,
		float64(baseStats.HeapAlloc)/(1024*1024),
		float64(finalStats.HeapAlloc)/(1024*1024))
	t.Logf("  Goroutine growth: %d (base: %d, final: %d)",
		goroutineGrowth, baseGoroutines, finalGoroutines)

	report := metrics.Report()
	report.Print()

	// Check for leaks
	if goroutineGrowth > 20 {
		t.Errorf("Goroutine leak detected: grew by %d", goroutineGrowth)
	}
	if heapGrowthMB > 50 {
		t.Errorf("Excessive heap growth: %.2f MB", heapGrowthMB)
	}
}
