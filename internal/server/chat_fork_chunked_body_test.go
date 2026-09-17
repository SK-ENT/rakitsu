package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestHandleChatFork_ChunkedBodyIsNotSkipped regression-guards: the handler
// only decoded the request body when r.ContentLength > 0, but Go sets
// ContentLength to -1 for a chunked-transfer-encoded body (or any request
// without a Content-Length header) — not just for a genuinely empty one.
// Such a request had turn_id/mode/env_vars silently fall back to their zero
// values instead of being parsed, with no error surfaced to the caller.
//
// This forks a persisted (non-live) session whose config carries a
// "[REDACTED]" api_key mask, sending real env_vars in a body with
// ContentLength == -1 — the same shape a chunked request arrives in. If the
// body is skipped, the fork proceeds with no env_vars and the forked
// session's config keeps the "[REDACTED]" mask; if it's parsed, the key
// gets de-redacted (mirrors the assertion TestForkPersistedOptionsActivePath
// already makes one layer down, at the HTTP-body-decoding layer this bug
// actually lives in).
func TestHandleChatFork_ChunkedBodyIsNotSkipped(t *testing.T) {
	m, cfgID := newForkTestManager(t)
	m.buildFunc = makeFakeChatBuildFunc("a1", nil, nil)
	persistForkFixture(t, m, "chat-past-chunked", cfgID)

	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.SetChatManager(m)

	body := `{"mode":"path","env_vars":{"OPENAI_API_KEY":"real-secret"}}`
	req := httptest.NewRequest("POST", "/api/chat/chat-past-chunked/fork", strings.NewReader(body))
	req.ContentLength = -1 // what net/http sets server-side for a chunked body

	rec := httptest.NewRecorder()
	s.handleChatFork(rec, req, "chat-past-chunked")

	if rec.Code != 200 {
		t.Fatalf("handleChatFork status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var meta struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	forked := m.Get(meta.ID)
	if forked == nil {
		t.Fatalf("forked session %q not registered in ChatManager", meta.ID)
	}
	got := forked.cfg.Settings.Providers["openai"].APIKey
	if got != "real-secret" {
		t.Errorf("forked session's openai api_key = %q, want de-redacted %q — env_vars from a ContentLength=-1 body were not parsed", got, "real-secret")
	}
}
