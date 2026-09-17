// Package gemini provides a Google Gemini implementation of the LLM provider interface.
// It uses the google.golang.org/genai SDK which supports both AI Studio and Vertex AI backends,
// including thought signatures required by Gemini 3.x models.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"cloud.google.com/go/auth/credentials"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"google.golang.org/genai"
)

// Provider implements the LLMProvider interface for Google Gemini
type Provider struct {
	client *genai.Client
	model  string
	config *llm.ProviderConfig
}

// NewProvider creates a new Gemini provider.
// Authentication priority: API key > credentials file > ADC.
func NewProvider(ctx context.Context, config *llm.ProviderConfig) (*Provider, error) {
	cc := &genai.ClientConfig{
		Backend: genai.BackendGeminiAPI,
	}

	switch {
	case config.APIKey != "":
		cc.APIKey = config.APIKey
	case config.CredentialsFile != "":
		// Service account — use Vertex AI backend with credentials from file.
		cc.Backend = genai.BackendVertexAI

		jsonBytes, err := os.ReadFile(config.CredentialsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read credentials file %s: %w", config.CredentialsFile, err)
		}

		creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			CredentialsJSON: jsonBytes,
			Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create credentials from %s: %w", config.CredentialsFile, err)
		}
		cc.Credentials = creds

		// Project: config > service account JSON > env var
		if config.Project != "" {
			cc.Project = config.Project
		} else {
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			if err := json.Unmarshal(jsonBytes, &sa); err == nil && sa.ProjectID != "" {
				cc.Project = sa.ProjectID
			} else if proj := os.Getenv("GOOGLE_CLOUD_PROJECT"); proj != "" {
				cc.Project = proj
			}
		}

		// Location: config > env var > default
		cc.Location = resolveLocation(config.Location)
	default:
		// ADC: auto-detected from environment.
		// Switch to Vertex AI if GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_GENAI_USE_VERTEXAI is set.
		if os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") == "true" || os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
			cc.Backend = genai.BackendVertexAI

			// Project: config > env var
			if config.Project != "" {
				cc.Project = config.Project
			} else if proj := os.Getenv("GOOGLE_CLOUD_PROJECT"); proj != "" {
				cc.Project = proj
			}

			// Location: config > env var > default
			cc.Location = resolveLocation(config.Location)
		}
	}

	client, err := genai.NewClient(ctx, cc)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Provider{
		client: client,
		model:  config.Model,
		config: config,
	}, nil
}

// resolveLocation returns the Vertex AI location from config, env var, or default.
func resolveLocation(configLoc string) string {
	if configLoc != "" {
		return configLoc
	}
	if loc := os.Getenv("GOOGLE_CLOUD_LOCATION"); loc != "" {
		return loc
	}
	return "global"
}

// Generate sends a request to Gemini and returns the response
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
	config := &genai.GenerateContentConfig{}

	if systemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		}
	}

	// An explicit override is applied even when it's zero (e.g. temperature: 0
	// for deterministic output) — only the config-default path falls back to
	// the API default via the > 0 check.
	temperature := p.config.Temperature
	hasTempOverride := override.Temperature != nil
	if hasTempOverride {
		temperature = *override.Temperature
	}
	if hasTempOverride || temperature > 0 {
		t := float32(temperature)
		config.Temperature = &t
	}

	maxTokens := p.config.MaxTokens
	if override.MaxTokens != nil {
		maxTokens = *override.MaxTokens
	}
	if maxTokens > 0 {
		config.MaxOutputTokens = int32(maxTokens)
	}

	topP := p.config.TopP
	hasTopPOverride := override.TopP != nil
	if hasTopPOverride {
		topP = *override.TopP
	}
	if hasTopPOverride || topP > 0 {
		tp := float32(topP)
		config.TopP = &tp
	}

	if len(tools) > 0 {
		config.Tools = p.buildTools(tools)
	}

	contents := p.buildContents(history)
	if len(contents) == 0 {
		return nil, fmt.Errorf("no messages to send")
	}

	model := p.model
	if override.Model != nil && *override.Model != "" {
		model = *override.Model
	}

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return nil, wrapAPIError("gemini API error", err)
	}

	return p.parseResponse(resp), nil
}

