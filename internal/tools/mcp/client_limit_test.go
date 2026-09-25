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
	"testing"
	"time"
)

func newMockStdio(t *testing.T, maxResponseBytes int) *StdioClient {
	t.Helper()
	client, err := NewStdioClient(os.Args[0], nil, map[string]string{helperProcessEnvVar: "1"}, 5, maxResponseBytes)
	if err != nil {
		t.Fatalf("NewStdioClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client
}

// TestStdioClient_OversizedResponseFailsOnlyThatCall is a regression test:
// a response line over the cap used to stop the read loop and kill the
// server, so every later call failed too. Now only the oversized call
// fails, with a clear error, and the server keeps answering.
func TestStdioClient_OversizedResponseFailsOnlyThatCall(t *testing.T) {
	for _, method := range []string{"big_id_first", "big_id_last", "trigger_oversized"} {
		t.Run(method, func(t *testing.T) {
			client := newMockStdio(t, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			resp, err := client.send(ctx, method, nil)
			if err != nil {
				t.Fatalf("send: want an rpc error response, got transport error %v", err)
			}
			if resp.Error == nil || !strings.Contains(resp.Error.Message, "max_response_bytes") {
				t.Fatalf("want an over-limit error naming max_response_bytes, got %+v", resp.Error)
			}

			got, err := client.CallTool(ctx, "greet", nil)
			if err != nil || got != "Hello!" {
				t.Fatalf("next call after oversized response: got %q, %v; want Hello!", got, err)
			}
		})
	}
}

func TestStdioClient_RaisedLimitAcceptsLargeResponse(t *testing.T) {
	client := newMockStdio(t, 4<<20)
	resp, err := client.send(context.Background(), "big_id_last", nil)
	if err != nil || resp.Error != nil {
		t.Fatalf("want success under a 4 MiB limit, got %v / %+v", err, resp.Error)
	}
	text, err := parseToolCallResult(resp.Result)
	if err != nil || len(text) != 2<<20 {
		t.Fatalf("want the full 2 MiB text, got len %d, err %v", len(text), err)
	}
}

func TestHTTPClient_OversizedResponseClearError(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req rpcRequest
				json.NewDecoder(r.Body).Decode(&req)    //nolint:errcheck
				payload, _ := json.Marshal(rpcResponse{ //nolint:errcheck
					JSONRPC: "2.0", ID: req.ID,
					Result: json.RawMessage(fmt.Sprintf(`{"padding":%q}`, strings.Repeat("x", 2<<20))),
				})
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(payload) //nolint:errcheck
			}))
			defer srv.Close()

			client, _ := NewHTTPClient(srv.URL, 10, 0)
			_, err := client.send(context.Background(), "tools/call", nil)
			if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
				t.Fatalf("want an over-limit error naming max_response_bytes, got %v", err)
			}

			big, _ := NewHTTPClient(srv.URL, 10, 4<<20)
			if _, err := big.send(context.Background(), "tools/call", nil); err != nil {
				t.Fatalf("want success under a 4 MiB limit, got %v", err)
			}
		})
	}
}

func TestReadBoundedLine_LimitExcludesNewline(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("abcd\nabcde\nxy"), 16)
	for _, want := range []struct {
		line string
		over bool
	}{{"abcd", false}, {"", true}, {"xy", false}} {
		line, over, _ := readBoundedLine(br, 4)
		if string(line) != want.line || (over != nil) != want.over {
			t.Fatalf("got %q over=%v, want %q over=%v", line, over != nil, want.line, want.over)
		}
	}
}
