package agentchat

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// tools can now put images into history, so a retained transcript
// must count an image by its payload size — otherwise a multi-MB block
// slips past max_transcript_bytes as if it were ~70 bytes.
func TestMessageBytes_CountsImagePayload(t *testing.T) {
	payload := strings.Repeat("A", 100_000)
	m := llm.Message{Role: "tool", Content: []llm.ContentBlock{{
		Type:     llm.ContentTypeImage,
		MIMEType: "image/png",
		Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: payload},
	}}}
	if got := messageBytes(m); got < len(payload) {
		t.Fatalf("messageBytes = %d, want at least the %d-byte base64 payload", got, len(payload))
	}
}
