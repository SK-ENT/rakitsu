package agentchat

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

func msgs(contents ...string) []llm.Message {
	out := make([]llm.Message, 0, len(contents))
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, llm.NewTextMessage(role, c))
	}
	return out
}

// TestEvictionDropsOldestFinishedFirst pins eviction to finish order, not
// creation order. Creation is A, B, C but finishing is deliberately C, A, B
// so the two orders diverge: an implementation that evicted by insertion
// order would pick A (created first); the real spec is "oldest finished"
// (C-1, which finished first), so C-1 must be the one evicted. Verified by
// mutation: temporarily making evictLocked pick oldest-by-insertion instead
// of oldest-by-finish-sequence flips this test to FAIL (see task-4-report.md
// for the red/green transcript).
func TestEvictionDropsOldestFinishedFirst(t *testing.T) {
	r := New(config.AgentChatConfig{MaxRetained: 2}, nil, nil)
	for _, name := range []string{"A-1", "B-1", "C-1"} {
		r.MarkRunning(name, KindSpawned, "m", "p")
	}
	for _, name := range []string{"C-1", "A-1", "B-1"} {
		r.RecordTranscript(name, msgs("q", "a"), nil)
	}
	byName := map[string]Entry{}
	for _, e := range r.List() {
		byName[e.Name] = e
	}
	if byName["C-1"].Status != StatusEvicted {
		t.Errorf("C-1 status = %q, want %q (oldest finished evicts first, not oldest created)", byName["C-1"].Status, StatusEvicted)
	}
	if byName["A-1"].Status != StatusDone || byName["B-1"].Status != StatusDone {
		t.Errorf("A-1=%q B-1=%q, both want %q", byName["A-1"].Status, byName["B-1"].Status, StatusDone)
	}
}

