package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestEnqueueSteerDropsOldestOnOverflow(t *testing.T) {
	ch := make(chan chat.InboundSessionMsg, 2)
	var dropped []string
	onDrop := func(m chat.InboundSessionMsg) { dropped = append(dropped, m.Text) }
	enqueueSteer(ch, chat.InboundSessionMsg{Text: "a"}, onDrop)
	enqueueSteer(ch, chat.InboundSessionMsg{Text: "b"}, onDrop)
	enqueueSteer(ch, chat.InboundSessionMsg{Text: "c"}, onDrop)
	if len(dropped) != 1 || dropped[0] != "a" {
		t.Fatalf("dropped = %v, want [a]", dropped)
	}
	if got := (<-ch).Text; got != "b" {
		t.Fatalf("head = %q, want b", got)
	}
	if got := (<-ch).Text; got != "c" {
		t.Fatalf("next = %q, want c", got)
	}
}

// TestEnqueueSteerOverflowEmitsDroppedOverflowStatus mirrors the onDrop
// closure wired in run.go's "session_message" OnCommand case — a mid-run
// queue-overflow eviction — and pins its Status to "dropped_overflow", the
// value documented on SessionMsgReceivedPayload.Status. This is distinct
// from drainSteerAtExit's "dropped_at_exit": the run is still going, so the
// two labels must never collapse into one string.
func TestEnqueueSteerOverflowEmitsDroppedOverflowStatus(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()

	ch := make(chan chat.InboundSessionMsg, 1)
	onDrop := func(old chat.InboundSessionMsg) {
		bus.Emit("worker", telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
			FromSessionID: old.FromSessionID, FromName: old.FromName,
			Text: chat.TruncateSessionMsg(old.Text), Status: "dropped_overflow",
		})
	}
	enqueueSteer(ch, chat.InboundSessionMsg{Text: "a"}, onDrop)
	enqueueSteer(ch, chat.InboundSessionMsg{Text: "b"}, onDrop) // queue cap 1 — evicts "a" mid-run

	var found bool
	for {
		select {
		case e := <-sub:
			if e.EventType == telemetry.EventSessionMsgReceived {
				var p telemetry.SessionMsgReceivedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.Status != "dropped_overflow" {
					t.Fatalf("status = %q, want dropped_overflow", p.Status)
				}
				found = true
			}
			continue
		default:
		}
		break
	}
	if !found {
		t.Fatal("SESSION_MSG_RECEIVED not emitted on overflow")
	}
}

func TestSteerHookWrapsAndEmits(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe() // buffered channel; Emit publishes into it synchronously

	ch := make(chan chat.InboundSessionMsg, 4)
	ch <- chat.InboundSessionMsg{FromSessionID: "cc-1", FromName: "claude", Text: "focus on X"}
	hook := steerHook(ch, bus, "worker")

	msgs := hook()
	if len(msgs) != 1 {
		t.Fatalf("drained %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].AsText(), "<session_message") ||
		!strings.Contains(msgs[0].AsText(), "focus on X") {
		t.Fatalf("bad wrapped message: %+v", msgs[0])
	}
	if hook() != nil {
		t.Fatal("empty queue must drain to nil")
	}
	if steerHook(nil, bus, "worker") != nil {
		t.Fatal("nil channel must produce nil hook")
	}

	found := false
	for {
		select {
		case e := <-sub:
			if e.EventType == telemetry.EventSessionMsgReceived {
				var p telemetry.SessionMsgReceivedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.Status != "injected" {
					t.Fatalf("status = %q, want injected", p.Status)
				}
				if p.FromSessionID != "cc-1" {
					t.Fatalf("from_session_id = %q, want cc-1", p.FromSessionID)
				}
				found = true
			}
			continue
		default:
		}
		break
	}
	if !found {
		t.Fatal("SESSION_MSG_RECEIVED not emitted on drain")
	}
}

func TestDrainSteerAtExitEmitsDroppedAndEmpties(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	sub := bus.Subscribe()

	ch := make(chan chat.InboundSessionMsg, 4)
	ch <- chat.InboundSessionMsg{FromSessionID: "cc-1", FromName: "claude", Text: "late one"}
	ch <- chat.InboundSessionMsg{FromSessionID: "cc-2", FromName: "other", Text: "later one"}

	drainSteerAtExit(ch, bus, "worker")

	if len(ch) != 0 {
		t.Fatalf("queue not emptied: %d left", len(ch))
	}
	var dropped int
	for {
		select {
		case e := <-sub:
			if e.EventType == telemetry.EventSessionMsgReceived {
				var p telemetry.SessionMsgReceivedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if p.Status != "dropped_at_exit" {
					t.Fatalf("status = %q, want dropped_at_exit", p.Status)
				}
				dropped++
			}
			continue
		default:
		}
		break
	}
	if dropped != 2 {
		t.Fatalf("dropped events = %d, want 2", dropped)
	}

	drainSteerAtExit(nil, bus, "worker") // nil channel must be a no-op, not a panic
}
