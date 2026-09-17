package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// startMsgSession is startSession with settings.session_msg.enabled — the
// receiver opt-in every SubmitExternal test needs.
func startMsgSession(t *testing.T, bf ChatBuildFunc) *ChatSession {
	t.Helper()
	cfg := &config.Config{
		Name:   "test",
		Agents: []config.AgentDefinition{{Name: "a1"}},
	}
	cfg.Settings.SessionMsg.Enabled = true
	sess, err := StartChatSession(context.Background(), ChatSessionOptions{
		ConfigID:  "test",
		Cfg:       cfg,
		BuildFunc: bf,
	})
	if err != nil {
		t.Fatalf("StartChatSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// TestSubmitExternalDoesNotInterrupt — an injected message must queue behind
// the in-flight turn, not cancel it, and only the LLM call may see the
// <session_message> wrapping (persisted transcript stays clean).
func TestSubmitExternalDoesNotInterrupt(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var queries []string
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, query string, bus *telemetry.EventBus) (string, error) {
			mu.Lock()
			queries = append(queries, query)
			n := len(queries)
			mu.Unlock()
			if n == 1 {
				select {
				case <-release:
					return "first-done", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "second-done", nil
		}, nil))
	col := newCollector(sess)

	if err := sess.Submit("first"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !col.waitFor(func(ms []serverMsg) bool {
		for _, m := range ms {
			if m.Type == "turn_started" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		t.Fatal("first turn never started")
	}

	if err := sess.SubmitExternal("hello over there", "chat-sender-1", "Kit"); err != nil {
		t.Fatalf("SubmitExternal: %v", err)
	}
	close(release)

	if !col.waitFor(func(ms []serverMsg) bool {
		var dones []serverMsg
		for _, m := range ms {
			if m.Type == "turn_done" {
				dones = append(dones, m)
			}
		}
		return len(dones) == 2
	}, 2*time.Second) {
		t.Fatalf("expected 2 turn_done, got: %+v", col.snapshot())
	}

	// The first turn completed on its own terms — not cancelled.
	for _, m := range col.snapshot() {
		if m.Type == "turn_done" && m.Final == "first-done" && m.Err != "" {
			t.Fatalf("first turn was interrupted: %+v", m)
		}
	}

	// LLM-facing split: the second query is wrapped, the stored text is not.
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("expected 2 runner calls, got %d", len(queries))
	}
	if !strings.Contains(queries[1], "<session_message") || !strings.Contains(queries[1], `from_session="chat-sender-1"`) {
		t.Fatalf("external turn query not wrapped: %q", queries[1])
	}
	var sawNote, sawCleanUser bool
	for _, e := range sess.snapshotTranscript() {
		if e.Kind == "system" && strings.Contains(e.Text, "message from Kit") {
			sawNote = true
		}
		if e.Kind == "user" && e.Text == "hello over there" {
			sawCleanUser = true
		}
		if strings.Contains(e.Text, "<session_message") {
			t.Fatalf("wrapper leaked into transcript entry: %+v", e)
		}
	}
	if !sawNote || !sawCleanUser {
		t.Fatalf("transcript missing note (%v) or clean user text (%v): %+v", sawNote, sawCleanUser, sess.snapshotTranscript())
	}
}

func TestSubmitExternalBusyWhenQueued(t *testing.T) {
	release := make(chan struct{})
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, query string, bus *telemetry.EventBus) (string, error) {
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}, nil))
	defer close(release)
	col := newCollector(sess)

	if err := sess.Submit("first"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !col.waitFor(func(ms []serverMsg) bool {
		for _, m := range ms {
			if m.Type == "turn_started" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		t.Fatal("first turn never started")
	}

	if err := sess.SubmitExternal("one", "s1", "A"); err != nil {
		t.Fatalf("first SubmitExternal should queue: %v", err)
	}
	if err := sess.SubmitExternal("two", "s1", "A"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second SubmitExternal = %v, want ErrSessionBusy", err)
	}
}

func TestSubmitExternalRejectsWhenNotEnabled(t *testing.T) {
	// Plain startSession does NOT set settings.session_msg.enabled.
	sess := startSession(t, makeFakeChatBuildFunc("a1", nil, nil))
	err := sess.SubmitExternal("hi", "s1", "A")
	if err == nil || !strings.Contains(err.Error(), "does not accept messages") {
		t.Fatalf("SubmitExternal = %v, want opt-in rejection", err)
	}
}

func TestSessionMsgLimiter(t *testing.T) {
	now := time.Now()
	l := newSessionMsgLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < sessionMsgRateLimitMax; i++ {
		// Unordered pair: alternate direction — budget must be shared.
		a, b := "s1", "s2"
		if i%2 == 1 {
			a, b = b, a
		}
		if !l.allow(a, b) {
			t.Fatalf("send %d unexpectedly rate-limited", i+1)
		}
	}
	if l.allow("s1", "s2") {
		t.Fatal("send over the limit was allowed")
	}
	// A different pair is unaffected.
	if !l.allow("s1", "s3") {
		t.Fatal("unrelated pair rate-limited")
	}
	// After the window passes, the pair may send again.
	now = now.Add(sessionMsgRateLimitWindow + time.Second)
	if !l.allow("s1", "s2") {
		t.Fatal("send after window still rate-limited")
	}
}

// newMsgServer builds an SSEServer with a ChatManager holding the given
// sessions and any active (hub-registered) sessions the test needs.
func newMsgServer(t *testing.T, chatSessions ...*ChatSession) *SSEServer {
	t.Helper()
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	cm := NewChatManager(telemetry.NewEventBus(64), nil, nil, makeFakeChatBuildFunc("a1", nil, nil))
	for _, cs := range chatSessions {
		cm.sessions[cs.ID] = cs
	}
	s.chatManager = cm
	return s
}

func TestDeliverMessageRouting(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1", nil, nil))
	s := newMsgServer(t, sess)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	s.activeSessions["tui-optout"] = &ActiveSession{ID: "tui-optout", Mode: "chat", Status: "running", AcceptsMsg: false}
	s.activeSessions["oneshot-1"] = &ActiveSession{ID: "oneshot-1", Status: "running"}
	s.activeSessions["tui-done"] = &ActiveSession{ID: "tui-done", Mode: "chat", Status: "completed", AcceptsMsg: true}

	if _, err := s.deliverMessage(context.Background(), sess.ID, "hi", "from-1", "A", false); err != nil {
		t.Fatalf("local chat delivery: %v", err)
	}
	if _, err := s.deliverMessage(context.Background(), "tui-1", "hi", "from-2", "A", false); err != nil {
		t.Fatalf("remote chat delivery: %v", err)
	}
	cmds := s.pendingCmds["tui-1"]
	if len(cmds) != 1 || cmds[0].Action != "session_message" {
		t.Fatalf("pendingCmds = %+v, want one session_message", cmds)
	}
	if cmds[0].Data["text"] != "hi" || cmds[0].Data["from_session_id"] != "from-2" {
		t.Fatalf("command data = %+v", cmds[0].Data)
	}

	if _, err := s.deliverMessage(context.Background(), "oneshot-1", "hi", "from-3", "A", false); err == nil || !strings.Contains(err.Error(), "not a messaging target") {
		t.Fatalf("one-shot delivery = %v, want not-a-target rejection", err)
	}
	if _, err := s.deliverMessage(context.Background(), "tui-optout", "hi", "from-4", "A", false); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("opt-out delivery = %v, want does-not-accept rejection", err)
	}
	if _, err := s.deliverMessage(context.Background(), "tui-done", "hi", "from-5", "A", false); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("finished-session delivery = %v, want not-running rejection", err)
	}
	if _, err := s.deliverMessage(context.Background(), "nope", "hi", "from-6", "A", false); !errors.Is(err, errMsgNotFound) {
		t.Fatalf("unknown delivery = %v, want errMsgNotFound", err)
	}
}

// TestDeliverMessageRoutingWaitLocal — wait:true against a local target
// returns the turn's outcome inline.
func TestDeliverMessageRoutingWaitLocal(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "pong", nil
		}, nil))
	s := newMsgServer(t, sess)

	result, err := s.deliverMessage(context.Background(), sess.ID, "ping", "from-1", "A", true)
	if err != nil {
		t.Fatalf("local wait delivery: %v", err)
	}
	if result.TimedOut || result.Outcome == nil || result.Outcome.Final != "pong" {
		t.Fatalf("result = %+v, want Outcome.Final=pong", result)
	}
}

