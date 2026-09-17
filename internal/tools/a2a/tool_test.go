package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// ─── Mock server helper ───────────────────────────────────────────────────────

// mockA2AServer creates an httptest.Server speaking the real A2A v1.0.1
// JSON-RPC binding: PascalCase methods, tenant-based agent routing.
// sendMessage is called for every SendMessage request; getTask (optional)
// answers GetTask polls for tasks the handler doesn't complete inline.
type mockHandlers struct {
	sendMessage func(tenant string, msg a2aMessage) a2aTask
	getTask     func(id string) (a2aTask, bool)
}

func newMockA2AServer(t *testing.T, h mockHandlers) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req a2aRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		paramsJSON, _ := json.Marshal(req.Params) //nolint:errcheck
		respID, _ := json.Marshal(req.ID)         //nolint:errcheck // a2aResponse.ID is json.RawMessage

		switch req.Method {
		case "SendMessage":
			var params a2aSendMessageParams
			json.Unmarshal(paramsJSON, &params) //nolint:errcheck
			task := h.sendMessage(params.Tenant, params.Message)
			result, _ := json.Marshal(a2aSendMessageResult{Task: &task})                       //nolint:errcheck
			json.NewEncoder(w).Encode(a2aResponse{JSONRPC: "2.0", ID: respID, Result: result}) //nolint:errcheck
		case "GetTask":
			var params a2aGetTaskParams
			json.Unmarshal(paramsJSON, &params) //nolint:errcheck
			if h.getTask == nil {
				json.NewEncoder(w).Encode(a2aResponse{ //nolint:errcheck
					JSONRPC: "2.0", ID: respID,
					Error: &a2aRPCError{Code: -32601, Message: "method not found"},
				})
				return
			}
			task, ok := h.getTask(params.ID)
			if !ok {
				json.NewEncoder(w).Encode(a2aResponse{ //nolint:errcheck
					JSONRPC: "2.0", ID: respID,
					Error: &a2aRPCError{Code: -32001, Message: "Task not found"},
				})
				return
			}
			result, _ := json.Marshal(task)                                                    //nolint:errcheck
			json.NewEncoder(w).Encode(a2aResponse{JSONRPC: "2.0", ID: respID, Result: result}) //nolint:errcheck
		default:
			json.NewEncoder(w).Encode(a2aResponse{ //nolint:errcheck
				JSONRPC: "2.0", ID: respID,
				Error: &a2aRPCError{Code: -32601, Message: "method not found"},
			})
		}
	}))
}

func newTool(t *testing.T, srv *httptest.Server, agentName, desc string) *A2ATool {
	t.Helper()
	tool, err := NewA2ATool(&config.ToolDefinition{
		Name:        "test_tool",
		Description: desc,
		URL:         srv.URL,
		AgentName:   agentName,
	})
	if err != nil {
		t.Fatalf("NewA2ATool: %v", err)
	}
	return tool
}

func completedTask(id, text string) a2aTask {
	return a2aTask{
		ID:        id,
		Status:    a2aStatus{State: taskStateCompleted},
		Artifacts: []a2aArtifact{{Parts: []a2aPart{{Text: text}}}},
	}
}

// ─── TestA2ATool_Execute ──────────────────────────────────────────────────────

func TestA2ATool_Execute(t *testing.T) {
	var gotTenant string
	var gotMsg a2aMessage
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			gotTenant = tenant
			gotMsg = msg
			return completedTask("t1", "research result: "+msg.Parts[0].Text)
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "AI trends"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "research result: AI trends" {
		t.Errorf("result = %q, want %q", result, "research result: AI trends")
	}
	if gotTenant != "Researcher" {
		t.Errorf("tenant sent to server = %q, want %q", gotTenant, "Researcher")
	}
	if gotMsg.MessageID == "" {
		t.Error("expected a non-empty messageId on the outgoing message")
	}
	if gotMsg.Role != roleUser {
		t.Errorf("role = %q, want %q", gotMsg.Role, roleUser)
	}
}

// ─── TestA2ATool_AgentFailed ──────────────────────────────────────────────────

func TestA2ATool_AgentFailed(t *testing.T) {
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			return a2aTask{
				ID:     "t1",
				Status: a2aStatus{State: taskStateFailed, Message: &a2aMessage{Parts: []a2aPart{{Text: "agent exploded"}}}},
			}
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agent exploded") {
		t.Errorf("error = %v, want it to contain %q", err, "agent exploded")
	}
}

func TestA2ATool_AgentRejected(t *testing.T) {
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			return a2aTask{ID: "t1", Status: a2aStatus{State: taskStateRejected}}
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "TASK_STATE_REJECTED") {
		t.Errorf("error = %v, want it to mention the rejected state", err)
	}
}

// ─── TestA2ATool_TrailingSlashEndpoint ─────────────────────────────────────────

