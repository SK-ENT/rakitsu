// Package memory exposes rakitsu's native memory / knowledge-graph store
// (internal/memory) to agents as six tools: memory_add, memory_query,
// memory_get, memory_list, memory_link, memory_retire.
//
// The tools are auto-registered on every agent when settings.memory.enabled
// is true (same pattern as the chat-mode user_input tool). Because rakitsu
// serve exposes the ToolRegistry over MCP (/mcp), enabling memory also makes
// these tools reachable from external MCP clients — native-first, MCP-for-free.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	mem "github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// Deps carries the shared dependencies for one agent's memory tool set.
type Deps struct {
	Store *mem.Store
	// ProjectScope is the resolved default scope ("project:<id>" from the
	// config's project_id, or "global" when unset).
	ProjectScope string
	// SessionScope is "session:<id>" when the surface binds one (chat), or
	// empty — the "session" shorthand then falls back to ProjectScope.
	SessionScope string
	AgentName    string
	Bus          *telemetry.EventBus // optional: emits MEMORY_WRITE / MEMORY_RECALL
}

// NewTools returns the six memory tools bound to d.
func NewTools(d Deps) []tools.Tool {
	return []tools.Tool{
		&addTool{d}, &queryTool{d}, &getTool{d}, &listTool{d}, &linkTool{d}, &retireTool{d},
	}
}

// Recaller fetches the top-k memories relevant to a query for Run-start
// auto-recall injection. It reuses the exact scope cascade and index
// formatting as memory_query and emits EventMemoryRecall with Op "auto_recall".
// Its Recall method satisfies the agent's MemoryRecaller interface structurally,
// so internal/agent need not import this package.
type Recaller struct {
	d        Deps
	topK     int
	nodeType string
	scopes   []string // optional override; empty = default cascade
}

// NewRecaller binds a Recaller to d. topK<=0 defaults to 5. nodeType ""
// recalls all types; scopeOverride entries may be shorthands or literals
// (resolved per-entry), empty uses the session->project->global cascade.
func NewRecaller(d Deps, topK int, nodeType string, scopeOverride []string) *Recaller {
	if topK <= 0 {
		topK = 5
	}
	return &Recaller{d: d, topK: topK, nodeType: nodeType, scopes: scopeOverride}
}

// Recall returns the formatted (unfenced) index block and the matched node IDs
// for the top-k memories relevant to query. Returns ("", nil) when the store is
// absent, the query is blank, the search errors, or nothing matches. A nil
// receiver is a no-op.
func (r *Recaller) Recall(query string) (string, []string) {
	if r == nil || r.d.Store == nil || strings.TrimSpace(query) == "" {
		return "", nil
	}
	var scopes []string
	if len(r.scopes) > 0 {
		for _, s := range r.scopes {
			scopes = append(scopes, r.d.resolveScope(s))
		}
	} else {
		scopes = r.d.recallScopes("")
	}
	// Always the current view: auto-recall must never inject superseded or
	// retired knowledge into an agent's context.
	view := mem.View{}
	results, err := r.d.Store.Query(query, scopes, r.nodeType, r.topK, view)
	if err != nil || len(results) == 0 {
		return "", nil
	}
	ids := make([]string, len(results))
	for i, res := range results {
		ids[i] = res.ID
	}
	r.d.emitRecall(telemetry.MemoryRecallPayload{
		Op: "auto_recall", Scope: strings.Join(scopes, ","), Query: query, ResultCount: len(results), NodeIDs: ids,
	})
	return formatIndex(results, view.At()), ids
}

// resolveScope maps the LLM-facing scope argument to a concrete scope.
// Shorthands: "" / "project" -> ProjectScope, "global", "session". Full
// literals ("project:foo", "session:bar") pass through.
func (d Deps) resolveScope(arg string) string {
	switch arg {
	case "", "project":
		return d.ProjectScope
	case "global":
		return mem.ScopeGlobal
	case "session":
		if d.SessionScope != "" {
			return d.SessionScope
		}
		return d.ProjectScope
	default:
		return arg
	}
}

func (d Deps) emitWrite(p telemetry.MemoryWritePayload) {
	if d.Bus != nil {
		d.Bus.Emit(d.AgentName, telemetry.EventMemoryWrite, p)
	}
}

