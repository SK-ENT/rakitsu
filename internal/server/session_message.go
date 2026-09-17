package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Cross-session messaging: POST /api/sessions/{id}/message injects a message
// into a live chat-mode session — a local ChatManager session (direct
// SubmitExternal/SubmitExternalWait) or a hub-registered interactive TUI
// (queued as a "session_message" command picked up by its 500ms poll) — or
// an opted-in hub-registered one-shot run (Mode == "run"; `wait` toward a
// run target parks the sender→target pair in pendingReplyWaits and is
// resolved by the target's own reply, not by turn completion — see spec §4;
// a timeout there is normal, not an error).
// Fire-and-forget is the default; an optional `wait` mode blocks until the
// target's turn finishes and returns the reply (or turn error) inline — see
// deliverMessage/handleSessionMessage. Receiving requires the TARGET
// session's own config to opt in (settings.session_msg.enabled); senders
// are additionally bounded by a
// pairwise rate limit and an optional bearer token
// (RAKITSU_SESSION_MSG_TOKEN) for non-localhost deployments.
const (
	sessionMsgMaxBytes        = 16 * 1024
	sessionMsgRateLimitMax    = 6
	sessionMsgRateLimitWindow = 30 * time.Second
	// sessionMsgTokenEnv, when set in the serve process env, makes the
	// message endpoint require "Authorization: Bearer <token>". Senders
	// (send_message tool, CLI) read the same variable.
	sessionMsgTokenEnv = "RAKITSU_SESSION_MSG_TOKEN"
	// sessionMsgAction is the hub DebugCommand action carrying a message to
	// a remote interactive TUI (underscored, matching the debug_enable
	// convention the frontend sends — hub.go's hyphenated doc comment is
	// stale).
	sessionMsgAction = "session_message"
	// sessionMsgWaitDefaultMs / sessionMsgWaitMaxMs bound how long
	// handleSessionMessage blocks when wait:true is requested. A request's
	// own timeout_ms is clamped to this range.
	sessionMsgWaitDefaultMs = 30_000
	sessionMsgWaitMaxMs     = 120_000
)

var (
	errMsgNotFound    = errors.New("session not found")
	errMsgRateLimited = errors.New("rate limited: too many messages between these two sessions recently — possible loop; wait and retry")
)

// SessionMessageRequest is the wire payload for POST /api/sessions/{id}/message.
// FromSessionID/FromName are sender-claimed attribution (unverified) used for
// the receiver's transcript note and reply addressing. Wait/TimeoutMs opt
// into the synchronous mode — see deliverMessage.
type SessionMessageRequest struct {
	Text          string `json:"text"`
	FromSessionID string `json:"from_session_id,omitempty"`
	FromName      string `json:"from_name,omitempty"`
	Wait          bool   `json:"wait,omitempty"`
	TimeoutMs     int    `json:"timeout_ms,omitempty"`
}

// deliverResult carries the outcome of a deliverMessage call beyond plain
// success/failure. Outcome is set only when Wait was requested and the
// target's turn finished before ctx expired. TimedOut means Wait was
// requested but ctx expired first — the message WAS still delivered, this
// is not a delivery failure.
type deliverResult struct {
	Outcome  *turnOutcome
	TimedOut bool
	// Mailboxed: target was not live; stored in its hub mailbox (spec §3).
	Mailboxed bool
	// ReplyRouted: set on every outcome (resolved or TimedOut) of a run-mode
	// wait (spec §4) — it went through pendingReplyWaits, not a chat turn, so
	// a timeout note pointing at /api/chat/{id}/tree would be wrong; it
	// should point at the run target's own inbox instead.
	ReplyRouted bool
}

// LiveSessionMeta is one row of GET /api/sessions/live — a live chat-mode
// session that could be a cross-session messaging target. AcceptsMessages
// reflects the session's own settings.session_msg opt-in.
type LiveSessionMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Location string `json:"location"` // "local" (this serve process) | "remote" (hub-registered CLI)
	// Kind distinguishes target lifecycles: "chat" (persistent turn loop,
	// replies arrive as later turns) vs "one-shot" (steerable run).
	Kind            string `json:"kind"`
	Status          string `json:"status,omitempty"`
	AcceptsMessages bool   `json:"accepts_messages"`
}

// sessionMsgLimiter enforces the pairwise sliding-window rate limit: at most
// `max` deliveries between the same unordered session pair per window. Bounds
// two agents auto-replying to each other (A↔B ping-pong) without needing any
// cooperation from either side.
type sessionMsgLimiter struct {
	mu     sync.Mutex
	now    func() time.Time // injectable for tests
	max    int
	window time.Duration
	pairs  map[string][]time.Time
}

