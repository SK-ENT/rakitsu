//go:build stress

package stress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Harness sets up in-process servers for stress testing.
type Harness struct {
	EventBus  *telemetry.EventBus
	SSE       *server.SSEServer
	DebugCtrl *debug.DebugController
	Metrics   *Metrics
	Server    *httptest.Server // underlying HTTP test server
	BaseURL   string

	mu       sync.Mutex
	cleanups []func()
}

// NewHarness creates a test harness with an event bus and SSE server.
func NewHarness() *Harness {
	eb := telemetry.NewEventBus(100)
	dc := debug.NewDebugController(eb)
	sse := server.NewSSEServer(eb, "127.0.0.1", 0) // port 0 = unused, we use httptest
	sse.SetDebugController(dc)

	return &Harness{
		EventBus:  eb,
		SSE:       sse,
		DebugCtrl: dc,
		Metrics:   NewMetrics(),
	}
}

// Start starts the SSE server on a random port via httptest.
func (h *Harness) Start() error {
	mux := h.SSE.Mux()
	h.Server = httptest.NewServer(server.CorsMiddleware(mux))
	h.BaseURL = h.Server.URL
	return nil
}

// Stop shuts down all servers and runs cleanups.
func (h *Harness) Stop() {
	if h.Server != nil {
		h.Server.Close()
	}
	h.mu.Lock()
	for _, fn := range h.cleanups {
		fn()
	}
	h.cleanups = nil
	h.mu.Unlock()
}

// OnCleanup registers a cleanup function to run on Stop.
func (h *Harness) OnCleanup(fn func()) {
	h.mu.Lock()
	h.cleanups = append(h.cleanups, fn)
	h.mu.Unlock()
}

// FreePort returns an available TCP port.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// HubRegister registers a session with the hub.
func (h *Harness) HubRegister(ctx context.Context, id, name, query string, agents []string) error {
	body := fmt.Sprintf(`{"id":"%s","name":"%s","query":"%s","config_path":"test.yaml","agents":%s}`,
		id, name, query, mustJSON(agents))
	return httpPost(ctx, h.BaseURL+"/api/hub/register", body)
}

// HubDeregister deregisters a session.
func (h *Harness) HubDeregister(ctx context.Context, id, status string) error {
	body := fmt.Sprintf(`{"id":"%s","status":"%s"}`, id, status)
	return httpPost(ctx, h.BaseURL+"/api/hub/deregister", body)
}

// HubIngest sends events to the hub.
func (h *Harness) HubIngest(ctx context.Context, sessionID string, events []telemetry.AgentEvent) error {
	body := fmt.Sprintf(`{"session_id":"%s","events":%s}`, sessionID, mustJSON(events))
	return httpPost(ctx, h.BaseURL+"/api/hub/ingest", body)
}

// ConnectSSE opens an SSE connection and counts raw reads via h.Metrics
// (AddEventReceived) until the connection ends. It does NOT parse the SSE
// stream into events -- the returned channel is closed on disconnect but
// nothing is ever sent on it; it exists so a future caller can add real
// event parsing without changing this function's signature. Cancel the
// context to disconnect.
func (h *Harness) ConnectSSE(ctx context.Context) (<-chan telemetry.AgentEvent, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", h.BaseURL+"/events", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan telemetry.AgentEvent, 1000)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				// Count raw reads as events received (simplified)
				h.Metrics.AddEventReceived()
			}
			if err != nil {
				return
			}
		}
	}()
	return ch, nil
}

// WaitForServer waits until the server responds to /health.
func (h *Harness) WaitForServer(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := http.Get(h.BaseURL + "/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
}