// A config's endpoint URL with a trailing slash must not produce a
// double-slash request path (t.endpoint + "/a2a" vs. TrimRight first).
func TestA2ATool_TrailingSlashEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req a2aRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		task := completedTask("t1", "ok")
		result, _ := json.Marshal(a2aSendMessageResult{Task: &task})                       //nolint:errcheck
		idJSON, _ := json.Marshal(req.ID)                                                  //nolint:errcheck
		json.NewEncoder(w).Encode(a2aResponse{JSONRPC: "2.0", ID: idJSON, Result: result}) //nolint:errcheck
	}))
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	tool.endpoint = srv.URL + "/" // simulate a trailing-slash config URL

	if _, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/a2a" {
		t.Errorf("request path = %q, want %q", gotPath, "/a2a")
	}
}

// ─── TestA2ATool_RPCError ──────────────────────────────────────────────────────

func TestA2ATool_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2aRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		idJSON, _ := json.Marshal(req.ID)      //nolint:errcheck
		json.NewEncoder(w).Encode(a2aResponse{ //nolint:errcheck
			JSONRPC: "2.0", ID: idJSON,
			Error: &a2aRPCError{Code: -32602, Message: "Invalid parameters"},
		})
	}))
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "-32602") {
		t.Errorf("error = %v, want it to contain the RPC error code", err)
	}
}

// ─── TestA2ATool_MissingQuery ───────────────────────────────────────────────────

func TestA2ATool_MissingQuery(t *testing.T) {
	var called atomic.Bool
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			called.Store(true)
			return completedTask("t1", "")
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing query, got nil")
	}
	if called.Load() {
		t.Error("server should not have been called")
	}
}

// ─── TestA2ATool_GetParametersSchema ────────────────────────────────────────────

func TestA2ATool_GetParametersSchema(t *testing.T) {
	tool, err := NewA2ATool(&config.ToolDefinition{Name: "t", URL: "http://x", AgentName: "A"})
	if err != nil {
		t.Fatalf("NewA2ATool: %v", err)
	}
	schema := tool.GetParametersSchema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("schema missing \"query\" property")
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "query" {
		t.Errorf("required = %v, want [\"query\"]", schema["required"])
	}
}

// ─── TestA2ATool_DefaultDescription ─────────────────────────────────────────────

func TestA2ATool_DefaultDescription(t *testing.T) {
	tool, err := NewA2ATool(&config.ToolDefinition{Name: "t", URL: "http://x", AgentName: "Researcher"})
	if err != nil {
		t.Fatalf("NewA2ATool: %v", err)
	}
	if tool.GetDescription() == "" {
		t.Error("expected a non-empty default description")
	}
}

// ─── TestNewA2ATool_TimeoutResolution ────────────────────────────────────────────

// Regression: def.Timeout <= 0 used to always fall back to the hardcoded 30s
// default, so there was no way to configure an unlimited HTTP client timeout
// (unlike run.go's --timeout, where <=0 disables the deadline). A negative
// value must now leave httpClient.Timeout at its zero value, which
// net/http.Client treats as "no timeout" — bounding the call only by the
// caller's own context.
func TestNewA2ATool_TimeoutResolution(t *testing.T) {
	tests := []struct {
		name        string
		def         int
		wantTimeout time.Duration
	}{
		{"unset (0) falls back to 30s default", 0, 30 * time.Second},
		{"positive value used as-is", 5, 5 * time.Second},
		{"negative value disables the client timeout", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, err := NewA2ATool(&config.ToolDefinition{
				Name: "t", URL: "http://x", AgentName: "A", Timeout: tt.def,
			})
			if err != nil {
				t.Fatalf("NewA2ATool: %v", err)
			}
			if tool.httpClient.Timeout != tt.wantTimeout {
				t.Errorf("httpClient.Timeout = %v, want %v", tool.httpClient.Timeout, tt.wantTimeout)
			}
		})
	}
}

// ─── TestA2ATool_PollsUntilTerminal ──────────────────────────────────────────────

// A real remote agent may leave a task WORKING instead of completing it
// synchronously — the client must poll GetTask until it reaches a terminal
// state, per the async task lifecycle the old client never modeled at all.
func TestA2ATool_PollsUntilTerminal(t *testing.T) {
	var polls atomic.Int32
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			return a2aTask{ID: "t1", Status: a2aStatus{State: taskStateWorking}}
		},
		getTask: func(id string) (a2aTask, bool) {
			if id != "t1" {
				return a2aTask{}, false
			}
			n := polls.Add(1)
			if n < 2 {
				return a2aTask{ID: "t1", Status: a2aStatus{State: taskStateWorking}}, true
			}
			return completedTask("t1", "done after polling"), true
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "done after polling" {
		t.Errorf("result = %q, want %q", result, "done after polling")
	}
	if polls.Load() < 2 {
		t.Errorf("expected at least 2 GetTask polls, got %d", polls.Load())
	}
	if elapsed := time.Since(start); elapsed < a2aPollInitialDelay {
		t.Errorf("expected polling to wait at least %v, took %v", a2aPollInitialDelay, elapsed)
	}
}

// ─── TestA2ATool_ContextCanceledWhilePolling ─────────────────────────────────────

