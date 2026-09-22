// Package anthropic provides an Anthropic Claude implementation of the LLM provider interface.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Provider implements the LLMProvider interface for Anthropic Claude
type Provider struct {
	client *anthropic.Client
	model  string
	config *llm.ProviderConfig
}

// NewProvider creates a new Anthropic provider
func NewProvider(config *llm.ProviderConfig) *Provider {
	opts := []option.RequestOption{
		option.WithAPIKey(config.APIKey.Reveal()),
	}
	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}

	client := anthropic.NewClient(opts...)

	return &Provider{
		client: &client,
		model:  config.Model,
		config: config,
	}
}

// Generate sends a request to Anthropic and returns the response
func (p *Provider) Generate(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolDefinition,
) (*llm.GenerateResult, error) {
	return p.generate(ctx, systemPrompt, history, tools, llm.OverrideConfig{})
}

// GenerateWithOverride implements llm.OverrideAware so the debug controller
// can experiment with sampling parameters mid-run.
func (p *Provider) GenerateWithOverride(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolDefinition,
	override llm.OverrideConfig,
) (*llm.GenerateResult, error) {
	return p.generate(ctx, systemPrompt, history, tools, override)
}

func (p *Provider) generate(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolDefinition,
	override llm.OverrideConfig,
) (*llm.GenerateResult, error) {
	maxTokens := int64(p.config.MaxTokens)
	if override.MaxTokens != nil {
		maxTokens = int64(*override.MaxTokens)
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	model := p.model
	if override.Model != nil && *override.Model != "" {
		model = *override.Model
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  p.buildMessages(history),
	}

	// System prompt is a top-level parameter, not a message
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	// Set optional parameters (override wins, falls back to config). An
	// explicit override is applied even when it's zero (e.g. temperature: 0
	// for deterministic output) — only the config-default path falls back
	// to the API default via the > 0 check.
	temperature := p.config.Temperature
	hasTempOverride := override.Temperature != nil
	if hasTempOverride {
		temperature = *override.Temperature
	}
	if hasTempOverride || temperature > 0 {
		params.Temperature = anthropic.Float(temperature)
	}

	topP := p.config.TopP
	hasTopPOverride := override.TopP != nil
	if hasTopPOverride {
		topP = *override.TopP
	}
	if hasTopPOverride || topP > 0 {
		params.TopP = anthropic.Float(topP)
	}

	// Add tools
	if len(tools) > 0 {
		params.Tools = p.buildTools(tools)
	}

	// Extended thinking budget cap (must be >= 1024 and < max_tokens)
	if p.config.MaxThinkingTokens >= 1024 && int64(p.config.MaxThinkingTokens) < maxTokens {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(p.config.MaxThinkingTokens))
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, wrapAPIError("anthropic API error", err)
	}

	return p.parseResponse(resp)
}

// GetName returns the provider name
func (p *Provider) GetName() string {
	return "anthropic"
}

// GetModel returns the model being used
func (p *Provider) GetModel() string {
	return p.model
}

