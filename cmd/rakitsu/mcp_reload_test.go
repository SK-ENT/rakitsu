package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestMCPReloadingHandlerRebuildsRegistryWhenConfigToolsChange(t *testing.T) {
	cfg := &config.Config{Tools: []config.ToolDefinition{{Name: "first", Type: "cli"}}}
	builds := 0
	handler := newMCPReloadingHandler(func() (*config.Config, error) { return cfg, nil }, "test", func(*config.Config) *tools.ToolRegistry {
		builds++
		registry := tools.NewToolRegistry()
		registry.RegisterTool(&mcpReloadTestTool{name: cfg.Tools[0].Name})
		return registry
	})
	request := func(body, session string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		handler.ServeHTTP(resp, req)
		if resp.Code == http.StatusServiceUnavailable {
			t.Fatalf("MCP handler returned %d", resp.Code)
		}
		return resp
	}

	initialize := request(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	session := initialize.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("initialize did not return a session ID")
	}
	request(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, session)
	if builds != 1 {
		t.Fatalf("initial registry builds = %d, want 1", builds)
	}
	listTools := func() []string {
		resp := request(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, session)
		var envelope struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode tools/list: %v", err)
		}
		names := make([]string, 0, len(envelope.Result.Tools))
		for _, tool := range envelope.Result.Tools {
			names = append(names, tool.Name)
		}
		return names
	}
	if names := listTools(); len(names) != 1 || names[0] != "first" {
		t.Fatalf("initial tools/list = %v, want [first]", names)
	}
	if builds != 1 {
		t.Fatalf("unchanged config rebuilt registry %d times, want 1", builds)
	}
	cfg.Tools[0].Name = "second"
	if names := listTools(); len(names) != 1 || names[0] != "second" {
		t.Fatalf("tools/list after config edit = %v, want [second]", names)
	}
	if builds != 2 {
		t.Fatalf("registry builds after config edit = %d, want 2", builds)
	}
}

type mcpReloadTestTool struct{ name string }

func (t *mcpReloadTestTool) GetName() string        { return t.name }
func (t *mcpReloadTestTool) GetDescription() string { return "test tool" }
func (t *mcpReloadTestTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *mcpReloadTestTool) Execute(context.Context, map[string]interface{}) (string, error) {
	return "ok", nil
}

var _ http.Handler = (*mcpReloadingHandler)(nil)
