// Wake-up timer audit log management with rotation.
package wake

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func appendAudit(dir, sessionID string, rec map[string]any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1024 * 1024 // 1 MiB default
	}

	path := filepath.Join(dir, sessionID+".audit.jsonl")

	// Check if rotation is needed
	if st, err := os.Stat(path); err == nil && st.Size() > maxBytes {
		oldPath := path + ".1"
		_ = os.Remove(oldPath)
		_ = os.Rename(path, oldPath)
	}

	// Append the record
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit: %w", err)
	}
	defer file.Close()

	out := make(map[string]any, len(rec)+1)
	for k, v := range rec {
		out[k] = v
	}
	if _, ok := out["ts"]; !ok {
		out["ts"] = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}
