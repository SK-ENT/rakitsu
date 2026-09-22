// Package openai provides an OpenAI implementation of the LLM provider interface.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/llm/format"
	openai "github.com/sashabaranov/go-openai"
)

// Provider implements the LLMProvider interface for OpenAI and compatible APIs
type Provider struct {
	client              *openai.Client
	model               string
	name                string
	config              *llm.ProviderConfig
	formatAdapter       format.ResponseFormat // pluggable response-format handler
	formatResolveReason string                // "pattern_match" | "override" | "fallback"
	formatResolver      string                // "registry" | "sniffing"
}

// warnFn receives operator-facing diagnostics this package needs to surface
// without going through the normal error return path (see omitRejectedParam).
// Defaults to os.Stderr, matching every non-interactive caller (rakitsu run,
// serve, chathost). A caller whose terminal is owned by an alt-screen
// renderer — the interactive chat TUI — MUST redirect this via
// SetWarnWriter before making any provider call: a raw write to the
// terminal here races bubbletea's own concurrent render writes and
// corrupts the screen. Making the write atomic doesn't help (unlike
// internal/chat/clipboard.go's OSC 52 write, which is invisible and
// non-positional) because this text is visible and cursor-moving, so even
// one atomic Write() still lands at an arbitrary point relative to
// bubbletea's own writes and desyncs its row bookkeeping from the
// terminal's real cursor position.
var warnFn = func(s string) { fmt.Fprint(os.Stderr, s) }

// SetWarnWriter overrides where this package's operator-facing diagnostics
// go. Process-wide: a rakitsu process is either interactive or headless for
// its entire lifetime, never both, so there's no ordering hazard in setting
// this once at startup before any provider is constructed.
func SetWarnWriter(fn func(string)) { warnFn = fn }

// headerTransport injects custom HTTP headers into every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// NewProvider creates a new OpenAI provider.
// Supports custom BaseURL for OpenAI-compatible APIs (e.g., Ollama, LiteLLM).
func NewProvider(config *llm.ProviderConfig) *Provider {
	clientConfig := openai.DefaultConfig(config.APIKey.Reveal())
	if config.BaseURL != "" {
		clientConfig.BaseURL = config.BaseURL
	}

	// Inject X-LiteLLM-Timeout header if timeout is configured
	if config.TimeoutSec > 0 {
		clientConfig.HTTPClient = &http.Client{
			Transport: &headerTransport{
				base: http.DefaultTransport,
				headers: map[string]string{
					"X-LiteLLM-Timeout": strconv.Itoa(config.TimeoutSec),
				},
			},
		}
	}

	client := openai.NewClientWithConfig(clientConfig)

	name := "openai"
	if config.BaseURL != "" {
		name = "openai-compatible"
	}

	// Resolve the response-format adapter once per provider instance.
	// Detection order: config override → model-name pattern → Sniffing fallback.
	// Known reasoning models (nemotron, deepseek-r1, qwq) auto-map to
	// ReasoningContentField; unknown models get Sniffing which commits to the
	// right inner adapter based on the first few streaming deltas.
	detect := format.DetectWithReason(config.Model, config.ResponseFormat)

	return &Provider{
		client:              client,
		model:               config.Model,
		name:                name,
		config:              config,
		formatAdapter:       detect.Adapter,
		formatResolveReason: detect.Reason,
		formatResolver:      detect.Resolver,
	}
}

