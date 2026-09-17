// Package format provides pluggable response-format adapters for LLM providers.
//
// Different LLM families emit responses in incompatible shapes — OpenAI returns
// clean `content` + `tool_calls` fields, Nemotron and DeepSeek-R1 return most
// of their output on a separate `reasoning_content` field, Qwen3 inlines
// `<think>` and `<tool_call>` tags in content, etc. This package centralizes
// the logic for extracting a uniform structured result from any of those shapes.
//
// An adapter knows how to extract content, reasoning, and tool calls from a
// particular response shape. The provider selects an adapter once per request
// based on detection (config override > model-name pattern > fallback) and
// delegates all delta/message handling to it.
//
// Phase 1 scope (Gate 2 unblock, 2026-04-06):
//   - ResponseFormat interface
//   - FormatState per-request accumulator
//   - Result output struct
//   - StandardOpenAI adapter (baseline)
//   - ReasoningContentField adapter (Nemotron, DeepSeek-R1, QwQ via vLLM/LiteLLM)
//   - Model-name pattern registry
//
// Future phases will migrate the existing <think>/<tool_call> workarounds from
// internal/llm/openai/provider.go into this system.
package format

import (
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// RawDelta represents one streaming chunk's raw fields from any provider.
// Adapters read whichever fields their target format uses; fields they
// don't care about are simply ignored.
type RawDelta struct {
	Content          string                       // delta.content — standard text
	ReasoningContent string                       // delta.reasoning_content — Nemotron/DeepSeek
	ToolCalls        []RawDeltaToolCall           // delta.tool_calls — OpenAI-standard tool calls
	FinishReason     string                       // may be empty until final chunk
}

// RawDeltaToolCall is the streaming tool-call delta shape.
// ID and Name arrive on the first chunk of a given tool call; subsequent
// chunks have empty ID/Name and only Arguments fragments.
type RawDeltaToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// RawMessage represents a full (non-streaming) response message from any provider.
type RawMessage struct {
	Content          string
	ReasoningContent string
	ToolCalls        []llm.ToolCall
	FinishReason     string
}

// Result is the uniform structured output an adapter produces after processing
// all deltas (streaming) or a full message (non-streaming).
type Result struct {
	Content      string         // user-visible final answer
	Reasoning    string         // chain-of-thought, if any
	ToolCalls    []llm.ToolCall // extracted tool calls
	FinishReason string

	// Salvaged reports that Content is not a real model commitment — it's
	// reasoning-only output that the adapter promoted to Content because
	// no committed answer (marker, structured list, etc.) could be found
	// in the reasoning. Currently set by ReasoningContentField when it
	// falls through to the [REASONING-ONLY OUTPUT — …] prefix path.
	//
	// Consumers (agent loop, supervisor, chat host) should NOT treat
	// salvaged Content as a successful final answer. See Phase 6.2
	// Mitigation A in PLAN-extraction-phase-6.md.
	Salvaged bool
}

// FormatState is the per-request accumulator. Adapters mutate it as deltas
// arrive; it is not safe for concurrent use but is scoped to a single request.
type FormatState struct {
	Content         strings.Builder
	Reasoning       strings.Builder
	ToolArgBuilders map[int]*strings.Builder
	ToolMeta        map[int]toolCallMeta
	FinishReason    string

	// fullMessageToolCalls is populated by ApplyFull (non-streaming path) when
	// the provider already has structured tool calls; streaming path leaves it nil.
	fullMessageToolCalls []llm.ToolCall

	// Scratch is adapter-specific state. Only one adapter may own the scratch
	// slot per request — currently used by Sniffing to hold its buffer + the
	// committed-inner-adapter pointer.
	Scratch interface{}

	// Labels is an optional list of ALL_CAPS_LABEL strings the caller (provider)
	// extracted from the system prompt schema when the agent opted in to
	// label-prefix harvest (Phase 6 Strategy 4). Adapters that support
	// label-prefix extraction read this on Finalize; left empty by default,
	// in which case Strategy 4 is a no-op and the other strategies handle
	// extraction as before.
	Labels []string

	// OnReasoningDelta, if set, is called by adapters that stream-extract
	// reasoning text from the content channel (currently ThinkTagInline,
	// for models that wrap chain-of-thought in <think>...</think> inline
	// rather than emitting it on a separate delta field).
	//
	// The provider sets this to forward incremental reasoning to its
	// StreamChunk{Reasoning: ...} peer-emit, unifying both reasoning shapes
	// (delta.reasoning_content and inline <think>) onto one telemetry path.
	//
	// Nil-safe: adapters must check before calling. When nil, adapters still
	// accumulate reasoning into state.Reasoning silently (the original behavior,
	// before this peer-emit hook existed).
	OnReasoningDelta func(reasoning string)

	// thinkInline holds per-request state for ThinkTagInline's streaming
	// <think>...</think> parser. nil until first ApplyDelta call on a
	// ThinkTagInline adapter; lazily initialized via getThinkInlineState.
	// Kept on FormatState (rather than in Scratch) so it doesn't collide
	// with Sniffing when ThinkTagInline wraps it.
	thinkInline *thinkInlineState
}

type toolCallMeta struct {
	ID   string
	Name string
}

// NewState returns a fresh per-request state.
func NewState() *FormatState {
	return &FormatState{
		ToolArgBuilders: map[int]*strings.Builder{},
		ToolMeta:        map[int]toolCallMeta{},
	}
}

// ResponseFormat is the adapter interface. Implementations are stateless
// across requests; per-request state lives in FormatState.
type ResponseFormat interface {
	// Name returns a stable identifier for this adapter (used in logs and config).
	Name() string

	// ApplyDelta processes one streaming chunk. It updates state and returns
	// any text that should be surfaced to the tracer/UI immediately. Reasoning
	// tokens are accumulated silently (empty return) so they don't pollute the
	// live trace output.
	ApplyDelta(state *FormatState, delta RawDelta) (surface string)

	// ApplyFull processes a complete non-streaming message.
	ApplyFull(state *FormatState, msg RawMessage)

	// Finalize extracts the structured result after all deltas (or the full
	// message) have been applied. Adapters may perform final transforms here
	// (e.g. extracting a "final answer" section from accumulated reasoning).
	Finalize(state *FormatState) Result
}

// ChainNames returns the adapter names in a composition chain, innermost first.
// For base adapters, returns a single-element slice with the adapter's own name.
// For wrapping adapters with an Inner field, recurses to unwrap. Used for
// telemetry and tracer display so operators see the full chain instead of
// just the outermost wrapper.
//
// Example: ToolCallTagInline{Inner: ThinkTagInline{Inner: ReasoningContentField{}}}
//   → ["reasoning_content_field", "think_tag_inline", "tool_call_tag_inline"]
func ChainNames(a ResponseFormat) []string {
	// Unwrap known wrapping adapters in reverse order (innermost first).
	switch w := a.(type) {
	case ToolCallTagInline:
		if w.Inner != nil {
			return append(ChainNames(w.Inner), a.Name())
		}
	case ThinkTagInline:
		if w.Inner != nil {
			return append(ChainNames(w.Inner), a.Name())
		}
	}
	return []string{a.Name()}
}

// ChainName returns the "+"-joined chain name suitable for display in a single
// telemetry field. Example: "reasoning_content_field+think_tag_inline+tool_call_tag_inline".
// For base adapters and sniffing, returns just the adapter name.
func ChainName(a ResponseFormat) string {
	names := ChainNames(a)
	if len(names) == 1 {
		return names[0]
	}
	result := names[0]
	for _, n := range names[1:] {
		result += "+" + n
	}
	return result
}
