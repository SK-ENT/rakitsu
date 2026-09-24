package anthropic

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Anthropic accepts images inside tool_result content directly.
func TestBuildMessages_ToolImageInsideToolResult(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{
		llm.NewTextMessage("user", "explain shot.png"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_image", Arguments: map[string]interface{}{}}}},
		{Role: "tool", ToolCallID: "c1", Content: []llm.ContentBlock{
			{Type: llm.ContentTypeText, Text: "Loaded image shot.png"},
			{Type: llm.ContentTypeImage, MIMEType: "image/png", Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "aGVsbG8="}},
		}},
	}
	msgs := p.buildMessages(history)
	last := msgs[len(msgs)-1]
	if len(last.Content) != 1 || last.Content[0].OfToolResult == nil {
		t.Fatalf("last message = %+v, want one tool_result block", last)
	}
	parts := last.Content[0].OfToolResult.Content
	if len(parts) != 2 || parts[0].OfText == nil || parts[1].OfImage == nil {
		t.Fatalf("tool_result content = %+v, want text then image", parts)
	}
	if parts[1].OfImage.Source.OfBase64 == nil || parts[1].OfImage.Source.OfBase64.Data != "aGVsbG8=" {
		t.Errorf("image source = %+v, want the base64 payload", parts[1].OfImage.Source)
	}
}
