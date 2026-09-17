package server

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// HistoryForResume loads a persisted session's conversation as the
// user/assistant llm.Message pairs Agent.RunWithHistory expects. It is the
// CLI's resume loader: one-shot `run --resume` replays the parent session's
// turns into the model context instead of silently starting fresh. Source
// order mirrors resolvePersistedSession — the .chat.json
// envelope when present (active root→leaf path), else reconstruction from
// the JSONL event log. One-shot single-agent runs emit neither CHAT_TURN_*
// nor root PIPELINE_START events, so their single turn is recovered from
// SessionMeta.Query plus the root-level final answer (AGENT_END /
// EXECUTION_COMPLETE). Never returns empty history without an error: a
// resume that finds no conversation must fail loudly — silent fresh context
// is exactly the bug this exists to prevent.
func HistoryForResume(ss *store.SessionStore, sessionID string) ([]llm.Message, error) {
	if ss == nil {
		return nil, fmt.Errorf("session store is unavailable")
	}
	if raw, lerr := ss.LoadChatTree(sessionID); lerr == nil {
		file, perr := LoadChatTreeFile(raw)
		if perr != nil {
			return nil, perr
		}
		if msgs := historyFromTree(file.Tree); len(msgs) > 0 {
			return msgs, nil
		}
		// A loadable-but-empty tree happens when persistTree's best-effort
		// write never landed (e.g. a crash before the first turn's
		// persistTree call, or a torn write) — fall through to the event
		// log instead of failing immediately; it may hold the real
		// history. The hard error above stays for an actual parse/schema
		// failure (perr != nil), which this is not.
	}
	events, err := ss.GetSessionEvents(sessionID)
	if err != nil {
		return nil, err
	}
	if tree := reconstructTreeFromEvents(events); tree != nil {
		if msgs := historyFromTree(tree); len(msgs) > 0 {
			return msgs, nil
		}
	}
	// Best-effort meta: the sessions.json index can lose entries under
	// concurrent writers; the JSONL above is the source of truth and only
	// this one-shot fallback needs the meta line's Query.
	if meta, merr := ss.GetSession(sessionID); merr == nil {
		if msgs := oneShotHistory(meta, events); len(msgs) > 0 {
			return msgs, nil
		}
	}
	return nil, fmt.Errorf("no conversation history in session")
}

// oneShotHistory recovers the single turn of a one-shot single-agent run:
// user text from SessionMeta.Query, assistant text from the last root-level
// AGENT_END / EXECUTION_COMPLETE final answer. Both are required — a query
// with no answer is not a resumable conversation.
func oneShotHistory(meta *store.SessionMeta, events []telemetry.AgentEvent) []llm.Message {
	if meta == nil || meta.Query == "" {
		return nil
	}
	final := ""
	for _, e := range events {
		if e.ParentID != "" {
			continue // nested agent ends are not the run's answer
		}
		if e.EventType != telemetry.EventAgentEnd && e.EventType != telemetry.EventExecutionComplete {
			continue
		}
		var p telemetry.AgentEndPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil && p.FinalAnswer != "" {
			final = p.FinalAnswer
		}
	}
	if final == "" {
		return nil
	}
	return []llm.Message{
		llm.NewTextMessage("user", meta.Query),
		llm.NewTextMessage("assistant", final),
	}
}

// historyFromTree flattens a turn tree's active root→leaf path into
// user/assistant message pairs: one User from each node's UserText, one
// Assistant per assistant Entry. Reasoning, tool, and system entries are
// intentionally omitted — user/assistant pairs are the portable minimum.
func historyFromTree(tree *turntree.Tree[transcriptEntry]) []llm.Message {
	if tree == nil {
		return nil
	}
	path := tree.ActivePath()
	msgs := make([]llm.Message, 0, 2*len(path))
	for _, node := range path {
		if node.UserText != "" {
			msgs = append(msgs, llm.NewTextMessage("user", node.UserText))
		}
		for _, e := range node.Entries {
			if e.Kind == "assistant" && e.Text != "" {
				msgs = append(msgs, llm.NewTextMessage("assistant", e.Text))
			}
		}
	}
	return msgs
}

