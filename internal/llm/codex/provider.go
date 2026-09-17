// Package codex implements llm.LLMProvider on top of the ChatGPT/Codex
// subscription backend, reusing the login Codex CLI stored in
// ~/.codex/auth.json. It speaks the Responses API (streaming only).
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// DefaultBaseURL is the subscription-backed Responses endpoint root.
const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

// Provider talks to the Codex backend with the user's ChatGPT login.
type Provider struct {
	model   string
	effort  string
	baseURL string
	tokens  *tokenSource
	client  *http.Client
}

// NewProvider builds a Provider. config.CredentialsFile overrides the
// auth.json path; config.BaseURL overrides the endpoint (tests). When
// config.Model is empty the `model` from the sibling config.toml is used.
func NewProvider(config *llm.ProviderConfig) (*Provider, error) {
	authPath := config.CredentialsFile
	if authPath == "" {
		authPath = DefaultAuthFile
	}
	authPath = expandHome(authPath)

	timeout := 10 * time.Minute
	if config.TimeoutSec > 0 {
		timeout = time.Duration(config.TimeoutSec) * time.Second
	}
	client := &http.Client{Timeout: timeout}

	model, effort := readCodexConfig(filepath.Join(filepath.Dir(authPath), "config.toml"))
	if config.Model != "" {
		model = config.Model
	}
	if model == "" {
		return nil, errors.New("codex: no model set — add default_model to the codex provider or `model` to ~/.codex/config.toml")
	}
	baseURL := DefaultBaseURL
	if config.BaseURL != "" {
		baseURL = config.BaseURL
	}
	return &Provider{
		model:   model,
		effort:  effort,
		baseURL: strings.TrimRight(baseURL, "/"),
		tokens:  newTokenSource(authPath),
		client:  client,
	}, nil
}

// ConfigTomlModel returns the `model` Codex CLI's config.toml (next to the
// given auth.json path) names, or "" when unset.
func ConfigTomlModel(authPath string) string {
	m, _ := readCodexConfig(filepath.Join(filepath.Dir(expandHome(authPath)), "config.toml"))
	return m
}

var tomlLine = regexp.MustCompile(`^\s*(model|model_reasoning_effort)\s*=\s*"([^"]*)"`)

// readCodexConfig pulls the two top-level keys we care about from Codex
// CLI's config.toml. A full TOML parser is overkill for two strings.
func readCodexConfig(path string) (model, effort string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break // top-level keys only
		}
		if m := tomlLine.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "model":
				model = m[2]
			case "model_reasoning_effort":
				effort = m[2]
			}
		}
	}
	return model, effort
}

func (p *Provider) GetName() string  { return "codex" }
func (p *Provider) GetModel() string { return p.model }

// Generate drains GenerateStream into a single result.
func (p *Provider) Generate(ctx context.Context, systemPrompt string, history []llm.Message, tools []llm.ToolDefinition) (*llm.GenerateResult, error) {
	sr, err := p.GenerateStream(ctx, systemPrompt, history, tools)
	if err != nil {
		return nil, err
	}
	for range sr.Chunks {
	}
	// The stream goroutine closes Chunks last, so Final and Err are settled here.
	res := <-sr.Final
	if res == nil {
		if err := <-sr.Err; err != nil {
			return nil, err
		}
		return nil, errors.New("codex: stream ended without a result")
	}
	return res, nil
}

// GenerateStream implements llm.StreamingProvider.
func (p *Provider) GenerateStream(ctx context.Context, systemPrompt string, history []llm.Message, tools []llm.ToolDefinition) (*llm.StreamResult, error) {
	req := responsesRequest{
		Model:             p.model,
		Instructions:      systemPrompt,
		Input:             buildInput(history),
		Tools:             buildTools(tools),
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning:         &reasoningParam{Effort: p.effort, Summary: "auto"},
		Store:             false,
		Stream:            true,
		Include:           []string{"reasoning.encrypted_content"},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("codex: encode request: %w", err)
	}

	resp, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}

	chunksCh := make(chan llm.StreamChunk, 64)
	finalCh := make(chan *llm.GenerateResult, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(chunksCh)
		defer close(finalCh)
		defer close(errCh)
		defer resp.Body.Close()
		res, err := readStream(resp.Body, chunksCh)
		if err != nil {
			errCh <- err
			return
		}
		finalCh <- res
	}()
	return &llm.StreamResult{Chunks: chunksCh, Final: finalCh, Err: errCh}, nil
}

