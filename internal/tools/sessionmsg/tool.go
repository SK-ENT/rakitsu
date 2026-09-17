// Package sessionmsg implements the cross-session messaging tool pair:
// send_message injects a fire-and-forget message into another live session
// via the hub's POST /api/sessions/{id}/message endpoint; list_sessions
// discovers reachable targets via GET /api/sessions/live.
//
// The tools are auto-registered on chat-mode sessions when
// settings.session_msg.enabled is true and a hub URL is known (same pattern
// as the memory_* and user_input tools) — and since one-shot runs can be
// steered mid-run via injected messages, also on hub-connected one-shot runs
// that set settings.session_msg.enabled.
// If the serve process sets RAKITSU_SESSION_MSG_TOKEN or RAKITSU_API_TOKEN,
// both tools send it as the bearer token (the message-specific token takes
// precedence when both are set) — mirrors the server's own
// sessionMsgAuthorized, which accepts either.
package sessionmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/tools"
)

// sessionMsgTokenEnv and apiTokenEnv mirror internal/server's constants;
// duplicated (two strings) rather than importing the server package into a
// tool.
const sessionMsgTokenEnv = "RAKITSU_SESSION_MSG_TOKEN"
const apiTokenEnv = "RAKITSU_API_TOKEN"

// bearerToken returns the token to send as the Authorization header,
// preferring the message-specific token and falling back to the
// server-wide one. Mirrors the server's own sessionMsgAuthorized, which
// accepts either — without this fallback, an operator who configures only
// RAKITSU_API_TOKEN (the more common single-token setup) would get both
// tools rejected with 401.
func bearerToken() string {
	if tok := os.Getenv(sessionMsgTokenEnv); tok != "" {
		return tok
	}
	return os.Getenv(apiTokenEnv)
}

// maxWaitTimeoutSeconds mirrors internal/server's sessionMsgWaitMaxMs (120s)
// — the tool's requested timeout_seconds is clamped to this before it's even
// sent, and WaitHTTPClient's own Timeout is set comfortably above it so the
// HTTP client never cuts the wait short before the server's own timeout does.
const maxWaitTimeoutSeconds = 120

// Deps are the dependencies both tools share. HubURL is the base URL of the
// server owning /api/sessions/* ("" disables both tools — NewTools returns
// nil). SessionID/AgentName identify the calling session for attribution and
// reply addressing.
type Deps struct {
	HubURL     string
	SessionID  string
	AgentName  string
	HTTPClient *http.Client // nil = 5s-timeout default, used for fire-and-forget sends
	// WaitHTTPClient is used only when wait:true is requested — it needs a
	// timeout comfortably larger than the server's own wait cap
	// (maxWaitTimeoutSeconds) so it never cuts the wait short. Kept separate
	// from HTTPClient so fire-and-forget calls keep their short 5s timeout.
	WaitHTTPClient *http.Client // nil = (maxWaitTimeoutSeconds + 10s)-timeout default
}

// NewTools returns the send_message + list_sessions pair, or nil when no hub
// URL is configured (e.g. --no-hub).
func NewTools(d Deps) []tools.Tool {
	if d.HubURL == "" {
		return nil
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if d.WaitHTTPClient == nil {
		d.WaitHTTPClient = &http.Client{Timeout: (maxWaitTimeoutSeconds + 10) * time.Second}
	}
	d.HubURL = strings.TrimRight(d.HubURL, "/")
	return []tools.Tool{&SendMessageTool{d: d}, &ListSessionsTool{d: d}}
}

// SendMessageTool sends a fire-and-forget message into another live session.
type SendMessageTool struct {
	d Deps
}

func (t *SendMessageTool) GetName() string { return "send_message" }

func (t *SendMessageTool) GetDescription() string {
	return "Send a message into another live rakitsu session (web chat or interactive TUI). By default this is " +
		"fire-and-forget: you will NOT get a reply from this call — if the other session's agent responds, its " +
		"reply arrives later as a new message into this conversation. Pass wait=true to instead block until the " +
		"target's turn finishes and get its reply (or error) back directly from this call. " +
		"Use list_sessions first to find a session id. The target session must have opted in (settings.session_msg.enabled). " +
		"A session id you received a message from is always a valid target even if list_sessions does not show it — such replies are stored in that sender's hub inbox for it to poll. " +
		"One-shot (kind \"one-shot\") targets fold your message into their running task; with wait=true a reply only arrives if that agent chooses to respond before the timeout — a timeout is normal, not an error."
}

func (t *SendMessageTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Target session id (from list_sessions)",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "The message to deliver",
			},
			"wait": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, block until the target session's agent replies (or times out) instead of returning immediately.",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "number",
				"description": "Max seconds to wait when wait=true (default 30, max 120). Ignored otherwise.",
			},
		},
		"required": []string{"session_id", "text"},
	}
}

