package chat

import (
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestBridgeConvertsAgentLifecycle(t *testing.T) {
	startEv := mkEvent(t, "Researcher-1", telemetry.EventAgentStart,
		telemetry.AgentStartPayload{Role: "worker", Model: "m1", ParentAgent: "Coordinator"})
	start := convertEvent(startEv)
	sm, ok := start.(AgentStartMsg)
	if !ok {
		t.Fatalf("AGENT_START converted to %T, want AgentStartMsg", start)
	}
	if sm.Name != "Researcher-1" || sm.Parent != "Coordinator" || sm.Model != "m1" {
		t.Errorf("AgentStartMsg = %+v", sm)
	}

	endEv := mkEvent(t, "Researcher-1", telemetry.EventAgentEnd,
		telemetry.AgentEndPayload{Status: "success", TotalTokens: 3000})
	endEv.Duration = 14100
	end := convertEvent(endEv)
	em, ok := end.(AgentEndMsg)
	if !ok {
		t.Fatalf("AGENT_END converted to %T, want AgentEndMsg", end)
	}
	if em.Name != "Researcher-1" || em.Status != "success" || em.Tokens != 3000 || em.Duration != 14100 {
		t.Errorf("AgentEndMsg = %+v", em)
	}

	// Parentless (root) AGENT_START still converts — the model decides display.
	rootStart := convertEvent(mkEvent(t, "ChatHost", telemetry.EventAgentStart,
		telemetry.AgentStartPayload{Role: "worker", Model: "m0"}))
	if rm, ok := rootStart.(AgentStartMsg); !ok || rm.Parent != "" {
		t.Errorf("root AGENT_START = %#v", rootStart)
	}
}

func TestAgentDepthWalksSpawnChain(t *testing.T) {
	m := Model{agentName: "Coordinator", spawnParents: map[string]string{
		"Researcher-1": "Coordinator",
		"Digger-1":     "Researcher-1",
	}}
	if d := m.agentDepth("Coordinator"); d != 0 {
		t.Errorf("root depth = %d, want 0", d)
	}
	if d := m.agentDepth("Researcher-1"); d != 1 {
		t.Errorf("child depth = %d, want 1", d)
	}
	if d := m.agentDepth("Digger-1"); d != 2 {
		t.Errorf("grandchild depth = %d, want 2", d)
	}
	// Unknown non-root agent falls back to the binary heuristic.
	if d := m.agentDepth("SomeWorker"); d != 1 {
		t.Errorf("fallback depth = %d, want 1", d)
	}
}

func TestSubagentBlockRender(t *testing.T) {
	running := ContentBlock{Type: BlockSubagent, AgentName: "Researcher-1", SubModel: "m1", Depth: 1}
	out := running.Render()
	if !strings.Contains(out, "Researcher-1") || !strings.Contains(out, "◐") {
		t.Errorf("running render = %q", out)
	}

	done := ContentBlock{Type: BlockSubagent, AgentName: "Researcher-1", SubDone: true,
		SubStatus: "success", SubTokens: 3000, Duration: 14100, Depth: 1}
	out = done.Render()
	if !strings.Contains(out, "✓") || !strings.Contains(out, "3.0K tok") || !strings.Contains(out, "14.1s") {
		t.Errorf("done render = %q", out)
	}

	failed := ContentBlock{Type: BlockSubagent, AgentName: "Researcher-1", SubDone: true,
		SubStatus: "timeout", Depth: 1}
	out = failed.Render()
	if !strings.Contains(out, "✗") || !strings.Contains(out, "timeout") {
		t.Errorf("failed render = %q", out)
	}
}

// TestUpdate_SpawnFanOutRendersNestedRows drives a full parallel-fan-out turn
// through Model.Update — no TTY required, Update is a pure function — the
// same way the real chat TUI would receive it from the bridge: two children
// spawned by the root, running concurrently, then finishing out of order.
// Asserts both children render as nested BlockSubagent rows with correct
// depth and that a finished-before-response turn leaves nothing "running".
func TestUpdate_SpawnFanOutRendersNestedRows(t *testing.T) {
	m := Model{
		agentName:    "Coordinator",
		generating:   true,
		spawnParents: make(map[string]string),
		blocks: []ContentBlock{
			{Type: BlockUser, Text: "research BM25 and HNSW"},
			{Type: BlockAssistant},
		},
	}

	m = step(t, m, AgentStartMsg{Name: "Coordinator", Parent: "", Model: "root-model"}) // root's own start — must be dropped
	m = step(t, m, AgentStartMsg{Name: "BM25_Researcher-1", Parent: "Coordinator", Model: "m1"})
	m = step(t, m, AgentStartMsg{Name: "HNSW_Researcher-1", Parent: "Coordinator", Model: "m1"})
	// Finish out of order (HNSW first) — completion must match by name, not position.
	m = step(t, m, AgentEndMsg{Name: "HNSW_Researcher-1", Status: "success", Tokens: 2800, Duration: 6100})
	m = step(t, m, AgentEndMsg{Name: "BM25_Researcher-1", Status: "success", Tokens: 3000, Duration: 3800})
	m = step(t, m, TokenChunkMsg{Text: "Combined summary..."})

	got := blockTypes(m.blocks)
	want := []BlockType{BlockUser, BlockSubagent, BlockSubagent, BlockAssistant}
	if len(got) != len(want) {
		t.Fatalf("block stream = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block[%d] = %v, want %v (full stream %v)", i, got[i], want[i], got)
		}
	}

	byName := map[string]ContentBlock{}
	for _, b := range m.blocks {
		if b.Type == BlockSubagent {
			byName[b.AgentName] = b
		}
	}
	bm25, hnsw := byName["BM25_Researcher-1"], byName["HNSW_Researcher-1"]
	if !bm25.SubDone || bm25.SubTokens != 3000 || bm25.Depth != 1 {
		t.Errorf("BM25_Researcher-1 = %+v", bm25)
	}
	if !hnsw.SubDone || hnsw.SubTokens != 2800 || hnsw.Depth != 1 {
		t.Errorf("HNSW_Researcher-1 = %+v", hnsw)
	}
	if m.spawnParents["BM25_Researcher-1"] != "Coordinator" || m.spawnParents["HNSW_Researcher-1"] != "Coordinator" {
		t.Errorf("spawnParents = %+v", m.spawnParents)
	}

	// Turn ends normally — no row should still read as running.
	m = step(t, m, AgentDoneMsg{Token: m.genToken, Response: "Combined summary..."})
	for _, b := range m.blocks {
		if b.Type == BlockSubagent && !b.SubDone {
			t.Errorf("subagent %q still running after AgentDoneMsg", b.AgentName)
		}
	}
}
