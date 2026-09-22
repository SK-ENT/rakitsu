// Package llm provides the LLM provider interface and implementations.
// It defines the abstract interface for LLM interactions, allowing
// different providers (OpenAI, Anthropic, etc.) to be plugged in.
package llm

import (
	"context"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/secret"
)

// ContentType identifies the kind of content carried by a ContentBlock.
// The current phase introduces these types but only ever constructs
// ContentTypeText blocks; the other four arrive in later phases.
type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeImage    ContentType = "image"
	ContentTypeDocument ContentType = "document"
	ContentTypeAudio    ContentType = "audio"
	ContentTypeVideo    ContentType = "video"
)

// SourceKind identifies how a non-text ContentBlock's payload is carried.
type SourceKind string

const (
	SourceKindURL    SourceKind = "url"
	SourceKindBase64 SourceKind = "base64"
	SourceKindFileID SourceKind = "file_id"
)

// BlockSource carries the payload location for a non-text ContentBlock.
// Zero value (unused) for ContentTypeText blocks.
type BlockSource struct {
	Kind   SourceKind `json:"kind,omitempty"`
	URL    string     `json:"url,omitempty"`
	Base64 string     `json:"base64,omitempty"`
	FileID string     `json:"file_id,omitempty"` // provider-native file upload (OpenAI Files API, etc.)
}

// ContentBlock is one typed unit of message content.
type ContentBlock struct {
	Type     ContentType    `json:"type"`
	Text     string         `json:"text,omitempty"`      // ContentTypeText
	Source   *BlockSource   `json:"source,omitempty"`    // non-text types; omitempty only works on struct fields via a pointer
	MIMEType string         `json:"mime_type,omitempty"` // "image/png", "application/pdf", ...
	Metadata map[string]any `json:"metadata,omitempty"`  // modality-specific (e.g. duration_seconds, page_count)
}

// Message represents a message in the conversation history
type Message struct {
	Role       string                 `json:"role"`    // "system", "user", "assistant", "tool"
	Content    []ContentBlock         `json:"content"` // The message content
	ToolCalls  []ToolCall             `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"` // For tool response messages
	Name       string                 `json:"name,omitempty"`         // For tool response messages
	Metadata   map[string]interface{} `json:"metadata,omitempty"`     // Provider-specific data
}

// NewTextMessage builds a text-only Message. This is the back-compat
// constructor every legacy string-based caller uses in place of a literal
// Message{Content: "..."}. Hand-built ContentBlock literals are for
// phase-1+ non-text callers only.
func NewTextMessage(role, text string) Message {
	return Message{Role: role, Content: []ContentBlock{{Type: ContentTypeText, Text: text}}}
}

