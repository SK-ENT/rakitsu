package agentchat

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// truncationMarker is inserted where transcript history was dropped, so a
// truncated conversation never reads as a complete one.
const truncationMarker = "[%d earlier turns dropped to stay within the transcript budget]"

// RecordTranscript stores an agent's settled conversation, replacing any
// prior one. Called from the transcript sink, which fires on the agent's own
// goroutine as its run returns — before the tool registry is closed. An agent
// that was never registered is recorded as a spawned child rather than
// dropped: the sink firing is proof the agent existed.
//
// runErr is the run's own outcome and decides the recorded status (see
// statusFor). The sink fires on EVERY exit path, so without it a child that
// hit settings.spawn.timeout_seconds, a provider error, or its own
// max_iterations budget was stored as StatusDone and offered to the user as
// "done — N turns". The transcript is still retained in all three cases — the
// run you most want to ask about is the one that did not finish — but it is
// labelled honestly.
//
// Turns is counted from the full, pre-truncation history: it answers "how
// many times was this agent addressed", a fact about the conversation that
// does not shrink just because the stored transcript later gets trimmed for
// space. A byte-truncated entry and a fully evicted one (Turns == 0,
// StatusEvicted) stay distinguishable this way — truncation never reports a
// smaller turn count than what actually happened.
//
// The history is copied before storage, so the caller mutating its own slice
// (or its backing array) afterward can never reach into what the roster
// retains.
func (r *Roster) RecordTranscript(name string, history []llm.Message, runErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e := r.ensureLocked(name, KindSpawned, "", "")
	e.Status = statusFor(runErr)
	e.Turns = countTurns(history)
	stored := trimDanglingToolCalls(cloneHistory(history))
	e.history = truncateHistory(stored, r.cfg.EffectiveMaxTranscriptBytes())
	r.seq++
	e.finished = r.seq

	r.evictLocked()
}

// incompleteRun is implemented by an error reporting a run that stopped
// without failing — today only agent.ErrMaxIterations. Declared as a
// behaviour, not an import, so this package keeps its one-way independence
// from internal/agent (the same reason HistoryRunner exists).
type incompleteRun interface{ IncompleteRun() bool }

// statusFor maps a run's own outcome to the status the picker shows. All three
// states are distinct on purpose: "done" claims the agent answered, "failed"
// claims something went wrong, and neither is true of a run that simply used
// up its iteration budget.
func statusFor(runErr error) Status {
	if runErr == nil {
		return StatusDone
	}
	var inc incompleteRun
	if errors.As(runErr, &inc) && inc.IncompleteRun() {
		return StatusIncomplete
	}
	return StatusFailed
}

// ClearTranscript drops an agent's retained transcript so the next Send
// starts from an empty history. Backs /clear inside a side thread: the blocks
// on screen are only the rendering — the conversation the agent will actually
// replay lives here, so clearing one without the other leaves the user
// looking at an empty thread that still remembers everything.
//
// Status is deliberately left alone: an agent that ran did run, and rewriting
// that to "not run yet" would be a fresh false claim in place of the one this
// fixes. Turns drops to 0 because there is no longer a transcript for it to
// count. An unknown name is a no-op.
func (r *Roster) ClearTranscript(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[name]; ok {
		e.history = nil
		e.Turns = 0
	}
}

// cloneHistory returns a copy of history backed by its own array, so storing
// the result can never be mutated through the caller's own copy of the
// slice. Preserves nil vs. empty: a nil transcript stays nil.
func cloneHistory(history []llm.Message) []llm.Message {
	if history == nil {
		return nil
	}
	out := make([]llm.Message, len(history))
	copy(out, history)
	return out
}

// evictLocked drops the oldest finished transcripts until the retained count
// is within max_retained. The Entry survives with StatusEvicted — the agent
// stays listed, labelled, and never silently disappears. The host is exempt:
// its conversation lives on the chat surface, not here. Caller holds r.mu.
func (r *Roster) evictLocked() {
	limit := r.cfg.EffectiveMaxRetained()
	for {
		var oldestName string
		var oldestSeq int64
		retained := 0
		for name, e := range r.entries {
			if e.Kind == KindHost || e.history == nil {
				continue
			}
			retained++
			if oldestName == "" || e.finished < oldestSeq {
				oldestName, oldestSeq = name, e.finished
			}
		}
		if retained <= limit || oldestName == "" {
			return
		}
		victim := r.entries[oldestName]
		victim.history = nil
		victim.Status = StatusEvicted
		victim.Turns = 0
	}
}