// TestDeliverMessageRoutingRemoteWait — wait:true against a remote
// (hub-registered) target queues a wait_token'd DebugCommand and blocks
// until handleHubMessageResult reports the outcome — simulates the poll
// side directly, in-process, the same way TestHubRegisterToCommandPoll does
// for the plain (non-wait) remote path; no real second CLI process needed.
func TestDeliverMessageRoutingRemoteWait(t *testing.T) {
	s := newMsgServer(t)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	type result struct {
		res deliverResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := s.deliverMessage(context.Background(), "tui-1", "ping", "from-2", "A", true)
		done <- result{res, err}
	}()

	// Poll for the queued command carrying a wait_token, mirroring how the
	// remote CLI's 500ms poll loop would pick it up.
	var token string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		cmds := s.pendingCmds["tui-1"]
		s.mu.Unlock()
		if len(cmds) == 1 {
			token, _ = cmds[0].Data["wait_token"].(string)
			if token != "" {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("wait_token never appeared on the queued DebugCommand")
	}

	resp := postHubMessageResult(t, srv.URL, map[string]interface{}{
		"wait_token": token, "final": "remote-pong", "interrupted": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message-result status = %d, want 200", resp.StatusCode)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("deliverMessage: %v", r.err)
		}
		if r.res.TimedOut || r.res.Outcome == nil || r.res.Outcome.Final != "remote-pong" {
			t.Fatalf("result = %+v, want Outcome.Final=remote-pong", r.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deliverMessage never unblocked")
	}
}

// TestDeliverMessageRoutingRemoteWaitTimeout — ctx expiry before any
// message-result arrives is a graceful TimedOut, not an error, and cleans
// up the pendingWaits entry so a late report doesn't leak.
func TestDeliverMessageRoutingRemoteWaitTimeout(t *testing.T) {
	s := newMsgServer(t)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := s.deliverMessage(ctx, "tui-1", "ping", "from-2", "A", true)
	if err != nil {
		t.Fatalf("deliverMessage: %v", err)
	}
	if !result.TimedOut || result.Outcome != nil {
		t.Fatalf("result = %+v, want TimedOut=true Outcome=nil", result)
	}

	s.mu.Lock()
	n := len(s.pendingWaits)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("pendingWaits = %d entries, want 0 (cleaned up on timeout)", n)
	}
}

func TestDeliverMessageRateLimit(t *testing.T) {
	s := newMsgServer(t)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	for i := 0; i < sessionMsgRateLimitMax; i++ {
		if _, err := s.deliverMessage(context.Background(), "tui-1", "hi", "sender", "A", false); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if _, err := s.deliverMessage(context.Background(), "tui-1", "hi", "sender", "A", false); !errors.Is(err, errMsgRateLimited) {
		t.Fatalf("send over limit = %v, want errMsgRateLimited", err)
	}
}

func postMsg(t *testing.T, base, id string, body map[string]string, header http.Header) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/sessions/%s/message", base, id), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHandleSessionMessageEndpoint(t *testing.T) {
	s := newMsgServer(t)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postMsg(t, srv.URL, "tui-1", map[string]string{"text": "hello", "from_session_id": "x"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delivered status = %d, want 200", resp.StatusCode)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "delivered" {
		t.Fatalf("status = %q, want delivered", out["status"])
	}

	resp = postMsg(t, srv.URL, "unknown-id", map[string]string{"text": "hello"}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-session status = %d, want 404", resp.StatusCode)
	}

	resp = postMsg(t, srv.URL, "tui-1", map[string]string{"text": strings.Repeat("x", sessionMsgMaxBytes+1)}, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d, want 413", resp.StatusCode)
	}

	resp = postMsg(t, srv.URL, "tui-1", map[string]string{}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty-text status = %d, want 400", resp.StatusCode)
	}
}

// postMsgJSON is postMsg with an arbitrary JSON body (map[string]string can't
// carry the wait bool / timeout_ms int fields).
func postMsgJSON(t *testing.T, base, id string, body map[string]interface{}) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(fmt.Sprintf("%s/api/sessions/%s/message", base, id), "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestHandleSessionMessageEndpointWaitHappyPath — wait:true against a local
// target blocks and returns the reply inline.
func TestHandleSessionMessageEndpointWaitHappyPath(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "pong", nil
		}, nil))
	s := newMsgServer(t, sess)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postMsgJSON(t, srv.URL, sess.ID, map[string]interface{}{"text": "ping", "wait": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["waited"] != true || out["reply"] != "pong" || out["interrupted"] != false {
		t.Fatalf("body = %+v, want waited=true reply=pong interrupted=false", out)
	}
	if _, ok := out["turn_error"]; ok {
		t.Fatalf("body = %+v, unexpected turn_error on success", out)
	}
}

// TestHandleSessionMessageEndpointWaitTurnError — the target's turn fails;
// this is still HTTP 200 (delivery succeeded) with turn_error populated.
func TestHandleSessionMessageEndpointWaitTurnError(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			return "", errors.New("boom")
		}, nil))
	s := newMsgServer(t, sess)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postMsgJSON(t, srv.URL, sess.ID, map[string]interface{}{"text": "ping", "wait": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["waited"] != true || out["interrupted"] != true || out["turn_error"] != "boom" {
		t.Fatalf("body = %+v, want waited=true interrupted=true turn_error=boom", out)
	}
}

// TestHandleSessionMessageEndpointWaitTimeout — a wait that outlasts
// timeout_ms is still HTTP 200 with waited:false, not an error: the message
// WAS delivered, the reply just didn't arrive in time.
func TestHandleSessionMessageEndpointWaitTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			<-release
			return "late", nil
		}, nil))
	s := newMsgServer(t, sess)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postMsgJSON(t, srv.URL, sess.ID, map[string]interface{}{"text": "ping", "wait": true, "timeout_ms": 50})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["waited"] != false {
		t.Fatalf("body = %+v, want waited=false", out)
	}
	if _, ok := out["note"]; !ok {
		t.Fatalf("body = %+v, want a note explaining how to poll for the reply", out)
	}
}