func (d Deps) emitRecall(p telemetry.MemoryRecallPayload) {
	if d.Bus != nil {
		d.Bus.Emit(d.AgentName, telemetry.EventMemoryRecall, p)
	}
}

// scopeParam is the shared JSON-schema fragment for the scope argument.
func scopeParam() map[string]interface{} {
	return map[string]interface{}{
		"type":        "string",
		"description": "Memory scope: 'project' (default), 'global', or 'session'. Use 'global' only for knowledge that applies across all projects.",
	}
}

// asOfParam / includeExpiredParam are the shared JSON-schema fragments for
// the temporal read arguments on memory_query and memory_list.
func asOfParam() map[string]interface{} {
	return map[string]interface{}{
		"type":        "string",
		"description": "Time-travel: return what was known and valid at this time (RFC3339, or YYYY-MM-DD meaning end of that day UTC). Default: current view.",
	}
}

func includeExpiredParam() map[string]interface{} {
	return map[string]interface{}{
		"type":        "boolean",
		"description": "Include retired/superseded entries, marked with their status. Default false.",
	}
}

func strArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

func boolArg(args map[string]interface{}, key string) bool {
	v, _ := args[key].(bool)
	return v
}

// parseView builds the temporal view from the optional as_of and
// include_expired arguments. An unparseable as_of is a loud error, never a
// silent fallback to the current view.
func parseView(args map[string]interface{}) (mem.View, error) {
	view := mem.View{IncludeExpired: boolArg(args, "include_expired")}
	asOf := strings.TrimSpace(strArg(args, "as_of"))
	if asOf == "" {
		return view, nil
	}
	if ts, err := time.Parse(time.RFC3339, asOf); err == nil {
		view.AsOf = ts.UTC()
		return view, nil
	}
	if d, err := time.Parse("2006-01-02", asOf); err == nil {
		// A bare date means "everything known on or during that day".
		view.AsOf = d.AddDate(0, 0, 1).Add(-time.Nanosecond)
		return view, nil
	}
	return mem.View{}, fmt.Errorf("invalid as_of %q (want RFC3339 like 2026-07-07T12:00:00Z, or YYYY-MM-DD)", asOf)
}

// lifecycleState classifies n's temporal status as of t: "retired",
// "superseded", "not_yet_valid", or "" (live), plus the relevant stamp.
// Shared by lifecycleMarker (compact bracket) and memory_get (full sentence)
// so the two classifications cannot drift apart.
func lifecycleState(n *mem.Node, t time.Time) (state string, at time.Time) {
	switch {
	case n.ExpiredAt != nil && !n.ExpiredAt.After(t):
		return "retired", *n.ExpiredAt
	case n.InvalidAt != nil && !n.InvalidAt.After(t):
		return "superseded", *n.InvalidAt
	case n.ValidAt != nil && n.ValidAt.After(t):
		return "not_yet_valid", *n.ValidAt
	default:
		return "", time.Time{}
	}
}

// lifecycleMarker renders a node's temporal status at t as a compact
// bracket, or "" when live at t. t must be the view's reference time
// (view.At()) — an as_of query has to label results against the
// reconstructed instant, not wall-clock now, or the marker would contradict
// the temporal slice the query itself returned.
func lifecycleMarker(n *mem.Node, t time.Time) string {
	switch state, at := lifecycleState(n, t); state {
	case "retired":
		return fmt.Sprintf(" [RETIRED %s]", at.Format("2006-01-02"))
	case "superseded":
		return fmt.Sprintf(" [SUPERSEDED %s]", at.Format("2006-01-02"))
	case "not_yet_valid":
		return fmt.Sprintf(" [NOT YET VALID until %s]", at.Format("2006-01-02"))
	default:
		return ""
	}
}

func intArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

// --- memory_add ---

type addTool struct{ d Deps }

func (t *addTool) GetName() string { return "memory_add" }

func (t *addTool) GetDescription() string {
	return "Persist a piece of knowledge to memory so it survives this conversation. Use when you learn a fact, rule, pattern, procedure, or gotcha worth keeping. Re-using an existing id updates that entry. Pass supersedes=<old-id> when new knowledge replaces an existing entry instead of editing it — the old entry is kept as history but hidden from recall."
}