// isReasoningModel reports whether model is one of OpenAI's reasoning-tier
// families (o1/o3/o4/gpt-5), using the same prefix rules as go-openai's
// own ReasoningValidator (reasoning_validator.go in that module). Those
// models reject MaxTokens (must use MaxCompletionTokens instead) and any
// Temperature/TopP other than 1 — the SDK enforces this client-side, before
// the request ever reaches the network, so matching its detection here is
// what keeps every reasoning-tier call from failing outright.
func isReasoningModel(model string) bool {
	for _, prefix := range []string{"o1", "o3", "o4", "gpt-5"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// paramCoder is implemented by an error that can name the specific request
// parameter an API call rejected — populated from the SDK's structured
// APIError.Param field (see statusError.Param in errors.go).
type paramCoder interface {
	Param() (string, bool)
}

// omitRejectedParam clears req.Temperature or req.TopP when wrapped names one
// of them as the rejected param via the SDK's structured `param` field, and
// the request actually set it non-zero this call. Returns true when a field
// was cleared, meaning the caller should retry the identical request once.
// It also prints a one-line stderr warning naming the model and the value
// dropped, with a concrete fix — this is a silent recovery from the caller's
// perspective (the call ends up succeeding), and a user who never sees it has
// no way to know their configured temperature/top_p is being ignored.
//
// This exists because isReasoningModel's prefix check (o1/o3/o4/gpt-5) misses
// vendor-prefixed aliases — LiteLLM routes models as "openai/gpt-5.6-luna",
// which doesn't start with "gpt-5" even though the real backend model is
// reasoning-tier and rejects non-default temperature/top_p. Rather than
// maintain a growing prefix/alias list, read what the API already told us:
// the 400 body names the exact rejected field, per real model, per real
// proxy config, in real time. Any other param name (or none at all) returns
// false unchanged — that's a different, real problem and should keep failing
// loudly rather than being silently retried.
func omitRejectedParam(req *openai.ChatCompletionRequest, wrapped error) bool {
	var pc paramCoder
	if !errors.As(wrapped, &pc) {
		return false
	}
	param, ok := pc.Param()
	if !ok {
		return false
	}
	switch param {
	case "temperature":
		if req.Temperature == 0 {
			return false
		}
		warnFn(fmt.Sprintf("Warning: model %q rejected temperature=%v — retrying without it. "+
			"That usually means it's a reasoning-tier model, which only accepts the default (temperature: 1). "+
			"Set model_config.temperature: 1 on this agent (or drop temperature entirely) to stop relying on this retry.\n",
			req.Model, req.Temperature))
		req.Temperature = 0
		return true
	case "top_p":
		if req.TopP == 0 {
			return false
		}
		warnFn(fmt.Sprintf("Warning: model %q rejected top_p=%v — retrying without it. "+
			"That usually means it's a reasoning-tier model, which only accepts the default (top_p: 1). "+
			"Set model_config.top_p: 1 on this agent (or drop top_p entirely) to stop relying on this retry.\n",
			req.Model, req.TopP))
		req.TopP = 0
		return true
	default:
		return false
	}
}

// setMaxTokens routes the token-limit onto whichever field the model
// accepts: classic models take MaxTokens, reasoning-tier models require
// MaxCompletionTokens (go-openai's ReasoningValidator rejects a non-zero
// MaxTokens for those with ErrReasoningModelMaxTokensDeprecated).
// applyReasoningEffort sets reasoning_effort on the request when configured.
// Left unset, the field is omitted and the API default applies. GPT-5.6
// models reject function tools on /v1/chat/completions unless the field is
// sent explicitly, so configs targeting them set it to "none"
// (or any level) on the provider or per agent via model_config.
func applyReasoningEffort(req *openai.ChatCompletionRequest, effort string) {
	if effort != "" {
		req.ReasoningEffort = effort
	}
}

// applyReasoningEffortFallback sets req.ReasoningEffort to "none" and
// reports true (caller should retry once) only when wrapped's message
// names reasoning_effort as the problem (see wrapAPIError's hint) AND the
// request didn't already carry an explicit value — never override a real
// config choice, and never retry the same request twice. This reacts to
// what the API actually said rather than guessing ahead of time which
// vendor-routed model aliases (e.g. LiteLLM's "openai/gpt-5.6-luna") are
// GPT-5.6-tier — the same reasoning as omitRejectedParam above.
func applyReasoningEffortFallback(req *openai.ChatCompletionRequest, wrapped error) bool {
	if req.ReasoningEffort != "" {
		return false
	}
	if !strings.Contains(wrapped.Error(), "reasoning_effort") {
		return false
	}
	warnFn(fmt.Sprintf("Warning: model %q requires reasoning_effort with tools — retrying with "+
		"reasoning_effort: none. Set it explicitly on the provider in settings.providers (or per "+
		"agent under model_config) to stop relying on this retry.\n", req.Model))
	req.ReasoningEffort = "none"
	return true
}

func setMaxTokens(req *openai.ChatCompletionRequest, model string, maxTokens int) {
	if maxTokens <= 0 {
		return
	}
	if isReasoningModel(model) {
		req.MaxCompletionTokens = maxTokens
	} else {
		req.MaxTokens = maxTokens
	}
}

// Generate sends a request to OpenAI and returns the response
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
	// Build messages for the API
	messages := p.buildMessages(systemPrompt, history)

	// Build tools for the API
	var toolDefs []openai.Tool
	if len(tools) > 0 {
		toolDefs = p.buildTools(tools)
	}

	model := p.model
	if override.Model != nil && *override.Model != "" {
		model = *override.Model
	}

	// Create the request
	req := openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		Tools:    toolDefs,
	}

	// Set optional parameters (override wins, falls back to config). An
	// explicit override is applied even when it's zero (e.g. temperature: 0
	// for deterministic output) — only the config-default path falls back
	// to the API default via the > 0 check.
	temperature := p.config.Temperature
	hasTempOverride := false
	if override.Temperature != nil {
		temperature = *override.Temperature
		hasTempOverride = true
	}
	// Reasoning-tier models (o1/o3/o4/gpt-5) fix Temperature/TopP at 1 and
	// reject any other value client-side (ErrReasoningModelLimitationsOther)
	// — omitting a non-1 value lets the API default apply instead of failing
	// the whole request.
	reasoning := isReasoningModel(model)
	if (hasTempOverride || temperature > 0) && (!reasoning || temperature == 1) {
		req.Temperature = float32(temperature)
	}

	maxTokens := p.config.MaxTokens
	if override.MaxTokens != nil {
		maxTokens = *override.MaxTokens
	}
	setMaxTokens(&req, model, maxTokens)
	applyReasoningEffort(&req, p.config.ReasoningEffort)

	topP := p.config.TopP
	hasTopPOverride := false
	if override.TopP != nil {
		topP = *override.TopP
		hasTopPOverride = true
	}
	if (hasTopPOverride || topP > 0) && (!reasoning || topP == 1) {
		req.TopP = float32(topP)
	}

	// Call the API
	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		wrapped := wrapAPIError("openai API error", err)
		if !omitRejectedParam(&req, wrapped) && !applyReasoningEffortFallback(&req, wrapped) {
			return nil, wrapped
		}
		// Retry exactly once with the rejected field omitted, or
		// reasoning_effort defaulted — see omitRejectedParam's doc for why
		// this is safer than guessing ahead of time which models are
		// reasoning-tier.
		resp, err = p.client.CreateChatCompletion(ctx, req)
		if err != nil {
			return nil, wrapAPIError("openai API error", err)
		}
	}

	// Parse the response
	return p.parseResponse(&resp)
}

