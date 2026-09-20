package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// statelessToolCallProvider decides its response from the conversation
// history it's given, not from a shared call counter — unlike
// sequenceProvider, this makes it safe to share across truly concurrent
// Runs, since each Run's own history correctly reflects only that Run's
// own progress regardless of how goroutines interleave. Calls the named
// tool once (when the history has no tool-result message yet), then stops
// (once it does).
type statelessToolCallProvider struct {
	toolName string
}

func (p *statelessToolCallProvider) Generate(_ context.Context, _ string, history []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	for _, m := range history {
		if m.Role == "tool" {
			r := stopResponse("done")
			return &r, nil
		}
	}
	r := toolCallResponse("", tc(p.toolName, map[string]interface{}{}))
	return &r, nil
}

func (p *statelessToolCallProvider) GetName() string  { return "stateless" }
func (p *statelessToolCallProvider) GetModel() string { return "stateless-model" }

// turnQueryProbeTool is a Tool whose Execute reads back whatever query
// tools.WithTurnQuery put in ctx, recording it. Used to pin that the
// agent loop actually carries the turn's query through ctx.
type turnQueryProbeTool struct {
	mu             sync.Mutex
	seen           []string
	seenAttachment []bool
}

func (t *turnQueryProbeTool) GetName() string        { return "noop" }
func (t *turnQueryProbeTool) GetDescription() string { return "noop tool" }

func (t *turnQueryProbeTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func (t *turnQueryProbeTool) Execute(ctx context.Context, _ map[string]interface{}) (string, error) {
	q, hasAttachments, _ := tools.TurnQueryFromContext(ctx)
	t.mu.Lock()
	t.seen = append(t.seen, q)
	t.seenAttachment = append(t.seenAttachment, hasAttachments)
	t.mu.Unlock()
	return "ok", nil
}

func (t *turnQueryProbeTool) queries() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.seen...)
}

func (t *turnQueryProbeTool) attachmentFlags() []bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]bool(nil), t.seenAttachment...)
}

// TestAgentRun_CarriesTurnQueryInContext pins the context-based turn-query wiring:
// RunWithAttachments must wrap ctx with tools.WithTurnQuery(ctx, query)
// before any tool calls happen, so a tool's Execute can read the current
// turn's query back via tools.TurnQueryFromContext instead of requiring
// the model to retype content it already has as a tool-call argument (see
// internal/tools/jev/tool.go). This carries the query via ctx rather than
// mutable state on the Tool itself specifically so the same registered
// Tool instance is safe to share across concurrent Runs — see
// TestAgentRun_ConcurrentRunsDoNotLeakTurnQuery below.
func TestAgentRun_CarriesTurnQueryInContext(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	probe := &turnQueryProbeTool{}
	registry := tools.NewToolRegistry()
	registry.RegisterTool(probe)
	def := &config.AgentDefinition{Name: "ProbeAgent", SystemPrompt: "x", Tools: []string{"noop"}}
	provider := newSequenceProvider(
		toolCallResponse("", tc("noop", map[string]interface{}{})),
		stopResponse("done 1"),
		toolCallResponse("", tc("noop", map[string]interface{}{})),
		stopResponse("done 2"),
	)
	a := NewAgent(def, provider, registry, bus, nil)

	if _, err := a.Run(context.Background(), "first query"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if _, err := a.Run(context.Background(), "second query"); err != nil {
		t.Fatalf("run 2: %v", err)
	}

	got := probe.queries()
	if len(got) != 2 || got[0] != "first query" || got[1] != "second query" {
		t.Fatalf("probe saw queries = %v, want [\"first query\" \"second query\"]", got)
	}
}

// TestAgentRunWithAttachments_MarksTurnQueryHasAttachments is a regression
// test for a real finding: a tool's Execute must be able to tell that the
// turn had attachments (e.g. --attach images), because the ctx-carried
// query is text-only and silently falling back to it as if it were the
// whole turn would drop attachment content entirely. This specifically
// exercises RunWithAttachments (Run/RunWithHistory always pass none).
func TestAgentRunWithAttachments_MarksTurnQueryHasAttachments(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	probe := &turnQueryProbeTool{}
	registry := tools.NewToolRegistry()
	registry.RegisterTool(probe)
	def := &config.AgentDefinition{Name: "ProbeAgent", SystemPrompt: "x", Tools: []string{"noop"}}
	provider := newSequenceProvider(
		toolCallResponse("", tc("noop", map[string]interface{}{})),
		stopResponse("done"),
	)
	a := NewAgent(def, provider, registry, bus, nil)

	attachment := []llm.ContentBlock{{Type: llm.ContentTypeImage, MIMEType: "image/png"}}
	if _, err := a.RunWithAttachments(context.Background(), "review the attached diff", nil, attachment); err != nil {
		t.Fatalf("run: %v", err)
	}

	flags := probe.attachmentFlags()
	if len(flags) != 1 || !flags[0] {
		t.Fatalf("attachment flags = %v, want [true]", flags)
	}
}

// TestAgentRun_ConcurrentRunsDoNotLeakTurnQuery is the regression test for
// a real concurrency bug caught by review: an earlier version of this
// fix stored the turn query as a mutable field on the Tool instance
// itself, which raced across concurrent Runs sharing the same registered
// Tool (e.g. rakitsu serve handling overlapping sessions, or parallel
// sub-agents). Runs N concurrent Runs against one shared *Agent/*Tool and
// asserts each Run's tool call saw only its own query, never another's.
func TestAgentRun_ConcurrentRunsDoNotLeakTurnQuery(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	probe := &turnQueryProbeTool{}
	registry := tools.NewToolRegistry()
	registry.RegisterTool(probe)
	def := &config.AgentDefinition{Name: "ProbeAgent", SystemPrompt: "x", Tools: []string{"noop"}}

	const n = 20
	provider := &statelessToolCallProvider{toolName: "noop"}
	a := NewAgent(def, provider, registry, bus, nil)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			query := fmt.Sprintf("run-%d query", i)
			if _, err := a.Run(context.Background(), query); err != nil {
				t.Errorf("run %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := probe.queries()
	if len(got) != n {
		t.Fatalf("probe recorded %d calls, want %d", len(got), n)
	}
	want := map[string]bool{}
	for i := 0; i < n; i++ {
		want[fmt.Sprintf("run-%d query", i)] = true
	}
	for _, q := range got {
		if !want[q] {
			t.Errorf("probe saw unexpected query %q — not one of this test's own run queries", q)
		}
	}
}
