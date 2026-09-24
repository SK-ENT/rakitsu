package gemini

import (
	"encoding/base64"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// tool-returned images ride in the function-response user turn as
// inline data, after all of that turn's function responses.
func TestBuildContents_ToolImagesAfterFunctionResponses(t *testing.T) {
	p := &Provider{}
	img := func(b []byte) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png",
			Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: base64.StdEncoding.EncodeToString(b)}}
	}
	history := []llm.Message{
		llm.NewTextMessage("user", "explain a.png and b.png"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_image"}, {ID: "2", Name: "read_image"}}},
		{Role: "tool", ToolCallID: "1", Name: "read_image", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "a"}, img([]byte{1})}},
		{Role: "tool", ToolCallID: "2", Name: "read_image", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "b"}, img([]byte{2})}},
	}
	contents := p.buildContents(history)
	last := contents[len(contents)-1]
	if last.Role != "user" || len(last.Parts) != 4 {
		t.Fatalf("last content = %+v, want one user turn with 4 parts", last)
	}
	for i, want := range []string{"fr", "fr", "img", "img"} {
		got := "img"
		if last.Parts[i].FunctionResponse != nil {
			got = "fr"
		} else if last.Parts[i].InlineData == nil {
			got = "other"
		}
		if got != want {
			t.Fatalf("part %d = %s, want %s (order fr, fr, img, img)", i, got, want)
		}
	}
	if last.Parts[3].InlineData.Data[0] != 2 {
		t.Errorf("image order not preserved: %+v", last.Parts[3].InlineData)
	}
}