func TestEvictedEntryIsStillListed(t *testing.T) {
	r := New(config.AgentChatConfig{MaxRetained: 1}, nil, nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", msgs("q", "a"), nil)
	r.MarkRunning("B-1", KindSpawned, "m", "p")
	r.RecordTranscript("B-1", msgs("q", "a"), nil)

	if len(r.List()) != 2 {
		t.Fatalf("List() = %d entries, want 2 — an evicted agent must never vanish", len(r.List()))
	}
	if _, ok := r.LastReply("A-1"); ok {
		t.Error("evicted agent should have no retained reply")
	}
}

func TestMaxRetainedZeroRetainsNothing(t *testing.T) {
	r := New(config.AgentChatConfig{MaxRetained: retainNothingConfigValue}, nil, nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", msgs("q", "a"), nil)
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() = %d, want 1", len(got))
	}
	if got[0].Status != StatusEvicted {
		t.Errorf("status = %q, want %q when max_retained is 0", got[0].Status, StatusEvicted)
	}
}

func TestEntryCapDropsOldestAndCounts(t *testing.T) {
	r := New(config.AgentChatConfig{MaxRetained: 1000}, nil, nil)
	for i := 0; i < maxEntries+5; i++ {
		r.MarkRunning(spawnName(i), KindSpawned, "m", "p")
	}
	if got := len(r.List()); got != maxEntries {
		t.Errorf("List() = %d entries, want %d (hard entry cap)", got, maxEntries)
	}
	if got := r.Dropped(); got != 5 {
		t.Errorf("Dropped() = %d, want 5", got)
	}
}

func spawnName(i int) string {
	return "child-" + string(rune('a'+i%26)) + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestTruncateKeepsSystemPromptAndRecentTurns(t *testing.T) {
	long := strings.Repeat("x", 400)
	in := []llm.Message{
		llm.NewTextMessage("system", "SYSTEM PROMPT"),
		llm.NewTextMessage("user", long),
		llm.NewTextMessage("assistant", long),
		llm.NewTextMessage("user", "recent question"),
		llm.NewTextMessage("assistant", "recent answer"),
	}
	out := truncateHistory(in, 600)

	if out[0].Role != "system" || out[0].AsText() != "SYSTEM PROMPT" {
		t.Fatalf("system prompt not preserved: %+v", out[0])
	}
	if out[len(out)-1].AsText() != "recent answer" {
		t.Errorf("most recent message not preserved: %+v", out[len(out)-1])
	}
	var sawMarker bool
	for _, m := range out {
		if strings.Contains(m.AsText(), "earlier turns dropped") {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Error("truncation must leave a marker naming what was dropped")
	}
	if historyBytes(out) > 600 {
		t.Errorf("truncated history is %d bytes, over the 600 budget", historyBytes(out))
	}
}

func TestTruncateUnderBudgetIsUnchanged(t *testing.T) {
	in := msgs("short", "reply")
	out := truncateHistory(in, 100000)
	if len(out) != len(in) {
		t.Errorf("history under budget was modified: %d -> %d", len(in), len(out))
	}
}

// TestTruncateEmptyInput covers Finding 1's first explicit case: nil and
// empty transcripts must pass straight through, no panic, no marker.
func TestTruncateEmptyInput(t *testing.T) {
	if out := truncateHistory(nil, 100); len(out) != 0 {
		t.Errorf("truncateHistory(nil, 100) = %+v, want empty", out)
	}
	if out := truncateHistory([]llm.Message{}, 100); len(out) != 0 {
		t.Errorf("truncateHistory([], 100) = %+v, want empty", out)
	}
	// Even a zero budget must not panic on empty input.
	if out := truncateHistory(nil, 0); len(out) != 0 {
		t.Errorf("truncateHistory(nil, 0) = %+v, want empty", out)
	}
}

// TestTruncateSingleMessageLargerThanBudget covers Finding 1's second
// explicit case: no system prompt, one message that alone blows the budget.
// It doesn't fit, so it is dropped entirely and replaced by a marker that
// itself respects the budget.
func TestTruncateSingleMessageLargerThanBudget(t *testing.T) {
	in := []llm.Message{llm.NewTextMessage("user", strings.Repeat("x", 1000))}
	out := truncateHistory(in, 100)

	if historyBytes(out) > 100 {
		t.Errorf("truncated history is %d bytes, over the 100 budget", historyBytes(out))
	}
	if len(out) != 1 || !strings.Contains(out[0].AsText(), "earlier turns dropped") {
		t.Errorf("out = %+v, want a single drop marker naming 1 turn dropped", out)
	}
	if !strings.Contains(out[0].AsText(), "[1 ") {
		t.Errorf("marker = %q, want it to name exactly 1 dropped turn", out[0].AsText())
	}
}

// TestTruncateBudgetSmallerThanSystemPrompt covers Finding 1's third
// explicit case and reproduces the reviewer's exact repro: a 1000-byte
// system prompt with maxBytes=100 and nothing else in the transcript. The
// system prompt must survive whole and unmangled — the doc comment says so
// deliberately — and since there was nothing else to drop, no truncation
// marker may be fabricated on top of it. Before the fix this produced 1062
// bytes (head + a marker claiming a drop that never happened); the fix must
// produce exactly the 1000-byte head, nothing more.
func TestTruncateBudgetSmallerThanSystemPrompt(t *testing.T) {
	in := []llm.Message{llm.NewTextMessage("system", strings.Repeat("s", 1000))}
	out := truncateHistory(in, 100)

	if len(out) != 1 {
		t.Fatalf("out = %+v, want exactly the head, no marker (nothing was dropped)", out)
	}
	if out[0].Role != "system" || out[0].AsText() != in[0].AsText() {
		t.Errorf("system prompt was mangled: %+v", out[0])
	}
	if historyBytes(out) != 1000 {
		t.Errorf("historyBytes(out) = %d, want exactly 1000 (head only, no fabricated marker)", historyBytes(out))
	}
}

// TestTruncateMarkerReservationCoversWideDropCounts is the Finding 2 repro:
// with the count-0 marker length used to reserve budget, 300 one-byte
// messages at maxBytes=100 overshot to 102 bytes because the real marker
// ("[300 ...]") is wider than the reserved ("[0 ...]") one. The fix reserves
// for the worst case up front, so the actual output must never exceed
// maxBytes.
func TestTruncateMarkerReservationCoversWideDropCounts(t *testing.T) {
	contents := make([]string, 300)
	for i := range contents {
		contents[i] = "x"
	}
	in := msgs(contents...)
	out := truncateHistory(in, 100)

	if historyBytes(out) > 100 {
		t.Errorf("truncated history is %d bytes, over the 100 budget (marker reservation too narrow)", historyBytes(out))
	}
}

// TestRecordTranscriptTurnsSurviveByteTruncation is the Finding 3 repro: a
// picker row reading "N turns retained" must not fabricate a small number
// just because the byte budget shrank the *stored* transcript. Turns is a
// fact about how many times the agent was addressed, counted from the full
// history, independent of what physically survives truncation.
func TestRecordTranscriptTurnsSurviveByteTruncation(t *testing.T) {
	history := []llm.Message{llm.NewTextMessage("system", "SYSTEM")}
	for i := 0; i < 50; i++ {
		history = append(history,
			llm.NewTextMessage("user", strings.Repeat("q", 200)),
			llm.NewTextMessage("assistant", strings.Repeat("a", 200)),
		)
	}

	r := New(config.AgentChatConfig{MaxTranscriptBytes: 600}, nil, nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", history, nil)

	got := r.List()[0]
	if got.Turns != 50 {
		t.Errorf("Turns = %d, want 50 (the true count; the stored transcript was budget-truncated, the fact was not)", got.Turns)
	}
	if got.Status != StatusDone {
		t.Errorf("Status = %q, want %q — truncation for budget is not eviction", got.Status, StatusDone)
	}
}

// TestRecordTranscriptCopiesCallerSlice is the Finding 5 repro: when a
// transcript is under budget, truncateHistory returns its input unchanged,
// so without a defensive copy the roster would retain the caller's own
// backing array. Mutating the caller's slice after RecordTranscript returns
// must not be visible through the roster.
func TestRecordTranscriptCopiesCallerSlice(t *testing.T) {
	in := []llm.Message{
		llm.NewTextMessage("user", "q"),
		llm.NewTextMessage("assistant", "original reply"),
	}
	r := New(config.AgentChatConfig{}, nil, nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", in, nil)

	in[1] = llm.NewTextMessage(in[1].Role, "mutated by caller after recording")

	reply, ok := r.LastReply("A-1")
	if !ok || reply != "original reply" {
		t.Errorf("LastReply = (%q, %v), want (\"original reply\", true) — roster must not alias the caller's slice", reply, ok)
	}
}

// toolPairingViolation reports the first place a transcript breaks the
// provider's tool-call pairing contract: a role:"tool" message with no
// preceding assistant tool_calls that claims it, or a trailing assistant
// tool_call with no result. Both are an immediate HTTP 400 on replay.
func toolPairingViolation(history []llm.Message) string {
	open := map[string]bool{}
	for i, m := range history {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				open[tc.ID] = true
			}
		case "tool":
			if !open[m.ToolCallID] {
				return "orphan tool result at index " + itoa(i) + " (tool_call_id=" + m.ToolCallID + ")"
			}
			delete(open, m.ToolCallID)
		}
	}
	for id := range open {
		return "unanswered tool call " + id + " at the end of the transcript"
	}
	return ""
}

// TestTruncateNeverOrphansToolResults is the C2 repro. A ReAct transcript is
// assistant{ToolCalls:[call_N]} followed by tool{ToolCallID:call_N}; a
// byte-count-only cut lands between the two and leaves the retained tail
// starting on an orphan tool result. The provider forwards ToolCallID
// verbatim and returns 400, and because the corrupted transcript is the one
// stored, every later Send to that agent fails identically — the thread is
// dead for the session.
func TestTruncateNeverOrphansToolResults(t *testing.T) {
	big := strings.Repeat("x", 200)
	in := []llm.Message{
		llm.NewTextMessage("system", "SYS"),
		{Role: "assistant", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: big}}, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "peek"}}},
		{Role: "tool", ToolCallID: "call_1", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: big}}},
		{Role: "assistant", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: big}}, ToolCalls: []llm.ToolCall{{ID: "call_2", Name: "peek"}}},
		{Role: "tool", ToolCallID: "call_2", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "tool result 2"}}},
		llm.NewTextMessage("assistant", "done"),
	}
	out := truncateHistory(in, 100)

	if v := toolPairingViolation(out); v != "" {
		t.Errorf("truncated transcript is not replayable: %s\nout = %+v", v, out)
	}
	var sawMarker bool
	for _, m := range out {
		if strings.Contains(m.AsText(), "earlier turns dropped") {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Error("truncation must still say what it dropped")
	}
}

