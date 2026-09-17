package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mem "github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func newDeps(t *testing.T) Deps {
	t.Helper()
	store, err := mem.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return Deps{
		Store:        store,
		ProjectScope: mem.ProjectScope("testproj"),
		AgentName:    "tester",
		// Bus nil: exercises the nil-guard on event emission.
	}
}

func toolByName(t *testing.T, d Deps, name string) interface {
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
} {
	t.Helper()
	for _, tool := range NewTools(d) {
		if tool.GetName() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestToolSetNamesAndSchemas(t *testing.T) {
	d := newDeps(t)
	want := map[string]bool{
		"memory_add": true, "memory_query": true, "memory_get": true,
		"memory_list": true, "memory_link": true, "memory_retire": true,
	}
	for _, tool := range NewTools(d) {
		if !want[tool.GetName()] {
			t.Errorf("unexpected tool %q", tool.GetName())
		}
		delete(want, tool.GetName())
		if tool.GetDescription() == "" {
			t.Errorf("%s: empty description", tool.GetName())
		}
		schema := tool.GetParametersSchema()
		if schema["type"] != "object" {
			t.Errorf("%s: schema type = %v", tool.GetName(), schema["type"])
		}
	}
	if len(want) != 0 {
		t.Errorf("missing tools: %v", want)
	}
}

func TestAddQueryGetFlow(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()

	out, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "gotcha", "title": "Embed serves stale bundle",
		"content": "always make build-embedded, never bare go build",
		"tags":    "embed, build",
	})
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	if !strings.Contains(out, "Stored") || !strings.Contains(out, "embed-serves-stale-bundle") {
		t.Errorf("add output = %q", out)
	}

	// Default scope is the project scope.
	if _, err := d.Store.Get(d.ProjectScope, "embed-serves-stale-bundle"); err != nil {
		t.Fatalf("node not in project scope: %v", err)
	}

	out, err = toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "stale embed bundle build",
	})
	if err != nil {
		t.Fatalf("memory_query: %v", err)
	}
	if !strings.Contains(out, "embed-serves-stale-bundle") {
		t.Errorf("query output = %q", out)
	}

	out, err = toolByName(t, d, "memory_get").Execute(ctx, map[string]interface{}{
		"id": "embed-serves-stale-bundle",
	})
	if err != nil {
		t.Fatalf("memory_get: %v", err)
	}
	if !strings.Contains(out, "gotcha") || !strings.Contains(out, "build-embedded") {
		t.Errorf("get output = %q", out)
	}
}

func TestAddUpdateWording(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	add := toolByName(t, d, "memory_add")
	args := map[string]interface{}{
		"type": "fact", "id": "x", "title": "X", "content": "v1",
	}
	if out, _ := add.Execute(ctx, args); !strings.Contains(out, "Stored") {
		t.Errorf("first add: %q", out)
	}
	args["content"] = "v2"
	if out, _ := add.Execute(ctx, args); !strings.Contains(out, "Updated") {
		t.Errorf("second add: %q", out)
	}
}

func TestGlobalScopeArg(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "rule", "title": "Global rule", "content": "applies everywhere", "scope": "global",
	}); err != nil {
		t.Fatalf("add global: %v", err)
	}
	if _, err := d.Store.Get(mem.ScopeGlobal, "global-rule"); err != nil {
		t.Fatalf("node not in global scope: %v", err)
	}
	// Default query cascade (project + global) finds it.
	out, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "global rule applies",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out, "global-rule") {
		t.Errorf("cascade query missed global node: %q", out)
	}
}

func TestSessionShorthandFallsBackWithoutBinding(t *testing.T) {
	d := newDeps(t) // SessionScope empty
	if got := d.resolveScope("session"); got != d.ProjectScope {
		t.Errorf("session shorthand = %q, want project fallback", got)
	}
	d.SessionScope = mem.SessionScope("abc")
	if got := d.resolveScope("session"); got != "session:abc" {
		t.Errorf("session shorthand = %q", got)
	}
	if got := d.resolveScope("project:explicit"); got != "project:explicit" {
		t.Errorf("literal scope = %q", got)
	}
}

func TestLinkAndListTools(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	add := toolByName(t, d, "memory_add")
	for _, id := range []string{"a", "b"} {
		if _, err := add.Execute(ctx, map[string]interface{}{
			"type": "note", "id": id, "title": id, "content": id,
		}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	out, err := toolByName(t, d, "memory_link").Execute(ctx, map[string]interface{}{
		"source_id": "a", "target_id": "b", "relation": "depends_on", "weight": 0.9,
	})
	if err != nil {
		t.Fatalf("memory_link: %v", err)
	}
	if !strings.Contains(out, "depends_on") {
		t.Errorf("link output = %q", out)
	}

	out, err = toolByName(t, d, "memory_list").Execute(ctx, map[string]interface{}{"limit": float64(10)})
	if err != nil {
		t.Fatalf("memory_list: %v", err)
	}
	if !strings.Contains(out, "[note:a]") || !strings.Contains(out, "[note:b]") {
		t.Errorf("list output = %q", out)
	}

	// Bad relation surfaces the store error.
	if _, err := toolByName(t, d, "memory_link").Execute(ctx, map[string]interface{}{
		"source_id": "a", "target_id": "b", "relation": "nope",
	}); err == nil {
		t.Error("expected invalid-relation error")
	}
}

func TestQueryEmptyAndMissingArgs(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{}); err == nil {
		t.Error("expected error for missing query")
	}
	out, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{"query": "nothing stored"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out, "No matching memories") {
		t.Errorf("empty query output = %q", out)
	}
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{"type": "fact", "title": "t"}); err == nil {
		t.Error("expected error for missing content")
	}
}

