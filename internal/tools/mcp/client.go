// Package mcp provides an MCP (Model Context Protocol) client that connects
// to external MCP servers over stdio or HTTP and exposes their tools as
// rakitsu tools.Tool implementations.
//
// Protocol: JSON-RPC 2.0, MCP spec 2024-11-05.
// No external dependencies — uses only stdlib.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// ToolDef describes a single tool exposed by an MCP server.
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

// MCPClient abstracts the transport to an MCP server.
type MCPClient interface {
	// Initialize performs the MCP handshake and must be called before any other method.
	Initialize(ctx context.Context) error
	// ListTools returns all tools advertised by the server.
	ListTools(ctx context.Context) ([]ToolDef, error)
	// CallTool invokes a named tool and returns its text output.
	CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error)
	// CallToolContent is CallTool plus the result's image content.
	CallToolContent(ctx context.Context, name string, args map[string]interface{}) (string, []llm.ContentBlock, error)
	// Close shuts down the connection / subprocess.
	Close() error
}

// ─── JSON-RPC types ──────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int        `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// ─── StdioClient ─────────────────────────────────────────────────────────────

// StdioClient connects to an MCP server that communicates over stdin/stdout
// (newline-delimited JSON-RPC 2.0).
type StdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	nextID atomic.Int64

	mu      sync.Mutex // guards pending only
	pending map[int]chan rpcResponse

	// writeSem serializes writes to stdin (required — concurrent writers
	// would interleave and corrupt the newline-delimited JSON framing)
	// without holding mu across the blocking Write call. Acquired via
	// select against ctx.Done() so a slow write on one call can't make a
	// concurrent caller's own context deadline unresponsive.
	writeSem chan struct{}

	done chan struct{}
}

// NewStdioClient spawns the MCP server subprocess and returns a client ready
// for Initialize to be called.
func NewStdioClient(command string, args []string, env map[string]string, timeoutSec int) (*StdioClient, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	cmd := exec.Command(command, args...)

	// Merge caller env into process env
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}
	// Discard stderr to avoid blocking
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %q: %w", command, err)
	}

	c := &StdioClient{
		cmd:      cmd,
		stdin:    stdin,
		pending:  make(map[int]chan rpcResponse),
		writeSem: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}

	go c.readLoop(stdout)
	return c, nil
}

// readLoop reads newline-delimited JSON from stdout and dispatches responses
// to their waiting channels. On any exit — clean EOF, a scan error such as
// a too-long line, or the subprocess dying — it kills the subprocess so a
// broken transport doesn't leak an unmanaged, still-running process.
func (c *StdioClient) readLoop(r io.Reader) {
	defer close(c.done)
	defer func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	}()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB max line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == nil {
			continue // notification — ignore
		}
		c.mu.Lock()
		ch, ok := c.pending[*resp.ID]
		if ok {
			delete(c.pending, *resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// send writes a JSON-RPC request and waits for the matching response.
func (c *StdioClient) send(ctx context.Context, method string, params interface{}) (rpcResponse, error) {
	id := int(c.nextID.Add(1))
	idCopy := id
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      &idCopy,
		Method:  method,
		Params:  params,
	}

	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	b, err := json.Marshal(req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcResponse{}, err
	}
	b = append(b, '\n')

	select {
	case c.writeSem <- struct{}{}:
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcResponse{}, ctx.Err()
	}
	_, writeErr := c.stdin.Write(b)
	<-c.writeSem
	if writeErr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcResponse{}, fmt.Errorf("mcp stdio: write: %w", writeErr)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return rpcResponse{}, ctx.Err()
	case <-c.done:
		return rpcResponse{}, fmt.Errorf("mcp stdio: server exited")
	}
}

// notify sends a JSON-RPC notification (no ID, no response expected).
func (c *StdioClient) notify(method string) error {
	req := rpcRequest{JSONRPC: "2.0", Method: method}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	c.writeSem <- struct{}{}
	defer func() { <-c.writeSem }()
	_, err := c.stdin.Write(b)
	return err
}