func (t *addTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Kind of knowledge",
				"enum":        []string{"rule", "pattern", "fact", "procedure", "gotcha", "note"},
			},
			"title":   map[string]interface{}{"type": "string", "description": "Short human-readable title"},
			"content": map[string]interface{}{"type": "string", "description": "The knowledge itself — compact, 1-5 sentences"},
			"id":      map[string]interface{}{"type": "string", "description": "Optional slug id (derived from title when omitted)"},
			"tags":    map[string]interface{}{"type": "string", "description": "Optional comma-separated tags for recall"},
			"priority": map[string]interface{}{
				"type":        "integer",
				"description": "1-10, higher = more important (default 5)",
			},
			"source": map[string]interface{}{"type": "string", "description": "Optional provenance (file path, URL)"},
			"supersedes": map[string]interface{}{
				"type":        "string",
				"description": "Optional id of an existing entry in this scope that this new entry replaces. The old entry is kept but marked invalid and hidden from default recall.",
			},
			"scope": scopeParam(),
		},
		"required": []string{"type", "title", "content"},
	}
}

func (t *addTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	scope := t.d.resolveScope(strArg(args, "scope"))
	var tags []string
	for _, tag := range strings.Split(strArg(args, "tags"), ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	node := mem.Node{
		ID:       strArg(args, "id"),
		Type:     strArg(args, "type"),
		Title:    strArg(args, "title"),
		Content:  strArg(args, "content"),
		Tags:     tags,
		Priority: intArg(args, "priority"),
		Scope:    scope,
		Source:   strArg(args, "source"),
	}
	if node.Title == "" || node.Content == "" {
		return "", fmt.Errorf("title and content are required")
	}
	if node.ID == "" {
		node.ID = mem.Slugify(node.Title)
	}
	supersedes := strArg(args, "supersedes")
	var res mem.AddResult
	var err error
	if supersedes != "" {
		res, err = t.d.Store.AddSuperseding(node, supersedes)
	} else {
		res, err = t.d.Store.Add(node)
	}
	if err != nil {
		return "", err
	}
	t.d.emitWrite(telemetry.MemoryWritePayload{
		Op: "add", Scope: scope, NodeID: node.ID, NodeType: node.Type, Title: node.Title, Created: res.Created, Superseded: supersedes,
	})
	verb := "Updated"
	if res.Created {
		verb = "Stored"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s [%s:%s] in scope %q", verb, node.Type, node.ID, scope)
	if supersedes != "" {
		fmt.Fprintf(&sb, ", superseding %q (kept as history, hidden from recall)", supersedes)
	}
	if res.Revived {
		sb.WriteString(" (revived — was retired/superseded)")
	}
	sb.WriteString(".")
	return sb.String(), nil
}

// --- memory_query ---

type queryTool struct{ d Deps }

func (t *queryTool) GetName() string { return "memory_query" }

func (t *queryTool) GetDescription() string {
	return "Search memory by free text and get a ranked index of the most relevant stored knowledge (rules, patterns, facts, procedures, gotchas): id, title, one-line snippet, and related ids. Fetch the full content of an entry with memory_get. Use before non-trivial work and whenever prior context might exist."
}

func (t *queryTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query":           map[string]interface{}{"type": "string", "description": "Free-text search query"},
			"type":            map[string]interface{}{"type": "string", "description": "Optional filter: rule|pattern|fact|procedure|gotcha|note"},
			"limit":           map[string]interface{}{"type": "integer", "description": "Max results (default 5)"},
			"as_of":           asOfParam(),
			"include_expired": includeExpiredParam(),
			"scope":           scopeParam(),
		},
		"required": []string{"query"},
	}
}

func (t *queryTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	query := strArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	limit := intArg(args, "limit")
	if limit <= 0 {
		limit = 5
	}
	scopes := t.d.recallScopes(strArg(args, "scope"))
	view, err := parseView(args)
	if err != nil {
		return "", err
	}

	results, err := t.d.Store.Query(query, scopes, strArg(args, "type"), limit, view)
	if err != nil {
		return "", err
	}
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	t.d.emitRecall(telemetry.MemoryRecallPayload{
		Op: "query", Scope: strings.Join(scopes, ","), Query: query, ResultCount: len(results), NodeIDs: ids,
	})
	if len(results) == 0 {
		return "No matching memories.", nil
	}
	return formatIndex(results, view.At()), nil
}

