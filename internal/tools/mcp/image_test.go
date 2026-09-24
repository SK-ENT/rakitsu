package mcp

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Regression: image content items in a tools/call result were silently
// dropped, so screenshot tools (Playwright MCP, computer-use servers) were
// blind. They must come back as image blocks.
func TestParseToolCallContent_ImageItems(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("fake-png"))
	raw := []byte(`{"content":[
		{"type":"text","text":"Took a screenshot"},
		{"type":"image","data":"` + data + `","mimeType":"image/png"},
		{"type":"resource","resource":{"uri":"file:///s.jpg","mimeType":"image/jpeg","blob":"` + data + `"}}
	]}`)
	text, blocks, err := parseToolCallContent(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if text != "Took a screenshot" {
		t.Errorf("text = %q", text)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (image + image resource)", len(blocks))
	}
	for i, want := range []string{"image/png", "image/jpeg"} {
		b := blocks[i]
		if b.Type != llm.ContentTypeImage || b.MIMEType != want || b.Source == nil || b.Source.Base64 != data {
			t.Errorf("block %d = %+v, want %s image with the payload", i, b, want)
		}
	}
}

// An image type no provider accepts becomes a text note instead of a block —
// never a silent drop, never a provider 400.
func TestParseToolCallContent_UnsupportedImageNoted(t *testing.T) {
	raw := []byte(`{"content":[{"type":"image","data":"AA==","mimeType":"image/svg+xml"}]}`)
	text, blocks, err := parseToolCallContent(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("got %d blocks, want 0", len(blocks))
	}
	if !strings.Contains(text, "[image omitted: image/svg+xml") {
		t.Errorf("text = %q, want an image-omitted note", text)
	}
}