// AsText concatenates every text block's Text, in document order, ignoring
// non-text blocks. Returns "" for a nil or empty Content slice.
//
// Phase 0: since no code path constructs non-text blocks yet, AsText()
// silently dropping them is a no-op in practice. Phase 1 image/doc/audio/
// video wiring must NOT rely on AsText() for messages carrying non-text
// blocks — each provider's translation loop needs to branch on
// ContentBlock.Type directly once that phase lands.
func (m Message) AsText() string {
	if len(m.Content) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, b := range m.Content {
		if b.Type == ContentTypeText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// HasNonTextContent reports whether this message carries at least one
// non-text block (image, document, audio, video). Providers use this to
// decide between a plain-text payload and a multi-part one — e.g. OpenAI's
// ChatCompletionMessage.Content and .MultiContent are mutually exclusive,
// so convertMessage branches on this before choosing which to populate.
func (m Message) HasNonTextContent() bool {
	for _, b := range m.Content {
		if b.Type != ContentTypeText {
			return true
		}
	}
	return false
}

// ToolCall represents a tool call from the LLM
type ToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`          // Structured JSON, not raw string
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // Provider-specific data (e.g., Gemini thought signatures)
}

// NormalizeArgKeys trims leading/trailing whitespace from argument map keys.
// Some models (notably Gemini) occasionally emit keys like `" args"` with
// stray whitespace, causing parameter lookup failures downstream.
func NormalizeArgKeys(args map[string]interface{}) map[string]interface{} {
	if args == nil {
		return nil
	}
	normalized := make(map[string]interface{}, len(args))
	for k, v := range args {
		normalized[strings.TrimSpace(k)] = v
	}
	return normalized
}

// ToolDefinition defines a tool that can be used by the LLM
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // JSON Schema for validation
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// GenerateResult is the result of a Generate call
type GenerateResult struct {
	Response         string                 `json:"response"`
	ToolCalls        []ToolCall             `json:"tool_calls,omitempty"`
	TokenUsage       *TokenUsage            `json:"token_usage,omitempty"`
	FinishReason     string                 `json:"finish_reason"`               // "stop", "tool_calls", "length", etc.
	ThinkingContent  string                 `json:"thinking_content,omitempty"`  // extracted thinking/reasoning text
	ProviderMetadata map[string]interface{} `json:"provider_metadata,omitempty"` // provider-specific data for round-tripping
	FormatInfo       *FormatInfo            `json:"format_info,omitempty"`       // response-format adapter that handled this result

	// Salvaged is set by providers that use internal/llm/format/ when the
	// adapter could not extract a committed final answer and fell back to
	// promoting raw reasoning text as content (with the [REASONING-ONLY OUTPUT]
	// marker prefix). Agent loops MUST treat a salvaged response as
	// not-yet-done — see Phase 6.2 Mitigation A.
	Salvaged bool `json:"salvaged,omitempty"`
}

// FormatInfo reports which response-format adapter handled a given request.
// Providers that implement the internal/llm/format/ adapter system populate
// this on every result; providers that don't leave it nil. Consumers (the
// agent layer for telemetry, the tracer, the debugger) can read it opportunistically.
type FormatInfo struct {
	Adapter  string `json:"adapter"`  // committed adapter name (e.g. "reasoning_content_field")
	Reason   string `json:"reason"`   // "pattern_match" | "override" | "sniff_commit" | "fallback"
	Resolved string `json:"resolved"` // "registry" | "sniffing"
}

// LLMProvider is the interface for LLM providers
// All implementations must support context for cancellation and timeout
type LLMProvider interface {
	// Generate sends a request to the LLM and returns the response
	// The context allows for cancellation and timeout
	Generate(
		ctx context.Context,
		systemPrompt string,
		history []Message,
		tools []ToolDefinition,
	) (*GenerateResult, error)

	// GetName returns the provider name (e.g., "openai", "anthropic")
	GetName() string

	// GetModel returns the model being used
	GetModel() string
}

// StreamChunk represents a single piece of a streaming response.
//
// At most one of Text, Reasoning, or Done is meaningful per chunk:
//   - Text:      a fragment of the user-visible answer (content stream).
//   - Reasoning: a fragment of the model's chain-of-thought
//     (reasoning_content stream). Reasoning models emit a large
//     volume of these before committing to a tool call or
//     final content. Surfaced as a separate channel so consumers
//     can render reasoning in a distinct lane without conflating
//     it with the visible answer.
//   - Done:      stream terminator, no payload.
type StreamChunk struct {
	Text      string // Incremental answer content
	Reasoning string // Incremental reasoning_content text (CoT)
	Done      bool   // True when stream is complete
}

// StreamResult carries channels for streaming responses
type StreamResult struct {
	Chunks <-chan StreamChunk     // Incremental text chunks
	Final  <-chan *GenerateResult // Final assembled result (sent once, when done)
	Err    <-chan error           // Error channel (sent once if error occurs)
}

// StreamingProvider is an optional interface for providers that support streaming.
// Providers implement this alongside LLMProvider to enable token-level output.
type StreamingProvider interface {
	LLMProvider
	GenerateStream(
		ctx context.Context,
		systemPrompt string,
		history []Message,
		tools []ToolDefinition,
	) (*StreamResult, error)
}

// ProviderConfig contains common configuration for LLM providers
type ProviderConfig struct {
	// APIKey is wrapped in secret.Value so it can't be accidentally
	// printed/logged by this or a future call site — use .Reveal() only
	// at the point a provider constructor actually needs the raw string
	// (building an Authorization/x-api-key header).
	APIKey            secret.Value
	Model             string
	BaseURL           string // optional: custom endpoint (e.g., Ollama at http://localhost:11434/v1)
	CredentialsFile   string // optional: path to service account JSON key file (Gemini)
	Location          string // optional: cloud region (e.g., "us-central1", "global") for Vertex AI
	Project           string // optional: GCP project ID for Vertex AI
	Temperature       float64
	MaxTokens         int
	TopP              float64
	TimeoutSec        int    // optional: per-request timeout in seconds (sent as X-LiteLLM-Timeout header)
	MaxThinkingTokens int    // optional: thinking budget cap (Anthropic extended thinking; must be >=1024)
	ResponseFormat    string // optional: explicit format-adapter override ("standard_openai", "reasoning_content_field"). Empty = auto-detect from model name.
	ReasoningEffort   string // optional: OpenAI reasoning_effort ("none", "minimal", "low", "medium", "high"); GPT-5.6 models require it when function tools are sent
}
