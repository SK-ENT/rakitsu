package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

const completed = `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`

type capture struct {
	body    responsesRequest
	headers http.Header
}

func newTestProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cap.body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	access := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeAuth(t, dir, access, "r1", "", "acct-9")
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"gpt-toml\"\nmodel_reasoning_effort = \"low\"\n[other]\nmodel = \"ignored\"\n"), 0o600)

	p, err := NewProvider(&llm.ProviderConfig{CredentialsFile: path, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	p.client = srv.Client()
	return p, cap
}

func TestProvider_ModelAndEffortFromConfigToml(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {})
	if p.GetModel() != "gpt-toml" || p.effort != "low" {
		t.Fatalf("model=%q effort=%q", p.GetModel(), p.effort)
	}
	if p.GetName() != "codex" {
		t.Fatalf("name = %q", p.GetName())
	}
}

func TestProvider_TextStreamAndRequestShape(t *testing.T) {
	p, cap := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"type":"response.created","response":{}}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			completed,
		))
	})
	history := []llm.Message{llm.NewTextMessage("user", "hi")}
	tools := []llm.ToolDefinition{{Name: "echo", Description: "d", Parameters: map[string]interface{}{"type": "object"}}}

	sr, err := p.GenerateStream(context.Background(), "be nice", history, tools)
	if err != nil {
		t.Fatal(err)
	}
	var text, reasoning string
	for c := range sr.Chunks {
		text += c.Text
		reasoning += c.Reasoning
	}
	res := <-sr.Final
	if text != "Hello" || reasoning != "think" {
		t.Fatalf("text=%q reasoning=%q", text, reasoning)
	}
	if res.Response != "Hello" || res.FinishReason != "stop" || res.ThinkingContent != "think" {
		t.Fatalf("result: %+v", res)
	}
	if res.TokenUsage == nil || res.TokenUsage.TotalTokens != 15 {
		t.Fatalf("usage: %+v", res.TokenUsage)
	}

	b := cap.body
	if b.Model != "gpt-toml" || b.Instructions != "be nice" || !b.Stream || b.Store || b.ToolChoice != "auto" {
		t.Fatalf("request body: %+v", b)
	}
	if b.Reasoning == nil || b.Reasoning.Effort != "low" {
		t.Fatalf("reasoning: %+v", b.Reasoning)
	}
	if len(b.Tools) != 1 || b.Tools[0].Type != "function" || b.Tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", b.Tools)
	}
	var first map[string]interface{}
	_ = json.Unmarshal(b.Input[0], &first)
	if first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("input[0]: %v", first)
	}
	h := cap.headers
	if h.Get("Authorization") == "" || h.Get("chatgpt-account-id") != "acct-9" || h.Get("originator") != "codex_cli_rs" || h.Get("Accept") != "text/event-stream" {
		t.Fatalf("headers: %v", h)
	}
}

func TestProvider_ToolCallRoundTrip(t *testing.T) {
	p, cap := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{\"msg\":\"x\"}"}}`,
			completed,
		))
	})
	res, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
		t.Fatalf("result: %+v", res)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "echo" || tc.Arguments["msg"] != "x" {
		t.Fatalf("tool call: %+v", tc)
	}
	items, ok := res.ProviderMetadata[reasoningItemsKey].([]json.RawMessage)
	if !ok || len(items) != 1 {
		t.Fatalf("reasoning items not kept: %+v", res.ProviderMetadata)
	}

	// Next turn: assistant message carries the call + metadata, then the tool result.
	assistant := llm.Message{Role: "assistant", ToolCalls: res.ToolCalls, Metadata: res.ProviderMetadata}
	toolMsg := llm.Message{Role: "tool", ToolCallID: "call_1", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "done"}}}
	if _, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go"), assistant, toolMsg}, nil); err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(cap.body.Input))
	var callOut map[string]interface{}
	for _, raw := range cap.body.Input {
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		types = append(types, m["type"].(string))
		if m["type"] == "function_call_output" {
			callOut = m
		}
	}
	want := []string{"message", "reasoning", "function_call", "function_call_output"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("input types = %v, want %v", types, want)
	}
	if callOut["call_id"] != "call_1" || callOut["output"] != "done" {
		t.Fatalf("function_call_output: %v", callOut)
	}
}

func TestProvider_ReasoningItemsSurviveJSONRoundTrip(t *testing.T) {
	meta := map[string]interface{}{reasoningItemsKey: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1"}`)}}
	b, _ := json.Marshal(meta)
	var decoded map[string]interface{}
	_ = json.Unmarshal(b, &decoded)
	items := storedReasoningItems(decoded)
	if len(items) != 1 || !strings.Contains(string(items[0]), `"rs_1"`) {
		t.Fatalf("items after round trip: %v", items)
	}
}