func newSessionMsgLimiter() *sessionMsgLimiter {
	return &sessionMsgLimiter{
		now:    time.Now,
		max:    sessionMsgRateLimitMax,
		window: sessionMsgRateLimitWindow,
		pairs:  make(map[string][]time.Time),
	}
}

func (l *sessionMsgLimiter) allow(a, b string) bool {
	if a > b {
		a, b = b, a
	}
	key := a + "\x00" + b
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.pairs[key][:0]
	for _, t := range l.pairs[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.pairs[key] = kept
		return false
	}
	l.pairs[key] = append(kept, now)
	return true
}

// replyWaitKey is the pendingReplyWaits map key for a directional wait:
// waiterID is parked on a reply from targetID.
func replyWaitKey(waiterID, targetID string) string { return waiterID + "\x00" + targetID }

// deliverMessage routes a message to sessionID — the single funnel for every
// sender (tool, CLI, web UI). The returned error is caller-safe and returned
// verbatim to senders; a nil error means delivery succeeded (queued, and —
// when wait is set — the wait itself did not error out).
//
// wait blocks until the target's turn finishes or ctx expires, via one of
// three mechanisms: local (same-process ChatManager, via SubmitExternalWait),
// remote chat (hub-registered TUI, via a wait_token relayed through
// POST /api/hub/message-result — see handleHubMessageResult), or remote
// run-mode (parked in pendingReplyWaits, resolved by the target's own reply
// — see spec §4).
func (s *SSEServer) deliverMessage(ctx context.Context, sessionID, text, fromID, fromName string, wait bool) (deliverResult, error) {
	if !s.msgLimiter.allow(fromID, sessionID) {
		return deliverResult{}, errMsgRateLimited
	}

	// Reply-routing (spec §4): if the addressee is a parked steering sender
	// waiting on THIS sender, the message is the reply — hand it over inline
	// and never deliver it a second way.
	s.mu.Lock()
	if ch, ok := s.pendingReplyWaits[replyWaitKey(sessionID, fromID)]; ok {
		delete(s.pendingReplyWaits, replyWaitKey(sessionID, fromID))
		// Send under s.mu is safe by construction: the channel is buffered
		// (cap 1) and this is its only-ever send — the key is removed in the
		// same critical section, so no second resolver can reach it. A
		// blocking send here is impossible; do NOT copy this pattern for
		// unbuffered or multi-send channels.
		ch <- turnOutcome{Final: text}
		s.mu.Unlock()
		return deliverResult{}, nil
	}
	s.mu.Unlock()

	// Local web-chat session in this serve process.
	if s.chatManager != nil {
		if sess := s.chatManager.Get(sessionID); sess != nil {
			if !wait {
				return deliverResult{}, sess.SubmitExternal(text, fromID, fromName)
			}
			out, err := sess.SubmitExternalWait(ctx, text, fromID, fromName)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					return deliverResult{TimedOut: true}, nil
				}
				return deliverResult{}, err
			}
			return deliverResult{Outcome: &out}, nil
		}
	}

	// Hub-registered remote CLI session (interactive TUI).
	//
	// IMPORTANT: s.mu also guards activeSessions/pendingCmds for every other
	// session's hub register/poll/ingest/debug traffic — it must be released
	// before any blocking wait below, or one slow waiter would serialize the
	// entire hub for up to the wait timeout.
	s.mu.Lock()
	active, ok := s.activeSessions[sessionID]
	if !ok {
		s.mu.Unlock()
		if s.mailboxes.deliver(sessionID, MailboxMsg{Text: text, FromSessionID: fromID, FromName: fromName}) {
			return deliverResult{Mailboxed: true}, nil
		}
		return deliverResult{}, fmt.Errorf("%s: %w", sessionID, errMsgNotFound)
	}
	if active.Mode != "chat" && active.Mode != "run" {
		s.mu.Unlock()
		return deliverResult{}, fmt.Errorf("session %s is a one-shot run, not a messaging target", sessionID)
	}
	if active.Status != "running" {
		s.mu.Unlock()
		return deliverResult{}, fmt.Errorf("session %s is not running (status %q)", sessionID, active.Status)
	}
	if !active.AcceptsMsg {
		s.mu.Unlock()
		return deliverResult{}, fmt.Errorf("session %s does not accept messages (settings.session_msg.enabled is false)", sessionID)
	}

	// run-mode targets have no wait_token machinery (they don't poll for a
	// result the way a remote CLI does) — a synchronous wait instead parks
	// on a reply routed back through deliverMessage itself (spec §4).
	if active.Mode == "run" {
		cmd := DebugCommand{
			Action: sessionMsgAction,
			Data: map[string]interface{}{
				"text":            text,
				"from_session_id": fromID,
				"from_name":       fromName,
			},
		}
		if !wait {
			s.pendingCmds[sessionID] = append(s.pendingCmds[sessionID], cmd)
			s.mu.Unlock()
			return deliverResult{}, nil
		}

		key := replyWaitKey(fromID, sessionID)
		if _, exists := s.pendingReplyWaits[key]; exists {
			s.mu.Unlock()
			return deliverResult{}, fmt.Errorf("%w: a wait from %s to %s is already in flight", ErrSessionBusy, fromID, sessionID)
		}
		waitCh := make(chan turnOutcome, 1)
		s.pendingReplyWaits[key] = waitCh
		s.pendingCmds[sessionID] = append(s.pendingCmds[sessionID], cmd)
		s.mu.Unlock()

		select {
		case out := <-waitCh:
			return deliverResult{Outcome: &out, ReplyRouted: true}, nil
		case <-ctx.Done():
			s.mu.Lock()
			_, stillParked := s.pendingReplyWaits[key]
			if stillParked {
				delete(s.pendingReplyWaits, key)
			}
			s.mu.Unlock()
			if !stillParked {
				// A resolver consumed the reply concurrently with our
				// timeout — its send committed under s.mu, so the outcome
				// is already buffered. Hand it to the sender instead of
				// losing it.
				out := <-waitCh
				return deliverResult{Outcome: &out, ReplyRouted: true}, nil
			}
			return deliverResult{TimedOut: true, ReplyRouted: true}, nil
		}
	}

	data := map[string]interface{}{
		"text":            text,
		"from_session_id": fromID,
		"from_name":       fromName,
	}
	var waitCh chan turnOutcome
	var token string
	if wait {
		token = uuid.New().String()
		waitCh = make(chan turnOutcome, 1)
		s.pendingWaits[token] = waitCh
		data["wait_token"] = token
	}
	s.pendingCmds[sessionID] = append(s.pendingCmds[sessionID], DebugCommand{
		Action: sessionMsgAction,
		Data:   data,
	})
	s.mu.Unlock()

	if !wait {
		return deliverResult{}, nil
	}
	select {
	case out := <-waitCh:
		return deliverResult{Outcome: &out}, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pendingWaits, token)
		s.mu.Unlock()
		return deliverResult{TimedOut: true}, nil
	}
}

