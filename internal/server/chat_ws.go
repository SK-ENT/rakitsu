package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// chatWSUpgrader enforces the same localhost/same-origin policy as
// CorsMiddleware. WebSocket upgrades bypass CORS entirely, so without an
// explicit CheckOrigin any web page the user visits could open a socket to
// the local hub and drive the agent. Mirror isAllowedOrigin here.
var chatWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return isAllowedOrigin(r.Header.Get("Origin")) },
}

const (
	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingPeriod      = 45 * time.Second
	clientSendQueue = 128
)

// chatClient represents a single connected WS client attached to a session.
// Each client has its own send queue and write goroutine to serialize all
// writes on the underlying connection (a gorilla/websocket requirement).
type chatClient struct {
	conn       *websocket.Conn
	session    *ChatSession
	sendCh     chan serverMsg
	closeOnce  sync.Once
	closed     chan struct{}
	forwardAll bool // if true, raw telemetry events are forwarded as "event" msgs
}

func (c *chatClient) send(msg serverMsg) {
	select {
	case c.sendCh <- msg:
	case <-c.closed:
	default:
		// Slow client — drop rather than block the session.
	}
}

func (c *chatClient) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// writePump serializes outbound messages on the ws connection and sends
// periodic pings to keep the connection alive.
func (c *chatClient) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()
	for {
		select {
		case msg, ok := <-c.sendCh:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			return
		}
	}
}

// readPump decodes incoming JSON frames and dispatches to the session.
func (c *chatClient) readPump() {
	defer c.close()
	c.conn.SetReadLimit(1 << 16) // 64KiB
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			c.send(serverMsg{Type: "error", Message: "invalid json: " + err.Error()})
			continue
		}
		switch msg.Type {
		case "turn":
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			// Slash commands are handled entirely server-side and never reach
			// the agent — the same dispatch the TUI uses, just routed back
			// as a system message instead of an inline block. Matches TUI
			// behavior: /model swap happens silently, the user sees only the
			// confirmation system block.
			if reply, handled := c.session.HandleSlashCommand(text); handled {
				c.send(serverMsg{Type: "system", Text: reply})
				continue
			}
			if err := c.session.Submit(text); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
			}
		case "interrupt":
			c.session.Interrupt()
		case "user_input_response":
			c.session.RespondUserInput(msg.RequestID, msg.Text)
		case "edit_turn":
			// PR D-2 auto-regen: EditTurn creates the sibling and switches
			// the active path; SubmitOnActiveLeaf then drives the agent to
			// respond on that new leaf. Without the second step the user
			// sees a new branch but no answer.
			if err := c.session.EditTurn(msg.TurnID, msg.Text); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
				continue
			}
			if err := c.session.SubmitOnActiveLeaf(msg.Text); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
			}
		case "regenerate":
			// PR D-2 auto-regen: same flow as edit_turn, but the text is
			// the existing turn's UserText (copied by RegenerateTurn into
			// the new sibling). Read it back off the active leaf so we
			// pass the correct text to SubmitOnActiveLeaf.
			if err := c.session.RegenerateTurn(msg.TurnID); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
				continue
			}
			text := c.session.ActiveLeafUserText()
			if text == "" {
				c.send(serverMsg{Type: "error", Message: "regenerate: active leaf has no user text"})
				continue
			}
			if err := c.session.SubmitOnActiveLeaf(text); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
			}
		case "switch_branch":
			dir := msg.Dir
			if dir == 0 {
				dir = 1
			}
			if err := c.session.SwitchBranch(msg.TurnID, dir); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
			}
		case "set_active_path":
			if err := c.session.SetActivePath(msg.TurnID); err != nil {
				c.send(serverMsg{Type: "error", Message: err.Error()})
			}
		case "request_tree":
			c.session.RequestTree()
		default:
			c.send(serverMsg{Type: "error", Message: "unknown message type: " + msg.Type})
		}
	}
}

// eventForwardPump forwards raw telemetry events from the session bus to the
// client when ?events=1 is set on the upgrade URL. Used by the debugger tab
// (PR B) which needs the full event stream, not just token chunks.
func (c *chatClient) eventForwardPump() {
	if !c.forwardAll {
		return
	}
	sub := c.session.eventBus.Subscribe()
	defer c.session.eventBus.Unsubscribe(sub)
	for {
		select {
		case <-c.closed:
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			// Token chunks are already handled by the session's per-turn
			// forwarder as "token" messages, so skip the duplicate here.
			if ev.EventType == telemetry.EventTokenChunk {
				continue
			}
			evCopy := ev
			evCopy.Payload = telemetry.RedactEventPayload(evCopy.EventType, evCopy.Payload)
			c.send(serverMsg{Type: "event", Event: &evCopy})
		}
	}
}

// handleChatWS upgrades to WebSocket and attaches the client to an existing
// ChatSession. Path format: /ws/chat/{sessionID}, optional ?events=1.
func (s *SSEServer) handleChatWS(w http.ResponseWriter, r *http.Request) {
	if s.chatManager == nil {
		http.Error(w, "chat manager not configured", http.StatusServiceUnavailable)
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/ws/chat/")
	sid = strings.TrimSuffix(sid, "/")
	if sid == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	sess := s.chatManager.Get(sid)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	conn, err := chatWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade writes its own error response.
		return
	}

	c := &chatClient{
		conn:       conn,
		session:    sess,
		sendCh:     make(chan serverMsg, clientSendQueue),
		closed:     make(chan struct{}),
		forwardAll: r.URL.Query().Get("events") == "1",
	}

	detach := sess.attach(c)
	defer detach()

	// Initial hello — carries meta AND a transcript snapshot so that a
	// reconnecting client (tab switch, browser reload) can rebuild the
	// visible conversation immediately, then continue receiving live
	// events normally.
	c.send(serverMsg{
		Type:       "attached",
		Meta:       sess.Meta(),
		Transcript: sess.snapshotTranscript(),
	})

	go c.writePump()
	go c.eventForwardPump()
	c.readPump() // blocks until client disconnects

	log.Printf("chat ws: client detached from session %s", sess.ID)
}
