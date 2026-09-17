package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	neutools "github.com/SK-ENT/rakitsu/internal/tools"
)

// ─── Stub tool ────────────────────────────────────────────────────────────────

type stubTool struct {
	name   string
	desc   string
	result string
	err    error
}

func (t *stubTool) GetName() string        { return t.name }
func (t *stubTool) GetDescription() string { return t.desc }
func (t *stubTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}},
	}
}
func (t *stubTool) Execute(_ context.Context, _ map[string]interface{}) (string, error) {
	return t.result, t.err
}

// ─── Spec-shaped test client ──────────────────────────────────────────────────
//
// Requests are built as raw JSON the way a real Streamable HTTP client sends
// them (string request IDs, Accept listing both content types, Mcp-Session-Id
// and MCP-Protocol-Version on every request after initialize) rather than via
// the server's own Go types, so the tests cannot inherit its assumptions.

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type mcpTestClient struct {
	t         *testing.T
	url       string
	sessionID string
	version   string
}

func newMCPTestClient(t *testing.T, stubs ...*stubTool) (*mcpTestClient, *MCPServer) {
	t.Helper()
	reg := neutools.NewToolRegistry()
	for _, s := range stubs {
		reg.RegisterTool(s)
	}
	mcp := NewMCPServer(reg, "test-version")
	srv := httptest.NewServer(mcp)
	t.Cleanup(srv.Close)
	return &mcpTestClient{t: t, url: srv.URL + "/mcp"}, mcp
}

// do sends one HTTP request. hdr overrides the defaults; an empty value
// deletes the header.
func (c *mcpTestClient) do(method, body string, hdr map[string]string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.url, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	if c.version != "" {
		req.Header.Set("MCP-Protocol-Version", c.version)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
	return resp
}

func (c *mcpTestClient) post(body string, hdr map[string]string) *http.Response {
	return c.do(http.MethodPost, body, hdr)
}

// readEnvelope drains the response and decodes it as a JSON-RPC envelope
// when the body is JSON (405s carry plain text). Returns the status, the
// envelope, and the raw body.
func readEnvelope(t *testing.T, resp *http.Response) (int, rpcEnvelope, []byte) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env rpcEnvelope
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode envelope %q: %v", raw, err)
		}
	}
	return resp.StatusCode, env, raw
}

// initialize runs the handshake for the given protocol version, records the
// session and negotiated version, and returns the initialize result.
func (c *mcpTestClient) initialize(version string) (map[string]interface{}, *http.Response) {
	c.t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":%q,"capabilities":{"roots":{}},"clientInfo":{"name":"spec-client","version":"1.0"}}}`, version)
	resp := c.post(body, nil)
	status, env, raw := readEnvelope(c.t, resp)
	if status != http.StatusOK {
		c.t.Fatalf("initialize: want 200, got %d: %s", status, raw)
	}
	if env.Error != nil {
		c.t.Fatalf("initialize: unexpected error %+v", env.Error)
	}
	c.sessionID = resp.Header.Get("Mcp-Session-Id")
	if c.sessionID == "" {
		c.t.Fatal("initialize: expected Mcp-Session-Id header")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		c.t.Fatalf("initialize result: %v", err)
	}
	c.version, _ = result["protocolVersion"].(string)
	n := c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	n.Body.Close()
	return result, resp
}

// ─── initialize / version negotiation ────────────────────────────────────────

func TestMCP_Initialize_EchoesSupportedVersion(t *testing.T) {
	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
		t.Run(v, func(t *testing.T) {
			c, _ := newMCPTestClient(t)
			result, _ := c.initialize(v)
			if result["protocolVersion"] != v {
				t.Errorf("want protocolVersion %q echoed, got %v", v, result["protocolVersion"])
			}
			si, _ := result["serverInfo"].(map[string]interface{})
			if si["name"] != "rakitsu" || si["version"] != "test-version" {
				t.Errorf("wrong serverInfo: %v", si)
			}
			caps, _ := result["capabilities"].(map[string]interface{})
			if _, ok := caps["tools"]; !ok {
				t.Errorf("capabilities missing tools: %v", caps)
			}
		})
	}
}

