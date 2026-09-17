package server

import (
	"encoding/json"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// TestHistoryForResumeFallsBackWhenChatJSONEmpty regression-guards:
// HistoryForResume only tried the JSONL event-log reconstruction when
// LoadChatTree itself failed. If it succeeded but produced a tree with zero
// messages — possible because persistTree's write is explicitly
// best-effort, only logged on failure — the function errored out
// immediately instead of falling back to the event log, which can still
// hold the real history (e.g. a crash between the JSONL events landing and
// the .chat.json write actually completing).
func TestHistoryForResumeFallsBackWhenChatJSONEmpty(t *testing.T) {
	ss := newHistoryTestStore(t)
	const id = "empty-envelope-1"
	persistJSONLFixture(t, id) // real history lives in the JSONL

	// Simulate a loadable-but-empty .chat.json for the same session — the
	// exact state a crash right after StartSession (before the first
	// turn's persistTree call) or a torn write can leave behind.
	empty, err := json.Marshal(ChatTreeFile{
		SchemaVersion: chatTreeSchemaVersion,
		SessionID:     id,
		Tree:          turntree.New[transcriptEntry](),
	})
	if err != nil {
		t.Fatalf("marshal empty ChatTreeFile: %v", err)
	}
	if err := ss.SaveChatTree(id, empty); err != nil {
		t.Fatalf("SaveChatTree: %v", err)
	}

	got, err := HistoryForResume(ss, id)
	if err != nil {
		t.Fatalf("HistoryForResume: %v — expected a fallback to the JSONL event log instead of an error", err)
	}
	wantHistory(t, got,
		llm.NewTextMessage("user", "first question"),
		llm.NewTextMessage("assistant", "first answer"),
		llm.NewTextMessage("user", "second question"),
		llm.NewTextMessage("assistant", "second answer"),
	)
}