// Initialize sends `initialize` + `notifications/initialized`.
func (c *StdioClient) Initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"clientInfo":      map[string]interface{}{"name": "rakitsu", "version": "0.1.0"},
	}
	resp, err := c.send(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp stdio initialize: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	return c.notify("notifications/initialized")
}

// ListTools calls tools/list and returns all discovered tools.
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	resp, err := c.send(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return parseToolsListResult(resp.Result)
}

// CallTool calls tools/call and concatenates text content from the result.
func (c *StdioClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	text, _, err := c.CallToolContent(ctx, name, args)
	return text, err
}

// CallToolContent calls tools/call and returns its text and image content.
func (c *StdioClient) CallToolContent(ctx context.Context, name string, args map[string]interface{}) (string, []llm.ContentBlock, error) {
	params := map[string]interface{}{"name": name, "arguments": args}
	resp, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return "", nil, fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	if resp.Error != nil {
		return "", nil, resp.Error
	}
	return parseToolCallContent(resp.Result)
}

// Close kills the subprocess.
func (c *StdioClient) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

// ─── HTTPClient ───────────────────────────────────────────────────────────────

// HTTPClient connects to an MCP server over HTTP (JSON-RPC POST).
// Compatible with servers implementing the Streamable HTTP transport.
// NewMCPServer wraps one HTTPClient into multiple MCPTool instances (one
// per discovered tool), and rakitsu's ReAct loop calls tools from parallel
// goroutines — so sessionID needs its own lock, unlike the rest of this
// client's fields, which are set once at construction and never mutated.
type HTTPClient struct {
	url    string
	http   *http.Client
	nextID atomic.Int64

	mu        sync.Mutex
	sessionID string // Mcp-Session-Id, set after initialize
}

// NewHTTPClient returns a client that will POST JSON-RPC to the given URL.
func NewHTTPClient(url string, timeoutSec int) (*HTTPClient, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	return &HTTPClient{
		url:  url,
		http: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}, nil
}

// send POSTs a JSON-RPC request and decodes the response.
func (c *HTTPClient) send(ctx context.Context, method string, params interface{}) (rpcResponse, error) {
	id := int(c.nextID.Add(1))
	idCopy := id
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      &idCopy,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return rpcResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return rpcResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Streamable-HTTP MCP servers require the client to accept both plain
	// JSON and SSE responses (MCP spec 2024-11-05); some reject the request
	// with 406 if text/event-stream is missing, even when they end up
	// responding with plain JSON.
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", sessionID)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return rpcResponse{}, fmt.Errorf("mcp http %s: %w", method, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return rpcResponse{}, fmt.Errorf("mcp http %s: unexpected status %d", method, httpResp.StatusCode)
	}

	// Store session ID from initialize response
	if sid := httpResp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	respBody, err := decodeMCPBody(httpResp)
	if err != nil {
		return rpcResponse{}, fmt.Errorf("mcp http decode: %w", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return rpcResponse{}, fmt.Errorf("mcp http decode: %w", err)
	}
	return resp, nil
}

// decodeMCPBody reads an MCP HTTP response body, transparently unwrapping
// an SSE-framed ("text/event-stream") response down to the JSON payload of
// its first "data:" event. A plain "application/json" response is returned
// as-is.
func decodeMCPBody(httpResp *http.Response) ([]byte, error) {
	limited := io.LimitReader(httpResp.Body, 1<<20)
	if !strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream") {
		return io.ReadAll(limited)
	}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20) // allow a "data:" line up to the same 1 MiB response cap
	for scanner.Scan() {
		line := scanner.Text()
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			return []byte(strings.TrimSpace(data)), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no data event in SSE response")
}