// GetName returns the provider name
func (p *Provider) GetName() string {
	return p.name
}

// GetModel returns the model being used
func (p *Provider) GetModel() string {
	return p.model
}

// buildMessages converts our message format to OpenAI's format
func (p *Provider) buildMessages(systemPrompt string, history []llm.Message) []openai.ChatCompletionMessage {
	messages := make([]openai.ChatCompletionMessage, 0, len(history)+1)

	// Add system prompt
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}

	// Add history
	for _, msg := range history {
		messages = append(messages, p.convertMessage(msg))
	}

	return messages
}

// convertMessage converts our message format to OpenAI's format
func (p *Provider) convertMessage(msg llm.Message) openai.ChatCompletionMessage {
	om := openai.ChatCompletionMessage{
		Role: msg.Role,
	}

	// Handle tool response messages
	if msg.Role == "tool" {
		om.Role = openai.ChatMessageRoleTool
		om.Content = msg.AsText()
		om.ToolCallID = msg.ToolCallID
		return om
	}

	// Handle messages with tool calls
	if len(msg.ToolCalls) > 0 {
		om.ToolCalls = make([]openai.ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			om.ToolCalls[i] = openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argsJSON),
				},
			}
		}
	}

	// Text-only messages (the common case, and the only shape assistant/tool
	// messages ever take today) keep the plain Content string — go-openai's
	// ChatCompletionMessage.MarshalJSON errors if both Content and
	// MultiContent are set, so leaving MultiContent nil here is required.
	if !msg.HasNonTextContent() {
		om.Content = msg.AsText()
		return om
	}

	// A user message carrying non-text content (e.g. --attach images) goes
	// through MultiContent instead.
	om.MultiContent = make([]openai.ChatMessagePart, 0, len(msg.Content))
	for _, b := range msg.Content {
		switch b.Type {
		case llm.ContentTypeText:
			om.MultiContent = append(om.MultiContent, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: b.Text,
			})
		case llm.ContentTypeImage:
			om.MultiContent = append(om.MultiContent, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL: "data:" + b.MIMEType + ";base64," + b.Source.Base64,
				},
			})
		}
	}
	return om
}

