package fs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// Same contract as the cli tool: "required" must marshal as a JSON array
// even when no parameter is marked required.
func TestGetParametersSchema_NoRequiredParams_RequiredIsEmptyArray(t *testing.T) {
	tool := NewTool(&config.ToolDefinition{
		Name: "read_repo",
		Type: "fs",
		Parameters: map[string]config.Parameter{
			"path": {Type: "string", Description: "path", Required: false},
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
