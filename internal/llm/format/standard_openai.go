package format

import (
	"sort"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// StandardOpenAI is the baseline adapter for clean OpenAI-shaped responses:
// `content` + `tool_calls` fields, no reasoning_content, no inline tags.
// This is the default when no model-specific adapter matches.
type StandardOpenAI struct{}

func (StandardOpenAI) Name() string { return "standard_openai" }

func (StandardOpenAI) ApplyDelta(state *FormatState, delta RawDelta) string {
	var surface strings.Builder

	if delta.Content != "" {
		state.Content.WriteString(delta.Content)
		surface.WriteString(delta.Content)
	}

	for _, tc := range delta.ToolCalls {
		if tc.ID != "" {
			state.ToolMeta[tc.Index] = toolCallMeta{ID: tc.ID, Name: tc.Name}
			state.ToolArgBuilders[tc.Index] = &strings.Builder{}
			surface.WriteString(tc.Name + "(")
		}
		if b, ok := state.ToolArgBuilders[tc.Index]; ok {
			b.WriteString(tc.Arguments)
			if tc.Arguments != "" {
				surface.WriteString(tc.Arguments)
			}
		}
	}

	if delta.FinishReason != "" {
		state.FinishReason = delta.FinishReason
	}

	return surface.String()
}

func (StandardOpenAI) ApplyFull(state *FormatState, msg RawMessage) {
	state.Content.WriteString(msg.Content)
	if msg.FinishReason != "" {
		state.FinishReason = msg.FinishReason
	}
	// Tool calls on full messages are already structured — cache them via a
	// synthetic "full-message" key so Finalize can return them.
	for i, tc := range msg.ToolCalls {
		state.ToolMeta[i] = toolCallMeta{ID: tc.ID, Name: tc.Name}
		// Encode arguments back through the builder so Finalize has one path.
		b := &strings.Builder{}
		state.ToolArgBuilders[i] = b
	}
	// Stash the structured tool calls on a well-known key for Finalize.
	state.fullMessageToolCalls = msg.ToolCalls
}

func (StandardOpenAI) Finalize(state *FormatState) Result {
	toolCalls := assembleToolCalls(state)
	return Result{
		Content:      state.Content.String(),
		Reasoning:    state.Reasoning.String(),
		ToolCalls:    toolCalls,
		FinishReason: state.FinishReason,
	}
}

// assembleToolCalls converts FormatState's per-index argument builders into
// concrete llm.ToolCall values. Used by StandardOpenAI and ReasoningContentField
// (which both handle tool calls via the standard OpenAI shape).
func assembleToolCalls(state *FormatState) []llm.ToolCall {
	// Non-streaming path: structured tool calls already present.
	if len(state.fullMessageToolCalls) > 0 {
		return state.fullMessageToolCalls
	}
	if len(state.ToolMeta) == 0 {
		return nil
	}
	// Go map iteration order is randomized; sort by index so multi-tool-call
	// streaming responses preserve issue order deterministically.
	keys := make([]int, 0, len(state.ToolMeta))
	for idx := range state.ToolMeta {
		keys = append(keys, idx)
	}
	sort.Ints(keys)

	toolCalls := make([]llm.ToolCall, 0, len(state.ToolMeta))
	for _, idx := range keys {
		meta := state.ToolMeta[idx]
		args := map[string]interface{}{}
		if b, ok := state.ToolArgBuilders[idx]; ok && b.Len() > 0 {
			if parsed, ok := parseJSONArgs(b.String()); ok {
				args = parsed
			}
		}
		toolCalls = append(toolCalls, llm.ToolCall{
			ID:        meta.ID,
			Name:      meta.Name,
			Arguments: llm.NormalizeArgKeys(args),
		})
	}
	return toolCalls
}
