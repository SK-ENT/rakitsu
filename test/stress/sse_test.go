//go:build stress

package stress

import (
	"bufio"
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestSSEConcurrentClients: 200 SSE clients, 5K events, rapid connect/disconnect.
func TestSSEConcurrentClients(t *testing.T) {
	const (
		numClients = 200
		numEvents  = 5_000
	)

	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	// Connect clients
	var clientWg sync.WaitGroup
	var totalReceived int64
	clientCtx, clientCancel := context.WithCancel(ctx)

	for i := 0; i < numClients; i++ {
		clientWg.Add(1)
		go func(id int) {
			defer clientWg.Done()
			req, err := http.NewRequestWithContext(clientCtx, "GET", h.BaseURL+"/events", nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if len(line) > 0 {
					atomic.AddInt64(&totalReceived, 1)
				}
			}
		}(i)
	}

	// Let clients connect
	time.Sleep(500 * time.Millisecond)

	// Emit events
	for i := 0; i < numEvents; i++ {
		h.EventBus.Emit("agent", telemetry.EventThoughtStart, telemetry.ThoughtStartPayload{
			Iteration: i,
		})
	}

	// Let events propagate
	time.Sleep(3 * time.Second)
	clientCancel()
	clientWg.Wait()

	t.Logf("Total SSE lines received across %d clients: %d", numClients, atomic.LoadInt64(&totalReceived))
	t.Log("SSE concurrent clients test passed")
}

// TestSSERapidConnectDisconnect: clients connect and disconnect rapidly for 15s.
func TestSSERapidConnectDisconnect(t *testing.T) {
	const duration = 15 * time.Second

	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), duration+5*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	// Background publisher
	pubCtx, pubCancel := context.WithTimeout(ctx, duration)
	defer pubCancel()
	go func() {
		for {
			select {
			case <-pubCtx.Done():
				return
			default:
				h.EventBus.Emit("agent", telemetry.EventAgentMessage, telemetry.AgentMessagePayload{Message: "churn"})
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Rapid connect/disconnect
	var wg sync.WaitGroup
	var connections int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-pubCtx.Done():
					return
				default:
					connCtx, connCancel := context.WithTimeout(pubCtx, 200*time.Millisecond)
					req, _ := http.NewRequestWithContext(connCtx, "GET", h.BaseURL+"/events", nil)
					resp, err := http.DefaultClient.Do(req)
					if err == nil {
						resp.Body.Close()
						atomic.AddInt64(&connections, 1)
					}
					connCancel()
				}
			}
		}()
	}

	<-pubCtx.Done()
	wg.Wait()

	t.Logf("Completed %d rapid connect/disconnect cycles", atomic.LoadInt64(&connections))
	t.Log("SSE rapid connect/disconnect test passed")
}