// buildTools converts our tool definitions to OpenAI's format
func (p *Provider) buildTools(tools []llm.ToolDefinition) []openai.Tool {
	result := make([]openai.Tool, len(tools))
	for i, tool := range tools {
		result[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		}
	}
	return result
}

// GenerateStream implements StreamingProvider for token-level output.
func (p *Provider) GenerateStream(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolDefinition,
) (*llm.StreamResult, error) {
	messages := p.buildMessages(systemPrompt, history)

	var toolDefs []openai.Tool
	if len(tools) > 0 {
		toolDefs = p.buildTools(tools)
	}

	req := openai.ChatCompletionRequest{
		Model:    p.model,
		Messages: messages,
		Tools:    toolDefs,
		Stream:   true,
		StreamOptions: &openai.StreamOptions{
			IncludeUsage: true,
		},
	}
	// See the matching comment in generate(): reasoning-tier models fix
	// Temperature/TopP at 1 and require MaxCompletionTokens over MaxTokens.
	reasoning := isReasoningModel(p.model)
	if p.config.Temperature > 0 && (!reasoning || p.config.Temperature == 1) {
		req.Temperature = float32(p.config.Temperature)
	}
	setMaxTokens(&req, p.model, p.config.MaxTokens)
	applyReasoningEffort(&req, p.config.ReasoningEffort)
	if p.config.TopP > 0 && (!reasoning || p.config.TopP == 1) {
		req.TopP = float32(p.config.TopP)
	}

	stream, err := p.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		wrapped := wrapAPIError("openai stream error", err)
		if !omitRejectedParam(&req, wrapped) && !applyReasoningEffortFallback(&req, wrapped) {
			return nil, wrapped
		}
		// Retry exactly once with the rejected field omitted, or
		// reasoning_effort defaulted — see omitRejectedParam's doc for why
		// this is safer than guessing ahead of time which models are
		// reasoning-tier.
		stream, err = p.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			return nil, wrapAPIError("openai stream error", err)
		}
	}

	chunksCh := make(chan llm.StreamChunk, 64)
	finalCh := make(chan *llm.GenerateResult, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(chunksCh)
		defer close(finalCh)
		defer close(errCh)
		defer stream.Close()

		// All delta handling goes through the pluggable ResponseFormat adapter.
		// The adapter knows how to extract content/reasoning/tool-calls from
		// whatever shape this model emits (standard OpenAI, reasoning_content
		// field for Nemotron/DeepSeek, etc). See internal/llm/format/.
		state := format.NewState()
		// Peer-emit reasoning bytes extracted at stream time by ThinkTagInline
		// (inline <think>...</think> shape). The other shape (delta.reasoning_content
		// field) is handled below; both converge on StreamChunk.Reasoning so
		// downstream telemetry (EventReasoningChunk) sees one unified channel.
		state.OnReasoningDelta = func(reasoning string) {
			chunksCh <- llm.StreamChunk{Reasoning: reasoning}
		}
		var streamUsage *llm.TokenUsage

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				errCh <- fmt.Errorf("openai stream recv: %w", err)
				return
			}

			// Capture usage from final chunk (stream_options.include_usage).
			// Per the OpenAI streaming spec, that final chunk carries an
			// EMPTY Choices slice and ONLY Usage populated, so this must run
			// before the empty-choices continue below or it's never reached.
			if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
				streamUsage = &llm.TokenUsage{
					InputTokens:  resp.Usage.PromptTokens,
					OutputTokens: resp.Usage.CompletionTokens,
					TotalTokens:  resp.Usage.TotalTokens,
				}
			}

			if len(resp.Choices) == 0 {
				continue
			}
			rawDelta := convertOpenAIDelta(resp.Choices[0].Delta, resp.Choices[0].FinishReason)

			surface := p.formatAdapter.ApplyDelta(state, rawDelta)
			if surface != "" {
				chunksCh <- llm.StreamChunk{Text: surface}
			}

			// Surface reasoning_content as its own stream channel. The format
			// adapter consumes it silently into state.Reasoning (so it
			// doesn't pollute the answer surface); without this peer emit,
			// the entire reasoning phase of a reasoning model is invisible
			// to the agent loop, the tracer, the SSE hub, and every UI
			// consumer.
			if rawDelta.ReasoningContent != "" {
				chunksCh <- llm.StreamChunk{Reasoning: rawDelta.ReasoningContent}
			}
		}

		result := p.formatAdapter.Finalize(state)

		// Send done chunk
		chunksCh <- llm.StreamChunk{Done: true}

		finalCh <- &llm.GenerateResult{
			Response:        result.Content,
			ToolCalls:       result.ToolCalls,
			FinishReason:    result.FinishReason,
			TokenUsage:      streamUsage,
			ThinkingContent: result.Reasoning,
			FormatInfo:      p.buildFormatInfo(state),
			Salvaged:        result.Salvaged,
		}
	}()

	return &llm.StreamResult{
		Chunks: chunksCh,
		Final:  finalCh,
		Err:    errCh,
	}, nil
}