func TestHandleSessionMessageBearerToken(t *testing.T) {
	t.Setenv(sessionMsgTokenEnv, "sekrit")
	s := newMsgServer(t)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp := postMsg(t, srv.URL, "tui-1", map[string]string{"text": "hello"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer wrong")
	resp = postMsg(t, srv.URL, "tui-1", map[string]string{"text": "hello"}, h)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", resp.StatusCode)
	}
	h.Set("Authorization", "Bearer sekrit")
	resp = postMsg(t, srv.URL, "tui-1", map[string]string{"text": "hello"}, h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good-token status = %d, want 200", resp.StatusCode)
	}
}

// TestHubRegisterToCommandPoll — full remote round-trip: a TUI registers
// with mode:"chat", a message is delivered, and the TUI's command poll
// returns it (and only once).
func TestHubRegisterToCommandPoll(t *testing.T) {
	s := newMsgServer(t)
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	regBody, _ := json.Marshal(map[string]interface{}{
		"id": "tui-9", "name": "cfg", "mode": "chat", "accepts_messages": true,
	})
	resp, err := http.Post(srv.URL+"/api/hub/register", "application/json", bytes.NewReader(regBody))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("register: %v (%d)", err, resp.StatusCode)
	}
	resp.Body.Close()

	resp = postMsg(t, srv.URL, "tui-9", map[string]string{"text": "ping", "from_session_id": "chat-a", "from_name": "A"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deliver status = %d, want 200", resp.StatusCode)
	}

	pollResp, err := http.Get(srv.URL + "/api/hub/commands?id=tui-9")
	if err != nil {
		t.Fatal(err)
	}
	defer pollResp.Body.Close()
	var cmds []DebugCommand
	json.NewDecoder(pollResp.Body).Decode(&cmds)
	if len(cmds) != 1 || cmds[0].Action != "session_message" || cmds[0].Data["text"] != "ping" {
		t.Fatalf("poll = %+v, want one session_message with text ping", cmds)
	}

	// Second poll is empty — commands are drained on delivery.
	pollResp2, err := http.Get(srv.URL + "/api/hub/commands?id=tui-9")
	if err != nil {
		t.Fatal(err)
	}
	defer pollResp2.Body.Close()
	var cmds2 []DebugCommand
	json.NewDecoder(pollResp2.Body).Decode(&cmds2)
	if len(cmds2) != 0 {
		t.Fatalf("second poll = %+v, want empty", cmds2)
	}
}

// TestDeliverMessageRunModeTargets — spec §2: run-mode sessions with
// AcceptsMsg become fire-and-forget targets; without opt-in (or legacy
// Mode "") they stay rejected; a wait:true whose ctx is already cancelled
// resolves as a clean TimedOut, reply-routed (spec §4) response — the
// message was still delivered.
func TestDeliverMessageRunModeTargets(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}
	s.activeSessions["run-optout"] = &ActiveSession{ID: "run-optout", Mode: "run", Status: "running"}
	s.activeSessions["legacy-1"] = &ActiveSession{ID: "legacy-1", Status: "running"}

	if _, err := s.deliverMessage(context.Background(), "run-1", "steer", "from-1", "A", false); err != nil {
		t.Fatalf("run-mode opt-in delivery: %v", err)
	}
	cmds := s.pendingCmds["run-1"]
	if len(cmds) != 1 || cmds[0].Action != "session_message" {
		t.Fatalf("expected 1 queued session_message, got %+v", cmds)
	}
	if _, hasToken := cmds[0].Data["wait_token"]; hasToken {
		t.Fatal("run-mode fire-and-forget must not carry a wait_token")
	}
	if got := cmds[0].Data["text"]; got != "steer" {
		t.Fatalf("queued text = %v", got)
	}

	if _, err := s.deliverMessage(context.Background(), "run-optout", "x", "from-2", "A", false); err == nil ||
		!strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("opt-out run must be rejected, got %v", err)
	}
	if _, err := s.deliverMessage(context.Background(), "legacy-1", "x", "from-3", "A", false); err == nil ||
		!strings.Contains(err.Error(), "not a messaging target") {
		t.Fatalf("legacy one-shot must stay rejected, got %v", err)
	}
	ctxC, cancelC := context.WithCancel(context.Background())
	cancelC()
	r, err := s.deliverMessage(ctxC, "run-1", "x", "from-4", "A", true)
	if err != nil || !r.TimedOut || !r.ReplyRouted {
		t.Fatalf("cancelled wait toward run-mode: r=%+v err=%v", r, err)
	}
	resp := sessionMessageResponse("delivered", true, r)
	note, _ := resp["note"].(string)
	if !strings.Contains(note, "/api/sessions/{id}/inbox") {
		t.Fatalf("note = %q, want reference to the run target's own inbox", note)
	}
}

