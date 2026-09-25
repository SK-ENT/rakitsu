package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// helperProcessEnvVar, when set to "1" in the test binary's own
// environment, tells TestMain this invocation IS the mock MCP subprocess
// (re-exec'd via os.Args[0]) rather than the real test run — the standard
// Go pattern for testing subprocess behavior without external
// dependencies (see e.g. os/exec_test.go upstream).
const helperProcessEnvVar = "RAKITSU_MCP_TEST_HELPER_PROCESS"

func TestMain(m *testing.M) {
	if os.Getenv(helperProcessEnvVar) == "1" {
		runMockStdioServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runMockStdioServer is a minimal MCP stdio server: it answers initialize,
// tools/list (one tool, "greet"), and tools/call (always "Hello!"). It
// never calls testing.M.Run, so none of the normal go-test output reaches
// stdout to corrupt the JSON-RPC framing the real client is parsing.
func runMockStdioServer() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification (e.g. notifications/initialized) — no response
		}
		if req.Method == "trigger_oversized" {
			// A single line over the client's 1 MB scanner limit — not
			// valid JSON-RPC, doesn't need to be; the client's readLoop
			// should fail on line length before ever trying to parse it.
			fmt.Fprintln(os.Stdout, strings.Repeat("x", 2<<20))
			continue
		}
		if req.Method == "big_id_first" || req.Method == "big_id_last" {
			// A valid ~2 MiB response. Python's SDK writes "id" before
			// "result"; the TypeScript SDK writes it after.
			text := fmt.Sprintf(`{"content":[{"type":"text","text":%q}]}`, strings.Repeat("y", 2<<20))
			if req.Method == "big_id_first" {
				fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":%s}\n", *req.ID, text)
			} else {
				fmt.Fprintf(os.Stdout, "{\"result\":%s,\"jsonrpc\":\"2.0\",\"id\":%d}\n", text, *req.ID)
			}
			continue
		}

		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo":      map[string]interface{}{"name": "mock", "version": "0.0.1"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "greet", "description": "says hello", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}
		case "tools/call":
			result = map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": "Hello!"}},
				"isError": false,
			}
		default:
			result = map[string]interface{}{}
		}

		resultRaw, _ := json.Marshal(result)
		respRaw, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		fmt.Fprintln(os.Stdout, string(respRaw))
	}
}

// ─── TestRPCParsing ──────────────────────────────────────────────────────────

func TestRPCParsing_ToolsList(t *testing.T) {
	raw := json.RawMessage(`{
		"tools": [
			{"name": "read_file", "description": "Read a file", "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}}},
			{"name": "list_dir",  "description": "List a dir",  "inputSchema": {"type": "object"}}
		]
	}`)
	defs, err := parseToolsListResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 tools, got %d", len(defs))
	}
	if defs[0].Name != "read_file" {
		t.Errorf("want read_file, got %q", defs[0].Name)
	}
	if defs[1].Description != "List a dir" {
		t.Errorf("wrong description: %q", defs[1].Description)
	}
}

func TestRPCParsing_ToolCallResult(t *testing.T) {
	raw := json.RawMessage(`{"content": [{"type": "text", "text": "hello"}, {"type": "text", "text": "world"}], "isError": false}`)
	out, err := parseToolCallResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hello\nworld" {
		t.Errorf("want %q, got %q", "hello\nworld", out)
	}
}