// reconstructTreeFromEvents rebuilds a chat session's turntree from its
// persisted JSONL event stream. Used as a fallback in the resume / tree /
// turn REST paths when a session has no .chat.json envelope on disk —
// typically because the session was created before PR B/3 introduced
// persistence, or was killed before its first turn finalized, or was a
// CLI `rakitsu run` invocation that never went through ChatSession.
//
// Result shape: a linear chain (no branches, no RetryCtx). Each user prompt
// produces a Node with UserText set; the matching final assistant answer
// is appended as a single transcriptEntry of kind "assistant" on that
// node's Entries slice. Reasoning, tool calls, and intermediate agent ends
// are intentionally omitted — they don't affect the resume contract
// (history = user+assistant pairs) and including them here would require
// the much more involved per-turn event grouping the frontend's
// reconstructChat.ts handles for display.
//
// Node IDs are deterministic ("recon-1", "recon-2", …): the /tree and
// /turn/{id} REST handlers each rebuild independently, so IDs must be
// stable across rebuilds of the same event stream or every per-turn
// lookup 404s.
//
// Returns nil when the event stream has no parseable turn — caller is
// expected to surface that as "no conversation to resume" rather than
// hand an empty tree to the runner.
//
// Two source formats, handled in a single chronological pass (mirrors
// reconstructChat.ts):
//
//	Modern (CHAT_TURN_START/END, added 2026-04-18): precise.
//	CHAT_TURN_START carries the user prompt verbatim; CHAT_TURN_END
//	carries the final assistant text and interrupted flag.
//
//	Legacy: root-level turn boundaries outside any open modern turn:
//	  - PIPELINE_START sometimes carries `query` — the text Runner.Run
//	    was called with, i.e. the user prompt for that turn.
//	  - AGENT_END / EXECUTION_COMPLETE → final_answer for the assistant
//	    block. Only the root-level (depth==0 / parent_id=="") instance
//	    is honored so nested agent ends don't get mistaken for replies.
//
// A single pass — rather than routing the whole stream to one format or the
// other based on whether any CHAT_TURN_START appears anywhere in it — is
// required because the two formats aren't mutually exclusive within one
// session's log: a session active since before the cutover has genuine
// legacy turns preceding its later modern ones, and a modern turn using the
// Pipeline orchestrator strategy emits its own PIPELINE_START/AGENT_END as
// internal telemetry NESTED inside that turn's own CHAT_TURN_START/END pair
// (chat_session.go's executeTurn always emits CHAT_TURN_START before
// invoking the runner, so this nesting order is guaranteed, never the
// reverse). Tracking whether a modern turn is currently open lets legacy
// events be recognized as real turn boundaries everywhere EXCEPT inside
// that window, where they're pipeline-internal noise for the turn already
// being tracked by CHAT_TURN_START/END.
func reconstructTreeFromEvents(events []telemetry.AgentEvent) *turntree.Tree[transcriptEntry] {
	if len(events) == 0 {
		return nil
	}
	tree := turntree.New[transcriptEntry]()
	var (
		parentID     string
		currentTurn  *turntree.Node[transcriptEntry]
		turnSeq      int
		insideModern bool
	)
	for _, e := range events {
		switch e.EventType {
		case telemetry.EventChatTurnStart:
			var p telemetry.ChatTurnStartPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil || p.Text == "" {
				continue
			}
			insideModern = true
			turnSeq++
			id := fmt.Sprintf("recon-%d", turnSeq)
			n, err := tree.AppendTurn(id, p.Text, parentID)
			if err != nil {
				continue
			}
			currentTurn = n
			parentID = id
			// Backdate Created so the timeline reads in order if the UI
			// sorts by it; otherwise every node would carry time.Now().
			currentTurn.Created = e.Timestamp

		case telemetry.EventChatTurnEnd:
			insideModern = false
			if currentTurn == nil {
				continue
			}
			var p telemetry.ChatTurnEndPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				continue
			}
			if p.Final != "" || p.Err != "" {
				currentTurn.Entries = append(currentTurn.Entries, transcriptEntry{
					Kind:        "assistant",
					Text:        p.Final,
					Interrupted: p.Interrupted,
					Err:         p.Err,
					Timestamp:   e.Timestamp.UTC().Format(time.RFC3339Nano),
				})
			}
			if p.Interrupted || p.Err != "" {
				currentTurn.Status = turntree.StatusInterrupted
			} else {
				currentTurn.Status = turntree.StatusComplete
			}
			currentTurn = nil

		case telemetry.EventPipelineStart:
			if insideModern || e.ParentID != "" {
				continue // pipeline-internal noise for the currently-open modern turn, or a nested pipeline
			}
			var p telemetry.PipelineStartPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil || p.Query == "" {
				continue
			}
			turnSeq++
			id := fmt.Sprintf("recon-%d", turnSeq)
			n, err := tree.AppendTurn(id, p.Query, parentID)
			if err != nil {
				continue
			}
			currentTurn = n
			currentTurn.Created = e.Timestamp
			parentID = id

		case telemetry.EventAgentEnd, telemetry.EventExecutionComplete:
			if insideModern || currentTurn == nil || e.ParentID != "" {
				continue // let CHAT_TURN_END close a modern turn instead; only root-level legacy ends contribute
			}
			var p telemetry.AgentEndPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil || p.FinalAnswer == "" {
				continue
			}
			currentTurn.Entries = append(currentTurn.Entries, transcriptEntry{
				Kind:      "assistant",
				Text:      p.FinalAnswer,
				AgentName: e.AgentName,
				Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			})
			if p.Status == "error" || p.Status == "max_iterations" {
				currentTurn.Status = turntree.StatusInterrupted
			} else {
				currentTurn.Status = turntree.StatusComplete
			}
			currentTurn = nil
		}
	}
	// Mark a still-open trailing turn interrupted (server crash mid-turn,
	// modern or legacy). Its assistant entry may be missing, which is
	// honest — Resume will let the user re-prompt or interrupt.
	if currentTurn != nil {
		currentTurn.Status = turntree.StatusInterrupted
	}
	if len(tree.Nodes) == 0 {
		return nil
	}
	return tree
}