func TestQueryReturnsIndexNotFullContent(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()

	long := strings.Repeat("vllm deployment detail sentence. ", 20) + "ZZTAIL"
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "title": "DGX deploy target", "content": long,
	}); err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "title": "vllm proxy", "content": "vllm served through litellm proxy",
	}); err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	if err := d.Store.AddLink(d.ProjectScope, "dgx-deploy-target", "vllm-proxy", "related_to", 0.8); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	out, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "vllm deployment",
	})
	if err != nil {
		t.Fatalf("memory_query: %v", err)
	}
	if strings.Contains(out, "ZZTAIL") {
		t.Errorf("query output must not include full content tail:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("long content should be truncated with ellipsis:\n%s", out)
	}
	if !strings.Contains(out, "memory_get") {
		t.Errorf("query output should point at memory_get for full content:\n%s", out)
	}
	if !strings.Contains(out, "-> related: vllm-proxy") {
		t.Errorf("query output should list linked ids:\n%s", out)
	}

	// Full body stays available via memory_get.
	out, err = toolByName(t, d, "memory_get").Execute(ctx, map[string]interface{}{
		"id": "dgx-deploy-target",
	})
	if err != nil {
		t.Fatalf("memory_get: %v", err)
	}
	if !strings.Contains(out, "ZZTAIL") {
		t.Errorf("memory_get should return full content:\n%s", out)
	}
}

func TestRetireToolHidesAndEmitsWrite(t *testing.T) {
	d := newDeps(t)
	bus := telemetry.NewEventBus(16)
	d.Bus = bus
	ctx := context.Background()

	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "obsolete", "title": "obsolete fact", "content": "deploy checklist",
	}); err != nil {
		t.Fatalf("memory_add: %v", err)
	}

	ch := bus.Subscribe()
	out, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "obsolete"})
	bus.Unsubscribe(ch)
	if err != nil {
		t.Fatalf("memory_retire: %v", err)
	}
	if !strings.Contains(out, "Retired [obsolete]") || !strings.Contains(out, "include_expired") {
		t.Errorf("retire output = %q", out)
	}

	q, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{"query": "deploy checklist"})
	if err != nil {
		t.Fatalf("memory_query: %v", err)
	}
	if !strings.Contains(q, "No matching memories") {
		t.Errorf("retired node must be hidden from default query, got %q", q)
	}

	var sawRetire bool
	for ev := range ch {
		if ev.EventType == telemetry.EventMemoryWrite {
			p := telemetry.MemoryWritePayload{}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if p.Op == "retire" && p.NodeID == "obsolete" {
				sawRetire = true
			}
		}
	}
	if !sawRetire {
		t.Error("expected an EventMemoryWrite with Op=retire")
	}
}

func TestRetireToolMissingIDErrors(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{}); err == nil {
		t.Error("expected error for missing id")
	}
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "ghost"}); err == nil {
		t.Error("expected error retiring a nonexistent node")
	}
}

func TestAddWithSupersedesFlow(t *testing.T) {
	d := newDeps(t)
	bus := telemetry.NewEventBus(16)
	d.Bus = bus
	ctx := context.Background()
	add := toolByName(t, d, "memory_add")

	if _, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "deploy-v1", "title": "deploy target", "content": "deploy to staging",
	}); err != nil {
		t.Fatalf("first add: %v", err)
	}

	ch := bus.Subscribe()
	out, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "deploy-v2", "title": "deploy target", "content": "deploy to production",
		"supersedes": "deploy-v1",
	})
	bus.Unsubscribe(ch)
	if err != nil {
		t.Fatalf("superseding add: %v", err)
	}
	if !strings.Contains(out, `superseding "deploy-v1"`) {
		t.Errorf("add output should mention the superseded id: %q", out)
	}

	q, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{"query": "deploy target"})
	if err != nil {
		t.Fatalf("memory_query: %v", err)
	}
	if !strings.Contains(q, "deploy-v2") || strings.Contains(q, "- [fact:deploy-v1]") {
		t.Errorf("default query must return only the replacement, got %q", q)
	}

	var sawSupersede bool
	for ev := range ch {
		if ev.EventType == telemetry.EventMemoryWrite {
			p := telemetry.MemoryWritePayload{}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if p.Op == "add" && p.NodeID == "deploy-v2" && p.Superseded == "deploy-v1" {
				sawSupersede = true
			}
		}
	}
	if !sawSupersede {
		t.Error("expected an add MEMORY_WRITE carrying Superseded=deploy-v1")
	}
}

