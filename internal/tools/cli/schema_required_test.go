package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// A cli tool with a fixed command and no parameters must still emit
// "required": [] (a JSON array), never null. The Codex/OpenAI Responses API
// validates tool schemas strictly and rejects null with HTTP 400
// ("None is not of type 'array'"), which fails every agent carrying the tool.
func TestGetParametersSchema_NoParams_RequiredIsEmptyArray(t *testing.T) {
	tool := NewTool(&config.ToolDefinition{
		Name:    "run_build",
		Type:    "cli",
		Command: "go build ./...",
	})

	b, err := json.Marshal(tool.GetParametersSchema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"required":[]`) {
		t.Fatalf("expected \"required\":[] in schema, got %s", b)
	}
}

// Optional-only parameters must also yield an empty array, not null.
func TestGetParametersSchema_OptionalParamsOnly_RequiredIsEmptyArray(t *testing.T) {
	tool := NewTool(&config.ToolDefinition{
		Name:    "gh",
		Type:    "cli",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Description: "arguments", Required: false},
		},
	})

	b, err := json.Marshal(tool.GetParametersSchema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"required":[]`) {
		t.Fatalf("expected \"required\":[] in schema, got %s", b)
	}
}