// TestLiveSessionsIncludesSteerableRuns — spec §2: discovery lists opted-in
// runs with kind "one-shot"; opted-out runs and legacy one-shots stay hidden.
func TestLiveSessionsIncludesSteerableRuns(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Name: "pipeline", Mode: "run", Status: "running", AcceptsMsg: true}
	s.activeSessions["run-optout"] = &ActiveSession{ID: "run-optout", Mode: "run", Status: "running"}
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Mode: "chat", Status: "running", AcceptsMsg: true}
	s.activeSessions["legacy-1"] = &ActiveSession{ID: "legacy-1", Status: "running"}

	rec := httptest.NewRecorder()
	s.handleLiveSessions(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/live", nil))
	var rows []LiveSessionMeta
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	kinds := map[string]string{}
	for _, r := range rows {
		kinds[r.ID] = r.Kind
	}
	if kinds["run-1"] != "one-shot" {
		t.Fatalf("run-1 kind = %q, want one-shot (rows %+v)", kinds["run-1"], rows)
	}
	if kinds["tui-1"] != "chat" {
		t.Fatalf("tui-1 kind = %q, want chat", kinds["tui-1"])
	}
	if _, ok := kinds["run-optout"]; ok {
		t.Fatal("opted-out run must not be listed")
	}
	if _, ok := kinds["legacy-1"]; ok {
		t.Fatal("legacy one-shot must not be listed")
	}
}

