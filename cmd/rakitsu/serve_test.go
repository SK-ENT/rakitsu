package main

import (
	"strings"
	"testing"
)

func TestServeRequiresConfigForMCPPort(t *testing.T) {
	originalPort, originalConfig := mcpPort, mcpConfig
	t.Cleanup(func() {
		mcpPort, mcpConfig = originalPort, originalConfig
	})

	mcpPort = 9200
	mcpConfig = ""
	err := serveCmd.PreRunE(serveCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--mcp-port requires --config") {
		t.Fatalf("PreRunE error = %v, want --mcp-port requires --config", err)
	}

	mcpPort = 0
	err = serveCmd.PreRunE(serveCmd, nil)
	if err != nil {
		t.Fatalf("PreRunE error without MCP port = %v, want nil", err)
	}
}