// GetName returns the provider name
func (p *Provider) GetName() string {
	return "gemini"
}

// GetModel returns the model being used
func (p *Provider) GetModel() string {
	return p.model
}

// buildContents converts our message format to Gemini's Content format.
func (p *Provider) buildContents(history []llm.Message) []*genai.Content {
	var contents []*genai.Content

	for _, msg := range history {
		switch msg.Role {
		case "user":
			parts := make([]*genai.Part, 0, len(msg.Content))
			for _, b := range msg.Content {
				switch b.Type {
				case llm.ContentTypeText:
					parts = append(parts, &genai.Part{Text: b.Text})
				case llm.ContentTypeImage:
					decoded, err := base64.StdEncoding.DecodeString(b.Source.Base64)
					if err != nil {
						// llm.LoadImageAttachment is the only encoder — this
						// should never fire. Skip rather than send a
						// corrupt/partial payload to the provider.
						continue
					}
					parts = append(parts, genai.NewPartFromBytes(decoded, b.MIMEType))
				}
			}
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: parts,
			})

		case "assistant":
			var parts []*genai.Part
			if text := msg.AsText(); text != "" {
				part := &genai.Part{Text: text}
				if msg.Metadata != nil {
					if sig, ok := msg.Metadata["thought_signature"].(string); ok {
						if decoded, err := base64.StdEncoding.DecodeString(sig); err == nil {
							part.ThoughtSignature = decoded
						}
					}
				}
				parts = append(parts, part)
			}
			for _, tc := range msg.ToolCalls {
				part := &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: tc.Arguments,
					},
				}
				if tc.Metadata != nil {
					if sig, ok := tc.Metadata["thought_signature"].(string); ok {
						if decoded, err := base64.StdEncoding.DecodeString(sig); err == nil {
							part.ThoughtSignature = decoded
						}
					}
				}
				parts = append(parts, part)
			}
			if len(parts) > 0 {
				contents = append(contents, &genai.Content{
					Role:  "model",
					Parts: parts,
				})
			}

		case "tool":
			funcResp := &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					ID:   msg.ToolCallID,
					Name: msg.Name,
					Response: map[string]any{
						// msg.Content is []ContentBlock; Response is map[string]any
						// so the compiler won't catch a raw msg.Content here — it
						// must be the flattened text, not the block slice itself.
						"output": msg.AsText(),
					},
				},
			}
			if msg.Metadata != nil {
				if sig, ok := msg.Metadata["thought_signature"].(string); ok {
					if decoded, err := base64.StdEncoding.DecodeString(sig); err == nil {
						funcResp.ThoughtSignature = decoded
					}
				}
			}

			// Merge consecutive tool results into one user message
			if len(contents) > 0 {
				lastContent := contents[len(contents)-1]
				if lastContent.Role == "user" && hasFunctionResponse(lastContent) {
					lastContent.Parts = append(lastContent.Parts, funcResp)
					continue
				}
			}
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{funcResp},
			})

		case "system":
			continue
		}
	}

	return contents
}

// hasFunctionResponse checks if a Content has any FunctionResponse parts
func hasFunctionResponse(c *genai.Content) bool {
	for _, part := range c.Parts {
		if part.FunctionResponse != nil {
			return true
		}
	}
	return false
}

// buildTools converts our tool definitions to Gemini's format
func (p *Provider) buildTools(tools []llm.ToolDefinition) []*genai.Tool {
	var declarations []*genai.FunctionDeclaration

	for _, tool := range tools {
		fd := &genai.FunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
		}
		if tool.Parameters != nil {
			fd.Parameters = jsonSchemaToGeminiSchema(tool.Parameters)
		}
		declarations = append(declarations, fd)
	}

	return []*genai.Tool{
		{FunctionDeclarations: declarations},
	}
}

