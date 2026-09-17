package main

import (
	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Steerable one-shot plumbing (spec §5): OnCommand enqueues inbound
// session_message commands here; the agent loop drains via steerHook at
// iteration boundaries. Bounded drop-oldest so a flooding sender can only
// displace its own older steers, never block the hub poll goroutine.

const steerQueueCap = 16

// enqueueSteer adds m, evicting the oldest queued message when full.
// onDrop observes evictions (telemetry); it must not block.
// Correctness of the drop-then-retry loop assumes a single producer goroutine
// (the hub client's serial pollCommands loop) — with concurrent producers the
// retry could starve. Do not call from multiple goroutines.
func enqueueSteer(ch chan chat.InboundSessionMsg, m chat.InboundSessionMsg, onDrop func(chat.InboundSessionMsg)) {
	if ch == nil {
		return // steering disabled — nothing to queue, onDrop never fires
	}
	for {
		select {
		case ch <- m:
			return
		default:
			select {
			case old := <-ch:
				if onDrop != nil {
					onDrop(old)
				}
			default:
			}
		}
	}
}

// steerHook adapts the queue to the agent.SetSteering contract: drain
// whatever is queued right now, emit SESSION_MSG_RECEIVED(injected) per
// message, return them fenced as user-role messages. Nil channel = nil
// hook so untouched runs pay nothing.
func steerHook(ch chan chat.InboundSessionMsg, bus *telemetry.EventBus, agentName string) func() []llm.Message {
	if ch == nil {
		return nil
	}
	return func() []llm.Message {
		var out []llm.Message
		for {
			select {
			case m, ok := <-ch:
				if !ok {
					return out // channel closed — nothing left to drain
				}
				bus.Emit(agentName, telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
					FromSessionID: m.FromSessionID,
					FromName:      m.FromName,
					Text:          chat.TruncateSessionMsg(m.Text),
					Status:        "injected",
				})
				out = append(out, llm.NewTextMessage("user", chat.WrapSessionMessage(m.Text, m.FromSessionID, m.FromName)))
			default:
				return out
			}
		}
	}
}

// rootRunnerName is the event lane steering telemetry lands in: the
// orchestrator when one exists, else the first (only-used) agent.
func rootRunnerName(cfg *config.Config) string {
	if cfg.Orchestrator != nil {
		return cfg.Orchestrator.Name
	}
	if len(cfg.Agents) > 0 {
		return cfg.Agents[0].Name
	}
	return "run"
}

// drainSteerAtExit reports queued-but-never-drained messages when the run
// ends (spec §6 known limit) — the sender's message was accepted but the
// loop finished first; the event makes that visible instead of silent.
func drainSteerAtExit(ch chan chat.InboundSessionMsg, bus *telemetry.EventBus, agentName string) {
	if ch == nil {
		return
	}
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return // channel closed — nothing left to drain
			}
			bus.Emit(agentName, telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
				FromSessionID: m.FromSessionID,
				FromName:      m.FromName,
				Text:          chat.TruncateSessionMsg(m.Text),
				Status:        "dropped_at_exit",
			})
		default:
			return
		}
	}
}