// TestRecordTranscriptDropsDanglingToolCall is C2 from the other direction
// (the I1 corollary): a run that died between issuing a tool call and
// recording its result hands the sink a history ending on an unanswered
// tool_calls message. Storing that verbatim means the next Send replays an
// invalid history and gets the same 400.
func TestRecordTranscriptDropsDanglingToolCall(t *testing.T) {
	r := New(config.AgentChatConfig{}, nil, nil)
	r.MarkRunning("A-1", KindSpawned, "m", "p")
	r.RecordTranscript("A-1", []llm.Message{
		llm.NewTextMessage("user", "go"),
		{Role: "assistant", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "reading"}}, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "peek"}}},
	}, nil)

	if v := toolPairingViolation(r.entries["A-1"].history); v != "" {
		t.Errorf("stored transcript is not replayable: %s\nhistory = %+v", v, r.entries["A-1"].history)
	}
}

// TestHistoryBytesCountsToolCallPayloads is the M1 repro: an assistant
// message carrying 100 KB of tool arguments has an empty Content, so a
// Content-only sum measures it as free and max_transcript_bytes does not
// bound what it claims to bound.
func TestHistoryBytesCountsToolCallPayloads(t *testing.T) {
	big := strings.Repeat("x", 100000)
	in := []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "call_1", Name: "write_file", Arguments: map[string]interface{}{"content": big}},
	}}}
	if got := historyBytes(in); got < 100000 {
		t.Errorf("historyBytes = %d, want >= 100000 — the tool-call payload is part of the transcript", got)
	}
}

// TestRecordTranscriptMarksMaxIterationsIncomplete: a child that used up its
// iteration budget did not finish, and "done — 3 turns" in the picker is a
// guess presented as fact. It is not a failure either — nothing errored — so
// it gets its own state rather than being flattened into either neighbour.
func TestRecordTranscriptMarksMaxIterationsIncomplete(t *testing.T) {
	r := New(config.AgentChatConfig{}, nil, nil)
	r.RecordTranscript("Researcher-1", []llm.Message{
		llm.NewTextMessage("user", "find it"),
		llm.NewTextMessage("assistant", "[max_iterations reached — partial result]"),
	}, agent.ErrMaxIterations)

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("roster = %+v, want one entry", list)
	}
	if list[0].Status != StatusIncomplete {
		t.Errorf("Status = %q, want %q — a run that ran out of iterations neither finished nor failed",
			list[0].Status, StatusIncomplete)
	}
	if list[0].Turns != 1 {
		t.Errorf("Turns = %d, want 1 — the turns it did take are real", list[0].Turns)
	}
}
