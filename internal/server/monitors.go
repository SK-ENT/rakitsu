package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/wake"
)

// MonitorState is the autostart status of one monitor. Err is for logs and
// authenticated views only; /healthz must not expose it.
type MonitorState struct {
	ID        string
	SessionID string
	State     string // starting|ok|failed|stopped (WakeState* constants)
	Err       string
}

// monitorLister provides access to monitor state for /healthz.
// Implemented by MonitorManager (sets this via SetMonitorLister).
type monitorLister interface {
	MonitorStates() []MonitorState
}

// monitorHost is the part of ChatManager the autostart needs.
type monitorHost interface {
	StartOrResume(ctx context.Context, configID, workdir string, envVars map[string]string, sessionID string) (*ChatSession, error)
	Get(id string) *ChatSession
}

// MonitorManager autostarts the configured monitors and tracks their state.
type MonitorManager struct {
	host     monitorHost
	register func(path string, id string) (configID string, err error)
	getenv   func(string) (string, bool)
	monitors []config.MonitorConfig

	mu       sync.Mutex
	states   map[string]*MonitorState
	order    []string
	stopOnce sync.Once
}

// NewMonitorManager wires a manager to a real ChatManager and ConfigStore.
func NewMonitorManager(cm *ChatManager, cs *ConfigStore, monitors []config.MonitorConfig) *MonitorManager {
	m := newMonitorManager(cm, monitors)
	m.register = cs.registerPath
	return m
}

func newMonitorManager(host monitorHost, monitors []config.MonitorConfig) *MonitorManager {
	m := &MonitorManager{host: host, monitors: monitors, getenv: os.LookupEnv, states: map[string]*MonitorState{}}
	for _, mc := range monitors {
		m.order = append(m.order, mc.ID)
		m.states[mc.ID] = &MonitorState{ID: mc.ID, SessionID: mc.SessionID(), State: WakeStateStarting}
	}
	return m
}

// registerPath makes an on-disk config resolvable by id without copying it.
func (cs *ConfigStore) registerPath(path, id string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("monitor config not readable: %w", err)
	}
	cid := "monitor-" + id
	cs.mu.Lock()
	cs.entries[cid] = abs
	cs.mu.Unlock()
	return cid, nil
}

// Autostart starts the monitors one by one in the background. It never
// blocks the caller and never panics the server.
func (m *MonitorManager) Autostart(ctx context.Context) {
	go m.Run(ctx)
}

// Run starts every monitor in order and returns when all have been tried.
// A failure marks only that monitor failed.
func (m *MonitorManager) Run(ctx context.Context) {
	for _, mc := range m.monitors {
		m.startOne(ctx, mc)
	}
}

func (m *MonitorManager) set(id, state, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[id]; ok {
		st.State, st.Err = state, errText
	}
}

func (m *MonitorManager) startOne(ctx context.Context, mc config.MonitorConfig) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("monitor %s: panic during start: %v", mc.ID, r)
			m.set(mc.ID, WakeStateFailed, "internal error during start")
		}
	}()
	// Idempotent: a running session with this id is not started again.
	if s := m.host.Get(mc.SessionID()); s != nil && s.WakeRunning() {
		m.set(mc.ID, WakeStateOK, "")
		return
	}
	fail := func(msg string) {
		log.Printf("monitor %s: failed: %s", mc.ID, msg)
		m.set(mc.ID, WakeStateFailed, msg)
	}
	env := map[string]string{}
	for _, name := range mc.EnvRefs {
		v, ok := m.getenv(name)
		if !ok || v == "" {
			fail("missing environment variable " + name) // name only, never a value
			return
		}
		env[name] = v
	}
	if m.register == nil {
		fail("no config store")
		return
	}
	cid, err := m.register(mc.Config, mc.ID)
	if err != nil {
		fail(err.Error())
		return
	}
	workdir := mc.Workdir
	if workdir != "" {
		if abs, aerr := filepath.Abs(workdir); aerr == nil {
			workdir = abs
		}
	}
	sess, err := m.host.StartOrResume(ctx, cid, workdir, env, mc.SessionID())
	if err != nil {
		if errors.Is(err, wake.ErrLocked) {
			fail("session is locked by another rakitsu process")
		} else {
			fail(scrub(err.Error(), env))
		}
		return
	}
	if sess == nil || !sess.cfg.Settings.Wake.Enabled {
		fail("config must set settings.wake.enabled")
		return
	}
	m.set(mc.ID, WakeStateOK, "")
	log.Printf("monitor %s: running as session %s", mc.ID, mc.SessionID())
}

func scrub(s string, env map[string]string) string {
	for _, v := range env {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// MonitorStates returns a snapshot in config order. A monitor that started
// but whose wake loop is no longer running reports stopped.
func (m *MonitorManager) MonitorStates() []MonitorState {
	m.mu.Lock()
	out := make([]MonitorState, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, *m.states[id])
	}
	m.mu.Unlock()
	for i := range out {
		if out[i].State != WakeStateOK {
			continue
		}
		if s := m.host.Get(out[i].SessionID); s == nil || !s.WakeRunning() {
			out[i].State = WakeStateStopped
		} else if st := s.WakeStatus(); st.State == WakeStateStale || st.State == WakeStateStarting {
			out[i].State = st.State // same rule as the status API
		}
	}
	return out
}

// Stop gracefully stops all monitor sessions and waits for them to exit.
// Idempotent: safe to call multiple times.
func (m *MonitorManager) Stop(ctx context.Context) {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		states := make([]*MonitorState, 0, len(m.order))
		for _, id := range m.order {
			if st, ok := m.states[id]; ok {
				states = append(states, st)
			}
		}
		m.mu.Unlock()

		// Close each monitor session. ChatSession.Close() calls StopWakeAndWait()
		// to ensure the wake loop fully exits before returning.
		for _, st := range states {
			if s := m.host.Get(st.SessionID); s != nil {
				s.Close()
			}
		}
	})
}

// LoadServeConfig loads the serve --config file. If the only problems are in
// the monitors block, the block is dropped (the server still boots, with no
// monitors) and dropped is non-empty.
func LoadServeConfig(path string) (cfg *config.Config, dropped string, err error) {
	cfg, err = config.Load(path)
	if cfg == nil {
		return nil, "", err
	}
	// Always validate the loaded config, even if config.Load succeeded.
	// Validation errors in the monitors block are recoverable; others are fatal.
	var bad []string
	for _, e := range cfg.Validate() {
		if !e.IsError() {
			continue
		}
		if !strings.HasPrefix(e.Field, "monitors") {
			return cfg, "", fmt.Errorf("config validation: %s", e.Error())
		}
		bad = append(bad, e.Error())
	}
	if len(bad) == 0 {
		return cfg, "", err
	}
	cfg.Monitors = nil
	return cfg, strings.Join(bad, "; "), nil
}