func TestMCP_Initialize_FallsBackToLatestForUnsupportedVersion(t *testing.T) {
	c, _ := newMCPTestClient(t)
	result, _ := c.initialize("1999-01-01")
	if result["protocolVersion"] != "2025-11-25" {
		t.Errorf("want latest supported version 2025-11-25, got %v", result["protocolVersion"])
	}
}

func TestMCP_Initialize_MissingParamsUsesLatest(t *testing.T) {
	c, _ := newMCPTestClient(t)
	resp := c.post(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("want 200 without error, got %d: %s", status, raw)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	json.Unmarshal(env.Result, &result) //nolint:errcheck
	if result.ProtocolVersion != "2025-11-25" {
		t.Errorf("want 2025-11-25, got %q", result.ProtocolVersion)
	}
}

// ─── request id typing ───────────────────────────────────────────────────────

func TestMCP_RequestID_StringEchoedVerbatim(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"req-6f1c-abc","method":"tools/list"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("want 200 without error, got %d: %s", status, raw)
	}
	if string(env.ID) != `"req-6f1c-abc"` {
		t.Errorf("want id echoed as JSON string, got %s", env.ID)
	}
}

func TestMCP_RequestID_NumberEchoedVerbatim(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":42,"method":"tools/list"}`, nil)
	_, env, _ := readEnvelope(t, resp)
	if string(env.ID) != `42` {
		t.Errorf("want id echoed as 42, got %s", env.ID)
	}
}

func TestMCP_RequestID_InvalidTypeRejected(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":{"bad":true},"method":"tools/list"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", status, raw)
	}
	if env.Error == nil || env.Error.Code != -32600 {
		t.Errorf("want -32600 invalid request, got %+v", env.Error)
	}
	if string(env.ID) != "null" {
		t.Errorf("want id null when the id is unusable, got %s", env.ID)
	}
}

// ─── notifications and client responses ──────────────────────────────────────

func TestMCP_Notification_Accepted202NoBody(t *testing.T) {
	for _, method := range []string{
		"notifications/initialized",
		"notifications/cancelled",
		"notifications/progress",
		"notifications/roots/list_changed",
	} {
		t.Run(method, func(t *testing.T) {
			c, _ := newMCPTestClient(t)
			c.initialize("2025-06-18")
			body := fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"requestId":"x","reason":"test"}}`, method)
			resp := c.post(body, nil)
			status, _, raw := readEnvelope(t, resp)
			if status != http.StatusAccepted {
				t.Errorf("want 202, got %d: %s", status, raw)
			}
			if len(raw) != 0 {
				t.Errorf("want empty body, got %s", raw)
			}
		})
	}
}

func TestMCP_ClientResponse_Accepted202(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"srv-1","result":{}}`, nil)
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusAccepted {
		t.Errorf("want 202 for a JSON-RPC response, got %d: %s", status, raw)
	}
}

// ─── framing errors ──────────────────────────────────────────────────────────

func TestMCP_ParseError_400WithNullID(t *testing.T) {
	c, _ := newMCPTestClient(t)
	resp := c.post(`{not json`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", status, raw)
	}
	if env.Error == nil || env.Error.Code != -32700 {
		t.Errorf("want -32700 parse error, got %+v", env.Error)
	}
	if string(env.ID) != "null" {
		t.Errorf("want id null, got %s", env.ID)
	}
}

func TestMCP_MissingMethod_400(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"no-method"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusBadRequest || env.Error == nil || env.Error.Code != -32600 {
		t.Errorf("want 400 with -32600, got %d: %s", status, raw)
	}
}

// ─── Origin validation ───────────────────────────────────────────────────────

func TestMCP_Origin_NonLocalForbidden(t *testing.T) {
	c, _ := newMCPTestClient(t)
	resp := c.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		map[string]string{"Origin": "http://evil.example"})
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusForbidden {
		t.Errorf("want 403 for non-local Origin, got %d: %s", status, raw)
	}
}

func TestMCP_Origin_LocalhostAllowed(t *testing.T) {
	c, _ := newMCPTestClient(t)
	resp := c.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		map[string]string{"Origin": "http://localhost:9100"})
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusOK {
		t.Errorf("want 200 for localhost Origin, got %d: %s", status, raw)
	}
}

