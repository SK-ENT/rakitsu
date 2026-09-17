package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// ─── Protocol versions ────────────────────────────────────────────────────────

// mcpSupportedProtocolVersions lists the handshake-based protocol revisions
// this server speaks, newest first. Revision 2026-07-28 replaced the
// initialize handshake and Mcp-Session-Id with per-request metadata and a
// mandatory server/discover RPC; that is a separate feature. A client
// speaking it gets a plain 400 here (unsupported MCP-Protocol-Version, or
// missing session) and, per its own backward-compatibility rules, falls back
// to initialize.
var mcpSupportedProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func mcpLatestProtocolVersion() string { return mcpSupportedProtocolVersions[0] }

func mcpVersionSupported(v string) bool {
	for _, s := range mcpSupportedProtocolVersions {
		if s == v {
			return true
		}
	}
	return false
}

// ─── JSON-RPC types ───────────────────────────────────────────────────────────

// mcpMessage is any client→server JSON-RPC message: a request (id + method),
// a notification (method, no id), or a response (id + result|error). The id
// is kept raw because the spec allows both strings and numbers, and it must
// be echoed back byte-for-byte.
type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // nil encodes as null (JSON-RPC: unusable id → null)
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// validRequestID reports whether a present id is a JSON string or number,
// the only two shapes RequestId allows. The body already passed the JSON
// decoder, so the first byte is enough to classify it.
func validRequestID(id json.RawMessage) bool {
	b := bytes.TrimSpace(id)
	if len(b) == 0 {
		return false
	}
	return b[0] == '"' || b[0] == '-' || (b[0] >= '0' && b[0] <= '9')
}

// ─── MCPServer ────────────────────────────────────────────────────────────────

// mcpSessionIdleTTL is how long a session survives without any request.
// Clients that re-initialize on every call would otherwise grow the session
// table without bound; a client that comes back after this gets a 404 and,
// per spec, must re-initialize.
const mcpSessionIdleTTL = 24 * time.Hour

// MCPServer exposes a ToolRegistry as an MCP HTTP server (Streamable HTTP
// transport, JSON-RPC 2.0). External MCP clients — Claude Desktop, Cursor,
// Zed — POST to /mcp and can discover and invoke all registered tools.
type MCPServer struct {
	registry *tools.ToolRegistry
	version  string
	now      func() time.Time // clock, swappable in tests

	mu       sync.Mutex
	sessions map[string]time.Time // session id → last request time
}

// NewMCPServer creates an MCP server backed by the given registry.
// version is the rakitsu version string included in serverInfo.
func NewMCPServer(registry *tools.ToolRegistry, version string) *MCPServer {
	return &MCPServer{registry: registry, version: version, now: time.Now, sessions: map[string]time.Time{}}
}

// ServeHTTP handles all MCP JSON-RPC requests. Mount this on any path — the
// recommended convention is /mcp. It has no auth check of its own and
// executes arbitrary registered tools (tools/call) — whichever path this
// gets mounted on MUST be added to requiresAuth (auth.go) in the same
// change, before it's reachable.
func (s *MCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// DNS-rebinding guard: the spec requires Origin validation on every
	// connection, 403 when the header is present and not allowed. Non-browser
	// clients send no Origin and pass.
	if !isAllowedOrigin(r.Header.Get("Origin")) {
		writeMCPHTTPError(w, http.StatusForbidden, -32000, "forbidden: origin not allowed")
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodDelete:
		s.serveDelete(w, r)
	default:
		// No server-initiated SSE stream is offered, so GET is 405 per spec.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *MCPServer) servePost(w http.ResponseWriter, r *http.Request) {
	// Absent header is fine (pre-2025-06-18 clients, and the session already
	// pins the negotiated version); a present-but-unsupported one is a 400.
	if v := r.Header.Get("Mcp-Protocol-Version"); v != "" && !mcpVersionSupported(v) {
		writeMCPHTTPError(w, http.StatusBadRequest, -32000, "unsupported MCP-Protocol-Version: "+v)
		return
	}

	var msg mcpMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&msg); err != nil {
		writeMCPHTTPError(w, http.StatusBadRequest, -32700, "parse error: "+err.Error())
		return
	}
	isRequest := len(msg.ID) > 0
	if isRequest && !validRequestID(msg.ID) {
		writeMCPHTTPError(w, http.StatusBadRequest, -32600, "invalid request: id must be a string or number")
		return
	}
	if msg.Method == "" {
		// A JSON-RPC response. This server never sends requests, so there is
		// nothing to correlate it with: accept and drop.
		if isRequest && (len(msg.Result) > 0 || len(msg.Error) > 0) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeMCPHTTPError(w, http.StatusBadRequest, -32600, "invalid request: method is required")
		return
	}

	if msg.Method == "initialize" {
		s.handleInitialize(w, msg)
		return
	}

	// Everything after initialize belongs to a session.
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		writeMCPHTTPError(w, http.StatusBadRequest, -32000, "Mcp-Session-Id header is required")
		return
	}
	if !s.touchSession(sid) {
		writeMCPHTTPError(w, http.StatusNotFound, -32001, "session not found")
		return
	}
	if !isRequest {
		// Notification: accepted, no body. Nothing here needs a reaction —
		// requests are served synchronously so there is nothing to cancel,
		// and this server consumes no client state such as roots.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeMCPJSON(w, http.StatusOK, s.dispatch(r.Context(), msg))
}