func (t *SendMessageTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	sessionID, _ := args["session_id"].(string)
	text, _ := args["text"].(string)
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("both session_id and text are required")
	}
	if strings.Contains(sessionID, "/") {
		// The server (internal/server/sse.go's handleSessionByID) parses
		// the target id by splitting the URL path on "/", taking only the
		// segment before the first one. A session_id containing "/" would
		// resolve to a DIFFERENT id server-side than what this tool
		// targeted — and, worse, could resolve back to t.d.SessionID even
		// though the raw string isn't literally equal to it, bypassing the
		// self-send check below. Reject it outright rather than trying to
		// escape it: net/http decodes a percent-escaped "/" back to a
		// literal one in r.URL.Path before the server-side split ever
		// sees it, so escaping alone would not close this.
		return "", fmt.Errorf("session_id must not contain '/'")
	}
	if sessionID == t.d.SessionID {
		return "", fmt.Errorf("session %s is this session — send_message targets OTHER sessions", sessionID)
	}
	wait, _ := args["wait"].(bool)

	reqBody := map[string]interface{}{
		"text":            text,
		"from_session_id": t.d.SessionID,
		"from_name":       t.d.AgentName,
	}
	client := t.d.HTTPClient
	if wait {
		reqBody["wait"] = true
		if ts, ok := args["timeout_seconds"].(float64); ok && ts > 0 {
			timeoutMs := int(ts * 1000)
			if timeoutMs > maxWaitTimeoutSeconds*1000 {
				timeoutMs = maxWaitTimeoutSeconds * 1000
			}
			reqBody["timeout_ms"] = timeoutMs
		}
		client = t.d.WaitHTTPClient
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	reqURL := fmt.Sprintf("%s/api/sessions/%s/message", t.d.HubURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := bearerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cross-session messaging unavailable: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		Status      string `json:"status"`
		Error       string `json:"error"`
		Waited      bool   `json:"waited"`
		Reply       string `json:"reply"`
		Interrupted bool   `json:"interrupted"`
		TurnError   string `json:"turn_error"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&out); err != nil {
		return "", fmt.Errorf("hub returned %d with unreadable body", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = out.Status
		}
		return "", fmt.Errorf("delivery failed (%s): %s", out.Status, msg)
	}
	if out.Status == "mailboxed" {
		return fmt.Sprintf("stored in %s's hub inbox — that id is not a live session; it will see the message when it polls its inbox", sessionID), nil
	}
	if !wait {
		return fmt.Sprintf("delivered to %s — if that session's agent replies, the reply will arrive here as a new message", sessionID), nil
	}
	if out.TurnError != "" {
		return "", fmt.Errorf("%s's turn failed: %s", sessionID, out.TurnError)
	}
	if out.Waited {
		return out.Reply, nil
	}
	// Delivered but the reply didn't arrive within the timeout.
	msg := out.Note
	if msg == "" {
		msg = "reply not received within timeout"
	}
	return fmt.Sprintf("delivered to %s — %s; if it replies later it will arrive here as a new message", sessionID, msg), nil
}

// ListSessionsTool lists live sessions reachable for cross-session messaging.
type ListSessionsTool struct {
	d Deps
}

func (t *ListSessionsTool) GetName() string { return "list_sessions" }

func (t *ListSessionsTool) GetDescription() string {
	return "List live rakitsu sessions that can be messaged with send_message. " +
		"Rows marked accepts=false have not opted in and will reject messages."
}

func (t *ListSessionsTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *ListSessionsTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.d.HubURL+"/api/sessions/live", nil)
	if err != nil {
		return "", err
	}
	if tok := bearerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := t.d.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cross-session messaging unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hub returned %d", resp.StatusCode)
	}

	var sessions []struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Kind            string `json:"kind"`
		Location        string `json:"location"`
		Status          string `json:"status"`
		AcceptsMessages bool   `json:"accepts_messages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sessions); err != nil {
		return "", fmt.Errorf("invalid live-sessions response: %w", err)
	}
	if len(sessions) == 0 {
		return "no live sessions", nil
	}

	var b strings.Builder
	b.WriteString("id | name | kind | location | accepts\n")
	for _, s := range sessions {
		marker := ""
		if s.ID == t.d.SessionID {
			marker = " (you — do not message yourself)"
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %v%s\n", s.ID, s.Name, s.Kind, s.Location, s.AcceptsMessages, marker)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
