package openai

import (
	"encoding/json"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	openai "github.com/sashabaranov/go-openai"
)

// TestParseResponse_MalformedToolCallArgumentsErrors is a regression test:
// malformed/truncated tool-call argument JSON must surface as an error, not
// silently become an empty args map.
func TestParseResponse_MalformedToolCallArgumentsErrors(t *testing.T) {
	p := &Provider{}
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role: "assistant",
					ToolCalls: []openai.ToolCall{
						{ID: "call1", Function: openai.FunctionCall{Name: "search", Arguments: `{"query": "truncated`}},
					},
				},
			},
		},
	}
	_, err := p.parseResponse(resp)
	if err == nil {
		t.Fatal("expected an error for malformed tool call arguments, got nil")
	}
}

// TestConvertMessage_TextOnlyUnchanged locks down the M1.5 phase 1 invariant
// that a text-only message keeps using the plain Content string, never
// MultiContent. go-openai's ChatCompletionMessage.MarshalJSON errors if both
// are set, so a regression here would fail at request-send time, not at
// compile time — the only thing catching it is this test.
func TestConvertMessage_TextOnlyUnchanged(t *testing.T) {
	p := &Provider{}
	msg := llm.NewTextMessage("user", "hello there")

	om := p.convertMessage(msg)

	if om.Content != "hello there" {
		t.Errorf("Content = %q, want %q", om.Content, "hello there")
	}
	if om.MultiContent != nil {
		t.Errorf("MultiContent = %+v, want nil", om.MultiContent)
	}
}

// TestConvertMessage_UserWithImage covers the new MultiContent path a
// --attach image takes.
func TestConvertMessage_UserWithImage(t *testing.T) {
	p := &Provider{}
	msg := llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{
			{Type: llm.ContentTypeText, Text: "what is this?"},
			{
				Type:     llm.ContentTypeImage,
				MIMEType: "image/png",
				Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "aGVsbG8="},
			},
		},
	}

	om := p.convertMessage(msg)

	if om.Content != "" {
		t.Errorf("Content = %q, want empty (MultiContent set instead)", om.Content)
	}
	if len(om.MultiContent) != 2 {
		t.Fatalf("MultiContent = %+v, want 2 parts", om.MultiContent)
	}
	if om.MultiContent[0].Type != openai.ChatMessagePartTypeText || om.MultiContent[0].Text != "what is this?" {
		t.Errorf("MultiContent[0] = %+v, want a text part", om.MultiContent[0])
	}
	part := om.MultiContent[1]
	if part.Type != openai.ChatMessagePartTypeImageURL {
		t.Fatalf("MultiContent[1].Type = %q, want %q", part.Type, openai.ChatMessagePartTypeImageURL)
	}
	if part.ImageURL == nil || part.ImageURL.URL != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("MultiContent[1].ImageURL = %+v, want data URI with the base64 payload", part.ImageURL)
	}
}

// TestConvertMessage_ToolAndAssistantUnaffected is a regression guard: tool
// and text-only assistant messages must keep taking the plain Content path.
func TestConvertMessage_ToolAndAssistantUnaffected(t *testing.T) {
	p := &Provider{}

	toolMsg := llm.Message{
		Role:       "tool",
		Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "42 widgets"}},
		ToolCallID: "call_1",
	}
	om := p.convertMessage(toolMsg)
	if om.Content != "42 widgets" || om.MultiContent != nil {
		t.Errorf("tool message: Content = %q, MultiContent = %+v, want plain Content only", om.Content, om.MultiContent)
	}

	assistantMsg := llm.NewTextMessage("assistant", "here you go")
	om = p.convertMessage(assistantMsg)
	if om.Content != "here you go" || om.MultiContent != nil {
		t.Errorf("assistant message: Content = %q, MultiContent = %+v, want plain Content only", om.Content, om.MultiContent)
	}
}

// TestConvertMessage_ToolCallOnly_ContentFieldSurvivesWireMarshal is a
// regression test: a tool-call-only assistant turn (no text) has
// Content == "" here, and go-openai's ChatCompletionMessage.MarshalJSON
// tags Content as `json:"content,omitempty"`, so an empty string drops the
// "content" key from the outgoing JSON entirely. Real OpenAI tolerates the
// missing field, but Ollama's OpenAI-compat endpoint does not — it 400s
// with "invalid message content type: <nil>". The fix must keep the
// "content" key present (even if blank) on the wire for this message shape.
func TestConvertMessage_ToolCallOnly_ContentFieldSurvivesWireMarshal(t *testing.T) {
	p := &Provider{}
	msg := llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "run_command", Arguments: map[string]interface{}{"command": "go build"}},
		},
	}

	om := p.convertMessage(msg)

	raw, err := json.Marshal(om)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("json.Unmarshal wire form: %v", err)
	}
	if _, present := wire["content"]; !present {
		t.Fatalf("wire JSON %s: \"content\" key missing — Ollama's OpenAI-compat endpoint rejects this as \"invalid message content type: <nil>\"", raw)
	}
}

// TestConvertMessage_ToolResponseEmptyResult_ContentFieldSurvivesWireMarshal
// is a regression test: the same omitempty gap fixed above for a
// tool-call-only assistant turn also applies one branch earlier, to a
// role=="tool" response message — a tool call with a legitimately empty
// result (e.g. a shell command producing 0 bytes of stdout) leaves
// Content == "" here too, dropping the "content" key from the wire the
// same way and 400ing on Ollama with the same "invalid message content
// type: <nil>" error.
func TestConvertMessage_ToolResponseEmptyResult_ContentFieldSurvivesWireMarshal(t *testing.T) {
	p := &Provider{}
	msg := llm.Message{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: ""}},
	}

	om := p.convertMessage(msg)

	raw, err := json.Marshal(om)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("json.Unmarshal wire form: %v", err)
	}
	if _, present := wire["content"]; !present {
		t.Fatalf("wire JSON %s: \"content\" key missing — Ollama's OpenAI-compat endpoint rejects this as \"invalid message content type: <nil>\"", raw)
	}
}
