//go:build stress

package stress

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestEventBusThroughput: 100 publishers x 10K events, 50 subscribers.
// Hard-fails if publishing doesn't complete within 60s (deadlock guard).
// Drop rate >5% is logged, not failed — expected under extreme load with
// non-blocking sends, not a correctness bug.
func TestEventBusThroughput(t *testing.T) {
	const (
		numPublishers  = 100
		eventsPerPub   = 10_000
		numSubscribers = 50
		totalEvents    = numPublishers * eventsPerPub
		timeout        = 60 * time.Second
	)

	eb := telemetry.NewEventBus(100)
	metrics := NewMetrics()

	// Start subscribers
	var received int64
	var subWg sync.WaitGroup
	done := make(chan struct{})

	subs := make([]<-chan telemetry.AgentEvent, numSubscribers)
	for i := 0; i < numSubscribers; i++ {
		subs[i] = eb.Subscribe()
	}

	for i := 0; i < numSubscribers; i++ {
		subWg.Add(1)
		go func(ch <-chan telemetry.AgentEvent) {
			defer subWg.Done()
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
					atomic.AddInt64(&received, 1)
					metrics.AddEventReceived()
				case <-done:
					// Drain remaining
					for range ch {
						atomic.AddInt64(&received, 1)
						metrics.AddEventReceived()
					}
					return
				}
			}
		}(subs[i])
	}

	// Start publishers
	pubCtx, pubCancel := context.WithCancel(context.Background())
	defer pubCancel()

	var pubWg sync.WaitGroup
	start := time.Now()

	for p := 0; p < numPublishers; p++ {
		pubWg.Add(1)
		go func(pid int) {
			defer pubWg.Done()
			for e := 0; e < eventsPerPub; e++ {
				select {
				case <-pubCtx.Done():
					return
				default:
				}
				eb.Emit("agent", telemetry.EventAgentMessage, telemetry.AgentMessagePayload{
					Message: "stress",
					Type:    "test",
				})
				metrics.AddEventSent()
			}
		}(p)
	}

	// close(done) unblocks the subscriber goroutines' "done" case; Unsubscribe
	// closes each subscriber channel (see EventBus.Unsubscribe), which also
	// unblocks their in-flight drain loop. Shared between the timeout and
	// normal paths below so a deadlock failure doesn't leak the 50 subscriber
	// goroutines into whatever test runs next in this process.
	cleanupSubscribers := func() {
		close(done)
		for _, ch := range subs {
			eb.Unsubscribe(ch)
		}
		subWg.Wait()
	}

	pubDone := make(chan struct{})
	go func() {
		pubWg.Wait()
		close(pubDone)
	}()
	select {
	case <-pubDone:
	case <-time.After(timeout):
		// Signal publishers to stop, then give them a short, bounded window
		// to notice and exit. Don't wait unboundedly here: this branch fires
		// precisely when a publisher might be stuck, so an unbounded
		// pubWg.Wait() could turn a clean 60s failure into an indefinite
		// hang — exactly what the timeout above exists to prevent.
		pubCancel()
		select {
		case <-pubDone:
		case <-time.After(5 * time.Second):
		}
		cleanupSubscribers()
		t.Fatalf("publishers did not complete within %v — likely deadlock", timeout)
	}
	publishDuration := time.Since(start)

	// Let subscribers drain for a bit
	time.Sleep(2 * time.Second)
	cleanupSubscribers()

	totalReceived := atomic.LoadInt64(&received)
	expectedTotal := int64(totalEvents) * int64(numSubscribers)
	dropRate := 1.0 - float64(totalReceived)/float64(expectedTotal)

	t.Logf("Published %d events in %v", totalEvents, publishDuration)
	t.Logf("Total received across subscribers: %d / %d (drop rate: %.2f%%)", totalReceived, expectedTotal, dropRate*100)

	if dropRate > 0.05 {
		t.Logf("Note: Drop rate %.2f%% exceeds 5%% — expected under extreme load with non-blocking sends", dropRate*100)
	}

	metrics.Snapshot()
	report := metrics.Report()
	report.Print()
}

// TestEventBusRapidSubscribeUnsubscribe: concurrent subscribe/unsubscribe while publishing.
func TestEventBusRapidSubscribeUnsubscribe(t *testing.T) {
	const (
		duration       = 10 * time.Second
		numPublishers  = 20
		numChurners    = 10
	)

	eb := telemetry.NewEventBus(50)
	ctx, cancel := contextWithTimeout(duration)
	defer cancel()

	// Publishers
	var pubWg sync.WaitGroup
	for i := 0; i < numPublishers; i++ {
		pubWg.Add(1)
		go func() {
			defer pubWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					eb.Emit("agent", telemetry.EventThoughtStart, telemetry.ThoughtStartPayload{Iteration: 1})
				}
			}
		}()
	}

	// Churners: rapidly subscribe and unsubscribe
	var churnWg sync.WaitGroup
	for i := 0; i < numChurners; i++ {
		churnWg.Add(1)
		go func() {
			defer churnWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					ch := eb.Subscribe()
					// Read a few events
					for j := 0; j < 10; j++ {
						select {
						case <-ch:
						case <-ctx.Done():
							eb.Unsubscribe(ch)
							return
						}
					}
					eb.Unsubscribe(ch)
				}
			}
		}()
	}

	<-ctx.Done()
	pubWg.Wait()
	churnWg.Wait()

	t.Log("EventBus rapid subscribe/unsubscribe completed without deadlock or panic")
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