// serveDelete terminates a session (spec: clients SHOULD DELETE when done).
func (s *MCPServer) serveDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		writeMCPHTTPError(w, http.StatusBadRequest, -32000, "Mcp-Session-Id header is required")
		return
	}
	s.mu.Lock()
	_, ok := s.sessions[sid]
	delete(s.sessions, sid)
	s.mu.Unlock()
	if !ok {
		writeMCPHTTPError(w, http.StatusNotFound, -32001, "session not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// touchSession reports whether sid is a live session and, if so, restarts its
// idle clock. An idle-expired session is dropped and reported as unknown.
func (s *MCPServer) touchSession(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.sessions[sid]
	if !ok {
		return false
	}
	now := s.now()
	if now.Sub(last) > mcpSessionIdleTTL {
		delete(s.sessions, sid)
		return false
	}
	s.sessions[sid] = now
	return true
}

// newSession mints a session id, sweeping idle-expired sessions on the way
// so the table stays bounded by the number of clients active within a TTL.
func (s *MCPServer) newSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, last := range s.sessions {
		if now.Sub(last) > mcpSessionIdleTTL {
			delete(s.sessions, id)
		}
	}
	sid := uuid.New().String()
	s.sessions[sid] = now
	return sid
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *MCPServer) handleInitialize(w http.ResponseWriter, msg mcpMessage) {
	if len(msg.ID) == 0 {
		writeMCPHTTPError(w, http.StatusBadRequest, -32600, "invalid request: initialize must be a request")
		return
	}
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			writeMCPJSON(w, http.StatusOK, mcpErrorResponse(msg.ID, -32602, "invalid params: "+err.Error()))
			return
		}
	}
	// Version negotiation: echo the requested version when supported,
	// otherwise offer the newest this server speaks and let the client decide.
	negotiated := mcpLatestProtocolVersion()
	if mcpVersionSupported(params.ProtocolVersion) {
		negotiated = params.ProtocolVersion
	}

	w.Header().Set("Mcp-Session-Id", s.newSession())
	writeMCPJSON(w, http.StatusOK, mcpResultResponse(msg.ID, map[string]interface{}{
		"protocolVersion": negotiated,
		"serverInfo": map[string]interface{}{
			"name":    "rakitsu",
			"version": s.version,
		},
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
	}))
}

// dispatch routes a request inside an established session.
func (s *MCPServer) dispatch(ctx context.Context, msg mcpMessage) mcpResponse {
	switch msg.Method {
	case "ping":
		return mcpResultResponse(msg.ID, map[string]interface{}{})
	case "tools/list":
		return s.handleToolsList(msg)
	case "tools/call":
		return s.handleToolsCall(ctx, msg)
	default:
		return mcpErrorResponse(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

type mcpToolEntry struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func (s *MCPServer) handleToolsList(msg mcpMessage) mcpResponse {
	allTools := s.registry.GetAllTools()
	entries := make([]mcpToolEntry, 0, len(allTools))
	for _, t := range allTools {
		schema := t.GetParametersSchema()
		if schema == nil {
			schema = map[string]interface{}{"type": "object"}
		}
		entries = append(entries, mcpToolEntry{
			Name:        t.GetName(),
			Description: t.GetDescription(),
			InputSchema: schema,
		})
	}
	return mcpResultResponse(msg.ID, map[string]interface{}{"tools": entries})
}

type mcpToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type mcpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *MCPServer) handleToolsCall(ctx context.Context, msg mcpMessage) mcpResponse {
	var params mcpToolCallParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return mcpErrorResponse(msg.ID, -32602, "invalid params: "+err.Error())
	}
	if params.Name == "" {
		return mcpErrorResponse(msg.ID, -32602, "name is required")
	}

	result, err := s.registry.ExecuteToolCall(ctx, tools.ToolCall{
		Name:      params.Name,
		Arguments: params.Arguments,
	})
	if err != nil {
		// Tool failures (including unknown tool) are tool errors the model can
		// react to, not protocol errors.
		return mcpResultResponse(msg.ID, map[string]interface{}{
			"content": []mcpContentBlock{{Type: "text", Text: err.Error()}},
			"isError": true,
		})
	}
	return mcpResultResponse(msg.ID, map[string]interface{}{
		"content": []mcpContentBlock{{Type: "text", Text: result}},
		"isError": false,
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func mcpResultResponse(id json.RawMessage, result interface{}) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func mcpErrorResponse(id json.RawMessage, code int, msg string) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpRPCError{Code: code, Message: msg}}
}

func writeMCPJSON(w http.ResponseWriter, status int, resp mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// writeMCPHTTPError answers a transport-level rejection: an HTTP error status
// with a JSON-RPC error body carrying a null id, since the request either
// could not be read or is not being processed.
func writeMCPHTTPError(w http.ResponseWriter, status, code int, msg string) {
	writeMCPJSON(w, status, mcpErrorResponse(nil, code, msg))
}