func TestA2ATool_ContextCanceledWhilePolling(t *testing.T) {
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			return a2aTask{ID: "t1", Status: a2aStatus{State: taskStateWorking}}
		},
		getTask: func(id string) (a2aTask, bool) {
			return a2aTask{ID: "t1", Status: a2aStatus{State: taskStateWorking}}, true
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected an error from a canceled context, got nil")
	}
}

// ─── TestA2ATool_UnrecognizedTaskState_ErrorsImmediately ───────────────────────

// TestA2ATool_UnrecognizedTaskState_ErrorsImmediately covers a task state
// awaitTerminal's switch doesn't recognize (a peer using different enum
// casing, a future state value, a malformed response). Without a default
// arm the switch falls through into the poll-and-continue path and the
// client polls forever — this asserts it instead errors on the very first
// state evaluation, before any GetTask poll (no getTask handler is even
// registered, so a poll here would panic the mock, not just hang).
func TestA2ATool_UnrecognizedTaskState_ErrorsImmediately(t *testing.T) {
	srv := newMockA2AServer(t, mockHandlers{
		sendMessage: func(tenant string, msg a2aMessage) a2aTask {
			return a2aTask{ID: "t1", Status: a2aStatus{State: "TASK_STATE_BOGUS"}}
		},
	})
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected an error for an unrecognized task state, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected task state") {
		t.Errorf("want an \"unexpected task state\" error, got: %v", err)
	}
}

// ─── TestA2ATool_NonSuccessHTTPStatus_ErrorsOnStatus ───────────────────────────

// TestA2ATool_NonSuccessHTTPStatus_ErrorsOnStatus covers a non-2xx HTTP
// response — the real shape of hitting a token-gated peer without a
// credential (internal/server's jsonErrorResponse writes {"error": "<msg
// string>"} with a 401 status). Decoding that straight into a2aResponse
// (whose Error field is *a2aRPCError, an object) fails with a confusing
// JSON-type-mismatch error that points at JSON shape, not at auth. This
// asserts the status is checked before decoding, so the diagnostic names
// the actual problem.
func TestA2ATool_NonSuccessHTTPStatus_ErrorsOnStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: missing or invalid API token"}) //nolint:errcheck
	}))
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err == nil {
		t.Fatal("expected an error for a 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("want the HTTP status surfaced in the error, got: %v", err)
	}
	if strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error names a JSON decode failure instead of the actual HTTP status: %v", err)
	}
}

// ─── TestA2ATool_NumericResponseID_Decodes ─────────────────────────────────────

// TestA2ATool_NumericResponseID_Decodes covers a peer echoing the request id
// as a JSON number rather than a string. JSON-RPC 2.0 permits id to be a
// String, Number, or Null; the server side of this same package already
// uses json.RawMessage for exactly this reason (cmd/rakitsu/a2a_serve.go's
// srvA2AResponse.ID) — this client was narrower than the server it talks to.
func TestA2ATool_NumericResponseID_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		task := completedTask("t1", "ok")
		result, _ := json.Marshal(a2aSendMessageResult{Task: &task}) //nolint:errcheck
		// A numeric, unquoted id — valid per JSON-RPC 2.0, invalid against
		// the old ID string field.
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result) //nolint:errcheck
	}))
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
}

// ─── credential (issue #22 item 1) ─────────────────────────────────────────

// TestA2ATool_SendsAuthorizationHeaderWhenAPIKeyConfigured covers the
// missing half of #22: the a2a tool had no way to authenticate against a
// token-gated peer at all. An APIKey on the tool definition must become a
// real "Authorization: Bearer <key>" header on every call.
func TestA2ATool_SendsAuthorizationHeaderWhenAPIKeyConfigured(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		task := completedTask("t1", "ok")
		result, _ := json.Marshal(a2aSendMessageResult{Task: &task})                                       //nolint:errcheck
		json.NewEncoder(w).Encode(a2aResponse{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Result: result}) //nolint:errcheck
	}))
	defer srv.Close()

	tool, err := NewA2ATool(&config.ToolDefinition{
		Name:      "test_tool",
		URL:       srv.URL,
		AgentName: "Researcher",
		APIKey:    "secret-token-123",
	})
	if err != nil {
		t.Fatalf("NewA2ATool: %v", err)
	}
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if want := "Bearer secret-token-123"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestA2ATool_NoAuthorizationHeaderWhenAPIKeyEmpty preserves today's
// unauthenticated behavior for the common case (loopback peer, no token
// configured) — an empty APIKey must not send a header at all, not an
// empty "Authorization: Bearer " one.
func TestA2ATool_NoAuthorizationHeaderWhenAPIKeyEmpty(t *testing.T) {
	headerSet := true // starts true so "never called" would fail loudly
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, headerSet = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		task := completedTask("t1", "ok")
		result, _ := json.Marshal(a2aSendMessageResult{Task: &task})                                       //nolint:errcheck
		json.NewEncoder(w).Encode(a2aResponse{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Result: result}) //nolint:errcheck
	}))
	defer srv.Close()

	tool := newTool(t, srv, "Researcher", "")
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"query": "q"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if headerSet {
		t.Error("expected no Authorization header when APIKey is empty")
	}
}
