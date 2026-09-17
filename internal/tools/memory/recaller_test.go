package memory

import (
	"encoding/json"
	"strings"
	"testing"

	mem "github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func mustAdd(t *testing.T, store *mem.Store, n mem.Node) {
	t.Helper()
	if _, err := store.Add(n); err != nil {
		t.Fatalf("Add %q: %v", n.ID, err)
	}
}

// TestRecaller_RecallCascadeAndFormat: Recall returns the index-only block
// (same shape as memory_query) across the session->project->global cascade,
// with the matched node IDs, and emits an auto_recall MEMORY_RECALL event.
func TestRecaller_RecallCascadeAndFormat(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	bus := telemetry.NewEventBus(16)
	d := Deps{
		Store:        store,
		ProjectScope: mem.ProjectScope("proj"),
		SessionScope: mem.SessionScope("sess"),
		AgentName:    "tester",
		Bus:          bus,
	}
	mustAdd(t, store, mem.Node{Type: "fact", ID: "tz-fact", Title: "Timezone", Content: "user timezone is JST", Scope: d.ProjectScope})
	mustAdd(t, store, mem.Node{Type: "gotcha", ID: "embed-gotcha", Title: "Embed bundle", Content: "always make build-embedded", Scope: mem.ScopeGlobal})

	ch := bus.Subscribe()
	r := NewRecaller(d, 5, "", nil)
	block, ids := r.Recall("timezone JST")
	bus.Unsubscribe(ch)

	if block == "" {
		t.Fatal("expected a non-empty recall block")
	}
	if !strings.Contains(block, "tz-fact") {
		t.Errorf("block missing top hit tz-fact: %q", block)
	}
	if !strings.Contains(block, "index only") {
		t.Errorf("block should reuse the index-only format: %q", block)
	}
	if len(ids) == 0 || ids[0] != "tz-fact" {
		t.Errorf("ids = %v, want tz-fact first", ids)
	}

	var sawRecall bool
	for ev := range ch {
		if ev.EventType == telemetry.EventMemoryRecall {
			p := telemetry.MemoryRecallPayload{}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if p.Op == "auto_recall" && p.Query == "timezone JST" && p.ResultCount > 0 {
				sawRecall = true
			}
		}
	}
	if !sawRecall {
		t.Error("expected an EventMemoryRecall with Op=auto_recall")
	}
}

// TestRecaller_EmptyStoreNoBlockNoEvent: an empty store yields no block, no
// ids, and no recall event noise.
func TestRecaller_EmptyStoreNoBlockNoEvent(t *testing.T) {
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	bus := telemetry.NewEventBus(16)
	d := Deps{Store: store, ProjectScope: mem.ProjectScope("proj"), AgentName: "t", Bus: bus}

	ch := bus.Subscribe()
	block, ids := NewRecaller(d, 5, "", nil).Recall("anything")
	bus.Unsubscribe(ch)

	if block != "" || ids != nil {
		t.Errorf("empty store should yield (\"\", nil), got (%q, %v)", block, ids)
	}
	for ev := range ch {
		if ev.EventType == telemetry.EventMemoryRecall {
			t.Error("no recall event should fire for an empty result")
		}
	}
}

// TestRecaller_NilAndBlankGuards: nil receiver, nil store, and blank query are
// all no-ops.
func TestRecaller_NilAndBlankGuards(t *testing.T) {
	var nilR *Recaller
	if b, ids := nilR.Recall("q"); b != "" || ids != nil {
		t.Errorf("nil recaller should no-op, got (%q, %v)", b, ids)
	}

	store, _ := mem.NewStore(t.TempDir())
	mustAdd(t, store, mem.Node{Type: "fact", ID: "f", Title: "F", Content: "x", Scope: mem.ProjectScope("proj")})
	r := NewRecaller(Deps{Store: store, ProjectScope: mem.ProjectScope("proj")}, 5, "", nil)
	if b, _ := r.Recall("   "); b != "" {
		t.Errorf("blank query should no-op, got %q", b)
	}
}

// TestRecaller_TopKDefaultsAndNodeTypeFilter: topK<=0 defaults to 5 and the
// node-type filter restricts results.
func TestRecaller_NodeTypeFilter(t *testing.T) {
	store, _ := mem.NewStore(t.TempDir())
	proj := mem.ProjectScope("proj")
	mustAdd(t, store, mem.Node{Type: "fact", ID: "fact-tz", Title: "tz", Content: "timezone jst", Scope: proj})
	mustAdd(t, store, mem.Node{Type: "gotcha", ID: "gotcha-tz", Title: "tz gotcha", Content: "timezone jst pitfall", Scope: proj})

	r := NewRecaller(Deps{Store: store, ProjectScope: proj}, 0, "fact", nil)
	block, ids := r.Recall("timezone")
	if strings.Contains(block, "gotcha-tz") {
		t.Errorf("node_type=fact should exclude gotcha, got %q", block)
	}
	if len(ids) != 1 || ids[0] != "fact-tz" {
		t.Errorf("ids = %v, want [fact-tz]", ids)
	}
}

// TestRecaller_SkipsRetiredAndSuperseded: auto-recall must never inject
// stale knowledge — retired and superseded nodes stay out of the block.
func TestRecaller_SkipsRetiredAndSuperseded(t *testing.T) {
	store, _ := mem.NewStore(t.TempDir())
	proj := mem.ProjectScope("proj")
	mustAdd(t, store, mem.Node{Type: "fact", ID: "live", Title: "deploy target", Content: "deploy to production", Scope: proj})
	mustAdd(t, store, mem.Node{Type: "fact", ID: "retired", Title: "deploy target", Content: "deploy to the old vm", Scope: proj})
	if err := store.Retire(proj, "retired"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if _, err := store.AddSuperseding(mem.Node{Type: "fact", ID: "stale-v2", Title: "deploy target", Content: "deploy to staging first", Scope: proj}, "live"); err != nil {
		t.Fatalf("AddSuperseding: %v", err)
	}

	block, ids := NewRecaller(Deps{Store: store, ProjectScope: proj}, 5, "", nil).Recall("deploy target")
	if strings.Contains(block, "retired") || strings.Contains(block, "- [fact:live]") {
		t.Errorf("recall block must exclude retired/superseded nodes, got %q", block)
	}
	if len(ids) != 1 || ids[0] != "stale-v2" {
		t.Errorf("ids = %v, want only the current node stale-v2", ids)
	}
}