// ─── session enforcement ─────────────────────────────────────────────────────

func TestMCP_Session_RequiredAfterInitialize(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"s1","method":"tools/list"}`, map[string]string{"Mcp-Session-Id": ""})
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusBadRequest {
		t.Errorf("want 400 without Mcp-Session-Id, got %d: %s", status, raw)
	}
	if env.Error == nil {
		t.Errorf("want a JSON-RPC error body, got %s", raw)
	}
}

func TestMCP_Session_UnknownIs404(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"s2","method":"tools/list"}`, map[string]string{"Mcp-Session-Id": "not-a-session"})
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusNotFound {
		t.Errorf("want 404 for unknown session, got %d: %s", status, raw)
	}
}

func TestMCP_Session_IsolatedPerInitialize(t *testing.T) {
	a, _ := newMCPTestClient(t)
	a.initialize("2025-06-18")
	b := &mcpTestClient{t: t, url: a.url}
	b.initialize("2025-06-18")
	if a.sessionID == b.sessionID {
		t.Fatal("two initializes must mint distinct sessions")
	}
	for _, c := range []*mcpTestClient{a, b} {
		resp := c.post(`{"jsonrpc":"2.0","id":"s3","method":"ping"}`, nil)
		status, _, raw := readEnvelope(t, resp)
		if status != http.StatusOK {
			t.Errorf("session %s: want 200, got %d: %s", c.sessionID, status, raw)
		}
	}
}

func TestMCP_Session_DeleteTerminates(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")

	del := c.do(http.MethodDelete, "", nil)
	status, _, raw := readEnvelope(t, del)
	if status != http.StatusOK {
		t.Fatalf("DELETE: want 200, got %d: %s", status, raw)
	}
	resp := c.post(`{"jsonrpc":"2.0","id":"s4","method":"tools/list"}`, nil)
	status, _, raw = readEnvelope(t, resp)
	if status != http.StatusNotFound {
		t.Errorf("after DELETE: want 404, got %d: %s", status, raw)
	}
}

func TestMCP_Session_IdleExpiryIs404(t *testing.T) {
	c, mcp := newMCPTestClient(t)
	c.initialize("2025-06-18")

	// Jump the server clock past the idle TTL: the session must be gone.
	mcp.now = func() time.Time { return time.Now().Add(mcpSessionIdleTTL + time.Minute) }
	resp := c.post(`{"jsonrpc":"2.0","id":"e1","method":"ping"}`, nil)
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusNotFound {
		t.Errorf("want 404 for an idle-expired session, got %d: %s", status, raw)
	}
}

func TestMCP_Session_ActivityRefreshesIdleClock(t *testing.T) {
	c, mcp := newMCPTestClient(t)
	c.initialize("2025-06-18")

	// The client is active half a TTL in. Just under a full TTL after that
	// ping (1.5 TTL after initialize) the session must still be alive,
	// because the idle clock restarted at the ping.
	half := mcpSessionIdleTTL / 2
	mcp.now = func() time.Time { return time.Now().Add(half) }
	readEnvelope(t, c.post(`{"jsonrpc":"2.0","id":"r1","method":"ping"}`, nil))

	mcp.now = func() time.Time { return time.Now().Add(half + mcpSessionIdleTTL - time.Minute) }
	resp := c.post(`{"jsonrpc":"2.0","id":"r2","method":"ping"}`, nil)
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusOK {
		t.Errorf("want 200 for a session refreshed by activity, got %d: %s", status, raw)
	}
}

func TestMCP_Session_DeleteWithoutHeaderIs400(t *testing.T) {
	c, _ := newMCPTestClient(t)
	del := c.do(http.MethodDelete, "", nil)
	status, _, raw := readEnvelope(t, del)
	if status != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", status, raw)
	}
}

func TestMCP_Session_DeleteUnknownIs404(t *testing.T) {
	c, _ := newMCPTestClient(t)
	del := c.do(http.MethodDelete, "", map[string]string{"Mcp-Session-Id": "ghost"})
	status, _, raw := readEnvelope(t, del)
	if status != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", status, raw)
	}
}

// ─── MCP-Protocol-Version header ─────────────────────────────────────────────

