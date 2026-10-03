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

// TestBuildContents_ToolAudioAfterFunctionResponses is a regression test: a
// tool result carrying ContentTypeAudio (e.g. a read_media/read_audio tool
// call) used to be silently dropped here — this branch only forwarded
// ContentTypeImage, so the model received a text label ("Loaded audio ...")
// but never the actual audio bytes, and answered as if it hadn't heard
// anything. Mirrors TestBuildContents_ToolImagesAfterFunctionResponses.
func TestBuildContents_ToolAudioAfterFunctionResponses(t *testing.T) {
	p := &Provider{}
	audio := func(b []byte) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.ContentTypeAudio, MIMEType: "audio/wave",
			Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: base64.StdEncoding.EncodeToString(b)}}
	}
	history := []llm.Message{
		llm.NewTextMessage("user", "what does a.wav say?"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_media"}}},
		{Role: "tool", ToolCallID: "1", Name: "read_media", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "Loaded audio a.wav (audio/wave, 1 bytes)."}, audio([]byte{9})}},
	}
	contents := p.buildContents(history)
	last := contents[len(contents)-1]
	if last.Role != "user" || len(last.Parts) != 2 {
		t.Fatalf("last content = %+v, want one user turn with 2 parts (function response + audio)", last)
	}
	if last.Parts[0].FunctionResponse == nil {
		t.Fatalf("part 0 = %+v, want the function response", last.Parts[0])
	}
	if last.Parts[1].InlineData == nil {
		t.Fatalf("part 1 = %+v, want the inline audio data — this is the bug: audio was being dropped here", last.Parts[1])
	}
	if last.Parts[1].InlineData.MIMEType != "audio/wave" || last.Parts[1].InlineData.Data[0] != 9 {
		t.Errorf("audio part = %+v, want MIMEType audio/wave and the original bytes", last.Parts[1].InlineData)
	}
}
