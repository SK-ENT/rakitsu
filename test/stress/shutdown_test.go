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

// TestGracefulShutdownWithActiveConnections: cancel mid-execution with SSE clients connected.
func TestGracefulShutdownWithActiveConnections(t *testing.T) {
	const numClients = 50

	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	// Connect SSE clients
	clientCtx, clientCancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ConnectSSE(clientCtx)
			<-clientCtx.Done()
		}()
	}

	// Emit events for a bit
	go func() {
		for i := 0; i < 1000; i++ {
			h.EventBus.Emit("agent", telemetry.EventThoughtStart, telemetry.ThoughtStartPayload{Iteration: i})
			time.Sleep(time.Millisecond)
		}
	}()

	// Register hub sessions
	for i := 0; i < 10; i++ {
		h.HubRegister(ctx, "shutdown-"+string(rune('0'+i)), "test", "q", []string{"a"})
	}

	// Wait a bit, then shut down abruptly
	time.Sleep(2 * time.Second)

	shutdownStart := time.Now()
	clientCancel()
	h.Stop()
	shutdownDuration := time.Since(shutdownStart)

	wg.Wait()

	t.Logf("Shutdown completed in %v with %d active SSE clients", shutdownDuration, numClients)
	if shutdownDuration > 5*time.Second {
		t.Errorf("Shutdown took %v, expected < 5s", shutdownDuration)
	}
}

// TestShutdownDuringEventFlood: shut down while events are being published rapidly.
func TestShutdownDuringEventFlood(t *testing.T) {
	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	// Flood events
	floodCtx, floodCancel := context.WithCancel(ctx)
	var eventCount int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-floodCtx.Done():
					return
				default:
					h.EventBus.Emit("agent", telemetry.EventAgentMessage, telemetry.AgentMessagePayload{Message: "flood"})
					atomic.AddInt64(&eventCount, 1)
				}
			}
		}()
	}

	time.Sleep(1 * time.Second)
	floodCancel()
	h.Stop()
	wg.Wait()

	t.Logf("Shut down cleanly after %d events published", atomic.LoadInt64(&eventCount))
}