// isLiveSession reports whether id is a currently-live messaging target
// (local web-chat session or hub-registered CLI). Used to keep mailbox
// provisioning external-only (spec §3): live senders have a real session,
// not a mailbox.
func (s *SSEServer) isLiveSession(id string) bool {
	if s.chatManager != nil && s.chatManager.Get(id) != nil {
		return true
	}
	s.mu.RLock()
	_, ok := s.activeSessions[id]
	s.mu.RUnlock()
	return ok
}

// sessionMsgAuthorized checks the shared bearer token when one is configured;
// with no token, every request is authorized (the documented localhost/Tailscale
// trust assumption). Either the message-specific RAKITSU_SESSION_MSG_TOKEN or the
// server-wide RAKITSU_API_TOKEN is accepted, so operators can set a single token
// for the whole control plane. Shared by handleSessionMessage and
// handleSessionInbox.
func sessionMsgAuthorized(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	for _, env := range []string{sessionMsgTokenEnv, apiTokenEnv} {
		tok := os.Getenv(env)
		if tok == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1 {
			return true
		}
	}
	// Authorized only if NO token is configured at all.
	return os.Getenv(sessionMsgTokenEnv) == "" && os.Getenv(apiTokenEnv) == ""
}

// handleSessionMessage serves POST /api/sessions/{id}/message. Dispatched
// from handleSessionByID's path parsing (the /api/sessions/ prefix is owned
// by the persisted-history handler).
func (s *SSEServer) handleSessionMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !sessionMsgAuthorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req SessionMessageRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, sessionMsgMaxBytes+1024)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, `{"error":"text is required"}`, http.StatusBadRequest)
		return
	}
	if len(req.Text) > sessionMsgMaxBytes {
		http.Error(w, `{"error":"text too large (16KB max)"}`, http.StatusRequestEntityTooLarge)
		return
	}

	ctx := r.Context()
	if req.Wait {
		ms := sessionMsgWaitDefaultMs
		if req.TimeoutMs > 0 {
			ms = req.TimeoutMs
		}
		if ms > sessionMsgWaitMaxMs {
			ms = sessionMsgWaitMaxMs
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}

	result, err := s.deliverMessage(ctx, sessionID, req.Text, req.FromSessionID, req.FromName, req.Wait)
	status, code := "delivered", http.StatusOK
	switch {
	case err == nil:
	case errors.Is(err, ErrSessionBusy):
		status, code = "busy", http.StatusConflict
	case errors.Is(err, errMsgRateLimited):
		status, code = "rate_limited", http.StatusTooManyRequests
	case errors.Is(err, errMsgNotFound):
		status, code = "not_found", http.StatusNotFound
	default:
		status, code = "rejected", http.StatusForbidden
	}
	if err == nil {
		if req.FromSessionID != "" && !s.isLiveSession(req.FromSessionID) {
			s.mailboxes.provision(req.FromSessionID)
		}
		if result.Mailboxed {
			status = "mailboxed"
		}
	}

	sent := telemetry.SessionMsgSentPayload{
		ToSessionID:   sessionID,
		FromSessionID: req.FromSessionID,
		FromName:      req.FromName,
		Text:          chat.TruncateSessionMsg(req.Text),
		Status:        status,
	}
	if err != nil {
		sent.Error = err.Error()
	}
	s.eventBus.Emit(sessionID, telemetry.EventSessionMsgSent, sent)

	w.WriteHeader(code)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"status": status, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(sessionMessageResponse(status, req.Wait, result))
}