// TestDeliverMessageMailboxFallback — spec §3: not-live target WITH a
// mailbox → stored; without → 404. Provisioning happens via the HTTP
// handler on a successful send (sender-side effect), tested below.
func TestDeliverMessageMailboxFallback(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.mailboxes.provision("cc-1")
	res, err := s.deliverMessage(context.Background(), "cc-1", "reply!", "run-9", "worker", false)
	if err != nil || !res.Mailboxed {
		t.Fatalf("mailbox delivery: res=%+v err=%v", res, err)
	}
	msgs, _ := s.mailboxes.drain("cc-1")
	if len(msgs) != 1 || msgs[0].Text != "reply!" || msgs[0].FromSessionID != "run-9" {
		t.Fatalf("stored = %+v", msgs)
	}
	if _, err := s.deliverMessage(context.Background(), "nobody", "x", "run-9", "w", false); !errors.Is(err, errMsgNotFound) {
		t.Fatalf("no mailbox must stay 404, got %v", err)
	}
}

// TestHandleSessionMessageProvisionsSender + inbox endpoint round-trip.
func TestSessionMessageInboxRoundTrip(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}

	// Foreign sender cc-1 steers run-1 → cc-1's mailbox is provisioned.
	body, _ := json.Marshal(SessionMessageRequest{Text: "steer", FromSessionID: "cc-1", FromName: "claude"})
	rec := httptest.NewRecorder()
	s.handleSessionMessage(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/run-1/message", bytes.NewReader(body)), "run-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("steer send: %d %s", rec.Code, rec.Body.String())
	}

	// run-1 replies to cc-1 → mailboxed.
	body, _ = json.Marshal(SessionMessageRequest{Text: "done", FromSessionID: "run-1", FromName: "worker"})
	rec = httptest.NewRecorder()
	s.handleSessionMessage(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/cc-1/message", bytes.NewReader(body)), "cc-1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mailboxed") {
		t.Fatalf("reply send: %d %s", rec.Code, rec.Body.String())
	}

	// cc-1 polls its inbox.
	rec = httptest.NewRecorder()
	s.handleSessionInbox(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/cc-1/inbox", nil), "cc-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox: %d", rec.Code)
	}
	var msgs []MailboxMsg
	if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil || len(msgs) != 1 || msgs[0].Text != "done" {
		t.Fatalf("inbox body: %v err=%v", msgs, err)
	}

	// Unknown inbox → 404.
	rec = httptest.NewRecorder()
	s.handleSessionInbox(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/nobody/inbox", nil), "nobody")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown inbox = %d, want 404", rec.Code)
	}
}