// parseResponse converts OpenAI's response to our format
func (p *Provider) parseResponse(resp *openai.ChatCompletionResponse) (*llm.GenerateResult, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response choices returned")
	}

	choice := resp.Choices[0]

	// Extract structured tool calls up front so the adapter can receive them
	// via RawMessage. (Quirk #3 migration: the old reasoning_content re-marshal
	// fallback is now handled by the ReasoningContentField adapter.)
	var toolCalls []llm.ToolCall
	if len(choice.Message.ToolCalls) > 0 {
		toolCalls = make([]llm.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("openai: parsing tool call arguments for %s: %w", tc.Function.Name, err)
			}
			toolCalls[i] = llm.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: llm.NormalizeArgKeys(args),
			}
		}
	}

	// Delegate content/reasoning extraction to the adapter. This replaces the
	// old ad-hoc reasoning_content JSON re-marshal fallback.
	state := format.NewState()
	p.formatAdapter.ApplyFull(state, format.RawMessage{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ToolCalls:        toolCalls,
		FinishReason:     string(choice.FinishReason),
	})
	adapted := p.formatAdapter.Finalize(state)

	result := &llm.GenerateResult{
		Response:        adapted.Content,
		ToolCalls:       adapted.ToolCalls,
		FinishReason:    adapted.FinishReason,
		ThinkingContent: adapted.Reasoning,
		FormatInfo:      p.buildFormatInfo(state),
		Salvaged:        adapted.Salvaged,
	}

	// Add token usage
	result.TokenUsage = &llm.TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}

	return result, nil
}

// buildFormatInfo produces the FormatInfo attached to every GenerateResult,
// so downstream consumers (telemetry, tracer, UI debugger) can see which
// response-format adapter actually handled the request. For Sniffing, the
// committed inner adapter is reported and the reason is upgraded to
// "sniff_commit" because the final decision was runtime, not registry.
// For composed wrapping adapters (ThinkTagInline{Inner: ReasoningContentField{}},
// etc.) the full chain is reported via format.ChainName so operators see the
// complete pipeline instead of just the outermost wrapper.
func (p *Provider) buildFormatInfo(state *format.FormatState) *llm.FormatInfo {
	adapterName := format.ChainName(p.formatAdapter)
	reason := p.formatResolveReason
	if sniff, ok := p.formatAdapter.(format.Sniffing); ok {
		committed := sniff.CommittedName(state)
		if committed != "" && committed != "sniffing_pending" {
			adapterName = committed
			reason = "sniff_commit"
		}
	}
	return &llm.FormatInfo{
		Adapter:  adapterName,
		Reason:   reason,
		Resolved: p.formatResolver,
	}
}

// convertOpenAIDelta converts a go-openai streaming delta into the generic
// format.RawDelta shape that adapters consume. Centralizes the library-specific
// field-name translation in one place.
func convertOpenAIDelta(d openai.ChatCompletionStreamChoiceDelta, finishReason openai.FinishReason) format.RawDelta {
	raw := format.RawDelta{
		Content:          d.Content,
		ReasoningContent: d.ReasoningContent,
		FinishReason:     string(finishReason),
	}
	if len(d.ToolCalls) > 0 {
		raw.ToolCalls = make([]format.RawDeltaToolCall, 0, len(d.ToolCalls))
		for _, tc := range d.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			raw.ToolCalls = append(raw.ToolCalls, format.RawDeltaToolCall{
				Index:     idx,
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	return raw
}