// post sends the request, refreshing the login once on a 401.
func (p *Provider) post(ctx context.Context, body []byte) (*http.Response, error) {
	access, account, err := p.tokens.token(ctx, p.client)
	if err != nil {
		return nil, err
	}
	resp, err := p.doPost(ctx, body, access, account)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := p.tokens.forceRefresh(ctx, p.client, access); err != nil {
			return nil, err
		}
		access, account, err = p.tokens.token(ctx, p.client)
		if err != nil {
			return nil, err
		}
		if resp, err = p.doPost(ctx, body, access, account); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, newStatusError(resp.StatusCode, "codex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (p *Provider) doPost(ctx context.Context, body []byte, access, account string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codex: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if account != "" {
		req.Header.Set("chatgpt-account-id", account)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "rakitsu")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: request: %w", err)
	}
	return resp, nil
}

// sseEvent is the envelope every Responses stream event shares.
type sseEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Response *struct {
		Status string `json:"status"`
		Usage  *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response,omitempty"`
}

// readStream consumes the SSE body, emitting chunks as they arrive and
// returning the assembled result on response.completed.
func readStream(r io.Reader, chunks chan<- llm.StreamChunk) (*llm.GenerateResult, error) {
	var (
		text      strings.Builder
		reasoning strings.Builder
		toolCalls []llm.ToolCall
		items     []json.RawMessage
		usage     *llm.TokenUsage
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	flush := func() (done bool, err error) {
		if data.Len() == 0 {
			return false, nil
		}
		payload := data.String()
		data.Reset()
		var ev sseEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return false, fmt.Errorf("codex: bad stream event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
			chunks <- llm.StreamChunk{Text: ev.Delta}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(ev.Delta)
			chunks <- llm.StreamChunk{Reasoning: ev.Delta}
		case "response.reasoning_summary_text.done":
			reasoning.WriteString("\n")
		case "response.output_item.done":
			var it outputItem
			if err := json.Unmarshal(ev.Item, &it); err != nil {
				return false, fmt.Errorf("codex: bad output item: %w", err)
			}
			switch it.Type {
			case "function_call":
				tc, err := parseToolCall(it)
				if err != nil {
					return false, err
				}
				toolCalls = append(toolCalls, tc)
			case "reasoning":
				items = append(items, ev.Item)
			}
		case "response.completed":
			if ev.Response != nil && ev.Response.Usage != nil {
				u := ev.Response.Usage
				usage = &llm.TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
			}
			return true, nil
		case "response.failed":
			msg := "unknown error"
			if ev.Response != nil && ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			return false, fmt.Errorf("codex: response failed: %s", msg)
		case "response.incomplete":
			reason := "unknown"
			if ev.Response != nil && ev.Response.IncompleteDetails != nil {
				reason = ev.Response.IncompleteDetails.Reason
			}
			return false, fmt.Errorf("codex: response incomplete: %s", reason)
		}
		return false, nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			done, err := flush()
			if err != nil {
				return nil, err
			}
			if done {
				return assemble(text.String(), reasoning.String(), toolCalls, items, usage), nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("codex: stream read: %w", err)
	}
	if done, err := flush(); err != nil {
		return nil, err
	} else if done {
		return assemble(text.String(), reasoning.String(), toolCalls, items, usage), nil
	}
	return nil, errors.New("codex: stream ended before response.completed")
}

func assemble(text, reasoning string, toolCalls []llm.ToolCall, items []json.RawMessage, usage *llm.TokenUsage) *llm.GenerateResult {
	res := &llm.GenerateResult{
		Response:        text,
		ToolCalls:       toolCalls,
		TokenUsage:      usage,
		FinishReason:    "stop",
		ThinkingContent: strings.TrimSpace(reasoning),
	}
	if len(toolCalls) > 0 {
		res.FinishReason = "tool_calls"
	}
	if len(items) > 0 {
		res.ProviderMetadata = map[string]interface{}{reasoningItemsKey: items}
	}
	return res
}
