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
	"github.com/SK-ENT/rakitsu/internal/tools/fs"
)

func TestGetSystemPrompt(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := NewAgent(&config.AgentDefinition{Name: "a", Role: "worker", SystemPrompt: "You are a helpful assistant."}, nil, tools.NewToolRegistry(), bus, nil)
	if got := a.GetSystemPrompt(); got != "You are a helpful assistant." {
		t.Errorf("GetSystemPrompt() = %q, want the configured prompt", got)
	}
}

func TestGetEffectiveToolDefs_ReturnsFullRegistryNotJustDeclaredTools(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(fs.NewTool(&config.ToolDefinition{Name: "fs", Type: "fs", Description: "filesystem access", AllowedPaths: []string{"."}}))
	reg.RegisterTool(fs.NewTool(&config.ToolDefinition{Name: "runtime_only", Type: "fs", Description: "registered at runtime, not in Tools:", AllowedPaths: []string{"."}}))
	// Agent only declares "fs" in its static Tools list — "runtime_only" mirrors
	// how memory_*/user_input/spawn_agent get added straight to the registry
	// without ever appearing in an agent's YAML Tools field.
	a := NewAgent(&config.AgentDefinition{Name: "a", Role: "worker", Tools: []string{"fs"}}, nil, reg, bus, nil)

	defs := a.GetEffectiveToolDefs()
	if len(defs) != 2 {
		t.Fatalf("GetEffectiveToolDefs() returned %d defs, want 2 (full registry, not filtered to declared Tools:): %+v", len(defs), defs)
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["fs"] || !names["runtime_only"] {
		t.Errorf("GetEffectiveToolDefs() = %+v, want both \"fs\" and \"runtime_only\" present", defs)
	}
}

// TestToolCallsForRun_RecordsCallsMadeDuringThatRun verifies the pipeline-
// level require_tool_call gate has real ground truth to check: an agent
// that calls a tool during a Run opted into tracking via
// NewToolCallRunContext must have that call show up in ToolCallsForRun for
// that specific run ID, in order, with its arguments intact — and reading
// it once consumes the entry (no unbounded growth across a long-lived
// agent's many Runs).
func TestToolCallsForRun_RecordsCallsMadeDuringThatRun(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("read_file", "file content"))

	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "RUN.md"}}},
			FinishReason: "tool_calls",
		},
		llm.GenerateResult{Response: "done", FinishReason: "stop"},
	)

	a := NewAgent(&config.AgentDefinition{Name: "a", Role: "worker", Tools: []string{"read_file"}}, provider, reg, bus, nil)

	ctx, runID := NewToolCallRunContext(context.Background())
	if calls := a.ToolCallsForRun(runID); len(calls) != 0 {
		t.Fatalf("ToolCallsForRun(runID) before any Run = %v, want empty", calls)
	}

	if _, err := a.Run(ctx, "read RUN.md"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	calls := a.ToolCallsForRun(runID)
	if len(calls) != 1 {
		t.Fatalf("ToolCallsForRun(runID) after Run = %d calls, want 1: %+v", len(calls), calls)
	}
	if calls[0].Name != "read_file" || calls[0].Arguments["path"] != "RUN.md" {
		t.Errorf("ToolCallsForRun(runID)[0] = %+v, want read_file(path=RUN.md)", calls[0])
	}

	// Reading again after the entry was consumed must come back empty, not
	// panic or return stale data.
	if calls := a.ToolCallsForRun(runID); len(calls) != 0 {
		t.Errorf("ToolCallsForRun(runID) after being read once = %v, want empty (entry consumed)", calls)
	}
}

// TestToolCallsForRun_NoOptIn_RecordsNothing verifies a Run() that never
// attached a run ID via NewToolCallRunContext isn't tracked at all — most
// callers (chat, normal delegation) don't use this mechanism and shouldn't
// pay for it, nor leave anything behind for an unrelated ToolCallsForRun
// call to accidentally pick up.
func TestToolCallsForRun_NoOptIn_RecordsNothing(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	reg := tools.NewToolRegistry()
	reg.RegisterTool(newMockTool("read_file", "file content"))

	provider := newSequenceProvider(
		llm.GenerateResult{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a"}}},
			FinishReason: "tool_calls",
		},
		llm.GenerateResult{Response: "done", FinishReason: "stop"},
	)

	a := NewAgent(&config.AgentDefinition{Name: "a", Role: "worker", Tools: []string{"read_file"}}, provider, reg, bus, nil)

	if _, err := a.Run(context.Background(), "plain run, no tracking"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// No run ID was ever minted for this Run, so there's nothing a caller
	// could even ask for — assert the internal map stayed empty rather than
	// silently accumulating an entry nobody will read.
	if len(a.toolCallsByRun) != 0 {
		t.Errorf("toolCallsByRun = %v, want empty — a Run without NewToolCallRunContext must not be tracked", a.toolCallsByRun)
	}
}

// TestToolCallsForRun_ConcurrentRuns_Isolated is the regression test for a
// concurrency bug: a single shared "last run" slot on *Agent would let two
// concurrent Run()s on the same instance (a
// worker reachable through independent delegation paths, the same failure
// mode orchestratorRunState exists to prevent for *Orchestrator) corrupt
// each other's recorded tool calls — one Run's reset wiping another's
// in-flight calls, or one Run observing another's calls as its own. This
// drives many concurrent "runs" (each with its own run ID, writing directly
// to the same map/mutex the real recording path uses) and asserts every
// run ID's calls come back exactly as written, never mixed with another's.
// Run with -race to also confirm no data race on the shared map.
func TestToolCallsForRun_ConcurrentRuns_Isolated(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := NewAgent(&config.AgentDefinition{Name: "a", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := uint64(i + 1)
			call := llm.ToolCall{Name: "sh", Arguments: map[string]interface{}{"cmd": fmt.Sprintf("cmd-%d", i)}}
			a.toolCallsByRunMu.Lock()
			if a.toolCallsByRun == nil {
				a.toolCallsByRun = make(map[uint64][]llm.ToolCall)
			}
			a.toolCallsByRun[runID] = append(a.toolCallsByRun[runID], call)
			a.toolCallsByRunMu.Unlock()
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		runID := uint64(i + 1)
		calls := a.ToolCallsForRun(runID)
		if len(calls) != 1 {
			t.Fatalf("run %d: ToolCallsForRun = %d calls, want 1: %+v", runID, len(calls), calls)
		}
		want := fmt.Sprintf("cmd-%d", i)
		if calls[0].Arguments["cmd"] != want {
			t.Errorf("run %d: got cmd %v, want %q — cross-run contamination", runID, calls[0].Arguments["cmd"], want)
		}
	}
}