func TestProvider_FailedEvent(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"type":"response.failed","response":{"status":"failed","error":{"code":"x","message":"boom"}}}`))
	})
	_, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go")}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want failed error, got %v", err)
	}
}

func TestProvider_HTTPErrorCarriesStatus(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	_, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go")}, nil)
	var sc llm.StatusCoder
	if err == nil || !asStatusCoder(err, &sc) || sc.StatusCode() != 429 {
		t.Fatalf("want 429 StatusCoder, got %v", err)
	}
}

func asStatusCoder(err error, out *llm.StatusCoder) bool {
	sc, ok := err.(llm.StatusCoder)
	if ok {
		*out = sc
	}
	return ok
}

func TestProvider_RefreshesOn401AndRetries(t *testing.T) {
	var attempts int32
	fresh := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	var p *Provider
	var cap *capture
	p, cap = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fresh})
			return
		}
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, sse(`{"type":"response.output_text.delta","delta":"ok"}`, completed))
	})
	origURL := refreshURL
	refreshURL = p.baseURL + "/oauth/token"
	defer func() { refreshURL = origURL }()

	res, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Response != "ok" || attempts != 2 {
		t.Fatalf("response=%q attempts=%d", res.Response, attempts)
	}
	if cap.headers.Get("Authorization") != "Bearer "+fresh {
		t.Fatalf("retry did not use refreshed token")
	}
}

func TestNewProvider_NoModelAnywhere(t *testing.T) {
	dir := t.TempDir()
	path := writeAuth(t, dir, "a", "r", "", "acct")
	_, err := NewProvider(&llm.ProviderConfig{CredentialsFile: path})
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("want no-model error, got %v", err)
	}
}

func TestListModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"models":[{"slug":"gpt-a","display_name":"A","visibility":"list"},{"slug":"gpt-hidden","visibility":"hide"},{"slug":"gpt-b"}]}`)
	}))
	defer srv.Close()
	access := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeAuth(t, t.TempDir(), access, "r1", "", "acct-1")
	models, err := ListModels(context.Background(), &llm.ProviderConfig{CredentialsFile: path, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/models?client_version=0.0.0" || gotAuth != "Bearer "+access {
		t.Fatalf("path=%q auth set=%v", gotPath, gotAuth != "")
	}
	slugs := ListedSlugs(models)
	if strings.Join(slugs, ",") != "gpt-a,gpt-b" {
		t.Fatalf("slugs = %v", slugs)
	}
}

// SSE permits CRLF line endings. bufio.Scanner's default ScanLines split
// already strips an optional \r before \n (see its doc comment), so no
// special handling is needed here — this test pins that behavior so a
// future change to the scanner's split function can't silently break it.
func TestProvider_CRLFFramedStream(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := strings.ReplaceAll(sse(
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"}}`,
			completed,
		), "\n", "\r\n")
		fmt.Fprint(w, body)
	})
	res, err := p.Generate(context.Background(), "", []llm.Message{llm.NewTextMessage("user", "go")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Response != "Hello" || len(res.ToolCalls) != 1 || res.TokenUsage == nil || res.TokenUsage.TotalTokens != 15 {
		t.Fatalf("CRLF stream parsed wrong: %+v", res)
	}
}