// recallScopes returns the scopes to search for a recall. An explicit non-empty
// scope arg searches just that scope; otherwise the session (when bound) +
// project + global cascade. Shared by memory_query and auto-recall so the two
// recall paths cannot drift.
func (d Deps) recallScopes(explicit string) []string {
	if explicit != "" {
		return []string{d.resolveScope(explicit)}
	}
	var scopes []string
	if d.SessionScope != "" {
		scopes = append(scopes, d.SessionScope)
	}
	scopes = append(scopes, d.ProjectScope)
	if d.ProjectScope != mem.ScopeGlobal {
		scopes = append(scopes, mem.ScopeGlobal)
	}
	return scopes
}

// formatIndex renders ranked results as the index-only block (one line per hit:
// title + snippet + tags + related ids), never full bodies — the caller pulls
// what it needs with memory_get. Shared by memory_query and auto-recall.
func formatIndex(results []mem.Scored, now time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d memories (best first; index only — use memory_get <id> for full content):\n", len(results))
	for _, r := range results {
		fmt.Fprintf(&sb, "- [%s:%s]%s %s — %s", r.Type, r.ID, lifecycleMarker(&r.Node, now), r.Title, indexSnippet(r.Content))
		if len(r.Tags) > 0 {
			fmt.Fprintf(&sb, " (tags: %s)", strings.Join(r.Tags, ","))
		}
		if len(r.Links) > 0 {
			targets := make([]string, len(r.Links))
			for i, l := range r.Links {
				targets[i] = l.Target
			}
			fmt.Fprintf(&sb, " -> related: %s", strings.Join(targets, ","))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// indexSnippet collapses content to a single-line hook for query results.
const indexSnippetMaxRunes = 140

func indexSnippet(content string) string {
	oneLine := strings.Join(strings.Fields(content), " ")
	runes := []rune(oneLine)
	if len(runes) <= indexSnippetMaxRunes {
		return oneLine
	}
	return string(runes[:indexSnippetMaxRunes]) + "…"
}

// --- memory_get ---

type getTool struct{ d Deps }

func (t *getTool) GetName() string { return "memory_get" }

func (t *getTool) GetDescription() string {
	return "Fetch one memory entry by id, including its links to related entries."
}

func (t *getTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":    map[string]interface{}{"type": "string", "description": "Node id (slug)"},
			"scope": scopeParam(),
		},
		"required": []string{"id"},
	}
}

func (t *getTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	id := strArg(args, "id")
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	scope := t.d.resolveScope(strArg(args, "scope"))
	n, err := t.d.Store.Get(scope, id)
	if err != nil {
		return "", err
	}
	t.d.emitRecall(telemetry.MemoryRecallPayload{
		Op: "get", Scope: scope, Query: id, ResultCount: 1, NodeIDs: []string{n.ID},
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s:%s] %s (priority %d)\n", n.Type, n.ID, n.Title, n.Priority)
	switch state, at := lifecycleState(n, time.Now().UTC()); state {
	case "retired":
		fmt.Fprintf(&sb, "Status: RETIRED since %s (hidden from default recall)\n", at.Format(time.RFC3339))
	case "superseded":
		fmt.Fprintf(&sb, "Status: SUPERSEDED (invalid since %s, hidden from default recall)\n", at.Format(time.RFC3339))
	case "not_yet_valid":
		fmt.Fprintf(&sb, "Status: NOT YET VALID (valid from %s)\n", at.Format(time.RFC3339))
	}
	fmt.Fprintf(&sb, "%s\n", n.Content)
	if len(n.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(n.Tags, ","))
	}
	if n.Source != "" {
		fmt.Fprintf(&sb, "Source: %s\n", n.Source)
	}
	for _, l := range n.Links {
		fmt.Fprintf(&sb, "-> %s -> %s (w:%.1f)\n", l.Relation, l.Target, l.Weight)
	}
	return sb.String(), nil
}

// --- memory_list ---

type listTool struct{ d Deps }

func (t *listTool) GetName() string { return "memory_list" }

func (t *listTool) GetDescription() string {
	return "Browse stored memories in a scope, newest first, optionally filtered by type."
}

func (t *listTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":            map[string]interface{}{"type": "string", "description": "Optional filter: rule|pattern|fact|procedure|gotcha|note"},
			"limit":           map[string]interface{}{"type": "integer", "description": "Max results (default 20)"},
			"as_of":           asOfParam(),
			"include_expired": includeExpiredParam(),
			"scope":           scopeParam(),
		},
	}
}