// countTurns counts user messages, which is how many times someone addressed
// the agent — the number a picker row should show. Run this over the full
// transcript before any byte-truncation; the retained-bytes budget is a
// storage concern, not a fact about how many turns actually happened.
func countTurns(history []llm.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// historyBytes is the total size of a transcript.
func historyBytes(history []llm.Message) int {
	n := 0
	for _, m := range history {
		n += messageBytes(m)
	}
	return n
}

// messageBytes is one message's contribution to the transcript budget: its
// content plus the tool-call payload it carries. A ReAct transcript's
// assistant messages routinely have empty Content and kilobytes of tool
// arguments, so counting Content alone measures the largest part of a
// tool-using conversation as free and lets max_transcript_bytes overshoot by
// orders of magnitude. Arguments are measured as the JSON they will be sent
// as; an unmarshalable value contributes only its keys rather than crashing a
// budget calculation.
func messageBytes(m llm.Message) int {
	n := len(m.ToolCallID)
	for _, b := range m.Content {
		switch b.Type {
		case llm.ContentTypeText:
			n += len(b.Text)
		default:
			// Non-text blocks (tool-returned images) count by their
			// payload, so a multi-MB image cannot slip past the transcript
			// budget as if it were a few bytes.
			n += len(b.MIMEType) + 64
			if b.Source != nil {
				n += len(b.Source.Base64) + len(b.Source.URL) + len(b.Source.FileID)
			}
		}
	}
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Name)
		if b, err := json.Marshal(tc.Arguments); err == nil {
			n += len(b)
		}
	}
	return n
}

// trimDanglingToolCalls drops a trailing assistant message whose tool_calls
// were never answered, along with the partial results that did arrive. That
// is the shape a run leaves behind when it dies between issuing a call and
// recording its result — a debug abort, a cancelled context, a provider
// error. Replaying it is an immediate provider 400 (the same failure a
// front-truncated orphan causes), so it is cut before storage rather than
// retained as an unusable transcript. Everything before it is a complete
// exchange and is kept. Repeats until the tail is clean, so a run that died
// two calls deep is trimmed back to its last settled turn.
func trimDanglingToolCalls(history []llm.Message) []llm.Message {
	for {
		// Walk back over the trailing run of tool results and collect which
		// calls they answer.
		i := len(history)
		answered := make(map[string]bool)
		for i > 0 && history[i-1].Role == "tool" {
			i--
			answered[history[i].ToolCallID] = true
		}
		if i == 0 || len(history[i-1].ToolCalls) == 0 {
			return history
		}
		complete := true
		for _, tc := range history[i-1].ToolCalls {
			if !answered[tc.ID] {
				complete = false
				break
			}
		}
		if complete {
			return history
		}
		history = history[:i-1]
	}
}

// truncateHistory brings a transcript's *stored* size toward maxBytes by
// dropping messages from the front and keeping the most recent ones, while
// leaving a leading system prompt untouched — always, in full, even when the
// prompt alone meets or exceeds maxBytes. This is deliberate: an agent
// rebuilt without its own system prompt is not the same agent, so mangling
// or dropping it to satisfy a byte budget would be the wrong trade. That
// means the result can legitimately come back larger than maxBytes: head
// plus whatever recent messages still fit (possibly none). A marker naming
// how many turns were dropped is appended only when messages were actually
// dropped — an oversized head with nothing else to drop gets no marker,
// since "N turns dropped" would not be true. A transcript already within
// budget is returned unchanged.
func truncateHistory(history []llm.Message, maxBytes int) []llm.Message {
	if historyBytes(history) <= maxBytes {
		return history
	}

	var head []llm.Message
	rest := history
	if len(history) > 0 && history[0].Role == "system" {
		head = []llm.Message{history[0]}
		rest = history[1:]
	}
	headBytes := historyBytes(head)

	// Reserve room for a marker sized for the worst case — every remaining
	// message dropped — so the marker actually appended below (whose count
	// can only be smaller or equal) never pushes the total past maxBytes on
	// its own. A fixed reservation based on a 1-digit count would undercount
	// once the real drop count runs to more digits.
	worstCaseMarker := fmt.Sprintf(truncationMarker, len(rest))
	budget := maxBytes - headBytes - len(worstCaseMarker)
	if budget < 0 {
		budget = 0
	}

	// Walk backwards keeping the most recent messages that fit.
	keep := 0
	used := 0
	for i := len(rest) - 1; i >= 0; i-- {
		if used+messageBytes(rest[i]) > budget {
			break
		}
		used += messageBytes(rest[i])
		keep++
	}
	dropped := len(rest) - keep

	// Never cut between an assistant's tool_calls and the tool results that
	// answer them. A ReAct transcript is assistant{ToolCalls:[call_N]} then
	// tool{ToolCallID:call_N}; a byte-count-only cut lands between the two and
	// leaves the retained tail starting on an orphan tool result, which
	// providers forward verbatim and reject with HTTP 400. Since the stored
	// transcript would be the corrupted one, that bricks the agent's thread
	// for the rest of the session. Advancing the cut to the next safe boundary
	// drops more messages, never fewer, so the byte budget still holds and the
	// marker below still names the real count.
	for dropped < len(rest) && rest[dropped].Role == "tool" {
		dropped++
	}

	out := make([]llm.Message, 0, len(head)+keep+1)
	out = append(out, head...)
	if dropped > 0 {
		out = append(out, llm.NewTextMessage("system", fmt.Sprintf(truncationMarker, dropped)))
	}
	out = append(out, rest[dropped:]...)
	return out
}