// TestProvisioningSkipsLiveSenders — spec §3: a live session sending a
// message must NOT get a mailbox; only external (not-live) sender IDs do.
func TestProvisioningSkipsLiveSenders(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}
	s.activeSessions["run-2"] = &ActiveSession{ID: "run-2", Mode: "run", Status: "running", AcceptsMsg: true}

	// Live sender run-2 → live target run-1: successful, but run-2 is live → no mailbox.
	body, _ := json.Marshal(SessionMessageRequest{Text: "hi", FromSessionID: "run-2", FromName: "w"})
	rec := httptest.NewRecorder()
	s.handleSessionMessage(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/run-1/message", bytes.NewReader(body)), "run-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("live->live send: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.mailboxes.drain("run-2"); ok {
		t.Fatal("live sender must not be provisioned a mailbox")
	}

	// External sender cc-1 → live target: provisioned.
	body, _ = json.Marshal(SessionMessageRequest{Text: "hi", FromSessionID: "cc-1", FromName: "claude"})
	rec = httptest.NewRecorder()
	s.handleSessionMessage(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/run-1/message", bytes.NewReader(body)), "run-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("external->live send: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.mailboxes.drain("cc-1"); !ok {
		t.Fatal("external sender must be provisioned")
	}
}

func TestHandleLiveSessions(t *testing.T) {
	sess := startMsgSession(t, makeFakeChatBuildFunc("a1", nil, nil))
	s := newMsgServer(t, sess)
	s.activeSessions["tui-1"] = &ActiveSession{ID: "tui-1", Name: "remote-cfg", Mode: "chat", Status: "running", AcceptsMsg: true}
	s.activeSessions["oneshot-1"] = &ActiveSession{ID: "oneshot-1", Status: "running"}
	s.activeSessions["tui-done"] = &ActiveSession{ID: "tui-done", Mode: "chat", Status: "completed", AcceptsMsg: true}
	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []LiveSessionMeta
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("live sessions = %+v, want exactly local chat + running remote chat", got)
	}
	byID := map[string]LiveSessionMeta{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if m, ok := byID[sess.ID]; !ok || m.Location != "local" || !m.AcceptsMessages {
		t.Fatalf("local session row = %+v", byID)
	}
	if m, ok := byID["tui-1"]; !ok || m.Location != "remote" || !m.AcceptsMessages {
		t.Fatalf("remote session row = %+v", byID)
	}
}

// TestReplyRoutedWait — spec §4: wait toward a run-mode target parks until
// a message flows target→sender; that reply is consumed inline and never
// reaches the sender's mailbox.
func TestReplyRoutedWait(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}
	s.mailboxes.provision("cc-1")

	type res struct {
		r   deliverResult
		err error
	}
	done := make(chan res, 1)
	go func() {
		r, err := s.deliverMessage(context.Background(), "run-1", "steer", "cc-1", "claude", true)
		done <- res{r, err}
	}()

	// Wait until the steer is queued AND the wait is parked.
	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		parked := len(s.pendingReplyWaits) == 1
		s.mu.Unlock()
		if parked {
			break
		}
		select {
		case <-deadline:
			t.Fatal("wait never parked")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The run replies to cc-1 → resolves the wait instead of mailboxing.
	if _, err := s.deliverMessage(context.Background(), "cc-1", "the answer", "run-1", "worker", false); err != nil {
		t.Fatalf("reply delivery: %v", err)
	}
	r := <-done
	if r.err != nil || r.r.TimedOut || r.r.Outcome == nil || r.r.Outcome.Final != "the answer" {
		t.Fatalf("wait outcome = %+v err=%v", r.r, r.err)
	}
	if msgs, _ := s.mailboxes.drain("cc-1"); len(msgs) != 0 {
		t.Fatalf("consumed reply must not be mailboxed, got %v", msgs)
	}
}

func TestReplyRoutedWaitTimeout(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r, err := s.deliverMessage(ctx, "run-1", "steer", "cc-1", "claude", true)
	if err != nil || !r.TimedOut {
		t.Fatalf("timeout: r=%+v err=%v", r, err)
	}
	s.mu.Lock()
	n := len(s.pendingReplyWaits)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("timed-out wait must be cleaned up")
	}
	// A late reply lands in the (now provisioned via HTTP path) mailbox —
	// covered by Task 2's fallback; nothing to assert here beyond cleanup.
}

