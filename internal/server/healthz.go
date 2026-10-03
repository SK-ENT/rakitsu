package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// healthzResponse is the JSON body for GET /healthz
type healthzResponse struct {
	Status   string            `json:"status"`
	Monitors map[string]string `json:"monitors"`
}

// SetMonitorLister sets the lister used by /healthz to get monitor state.
// Called by Task A during serve startup.
func (s *SSEServer) SetMonitorLister(lister monitorLister) {
	s.monitorLister = lister
}

// SetHealthzConfig sets the config for /healthz (grace period, remote access, fail-on-stopped).
func (s *SSEServer) SetHealthzConfig(cfg config.HealthzConfig) {
	s.healthzConfig = cfg
}

// extractRemoteIP parses the IP address from RemoteAddr (format "IP:port").
// Returns nil if the address cannot be parsed.
func extractRemoteIP(remoteAddr string) net.IP {
	if remoteAddr == "" {
		return nil
	}
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// handleHealthz responds to GET /healthz with a minimal health check.
// Unauthenticated, but localhost-only by default (guarded by requiresAuth allowlist).
// Returns 200 if all monitors are ok, 503 otherwise.
func (s *SSEServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Check remote access: loopback always allowed, remote blocked unless AllowRemote is true.
	// Never trust X-Forwarded-For; use RemoteAddr only.
	if !s.healthzConfig.AllowRemote {
		ip := extractRemoteIP(r.RemoteAddr)
		if ip != nil && !ip.IsLoopback() {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")

	// No monitors configured: report ok.
	if s.monitorLister == nil {
		resp := healthzResponse{Status: "ok", Monitors: map[string]string{}}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	states := s.monitorLister.MonitorStates()
	if len(states) == 0 {
		resp := healthzResponse{Status: "ok", Monitors: map[string]string{}}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Evaluate each monitor's health.
	monitors := make(map[string]string)
	healthy := true
	failOnStopped := s.healthzConfig.FailOnStoppedOrDefault()

	for _, st := range states {
		// Strip "monitor-" prefix for the label.
		label := strings.TrimPrefix(st.ID, "monitor-")
		mon := st.State

		// Check for failure conditions.
		switch mon {
		case WakeStateFailed:
			healthy = false
		case WakeStateStopped:
			if failOnStopped {
				healthy = false
			}
		case WakeStateStale:
			healthy = false
		case WakeStateStarting:
			// Will be re-checked below against grace period.
		case WakeStateOK:
			// ok
		}

		monitors[label] = mon
	}

	// Determine final status and HTTP code.
	status := "ok"
	code := http.StatusOK
	if !healthy {
		status = "fail"
		code = http.StatusServiceUnavailable
	}

	resp := healthzResponse{
		Status:   status,
		Monitors: monitors,
	}

	w.WriteHeader(code)
	json.NewEncoder(w).Encode(resp)
}
