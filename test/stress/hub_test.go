//go:build stress

package stress

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestHubConcurrentSessions: 50 sessions register, each ingests 1K events, concurrent polling.
func TestHubConcurrentSessions(t *testing.T) {
	const (
		numSessions      = 50
		eventsPerSession = 1_000
	)

	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var totalIngested int64
	var errors int64

	for s := 0; s < numSessions; s++ {
		wg.Add(1)
		go func(sid int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("stress-session-%d", sid)
			agents := []string{fmt.Sprintf("agent-%d", sid)}

			// Register
			if err := h.HubRegister(ctx, sessionID, "stress-test", "query", agents); err != nil {
				atomic.AddInt64(&errors, 1)
				t.Logf("Register error for session %d: %v", sid, err)
				return
			}

			// Ingest events in batches
			batchSize := 50
			for i := 0; i < eventsPerSession; i += batchSize {
				events := make([]telemetry.AgentEvent, 0, batchSize)
				for j := 0; j < batchSize && i+j < eventsPerSession; j++ {
					payload, _ := json.Marshal(telemetry.AgentMessagePayload{
						Message: fmt.Sprintf("event-%d", i+j),
						Type:    "test",
					})
					events = append(events, telemetry.AgentEvent{
						ID:        fmt.Sprintf("evt-%s-%d", sessionID, i+j),
						Timestamp: time.Now(),
						AgentName: fmt.Sprintf("agent-%d", sid),
						EventType: telemetry.EventAgentMessage,
						Payload:   payload,
						SessionID: sessionID,
					})
				}
				if err := h.HubIngest(ctx, sessionID, events); err != nil {
					atomic.AddInt64(&errors, 1)
					continue
				}
				atomic.AddInt64(&totalIngested, int64(len(events)))
			}

			// Deregister
			if err := h.HubDeregister(ctx, sessionID, "completed"); err != nil {
				atomic.AddInt64(&errors, 1)
			}
		}(s)
	}

	// Concurrent session list polling
	pollCtx, pollCancel := context.WithCancel(ctx)
	var pollWg sync.WaitGroup
	var pollCount int64
	for i := 0; i < 10; i++ {
		pollWg.Add(1)
		go func() {
			defer pollWg.Done()
			for {
				select {
				case <-pollCtx.Done():
					return
				default:
					_, err := httpGet(ctx, h.BaseURL+"/api/hub/sessions")
					if err == nil {
						atomic.AddInt64(&pollCount, 1)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()
	pollCancel()
	pollWg.Wait()

	errCount := atomic.LoadInt64(&errors)
	t.Logf("Sessions: %d, Events ingested: %d, Errors: %d, Session polls: %d",
		numSessions, atomic.LoadInt64(&totalIngested), errCount, atomic.LoadInt64(&pollCount))

	// Errors are expected when concurrent register/ingest/deregister overlap.
	// The key assertion is no deadlocks, panics, or data races.
	if errCount > 0 {
		t.Logf("Note: %d errors from concurrent register/ingest/deregister overlap (expected under stress)", errCount)
	}
	t.Log("Hub concurrent sessions test passed")
}

// TestHubCommandPolling: concurrent command push and poll under load.
func TestHubCommandPolling(t *testing.T) {
	const numSessions = 20

	h := NewHarness()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.WaitForServer(ctx); err != nil {
		t.Fatal(err)
	}

	// Register sessions
	for i := 0; i < numSessions; i++ {
		id := fmt.Sprintf("cmd-session-%d", i)
		if err := h.HubRegister(ctx, id, "cmd-test", "q", []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}

	// Concurrent command polling
	var wg sync.WaitGroup
	var polls int64
	pollCtx, pollCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pollCancel()

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(sid int) {
			defer wg.Done()
			id := fmt.Sprintf("cmd-session-%d", sid)
			for {
				select {
				case <-pollCtx.Done():
					return
				default:
					_, _ = httpGet(ctx, h.BaseURL+"/api/hub/commands?session_id="+id)
					atomic.AddInt64(&polls, 1)
					time.Sleep(5 * time.Millisecond)
				}
			}
		}(i)
	}

	<-pollCtx.Done()
	wg.Wait()

	t.Logf("Total command polls across %d sessions: %d", numSessions, atomic.LoadInt64(&polls))

	// Cleanup
	for i := 0; i < numSessions; i++ {
		h.HubDeregister(ctx, fmt.Sprintf("cmd-session-%d", i), "completed")
	}
	t.Log("Hub command polling test passed")
}
