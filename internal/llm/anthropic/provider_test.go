package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// TestParseResponse_MalformedToolCallArgumentsErrors is a regression test:
// malformed/truncated tool-call argument JSON must surface as an error, not
// silently become an empty args map.
func TestParseResponse_MalformedToolCallArgumentsErrors(t *testing.T) {
	p := &Provider{}
	resp := &anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "tool_use", ID: "call1", Name: "search", Input: json.RawMessage(`{"query": "truncated`)},
		},
	}
	_, err := p.parseResponse(resp)
	if err == nil {
		t.Fatal("expected an error for malformed tool_use input, got nil")
	}
}

// TestBuildMessages_ThinkingBlocksSurviveJSONRoundTrip is a regression test:
// the metadata read used a rigid []map[string]interface{} type assertion,
// which only ever matched the native in-process shape. Once a Message
// round-trips through encoding/json — the JSONL session store / debugger
// replay path — a JSON array decoded into interface{} comes back as
// []interface{}, not []map[string]interface{}, so the assertion silently
// failed and thinking blocks vanished instead of erroring.
func TestBuildMessages_ThinkingBlocksSurviveJSONRoundTrip(t *testing.T) {
	p := &Provider{}
	native := []llm.Message{
		{
			Role:    "assistant",
			Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "42"}},
			Metadata: map[string]interface{}{
				"anthropic_thinking_blocks": []map[string]interface{}{
					{"type": "thinking", "signature": "sig1", "thinking": "let me think"},
				},
			},
		},
	}

	// Simulate the JSONL round-trip: marshal then unmarshal through
	// encoding/json, exactly what session persistence/replay does.
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var replayed []llm.Message
	if err := json.Unmarshal(raw, &replayed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	// Sanity check the round-trip actually produced the []interface{} shape
	// this test exists to cover, not the native type by some fluke.
	if _, ok := replayed[0].Metadata["anthropic_thinking_blocks"].([]interface{}); !ok {
		t.Fatalf("test setup: expected []interface{} after JSON round-trip, got %T", replayed[0].Metadata["anthropic_thinking_blocks"])
	}

	messages := p.buildMessages(replayed)
	if len(messages) != 1 {
		t.Fatalf("buildMessages() = %d messages, want 1", len(messages))
	}
	var found bool
	for _, block := range messages[0].Content {
		if block.OfThinking != nil && block.OfThinking.Signature == "sig1" && block.OfThinking.Thinking == "let me think" {
			found = true
		}
	}
	if !found {
		t.Errorf("buildMessages() content = %+v, want a thinking block with signature %q", messages[0].Content, "sig1")
	}
}

// TestBuildMessages_UserTextOnly covers the ordinary text-only path.
func TestBuildMessages_UserTextOnly(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{llm.NewTextMessage("user", "hello there")}

	messages := p.buildMessages(history)

	if len(messages) != 1 || len(messages[0].Content) != 1 {
		t.Fatalf("buildMessages() = %+v, want exactly one message with one block", messages)
	}
	block := messages[0].Content[0]
	if block.OfText == nil || block.OfText.Text != "hello there" {
		t.Errorf("Content[0] = %+v, want a text block with %q", block, "hello there")
	}
}

// TestBuildMessages_UserWithImage covers the new image-block path a
// --attach image takes.
func TestBuildMessages_UserWithImage(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: llm.ContentTypeText, Text: "what is this?"},
				{
					Type:     llm.ContentTypeImage,
					MIMEType: "image/png",
					Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "aGVsbG8="},
				},
			},
		},
	}

	messages := p.buildMessages(history)

	if len(messages) != 1 || len(messages[0].Content) != 2 {
		t.Fatalf("buildMessages() = %+v, want exactly one message with two blocks", messages)
	}
	textBlock := messages[0].Content[0]
	if textBlock.OfText == nil || textBlock.OfText.Text != "what is this?" {
		t.Errorf("Content[0] = %+v, want a text block", textBlock)
	}
	imageBlock := messages[0].Content[1]
	if imageBlock.OfImage == nil || imageBlock.OfImage.Source.OfBase64 == nil {
		t.Fatalf("Content[1] = %+v, want a base64 image block", imageBlock)
	}
	if imageBlock.OfImage.Source.OfBase64.Data != "aGVsbG8=" {
		t.Errorf("Data = %q, want %q", imageBlock.OfImage.Source.OfBase64.Data, "aGVsbG8=")
	}
	if string(imageBlock.OfImage.Source.OfBase64.MediaType) != "image/png" {
		t.Errorf("MediaType = %q, want %q", imageBlock.OfImage.Source.OfBase64.MediaType, "image/png")
	}
}
