package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Values the redaction rules must catch: a credential-named tool argument
// and a credential-named KEY=value line in tool output. Deliberately not
// shaped like real tokens (no sk-/ghp_ prefix) so the repo's own leak scan
// doesn't flag this file — the rules key on the names, not the values.
const (
	liveArgSecret = "liveargsecret123456"
	liveOutSecret = "liveoutputsecret456789"
)

func emitSecretToolCall(bus *telemetry.EventBus, agent string) {
	bus.Emit(agent, telemetry.EventToolCallStart, telemetry.ToolCallStartPayload{
		ToolCallID: "tc1", ToolName: "cli",
		Arguments: map[string]interface{}{"api_key": liveArgSecret, "cmd": "env"},
	})
	bus.Emit(agent, telemetry.EventToolCallEnd, telemetry.ToolCallEndPayload{
		ToolCallID: "tc1", ToolName: "cli",
		Output: "HOME=/root\nexport GITHUB_TOKEN=" + liveOutSecret + "\n",
	})
}

func containsLiveSecret(s string) bool {
	return strings.Contains(s, liveArgSecret) || strings.Contains(s, liveOutSecret)
}

// Runs started inside `serve` (web UI chat) forward tool events to the
// browser and record them in the chat transcript, which is persisted as
// .chat.json. None of those may carry the raw secret — only session JSONL
// and the CLI hub forwarder were redacted before.
func TestChatSession_ToolEventsRedactedForClientsAndTranscript(t *testing.T) {
	sess := startSession(t, makeFakeChatBuildFunc("a1",
		func(ctx context.Context, q string, bus *telemetry.EventBus) (string, error) {
			emitSecretToolCall(bus, "a1")
			// A real turn makes another LLM call after a tool result; give
			// the forwarder time to handle the tool events before the turn
			// ends (the end-of-turn drain only picks up token chunks).
			time.Sleep(100 * time.Millisecond)
			return "done", nil
		},
		nil,
	))
	col := newCollector(sess)

	// A ?events=1 client (debugger tab) gets raw telemetry events too.
	evClient := &chatClient{session: sess, sendCh: make(chan serverMsg, 256), closed: make(chan struct{}), forwardAll: true}
	go evClient.eventForwardPump()
	t.Cleanup(func() { close(evClient.closed) })
	time.Sleep(20 * time.Millisecond) // let the pump subscribe before the turn emits

	if err := sess.Submit("go"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !col.waitFor(func(msgs []serverMsg) bool {
		var done, end bool
		for _, m := range msgs {
			done = done || m.Type == "turn_done"
			end = end || m.Type == "tool_end"
		}
		return done && end
	}, 3*time.Second) {
		t.Fatalf("turn did not finish: %+v", col.snapshot())
	}

	var sawStart, sawEnd bool
	for _, m := range col.snapshot() {
		switch m.Type {
		case "tool_start":
			sawStart = true
			if containsLiveSecret(m.ToolArgs) {
				t.Errorf("tool_start args carry the secret: %s", m.ToolArgs)
			}
		case "tool_end":
			sawEnd = true
			if containsLiveSecret(m.Output) {
				t.Errorf("tool_end output carries the secret: %s", m.Output)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("tool frames missing (start=%v end=%v)", sawStart, sawEnd)
	}

	transcript, _ := json.Marshal(sess.snapshotTranscript())
	if containsLiveSecret(string(transcript)) {
		t.Errorf("chat transcript (persisted as .chat.json) carries the secret: %s", transcript)
	}

	deadline := time.Now().Add(time.Second)
	var sawEvent bool
	for time.Now().Before(deadline) && !sawEvent {
		select {
		case m := <-evClient.sendCh:
			if m.Type != "event" || m.Event == nil {
				continue
			}
			if containsLiveSecret(string(m.Event.Payload)) {
				t.Errorf("forwarded %s event carries the secret: %s", m.Event.EventType, m.Event.Payload)
			}
			if m.Event.EventType == telemetry.EventToolCallEnd {
				sawEvent = true
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !sawEvent {
		t.Fatal("events=1 client never received TOOL_CALL_END")
	}
}

// The hub's /events SSE stream carries events from in-process runs (bus)
// and replays buffered hub-ingested events; /api/hub/sessions/events
// returns the buffer. An older or third-party CLI may push unredacted
// events, so ingest redacts too.
func TestSSEAndHubIngest_RedactToolEvents(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	s := NewSSEServer(bus, "localhost", 0)
	s.activeSessions["cli-1"] = &ActiveSession{ID: "cli-1", Status: "running"}
	srv := httptest.NewServer(s.Mux())
	t.Cleanup(srv.Close)

	// Hub ingest of a raw (unredacted) event from a CLI.
	raw, _ := json.Marshal(telemetry.ToolCallEndPayload{ToolCallID: "x", Output: "export GITHUB_TOKEN=" + liveOutSecret})
	body, _ := json.Marshal([]telemetry.AgentEvent{{SessionID: "cli-1", EventType: telemetry.EventToolCallEnd, AgentName: "a", Payload: raw}})
	resp, err := http.Post(srv.URL+"/api/hub/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Get(srv.URL + "/api/hub/sessions/events?id=cli-1")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if containsLiveSecret(buf.String()) {
		t.Errorf("hub session events carry the secret: %s", buf.String())
	}

	// Live /events stream: replayed buffer + an in-process event.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		emitSecretToolCall(bus, "inproc")
	}()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	toolEnds := 0
	for sc.Scan() && toolEnds < 2 {
		line := sc.Text()
		if containsLiveSecret(line) {
			t.Errorf("/events stream carries the secret: %s", line)
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"TOOL_CALL_END"`) {
			toolEnds++
		}
	}
	if toolEnds < 2 {
		t.Fatalf("saw %d TOOL_CALL_END frames on /events, want 2 (replay + live)", toolEnds)
	}
}