func TestMCP_ProtocolVersionHeader_UnsupportedIs400(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"v1","method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "2026-07-28"})
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusBadRequest {
		t.Errorf("want 400 for unsupported MCP-Protocol-Version, got %d: %s", status, raw)
	}
	// A dual-era client falls back to initialize only when the 400 body is
	// NOT one of the modern-era error codes (-32020..-32099).
	if env.Error != nil && env.Error.Code <= -32020 && env.Error.Code >= -32099 {
		t.Errorf("must not answer with a modern-era error code, got %d", env.Error.Code)
	}
}

func TestMCP_ProtocolVersionHeader_AbsentIsAccepted(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"v2","method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": ""})
	status, _, raw := readEnvelope(t, resp)
	if status != http.StatusOK {
		t.Errorf("want 200 when the header is absent, got %d: %s", status, raw)
	}
}

// ─── ping ────────────────────────────────────────────────────────────────────

func TestMCP_Ping_ReturnsEmptyResult(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"p1","method":"ping"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("want 200 without error, got %d: %s", status, raw)
	}
	if string(env.Result) != "{}" {
		t.Errorf("want result {}, got %s", env.Result)
	}
}

// ─── tools ───────────────────────────────────────────────────────────────────

func TestMCP_ToolsList(t *testing.T) {
	c, _ := newMCPTestClient(t,
		&stubTool{name: "greet", desc: "Say hello"},
		&stubTool{name: "farewell", desc: "Say goodbye"},
	)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"t1","method":"tools/list"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("want 200 without error, got %d: %s", status, raw)
	}
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	json.Unmarshal(env.Result, &result) //nolint:errcheck
	if len(result.Tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(result.Tools))
	}
	names := map[string]bool{}
	for _, te := range result.Tools {
		names[te.Name] = true
		if te.InputSchema == nil {
			t.Errorf("tool %q has nil inputSchema", te.Name)
		}
	}
	if !names["greet"] || !names["farewell"] {
		t.Errorf("missing expected tools in: %v", names)
	}
}

func TestMCP_ToolsCall(t *testing.T) {
	c, _ := newMCPTestClient(t, &stubTool{name: "echo", desc: "Echo input", result: "echoed!"})
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"t2","method":"tools/call","params":{"name":"echo","arguments":{"input":"hello"}}}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("want 200 without error, got %d: %s", status, raw)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(env.Result, &result) //nolint:errcheck
	if result.IsError {
		t.Error("isError should be false")
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != "echoed!" {
		t.Errorf("unexpected content: %+v", result.Content)
	}
}

func TestMCP_ToolsCall_UnknownToolIsToolError(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"t3","method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("tool-not-found must be a tool error, not a protocol error: %d %s", status, raw)
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	json.Unmarshal(env.Result, &result) //nolint:errcheck
	if !result.IsError {
		t.Error("expected isError:true for unknown tool")
	}
}

func TestMCP_ToolsCall_MissingNameIsInvalidParams(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"t4","method":"tools/call","params":{"arguments":{}}}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK || env.Error == nil || env.Error.Code != -32602 {
		t.Errorf("want 200 with -32602, got %d: %s", status, raw)
	}
}

// ─── method routing / transport ──────────────────────────────────────────────

func TestMCP_UnknownMethod_JSONRPCError(t *testing.T) {
	c, _ := newMCPTestClient(t)
	c.initialize("2025-06-18")
	resp := c.post(`{"jsonrpc":"2.0","id":"m1","method":"server/discover"}`, nil)
	status, env, raw := readEnvelope(t, resp)
	if status != http.StatusOK {
		t.Errorf("a well-formed request with an unknown method is a JSON-RPC error over 200, got %d: %s", status, raw)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Errorf("want -32601, got %+v", env.Error)
	}
	if string(env.ID) != `"m1"` {
		t.Errorf("want id echoed, got %s", env.ID)
	}
}

func TestMCP_GET_MethodNotAllowed(t *testing.T) {
	c, _ := newMCPTestClient(t)
	resp := c.do(http.MethodGet, "", nil)
	status, _, _ := readEnvelope(t, resp)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("want 405 (no server-initiated SSE stream offered), got %d", status)
	}
}
