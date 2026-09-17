package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func postHubMessageResult(t *testing.T, base string, body map[string]interface{}) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(base+"/api/hub/message-result", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestHandleHubMessageResultDeliversToWaiter — a matching token unblocks the
// registered channel with the reported outcome and cleans up pendingWaits.
func TestHandleHubMessageResultDeliversToWaiter(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	ch := make(chan turnOutcome, 1)
	s.mu.Lock()
	s.pendingWaits["tok-1"] = ch
	s.mu.Unlock()

	resp := postHubMessageResult(t, srv.URL, map[string]interface{}{
		"wait_token": "tok-1", "final": "pong", "interrupted": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	select {
	case out := <-ch:
		if out.Final != "pong" || out.Interrupted {
			t.Fatalf("outcome = %+v, want Final=pong Interrupted=false", out)
		}
	default:
		t.Fatal("channel never received the outcome")
	}

	s.mu.Lock()
	_, stillPending := s.pendingWaits["tok-1"]
	s.mu.Unlock()
	if stillPending {
		t.Fatal("pendingWaits entry not cleaned up")
	}
}

// TestHandleHubMessageResultUnknownTokenIsNoop — a token nobody is waiting on
// (already timed out and cleaned up, or bogus) is a silent 200, not an error.
func TestHandleHubMessageResultUnknownTokenIsNoop(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postHubMessageResult(t, srv.URL, map[string]interface{}{
		"wait_token": "nobody-is-waiting", "final": "pong",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unknown token must not surface as an error)", resp.StatusCode)
	}
}

// TestHandleHubMessageResultMethodNotAllowed — matches every other hub
// handler's GET-only-where-appropriate convention.
func TestHandleHubMessageResultMethodNotAllowed(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/hub/message-result")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}
