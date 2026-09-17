// Package mcp provides MCPTool, which wraps a single MCP server tool as a
// rakitsu tools.Tool, and NewMCPServer, which creates and initializes an
// MCP client and returns all its tools ready for registration.
package mcp

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// sanitize replaces any run of non-alphanumeric/underscore chars with "_".
func sanitize(s string) string {
	return strings.Trim(nonAlphanumRe.ReplaceAllString(s, "_"), "_")
}

// MCPTool adapts one MCP server tool as a rakitsu tools.Tool.
type MCPTool struct {
	client MCPClient
	def    ToolDef
	name   string // "{serverName}_{mcpToolName}" sanitized
}

func (t *MCPTool) GetName() string                            { return t.name }
func (t *MCPTool) GetDescription() string                     { return t.def.Description }
func (t *MCPTool) GetParametersSchema() map[string]interface{} { return t.def.InputSchema }

func (t *MCPTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return t.client.CallTool(ctx, t.def.Name, args)
}

// NewMCPServer creates an MCP client from a ToolDefinition, runs the
// initialize handshake, discovers tools, and returns:
//   - a slice of tools.Tool (one per discovered MCP tool)
//   - an io.Closer for shutdown (the client itself)
//   - an error if initialization fails
func NewMCPServer(ctx context.Context, def *config.ToolDefinition) ([]tools.Tool, io.Closer, error) {
	serverName := sanitize(def.Name)

	client, err := buildClient(def)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp %q: %w", def.Name, err)
	}

	if err := client.Initialize(ctx); err != nil {
		client.Close() //nolint:errcheck
		return nil, nil, fmt.Errorf("mcp %q initialize: %w", def.Name, err)
	}

	defs, err := client.ListTools(ctx)
	if err != nil {
		client.Close() //nolint:errcheck
		return nil, nil, fmt.Errorf("mcp %q list tools: %w", def.Name, err)
	}

	result := make([]tools.Tool, 0, len(defs))
	for _, d := range defs {
		toolName := serverName + "_" + sanitize(d.Name)
		result = append(result, &MCPTool{
			client: client,
			def:    d,
			name:   toolName,
		})
	}

	return result, client, nil
}

// buildClient selects transport based on def.Transport / def.URL.
func buildClient(def *config.ToolDefinition) (MCPClient, error) {
	transport := strings.ToLower(def.Transport)
	if transport == "" {
		if def.URL != "" {
			transport = "http"
		} else {
			transport = "stdio"
		}
	}

	switch transport {
	case "stdio":
		if def.Command == "" {
			return nil, fmt.Errorf("stdio transport requires command")
		}
		env := make(map[string]string, len(def.Env))
		for k, v := range def.Env {
			env[k] = v
		}
		return NewStdioClient(def.Command, def.Args, env, def.Timeout)

	case "http":
		if def.URL == "" {
			return nil, fmt.Errorf("http transport requires url")
		}
		return NewHTTPClient(def.URL, def.Timeout)

	default:
		return nil, fmt.Errorf("unknown transport %q (want stdio or http)", def.Transport)
	}
}
