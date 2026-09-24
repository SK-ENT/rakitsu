package openai

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	openai "github.com/sashabaranov/go-openai"
)

// Chat Completions tool messages are text-only, so a tool-returned
// image must arrive as a user message right after the tool message.
func TestBuildMessages_ToolImageFollowsAsUserMessage(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{
		llm.NewTextMessage("user", "explain shot.png"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_image"}}},
		{Role: "tool", ToolCallID: "c1", Content: []llm.ContentBlock{
			{Type: llm.ContentTypeText, Text: "Loaded image shot.png"},
			{Type: llm.ContentTypeImage, MIMEType: "image/png", Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "aGVsbG8="}},
		}},
	}
	msgs := p.buildMessages("", history)

	var toolIdx = -1
	for i, m := range msgs {
		if m.Role == openai.ChatMessageRoleTool {
			toolIdx = i
		}
	}
	if toolIdx < 0 || toolIdx+1 >= len(msgs) {
		t.Fatalf("messages = %+v, want a tool message followed by one more", msgs)
	}
	if msgs[toolIdx].Content != "Loaded image shot.png" || msgs[toolIdx].MultiContent != nil {
		t.Errorf("tool message = %+v, want text-only content", msgs[toolIdx])
	}
	carrier := msgs[toolIdx+1]
	if carrier.Role != openai.ChatMessageRoleUser || len(carrier.MultiContent) != 2 {
		t.Fatalf("carrier = %+v, want a user message with text + image parts", carrier)
	}
	img := carrier.MultiContent[1]
	if img.ImageURL == nil || !strings.HasPrefix(img.ImageURL.URL, "data:image/png;base64,aGVsbG8=") {
		t.Errorf("image part = %+v, want the data URL", img)
	}
}