// Initialize sends the MCP initialize handshake.
func (c *HTTPClient) Initialize(ctx context.Context) error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"clientInfo":      map[string]interface{}{"name": "rakitsu", "version": "0.1.0"},
	}
	resp, err := c.send(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp http initialize: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	// Send initialized notification (best-effort, some servers don't require it over HTTP)
	notif := rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}
	b, _ := json.Marshal(notif)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp2, err2 := c.http.Do(httpReq)
	if err2 == nil {
		resp2.Body.Close()
	}
	return nil
}

// ListTools calls tools/list.
func (c *HTTPClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	resp, err := c.send(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp http tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return parseToolsListResult(resp.Result)
}

// CallTool calls tools/call.
func (c *HTTPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	text, _, err := c.CallToolContent(ctx, name, args)
	return text, err
}

// CallToolContent calls tools/call and returns its text and image content.
func (c *HTTPClient) CallToolContent(ctx context.Context, name string, args map[string]interface{}) (string, []llm.ContentBlock, error) {
	params := map[string]interface{}{"name": name, "arguments": args}
	resp, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return "", nil, fmt.Errorf("mcp http tools/call %q: %w", name, err)
	}
	if resp.Error != nil {
		return "", nil, resp.Error
	}
	return parseToolCallContent(resp.Result)
}

// Close is a no-op for HTTP (stateless).
func (c *HTTPClient) Close() error { return nil }

// ─── Shared result parsers ────────────────────────────────────────────────────

func parseToolsListResult(raw json.RawMessage) ([]ToolDef, error) {
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list parse: %w", err)
	}
	defs := make([]ToolDef, len(result.Tools))
	for i, t := range result.Tools {
		defs[i] = ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return defs, nil
}

func parseToolCallResult(raw json.RawMessage) (string, error) {
	text, _, err := parseToolCallContent(raw)
	return text, err
}

// parseToolCallContent returns a tools/call result's text (items joined by
// newlines) and its image content: "image" items and "resource" items whose
// mimeType is an image. An image type no provider accepts becomes an
// "[image omitted: ...]" note in the text. Size needs no check here: both
// transports cap a whole response at 1 MiB before it is parsed (readLoop,
// decodeMCPBody), which bounds every image and their total.
func parseToolCallContent(raw json.RawMessage) (string, []llm.ContentBlock, error) {
	type resource struct {
		MIMEType string `json:"mimeType"`
		Blob     string `json:"blob"`
	}
	var result struct {
		Content []struct {
			Type     string    `json:"type"`
			Text     string    `json:"text"`
			Data     string    `json:"data"`
			MIMEType string    `json:"mimeType"`
			Resource *resource `json:"resource"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", nil, fmt.Errorf("mcp tools/call parse: %w", err)
	}
	if result.IsError {
		// Collect error text from content
		var sb []byte
		for _, c := range result.Content {
			if c.Type == "text" {
				sb = append(sb, c.Text...)
			}
		}
		return "", nil, fmt.Errorf("mcp tool error: %s", string(sb))
	}
	var lines []string
	var blocks []llm.ContentBlock
	for _, c := range result.Content {
		data, mimeType := c.Data, c.MIMEType
		switch {
		case c.Type == "text":
			lines = append(lines, c.Text)
			continue
		case c.Type == "resource" && c.Resource != nil && strings.HasPrefix(c.Resource.MIMEType, "image/"):
			data, mimeType = c.Resource.Blob, c.Resource.MIMEType
		case c.Type != "image":
			continue
		}
		size := base64.StdEncoding.DecodedLen(len(data))
		if !llm.IsSupportedImageMIME(mimeType) {
			lines = append(lines, fmt.Sprintf("[image omitted: %s, %d bytes — unsupported image type]", mimeType, size))
			continue
		}
		blocks = append(blocks, llm.ContentBlock{
			Type:     llm.ContentTypeImage,
			MIMEType: mimeType,
			Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: data},
			Metadata: map[string]any{"size_bytes": int64(size)},
		})
	}
	return strings.Join(lines, "\n"), blocks, nil
}