// handleSessionInbox serves GET /api/sessions/{id}/inbox — drain-on-read
// mailbox poll for external senders (spec §3). 404 for never-provisioned
// (or expired) IDs so callers can distinguish "no mailbox" from "empty".
func (s *SSEServer) handleSessionInbox(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !sessionMsgAuthorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	msgs, ok := s.mailboxes.drain(id)
	if !ok {
		http.Error(w, `{"error":"no mailbox for this id"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msgs)
}

// sessionMessageResponse builds the wire response body for a successful
// (err == nil) deliverMessage call. Delivery-failure responses are built
// separately, above, and never carry wait/reply fields.
func sessionMessageResponse(status string, wait bool, result deliverResult) map[string]interface{} {
	out := map[string]interface{}{"status": status}
	if !wait {
		return out
	}
	if result.TimedOut {
		out["waited"] = false
		if result.ReplyRouted {
			out["note"] = "no reply within timeout; poll GET /api/sessions/{id}/inbox"
		} else {
			out["note"] = "reply not received within timeout; poll GET /api/chat/{id}/tree"
		}
		return out
	}
	out["waited"] = true
	if result.Outcome != nil {
		out["reply"] = result.Outcome.Final
		out["interrupted"] = result.Outcome.Interrupted
		if result.Outcome.Err != "" {
			out["turn_error"] = result.Outcome.Err
		}
	}
	return out
}

// handleLiveSessions serves GET /api/sessions/live — live chat-mode sessions
// (messaging targets) merged from the local ChatManager and hub-registered
// remote CLIs, plus opted-in one-shot runs (kind "one-shot"); legacy
// one-shots and opted-out runs stay excluded. Backed by live state only,
// never the sessions.json index (known concurrency races there).
func (s *SSEServer) handleLiveSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	out := []LiveSessionMeta{}
	if s.chatManager != nil {
		for _, cs := range s.chatManager.Sessions() {
			out = append(out, LiveSessionMeta{
				ID:              cs.ID,
				Name:            cs.agentName,
				Location:        "local",
				Kind:            "chat",
				Status:          "running",
				AcceptsMessages: cs.cfg.Settings.SessionMsg.Enabled,
			})
		}
	}
	s.mu.RLock()
	for id, as := range s.activeSessions {
		if as.Status != "running" {
			continue
		}
		kind := ""
		switch {
		case as.Mode == "chat":
			kind = "chat"
		case as.Mode == "run" && as.AcceptsMsg:
			kind = "one-shot"
		default:
			continue // legacy one-shots and opted-out runs stay invisible
		}
		out = append(out, LiveSessionMeta{
			ID:              id,
			Name:            as.Name,
			Location:        "remote",
			Kind:            kind,
			Status:          as.Status,
			AcceptsMessages: as.AcceptsMsg,
		})
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	json.NewEncoder(w).Encode(out)
}