func (t *listTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	scope := t.d.resolveScope(strArg(args, "scope"))
	limit := intArg(args, "limit")
	if limit <= 0 {
		limit = 20
	}
	view, err := parseView(args)
	if err != nil {
		return "", err
	}
	nodes, err := t.d.Store.List(scope, strArg(args, "type"), limit, view)
	if err != nil {
		return "", err
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	t.d.emitRecall(telemetry.MemoryRecallPayload{
		Op: "list", Scope: scope, ResultCount: len(nodes), NodeIDs: ids,
	})
	if len(nodes) == 0 {
		return fmt.Sprintf("No memories in scope %q.", scope), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d memories in scope %q (newest first):\n", len(nodes), scope)
	now := view.At()
	for i := range nodes {
		fmt.Fprintf(&sb, "- [%s:%s]%s %s\n", nodes[i].Type, nodes[i].ID, lifecycleMarker(&nodes[i], now), nodes[i].Title)
	}
	return sb.String(), nil
}

// --- memory_link ---

type linkTool struct{ d Deps }

func (t *linkTool) GetName() string { return "memory_link" }

func (t *linkTool) GetDescription() string {
	return "Create a typed relationship between two memory entries (applies_to, depends_on, part_of, related_to, supersedes, contradicts)."
}

func (t *linkTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source_id": map[string]interface{}{"type": "string", "description": "Source node id"},
			"target_id": map[string]interface{}{"type": "string", "description": "Target node id (may not exist yet)"},
			"relation": map[string]interface{}{
				"type":        "string",
				"description": "Relationship type",
				"enum":        []string{"applies_to", "depends_on", "part_of", "related_to", "supersedes", "contradicts"},
			},
			"weight": map[string]interface{}{"type": "number", "description": "Relevance strength 0.0-1.0 (default 0.5)"},
			"scope":  scopeParam(),
		},
		"required": []string{"source_id", "target_id", "relation"},
	}
}

func (t *linkTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	sourceID, targetID := strArg(args, "source_id"), strArg(args, "target_id")
	relation := strArg(args, "relation")
	if sourceID == "" || targetID == "" || relation == "" {
		return "", fmt.Errorf("source_id, target_id, and relation are required")
	}
	weight, _ := args["weight"].(float64)
	scope := t.d.resolveScope(strArg(args, "scope"))
	if err := t.d.Store.AddLink(scope, sourceID, targetID, relation, weight); err != nil {
		return "", err
	}
	t.d.emitWrite(telemetry.MemoryWritePayload{Op: "link", Scope: scope, NodeID: sourceID})
	return fmt.Sprintf("Linked %s -> %s -> %s in scope %q.", sourceID, relation, targetID, scope), nil
}

// --- memory_retire ---

type retireTool struct{ d Deps }

func (t *retireTool) GetName() string { return "memory_retire" }

func (t *retireTool) GetDescription() string {
	return "Retire a memory entry: a soft-delete that hides it from default recall but keeps it for history and as_of time-travel. Never destructive. Use for knowledge that is obsolete with no direct replacement; when there is one, use memory_add with supersedes instead."
}

func (t *retireTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":    map[string]interface{}{"type": "string", "description": "Node id (slug) to retire"},
			"scope": scopeParam(),
		},
		"required": []string{"id"},
	}
}

func (t *retireTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	id := strArg(args, "id")
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	scope := t.d.resolveScope(strArg(args, "scope"))
	if err := t.d.Store.Retire(scope, id); err != nil {
		return "", err
	}
	t.d.emitWrite(telemetry.MemoryWritePayload{Op: "retire", Scope: scope, NodeID: id})
	return fmt.Sprintf("Retired [%s] in scope %q (hidden from default recall; still visible via include_expired or as_of).", id, scope), nil
}
