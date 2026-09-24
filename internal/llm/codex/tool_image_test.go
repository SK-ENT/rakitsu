package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// function_call_output is text-only, so a tool image follows as a
// user message with an input_image part. User images (--attach) also map
// to input_image instead of being dropped.
func TestBuildInput_ToolImageAsUserInputImage(t *testing.T) {
	image := llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png",
		Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "aGVsbG8="}}
	history := []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "look"}, image}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_image"}}},
		{Role: "tool", ToolCallID: "c1", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "Loaded"}, image}},
	}
	items := buildInput(history)
	var types []string
	for _, it := range items {
		var m map[string]interface{}
		_ = json.Unmarshal(it, &m)
		types = append(types, m["type"].(string))
	}
	if strings.Join(types, ",") != "message,function_call,function_call_output,message" {
		t.Fatalf("item types = %v, want message,function_call,function_call_output,message", types)
	}
	for _, i := range []int{0, 3} {
		if !strings.Contains(string(items[i]), `"input_image"`) || !strings.Contains(string(items[i]), "data:image/png;base64,aGVsbG8=") {
			t.Errorf("item %d = %s, want an input_image data URL", i, items[i])
		}
	}
	if strings.Contains(string(items[2]), "aGVsbG8=") {
		t.Errorf("function_call_output carries the image payload: %s", items[2])
	}
}
