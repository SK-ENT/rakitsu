package server

import (
	"time"

	"github.com/SK-ENT/rakitsu/internal/wake"
)

// Monitor/wake states shared by /healthz, the status API, A2A skills and the UI.
const (
	WakeStateOK       = "ok"
	WakeStateStale    = "stale"
	WakeStateStopped  = "stopped"
	WakeStateFailed   = "failed"
	WakeStateStarting = "starting"
)

// WakeStatus is the read-only status of one wake session. It is the JSON
// body of GET /api/chat/{id}/wake/status. It must never carry check paths,
// URLs, error text or secret names. Task B owns the real implementation.
type WakeStatus struct {
	ID                string     `json:"id"`
	State             string     `json:"state"`
	Label             string     `json:"label,omitempty"`
	LastTick          *time.Time `json:"last_tick,omitempty"`
	LastAlarm         *time.Time `json:"last_alarm,omitempty"`
	TurnsLastHour     int        `json:"turns_last_hour"`
	MaxTurnsPerHour   int        `json:"max_turns_per_hour"`
	RunningTasks      int        `json:"running_tasks"`
	NextTickInSeconds int        `json:"next_tick_in_seconds"`
	IntervalSeconds   int        `json:"interval_seconds"`
}

// WakeStatus returns the session's wake status.
// Reads wake loop state atomically without holding the engine lock.
func (s *ChatSession) WakeStatus() WakeStatus {
	st := WakeStatus{
		ID:              s.ID,
		State:           WakeStateStopped,
		MaxTurnsPerHour: s.cfg.Settings.Wake.MaxTurnsPerHour,
		IntervalSeconds: s.cfg.Settings.Wake.IntervalSeconds,
	}

	// Strip "monitor-" prefix from ID for label if present.
	if label := s.ID; len(label) > 8 && label[:8] == "monitor-" {
		st.Label = label[8:]
	} else {
		st.Label = label
	}

	now := time.Now()

	s.wakeMu.Lock()
	var eng *wake.Engine
	var started time.Time
	if s.wake != nil {
		eng = s.wake.eng
		started = s.wake.started
		if clock := s.wake.opts.Clock; clock != nil {
			now = clock.Now()
		}
	}
	s.wakeMu.Unlock()

	// Rolling-window figures come from the same source as the cap, running or not
	// (after a kill the window still holds its entries).
	if eng != nil {
		st.TurnsLastHour = eng.TurnsLastHour(now)
		st.LastAlarm = eng.LastAlarm(now)
	}

	// Determine state based on whether the wake loop is running.
	if s.WakeRunning() {
		st.State = WakeStateOK

		if eng != nil {
			// LastTick from heartbeat file
			st.LastTick = eng.LastHeartbeat(now)

			// No readable heartbeat: starting within the grace window, stale after it.
			if grace := time.Duration(s.cfg.Settings.Wake.HeartbeatStaleSeconds) * time.Second; st.LastTick == nil && grace > 0 && !started.IsZero() {
				if now.Sub(started) > grace {
					st.State = WakeStateStale
				} else {
					st.State = WakeStateStarting
				}
			}

			// Check if heartbeat is stale
			if st.LastTick != nil {
				staleness := now.Sub(*st.LastTick)
				staleSeconds := int64(s.cfg.Settings.Wake.HeartbeatStaleSeconds)
				if staleSeconds > 0 && staleness > time.Duration(staleSeconds)*time.Second {
					st.State = WakeStateStale
				}
			}

			// NextTickInSeconds calculated from NextDelay
			nextDelay := eng.NextDelay()
			st.NextTickInSeconds = int(nextDelay.Seconds())
		}
	}

	// RunningTasks: currently 0 as task manager not implemented
	st.RunningTasks = 0

	return st
}
