// Wake-up timer persistent state management.
package wake

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type persisted struct {
	Version    int                    `json:"version"`
	Tick       int                    `json:"tick"`
	IntervalS  float64                `json:"interval_sec"`
	Checks     map[string]*checkState `json:"checks,omitempty"`
	CapWindow  []int64                `json:"cap_window,omitempty"` // unix nanos of enqueues
	LastEnq    int64                  `json:"last_enq"`
	TaskWindow []int64                `json:"task_window,omitempty"` // unix nanos of start_task launches (rolling hour)
	OwnerPID   int                    `json:"owner_pid"`
	Stopped    bool                   `json:"stopped"`
	AlarmSig   string                 `json:"alarm_sig,omitempty"` // non-OK check set at the last escalation; repeats are suppressed
}

func loadState(dir, sessionID string) (*persisted, error) {
	path := filepath.Join(dir, sessionID+".state.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &persisted{Version: 1, Checks: map[string]*checkState{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	var st persisted
	if err := json.Unmarshal(data, &st); err != nil {
		// Corrupt state: rename it and start fresh
		_ = os.Rename(path, path+".corrupt")
		return &persisted{Version: 1, Checks: map[string]*checkState{}}, nil
	}

	if st.Checks == nil {
		st.Checks = map[string]*checkState{}
	}
	return &st, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer file.Close()

	if err := os.Chmod(file.Name(), 0o600); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("chmod: %w", err)
	}

	if _, err := file.Write(data); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("write: %w", err)
	}

	if err := file.Sync(); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("sync: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("close: %w", err)
	}

	if err := os.Rename(file.Name(), path); err != nil {
		os.Remove(file.Name())
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

func writeState(dir, sessionID string, st *persisted) error {
	path := filepath.Join(dir, sessionID+".state.json")
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return writeAtomic(path, data)
}