// buildMessages converts our message format to Anthropic's format.
// Anthropic requires alternating user/assistant messages.
// Tool results must be sent as tool_result content blocks within user messages.
func (p *Provider) buildMessages(history []llm.Message) []anthropic.MessageParam {
	var messages []anthropic.MessageParam

	for _, msg := range history {
		switch msg.Role {
		case "user":
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
			for _, b := range msg.Content {
				switch b.Type {
				case llm.ContentTypeText:
					blocks = append(blocks, anthropic.NewTextBlock(b.Text))
				case llm.ContentTypeImage:
					blocks = append(blocks, anthropic.NewImageBlockBase64(b.MIMEType, b.Source.Base64))
				}
			}
			messages = append(messages, anthropic.NewUserMessage(blocks...))

		case "assistant":
			var blocks []anthropic.ContentBlockParamUnion
			// Re-inject stored thinking blocks first (required for Anthropic
			// extended thinking round-trips). msg.Metadata may hold the
			// native []map[string]interface{} (in-process, e.g. tests
			// constructing history directly) or []interface{} with
			// map[string]interface{} elements (after a JSON round-trip —
			// the JSONL session store / debugger replay path decodes a JSON
			// array targeting interface{} as []interface{}, never the
			// concrete slice type). A rigid single-type assertion silently
			// dropped thinking blocks on the replay path instead of erroring.
			var thinkingBlocks []map[string]interface{}
			switch raw := msg.Metadata["anthropic_thinking_blocks"].(type) {
			case []map[string]interface{}:
				thinkingBlocks = raw
			case []interface{}:
				for _, item := range raw {
					if b, ok := item.(map[string]interface{}); ok {
						thinkingBlocks = append(thinkingBlocks, b)
					}
				}
			}
			for _, b := range thinkingBlocks {
				switch b["type"] {
				case "thinking":
					sig, _ := b["signature"].(string)
					txt, _ := b["thinking"].(string)
					blocks = append(blocks, anthropic.NewThinkingBlock(sig, txt))
				case "redacted_thinking":
					data, _ := b["data"].(string)
					blocks = append(blocks, anthropic.NewRedactedThinkingBlock(data))
				}
			}
			// Add text content if present
			if text := msg.AsText(); text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(text))
			}
			// Add tool use blocks
			for _, tc := range msg.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, tc.Arguments, tc.Name))
			}
			if len(blocks) > 0 {
				messages = append(messages, anthropic.NewAssistantMessage(blocks...))
			}

		case "tool":
			// Tool results become tool_result content blocks in a user message.
			// If the previous message is also a tool result (user role with tool results),
			// we need to merge them into one user message since Anthropic requires
			// alternating roles.
			toolBlock := anthropic.NewToolResultBlock(msg.ToolCallID, msg.AsText(), false)

			if len(messages) > 0 {
				lastMsg := &messages[len(messages)-1]
				if lastMsg.Role == anthropic.MessageParamRoleUser {
					// Merge into existing user message
					lastMsg.Content = append(lastMsg.Content, toolBlock)
					continue
				}
			}
			messages = append(messages, anthropic.NewUserMessage(toolBlock))

		case "system":
			// System messages are handled at the top level, skip here
			continue
		}
	}

	return messages
}

// buildTools converts our tool definitions to Anthropic's format
func (p *Provider) buildTools(tools []llm.ToolDefinition) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, len(tools))
	for i, tool := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: tool.Parameters["properties"],
		}
		if req, ok := tool.Parameters["required"].([]interface{}); ok {
			required := make([]string, len(req))
			for j, r := range req {
				required[j] = fmt.Sprintf("%v", r)
			}
			schema.Required = required
		}

		result[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: schema,
			},
		}
	}
	return result
}

// parseResponse converts Anthropic's response to our format
func (p *Provider) parseResponse(resp *anthropic.Message) (*llm.GenerateResult, error) {
	result := &llm.GenerateResult{}

	// Extract text and tool use blocks from response content
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if result.Response != "" {
				result.Response += "\n"
			}
			result.Response += block.Text
		case "tool_use":
			var args map[string]interface{}
			if err := json.Unmarshal(block.Input, &args); err != nil {
				return nil, fmt.Errorf("anthropic: parsing tool_use arguments for %s: %w", block.Name, err)
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		case "thinking":
			tb := block.AsThinking()
			result.ThinkingContent += tb.Thinking
			result.ProviderMetadata = appendThinkingBlock(result.ProviderMetadata, map[string]interface{}{
				"type":      "thinking",
				"thinking":  tb.Thinking,
				"signature": tb.Signature,
			})
		case "redacted_thinking":
			rb := block.AsRedactedThinking()
			result.ProviderMetadata = appendThinkingBlock(result.ProviderMetadata, map[string]interface{}{
				"type": "redacted_thinking",
				"data": rb.Data,
			})
		}
	}

	// Map finish reason
	switch resp.StopReason {
	case "end_turn":
		result.FinishReason = "stop"
	case "tool_use":
		result.FinishReason = "tool_calls"
	case "max_tokens":
		result.FinishReason = "length"
	default:
		result.FinishReason = string(resp.StopReason)
	}

	// Token usage
	result.TokenUsage = &llm.TokenUsage{
		InputTokens:  int(resp.Usage.InputTokens),
		OutputTokens: int(resp.Usage.OutputTokens),
		TotalTokens:  int(resp.Usage.InputTokens) + int(resp.Usage.OutputTokens),
	}

	return result, nil
}

// appendThinkingBlock appends a thinking/redacted_thinking block map to the
// "anthropic_thinking_blocks" key in ProviderMetadata, initialising the map if needed.
func appendThinkingBlock(meta map[string]interface{}, block map[string]interface{}) map[string]interface{} {
	if meta == nil {
		meta = map[string]interface{}{}
	}
	existing, _ := meta["anthropic_thinking_blocks"].([]map[string]interface{})
	meta["anthropic_thinking_blocks"] = append(existing, block)
	return meta
}
