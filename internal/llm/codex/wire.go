package codex

import (
	"encoding/json"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// reasoningItemsKey is the ProviderMetadata / Message.Metadata key under
// which raw Responses "reasoning" output items are kept so they can be
// re-sent on the next turn (the backend needs them for multi-step tool use).
const reasoningItemsKey = "codex_reasoning_items"

type responsesRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []json.RawMessage `json:"input"`
	Tools             []functionTool    `json:"tools,omitempty"`
	ToolChoice        string            `json:"tool_choice"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Reasoning         *reasoningParam   `json:"reasoning,omitempty"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include"`
}

type functionTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
	Strict      bool                   `json:"strict"`
}

type reasoningParam struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// outputItem is the subset of a Responses output item we act on.
type outputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func rawItem(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func textMessage(role, contentType, text string) json.RawMessage {
	return rawItem(map[string]interface{}{
		"type": "message",
		"role": role,
		"content": []map[string]string{
			{"type": contentType, "text": text},
		},
	})
}

// buildInput maps rakitsu history onto Responses input items.
func buildInput(history []llm.Message) []json.RawMessage {
	var items []json.RawMessage
	for _, msg := range history {
		switch msg.Role {
		case "user":
			if text := msg.AsText(); text != "" {
				items = append(items, textMessage("user", "input_text", text))
			}
		case "assistant":
			items = append(items, storedReasoningItems(msg.Metadata)...)
			if text := msg.AsText(); text != "" {
				items = append(items, textMessage("assistant", "output_text", text))
			}
			for _, tc := range msg.ToolCalls {
				args, _ := json.Marshal(tc.Arguments)
				items = append(items, rawItem(map[string]interface{}{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": string(args),
				}))
			}
		case "tool":
			items = append(items, rawItem(map[string]interface{}{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.AsText(),
			}))
		}
	}
	return items
}

// storedReasoningItems reads reasoning items saved by a previous turn.
// After a JSONL round-trip they arrive as []interface{} of maps, in-process
// as []json.RawMessage; both are accepted.
func storedReasoningItems(meta map[string]interface{}) []json.RawMessage {
	if meta == nil {
		return nil
	}
	switch raw := meta[reasoningItemsKey].(type) {
	case []json.RawMessage:
		return raw
	case []interface{}:
		out := make([]json.RawMessage, 0, len(raw))
		for _, it := range raw {
			out = append(out, rawItem(it))
		}
		return out
	}
	return nil
}

func buildTools(tools []llm.ToolDefinition) []functionTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]functionTool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out[i] = functionTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params}
	}
	return out
}

func parseToolCall(it outputItem) (llm.ToolCall, error) {
	args := map[string]interface{}{}
	if it.Arguments != "" {
		if err := json.Unmarshal([]byte(it.Arguments), &args); err != nil {
			return llm.ToolCall{}, fmt.Errorf("codex: tool call %q has invalid arguments JSON: %w", it.Name, err)
		}
	}
	return llm.ToolCall{ID: it.CallID, Name: it.Name, Arguments: args}, nil
}
