package llm

import "testing"

func img(b64 string) ContentBlock {
	return ContentBlock{Type: ContentTypeImage, MIMEType: "image/png", Source: &BlockSource{Kind: SourceKindBase64, Base64: b64}}
}

// for APIs whose tool-result messages are text-only, tool images move
// into one user message placed right after the run of tool messages — never
// between two tool messages, which would break the tool_call → tool pairing.
func TestMoveToolImagesToUser(t *testing.T) {
	history := []Message{
		NewTextMessage("user", "explain a.png and b.png"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "read_image"}, {ID: "2", Name: "read_image"}}},
		{Role: "tool", ToolCallID: "1", Content: []ContentBlock{{Type: ContentTypeText, Text: "Loaded a.png"}, img("AAAA")}},
		{Role: "tool", ToolCallID: "2", Content: []ContentBlock{{Type: ContentTypeText, Text: "Loaded b.png"}, img("BBBB")}},
		NewTextMessage("assistant", "two screenshots"),
	}
	got := MoveToolImagesToUser(history)

	wantRoles := []string{"user", "assistant", "tool", "tool", "user", "assistant"}
	if len(got) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d", len(got), len(wantRoles))
	}
	for i, r := range wantRoles {
		if got[i].Role != r {
			t.Fatalf("message %d role = %q, want %q", i, got[i].Role, r)
		}
	}
	for _, i := range []int{2, 3} {
		if got[i].HasNonTextContent() {
			t.Errorf("tool message %d still carries non-text content", i)
		}
	}
	if got[2].AsText() != "Loaded a.png" || got[3].ToolCallID != "2" {
		t.Errorf("tool messages lost their text or call ID: %+v / %+v", got[2], got[3])
	}
	carrier := got[4]
	var n int
	for _, b := range carrier.Content {
		if b.Type == ContentTypeImage {
			n++
		}
	}
	if n != 2 || carrier.AsText() == "" {
		t.Fatalf("carrier = %+v, want a text line plus both images", carrier)
	}
	// The input must not be mutated: the agent keeps using it.
	if !history[2].HasNonTextContent() {
		t.Fatal("MoveToolImagesToUser mutated the caller's history")
	}
}

func TestMoveToolImagesToUser_TrailingToolRunAndNoImages(t *testing.T) {
	plain := []Message{NewTextMessage("user", "hi"), {Role: "tool", Content: []ContentBlock{{Type: ContentTypeText, Text: "ok"}}}}
	if got := MoveToolImagesToUser(plain); len(got) != 2 {
		t.Fatalf("no images: got %d messages, want history unchanged (2)", len(got))
	}

	trailing := []Message{{Role: "tool", Content: []ContentBlock{{Type: ContentTypeText, Text: "x"}, img("CC")}}}
	got := MoveToolImagesToUser(trailing)
	if len(got) != 2 || got[1].Role != "user" || !got[1].HasNonTextContent() {
		t.Fatalf("trailing tool run: got %+v, want tool + image carrier", got)
	}
}