func TestReplyRoutedWaitDoublePark(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	s.activeSessions["run-1"] = &ActiveSession{ID: "run-1", Mode: "run", Status: "running", AcceptsMsg: true}
	s.mu.Lock()
	s.pendingReplyWaits["cc-1\x00run-1"] = make(chan turnOutcome, 1)
	s.mu.Unlock()
	if _, err := s.deliverMessage(context.Background(), "run-1", "again", "cc-1", "claude", true); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("double park must be busy, got %v", err)
	}
}

// TestReplyRoutedWaitResolverSendIsAtomicWithDelete pins the lossless-resolve
// invariant from the controller ruling: the resolver's map-delete and
// channel-send happen in ONE s.mu critical section, so any observer that
// sees the key absent is guaranteed the outcome is already sitting in the
// (buffered, cap-1) channel — never lost, never a torn state. Deterministic:
// no goroutines, no sleeps — the delete+send atomicity is the property under
// test, not a race between two callers.
func TestReplyRoutedWaitResolverSendIsAtomicWithDelete(t *testing.T) {
	s := NewSSEServer(telemetry.NewEventBus(64), "localhost", 0)
	key := replyWaitKey("cc-1", "run-1")
	ch := make(chan turnOutcome, 1)
	s.mu.Lock()
	s.pendingReplyWaits[key] = ch
	s.mu.Unlock()

	// cc-1 has no chatManager entry and no activeSessions entry, so if the
	// reply-routing block did NOT consume this inline, deliverMessage would
	// fall through to the not-found path — proving the resolver branch is
	// what handled it.
	if _, err := s.deliverMessage(context.Background(), "cc-1", "the answer", "run-1", "worker", false); err != nil {
		t.Fatalf("reply delivery: %v", err)
	}

	s.mu.Lock()
	_, stillParked := s.pendingReplyWaits[key]
	s.mu.Unlock()
	if stillParked {
		t.Fatal("resolved wait must be removed from pendingReplyWaits")
	}

	select {
	case out := <-ch:
		if out.Final != "the answer" {
			t.Fatalf("buffered outcome = %+v", out)
		}
	default:
		t.Fatal("resolver's delete and send must commit together under s.mu — key absent must imply the outcome is already buffered")
	}
}
