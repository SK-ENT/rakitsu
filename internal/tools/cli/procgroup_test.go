//go:build !windows

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// TestExecute_TimeoutKillsProcessGroup regression-tests the fix for a
// context-deadline timeout only killing the direct `sh` process while a
// background grandchild it spawned kept running as an orphan.
func TestExecute_TimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	// The grandchild (a backgrounded `sleep`) outlives a naive kill of just
	// the direct `sh` process — assert it's actually gone once the timeout
	// fires, not left running unmanaged.
	def := &config.ToolDefinition{
		Name:    "orphan-check",
		Command: fmt.Sprintf(`sh -c 'sleep 30 & echo $! > %s; wait'`, pidFile),
		Sandbox: &config.SandboxConfig{
			Type: "local_restricted",
			ResourceLimits: config.ResourceLimits{
				TimeoutSec: 1,
			},
		},
	}
	tool := NewTool(def)
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("grandchild never wrote its pid: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if convErr != nil {
		t.Fatalf("bad pid file contents %q: %v", pidBytes, convErr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if killErr := syscall.Kill(pid, 0); killErr != nil {
			return // signal delivery failed — the process is gone
		}
		if time.Now().After(deadline) {
			t.Errorf("grandchild pid %d still alive after the tool's timeout fired", pid)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