func TestRPCParsing_ToolCallError(t *testing.T) {
	raw := json.RawMessage(`{"content": [{"type": "text", "text": "something broke"}], "isError": true}`)
	_, err := parseToolCallResult(raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ─── TestMCPTool_NameSanitize ────────────────────────────────────────────────

func TestMCPTool_NameSanitize(t *testing.T) {
	cases := []struct {
		server, tool, want string
	}{
		{"filesystem", "read_file", "filesystem_read_file"},
		{"my-server", "list-dir", "my_server_list_dir"},
		{"srv.v2", "tool.name", "srv_v2_tool_name"},
		{"brave", "brave_web_search", "brave_brave_web_search"},
	}
	for _, c := range cases {
		got := sanitize(c.server) + "_" + sanitize(c.tool)
		if got != c.want {
			t.Errorf("server=%q tool=%q: want %q, got %q", c.server, c.tool, c.want, got)
		}
	}
}

// ─── TestHTTPClient_MockServer ───────────────────────────────────────────────

func TestHTTPClient_MockServer(t *testing.T) {
	var callCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing Content-Type header")
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		callCount++

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session-123")
			json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
			})
		case "notifications/initialized":
			// some servers reply 204, some reply JSON notification ACK
			w.WriteHeader(http.StatusNoContent)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "test-session-123" {
				t.Error("expected Mcp-Session-Id header on tools/list")
			}
			json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"greet","description":"say hi","inputSchema":{}}]}`),
			})
		case "tools/call":
			json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"Hello!"}],"isError":false}`),
			})
		default:
			t.Errorf("unexpected method: %q", req.Method)
		}
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, 10, 0)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	ctx := context.Background()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	defs, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Errorf("unexpected tools: %v", defs)
	}

	result, err := client.CallTool(ctx, "greet", map[string]interface{}{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "Hello!" {
		t.Errorf("want %q, got %q", "Hello!", result)
	}
}

// ─── TestStdioClient_MockEcho ────────────────────────────────────────────────

// TestStdioClient_MockEcho re-execs the test binary itself as the MCP
// subprocess (see TestMain/runMockStdioServer) and exercises the full
// stdio transport path: process spawn, pipe framing, line scanning, and
// teardown — the path TestRPCParsing_* above deliberately doesn't cover.
func TestStdioClient_MockEcho(t *testing.T) {
	client, err := NewStdioClient(os.Args[0], nil, map[string]string{helperProcessEnvVar: "1"}, 5, 0)
	if err != nil {
		t.Fatalf("NewStdioClient: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	defs, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Fatalf("unexpected tools: %v", defs)
	}

	result, err := client.CallTool(ctx, "greet", map[string]interface{}{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "Hello!" {
		t.Errorf("want %q, got %q", "Hello!", result)
	}
}

// TestHTTPClient_SendsAcceptBothJSONAndEventStream is the regression test
// for the 406 bug: streamable-HTTP MCP servers (e.g. the real kg MCP
// server) reject a request that doesn't accept both application/json and
// text/event-stream, even though they may still answer with plain JSON.
func TestHTTPClient_SendsAcceptBothJSONAndEventStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			t.Errorf("Accept header = %q, want it to contain both application/json and text/event-stream", accept)
		}
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
		})
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, 10, 0)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestHTTPClient_SSEFramedResponse covers a real streamable-HTTP MCP
// server's actual behavior: once it sees Accept: text/event-stream, it
// replies with Content-Type: text/event-stream and an "event: message\n
// data: {...}\n\n" framed body instead of a plain JSON body. The client
// must unwrap that framing to reach the JSON-RPC payload.
func TestHTTPClient_SSEFramedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)    //nolint:errcheck
		payload, _ := json.Marshal(rpcResponse{ //nolint:errcheck
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, 10, 0)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestHTTPClient_SSEFramedResponse_LargeLine is the regression test for the
// scanner buffer-size finding: a "data:" line bigger than bufio.Scanner's
// default 64 KiB token limit (but within the intended 1 MiB response cap)
// must decode, not fail with "token too long".
func TestHTTPClient_SSEFramedResponse_LargeLine(t *testing.T) {
	// Padding pushes the single SSE line well past the 64 KiB default
	// scanner limit while staying under the 1 MiB response cap.
	padding := strings.Repeat("x", 100*1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)    //nolint:errcheck
		payload, _ := json.Marshal(rpcResponse{ //nolint:errcheck
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(fmt.Sprintf(`{"protocolVersion":"2024-11-05","padding":%q}`, padding)),
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, 10, 0)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestHTTPClient_ConcurrentSessionID exercises HTTPClient.sessionID from
// many goroutines at once — the shape NewMCPServer actually produces
// (multiple MCPTool instances sharing one HTTPClient), which rakitsu's own
// parallel ReAct tool calls can hit. Run with -race: this is the
// regression test for the fix, not just a functional check.
func TestHTTPClient_ConcurrentSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "concurrent-session")
		json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(`{"content":[],"isError":false}`),
		})
	}))
	defer srv.Close()

	client, err := NewHTTPClient(srv.URL, 10, 0)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.CallTool(context.Background(), "noop", nil); err != nil {
				t.Errorf("CallTool: %v", err)
			}
		}()
	}
	wg.Wait()
}
