package llm

// toolImagesNote introduces the user message that carries tool-returned
// images for APIs whose tool-result messages accept text only.
const toolImagesNote = "[Image content returned by the tool call(s) above.]"

// MoveToolImagesToUser returns history with non-text blocks taken out of
// tool messages and re-sent in one user message placed right after each run
// of consecutive tool messages. It is for APIs whose tool results are
// text-only (OpenAI Chat Completions and compatibles, Codex Responses).
// Placing the carrier after the whole run keeps every tool message directly
// after the assistant turn that called it. history itself is not modified;
// it is returned as-is when no tool message carries non-text content.
func MoveToolImagesToUser(history []Message) []Message {
	found := false
	for _, m := range history {
		if m.Role == "tool" && m.HasNonTextContent() {
			found = true
			break
		}
	}
	if !found {
		return history
	}

	out := make([]Message, 0, len(history)+1)
	var pending []ContentBlock
	flush := func() {
		if len(pending) == 0 {
			return
		}
		content := append([]ContentBlock{{Type: ContentTypeText, Text: toolImagesNote}}, pending...)
		out = append(out, Message{Role: "user", Content: content})
		pending = nil
	}
	for _, m := range history {
		if m.Role != "tool" {
			flush()
			out = append(out, m)
			continue
		}
		if !m.HasNonTextContent() {
			out = append(out, m)
			continue
		}
		text := make([]ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == ContentTypeText {
				text = append(text, b)
			} else {
				pending = append(pending, b)
			}
		}
		m.Content = text
		out = append(out, m)
	}
	flush()
	return out
}