// parseResponse converts Gemini's response to our format
func (p *Provider) parseResponse(resp *genai.GenerateContentResponse) *llm.GenerateResult {
	result := &llm.GenerateResult{}

	if len(resp.Candidates) == 0 {
		// No candidates usually means the prompt itself was blocked before
		// generation started; report it distinctly from a clean completion.
		result.FinishReason = "content_filter"
		return result
	}

	candidate := resp.Candidates[0]

	if candidate.Content != nil {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				if result.Response != "" {
					result.Response += "\n"
				}
				result.Response += part.Text
				// A text-only turn (no tool call) still needs its thought
				// signature preserved for round-tripping — Gemini 3.x
				// requires it, and the FunctionCall branch below only ever
				// captured it for tool-call parts, silently dropping it here.
				if len(part.ThoughtSignature) > 0 {
					if result.ProviderMetadata == nil {
						result.ProviderMetadata = map[string]interface{}{}
					}
					result.ProviderMetadata["thought_signature"] = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				}
			}
			if part.FunctionCall != nil {
				tc := llm.ToolCall{
					ID:        part.FunctionCall.ID,
					Name:      part.FunctionCall.Name,
					Arguments: part.FunctionCall.Args,
				}
				if tc.ID == "" {
					tc.ID = fmt.Sprintf("call_%s_%d", tc.Name, len(result.ToolCalls))
				}
				// Preserve thought signature for round-tripping
				if len(part.ThoughtSignature) > 0 {
					tc.Metadata = map[string]interface{}{
						"thought_signature": base64.StdEncoding.EncodeToString(part.ThoughtSignature),
					}
				}
				result.ToolCalls = append(result.ToolCalls, tc)
			}
		}
	}

	switch candidate.FinishReason {
	case genai.FinishReasonStop:
		if len(result.ToolCalls) > 0 {
			result.FinishReason = "tool_calls"
		} else {
			result.FinishReason = "stop"
		}
	case genai.FinishReasonMaxTokens:
		result.FinishReason = "length"
	default:
		// SAFETY, RECITATION, OTHER, BLOCKLIST, PROHIBITED_CONTENT, etc. —
		// the response was blocked or cut short for a reason other than a
		// clean stop; don't let callers mistake it for one.
		result.FinishReason = "content_filter"
	}

	if resp.UsageMetadata != nil {
		// TotalTokenCount is the SDK's own sum of prompt + candidates +
		// tool-use-prompt + thoughts token counts — use it directly rather
		// than re-summing just two of the four components, which silently
		// undercounted extended-thinking Gemini 3.x usage/cost. Thinking
		// tokens are billed as output, so fold them into OutputTokens too,
		// matching how the OpenAI/Anthropic providers already report a
		// single output/completion figure that includes reasoning tokens.
		result.TokenUsage = &llm.TokenUsage{
			InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount) + int(resp.UsageMetadata.ThoughtsTokenCount),
			TotalTokens:  int(resp.UsageMetadata.TotalTokenCount),
		}
	}

	return result
}

// jsonSchemaToGeminiSchema converts a JSON Schema map to a genai.Schema.
func jsonSchemaToGeminiSchema(schema map[string]interface{}) *genai.Schema {
	s := &genai.Schema{}

	if t, ok := schema["type"].(string); ok {
		switch t {
		case "object":
			s.Type = genai.TypeObject
		case "string":
			s.Type = genai.TypeString
		case "number":
			s.Type = genai.TypeNumber
		case "integer":
			s.Type = genai.TypeInteger
		case "boolean":
			s.Type = genai.TypeBoolean
		case "array":
			s.Type = genai.TypeArray
		}
	}

	if desc, ok := schema["description"].(string); ok {
		s.Description = desc
	}

	if enum, ok := schema["enum"].([]interface{}); ok {
		for _, e := range enum {
			s.Enum = append(s.Enum, fmt.Sprintf("%v", e))
		}
	}

	if props, ok := schema["properties"].(map[string]interface{}); ok {
		s.Properties = make(map[string]*genai.Schema)
		for name, prop := range props {
			if propMap, ok := prop.(map[string]interface{}); ok {
				s.Properties[name] = jsonSchemaToGeminiSchema(propMap)
			}
		}
	}

	if req, ok := schema["required"].([]interface{}); ok {
		for _, r := range req {
			s.Required = append(s.Required, fmt.Sprintf("%v", r))
		}
	}

	if items, ok := schema["items"].(map[string]interface{}); ok {
		s.Items = jsonSchemaToGeminiSchema(items)
	}

	return s
}