func TestAddSupersedesMissingTargetErrors(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "new", "title": "new", "content": "x", "supersedes": "ghost",
	}); err == nil {
		t.Error("expected error for a missing supersede target")
	}
}

func TestQueryAsOfAndIncludeExpired(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()

	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "gone", "title": "old truth", "content": "deploy checklist",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "gone"}); err != nil {
		t.Fatalf("retire: %v", err)
	}

	// include_expired brings it back, marked.
	out, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "deploy checklist", "include_expired": true,
	})
	if err != nil {
		t.Fatalf("query include_expired: %v", err)
	}
	if !strings.Contains(out, "gone") || !strings.Contains(out, "[RETIRED") {
		t.Errorf("include_expired output should show the marked node, got %q", out)
	}

	// as_of before the retirement sees it live (RFC3339).
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	out, err = toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "deploy checklist", "as_of": past,
	})
	if err != nil {
		t.Fatalf("query as_of: %v", err)
	}
	if strings.Contains(out, "gone") {
		t.Errorf("as_of before creation must not see the node, got %q", out)
	}

	// Date-only as_of covering today (end of day) sees the retirement applied
	// but parses fine.
	if _, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "deploy checklist", "as_of": time.Now().UTC().Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("date-only as_of should parse: %v", err)
	}

	// Garbage as_of errors loudly.
	if _, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		"query": "deploy checklist", "as_of": "yesterday-ish",
	}); err == nil {
		t.Error("expected error for unparseable as_of")
	}
}

// TestQueryAsOfLabelsLifecycleAtQueriedInstant pins that an as_of query
// labels a result against the reconstructed instant, not wall-clock now: a
// node that was still live at the as_of time must not be marked SUPERSEDED
// just because it was superseded later, before the query ran.
func TestQueryAsOfLabelsLifecycleAtQueriedInstant(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	add := toolByName(t, d, "memory_add")

	if _, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "deploy-v1", "title": "deploy target", "content": "deploy to staging",
	}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	asOf := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	if _, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "deploy-v2", "title": "deploy target", "content": "deploy to production",
		"supersedes": "deploy-v1",
	}); err != nil {
		t.Fatalf("superseding add: %v", err)
	}

	out, err := toolByName(t, d, "memory_query").Execute(ctx, map[string]interface{}{
		// RFC3339Nano to avoid truncating asOf to whole seconds, which could
		// otherwise land it before deploy-v1's own sub-second Created stamp
		// (time.Parse accepts a fractional second against the RFC3339 layout).
		"query": "deploy target", "as_of": asOf.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("query as_of: %v", err)
	}
	if !strings.Contains(out, "[fact:deploy-v1]") {
		t.Fatalf("as_of before the supersede must still return deploy-v1, got %q", out)
	}
	if strings.Contains(out, "deploy-v2") {
		t.Errorf("as_of before the supersede must not return the not-yet-existing replacement, got %q", out)
	}
	if strings.Contains(out, "[fact:deploy-v1] [SUPERSEDED") {
		t.Errorf("a node live at the as_of instant must not be labeled SUPERSEDED, got %q", out)
	}
}

func TestListIncludeExpiredShowsMarker(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "note", "id": "n1", "title": "n1", "content": "x",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "n1"}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	out, err := toolByName(t, d, "memory_list").Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "No memories") {
		t.Errorf("default list must hide the retired node, got %q", out)
	}
	out, err = toolByName(t, d, "memory_list").Execute(ctx, map[string]interface{}{"include_expired": true})
	if err != nil {
		t.Fatalf("list include_expired: %v", err)
	}
	if !strings.Contains(out, "[note:n1] [RETIRED") {
		t.Errorf("include_expired list should mark the retired node, got %q", out)
	}
}

func TestGetShowsLifecycleStatus(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	if _, err := toolByName(t, d, "memory_add").Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "f1", "title": "f1", "content": "x",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := toolByName(t, d, "memory_get").Execute(ctx, map[string]interface{}{"id": "f1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(out, "Status:") {
		t.Errorf("live node must not carry a Status line, got %q", out)
	}
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "f1"}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	out, err = toolByName(t, d, "memory_get").Execute(ctx, map[string]interface{}{"id": "f1"})
	if err != nil {
		t.Fatalf("get retired: %v", err)
	}
	if !strings.Contains(out, "Status: RETIRED since") {
		t.Errorf("retired node must carry a Status line, got %q", out)
	}
}

func TestAddReviveWording(t *testing.T) {
	d := newDeps(t)
	ctx := context.Background()
	add := toolByName(t, d, "memory_add")
	if _, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "r1", "title": "r1", "content": "v1",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := toolByName(t, d, "memory_retire").Execute(ctx, map[string]interface{}{"id": "r1"}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	out, err := add.Execute(ctx, map[string]interface{}{
		"type": "fact", "id": "r1", "title": "r1", "content": "v2",
	})
	if err != nil {
		t.Fatalf("revive add: %v", err)
	}
	if !strings.Contains(out, "revived") {
		t.Errorf("re-add of a retired node should say revived, got %q", out)
	}
}
