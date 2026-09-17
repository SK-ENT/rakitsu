package format

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// ToolCallTagInline handles models that emit tool calls as XML-ish tags
// embedded in the content stream: <tool_call>{"name":"...","arguments":{...}}</tool_call>
//
// This is the vLLM / Qwen3 convention for models that haven't been trained
// with the OpenAI function-calling protocol but still want to call tools.
// The content stream contains a mix of natural text and tool_call tags.
//
// ToolCallTagInline is a POST-PROCESSING adapter — it delegates all delta/full
// handling to an inner adapter (StandardOpenAI by default) and then transforms
// the inner adapter's Result in Finalize: <tool_call> tags are parsed into
// structured llm.ToolCall values and removed from Content. If the inner
// adapter already produced tool calls via the OpenAI standard path, this
// adapter leaves them alone — the inline parsing is a fallback only.
//
// Use via Composite when a model emits BOTH <think> tags and <tool_call>
// tags (Qwen3-coder, some DeepSeek variants).
type ToolCallTagInline struct {
	// Inner is the adapter that handles raw delta/full processing before
	// this adapter post-processes the result. If nil, defaults to StandardOpenAI.
	Inner ResponseFormat
}

func (ToolCallTagInline) Name() string { return "tool_call_tag_inline" }

// toolCallTagRegex matches <tool_call>{json}</tool_call> blocks. Identical to
// the original toolCallTagRe in openai/provider.go.
var toolCallTagRegex = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

func (t ToolCallTagInline) inner() ResponseFormat {
	if t.Inner != nil {
		return t.Inner
	}
	return StandardOpenAI{}
}

func (t ToolCallTagInline) ApplyDelta(state *FormatState, delta RawDelta) string {
	return t.inner().ApplyDelta(state, delta)
}

func (t ToolCallTagInline) ApplyFull(state *FormatState, msg RawMessage) {
	t.inner().ApplyFull(state, msg)
}

func (t ToolCallTagInline) Finalize(state *FormatState) Result {
	r := t.inner().Finalize(state)

	// Only run inline tool-call extraction when the inner adapter didn't
	// already produce structured tool calls via the standard OpenAI path.
	// If it did, the model is using the real protocol and the <tool_call>
	// tags (if any) would be duplicates or noise — trust the structured ones.
	if len(r.ToolCalls) > 0 || r.Content == "" {
		return r
	}

	parsed, cleaned := parseInlineToolCalls(r.Content)
	if len(parsed) > 0 {
		r.ToolCalls = parsed
		r.Content = cleaned
		// When content-only tool calls are found, the model intended this as a
		// tool-calling turn. Update FinishReason to match standard OpenAI
		// semantics so the agent loop treats it correctly.
		r.FinishReason = "tool_calls"
	}
	return r
}

// parseInlineToolCalls extracts tool calls from <tool_call>{json}</tool_call>
// blocks in a content string. Returns the parsed calls and the content with
// all tool_call tags removed and surrounding whitespace trimmed. Behavior
// mirrors the original parseContentToolCalls helper in openai/provider.go.
func parseInlineToolCalls(content string) ([]llm.ToolCall, string) {
	matches := toolCallTagRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, content
	}

	var toolCalls []llm.ToolCall
	for i, m := range matches {
		var raw struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &raw); err != nil {
			continue
		}
		if raw.Name == "" {
			continue
		}
		toolCalls = append(toolCalls, llm.ToolCall{
			ID:        fmt.Sprintf("content_tc_%d", i),
			Name:      raw.Name,
			Arguments: llm.NormalizeArgKeys(raw.Arguments),
		})
	}

	cleaned := toolCallTagRegex.ReplaceAllString(content, "")
	cleaned = strings.TrimSpace(cleaned)
	return toolCalls, cleaned
}
