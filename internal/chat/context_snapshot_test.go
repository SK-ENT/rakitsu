package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestBuildContextSnapshot_CategoriesAndTotal(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	a := agent.NewAgent(&config.AgentDefinition{
		Name:         "Coordinator",
		Role:         "worker",
		Model:        "gemini-3.5-flash-lite",
		SystemPrompt: "You coordinate work by spawning subagents.",
	}, nil, tools.NewToolRegistry(), bus, nil)

	m := Model{
		singleAgent: a,
		runner:      a,
		history: []llm.Message{
			llm.NewTextMessage("user", "hello"),
			llm.NewTextMessage("assistant", "hi there"),
		},
	}

	snap, ok := m.buildContextSnapshot("Coordinator")
	if !ok {
		t.Fatalf("buildContextSnapshot(Coordinator) ok=false, want true")
	}
	if snap.Model != "gemini-3.5-flash-lite" {
		t.Errorf("Model = %q, want gemini-3.5-flash-lite", snap.Model)
	}

	names := make(map[string]int)
	for _, c := range snap.Categories {
		names[c.Name] = c.Tokens
	}
	if _, ok := names["System prompt"]; !ok {
		t.Errorf("missing System prompt category, got %+v", snap.Categories)
	}
	if _, ok := names["Tools"]; !ok {
		t.Errorf("missing Tools category, got %+v", snap.Categories)
	}
	if _, ok := names["History"]; !ok {
		t.Errorf("missing History category, got %+v", snap.Categories)
	}
	if names["System prompt"] == 0 {
		t.Errorf("System prompt tokens = 0, want > 0 for a non-empty prompt")
	}
	if names["History"] == 0 {
		t.Errorf("History tokens = 0, want > 0 for two non-empty messages")
	}

	var sum int
	for _, c := range snap.Categories {
		sum += c.Tokens
	}
	if snap.TotalTokens != sum {
		t.Errorf("TotalTokens = %d, want sum of categories = %d", snap.TotalTokens, sum)
	}
}

func TestBuildContextSnapshot_UnknownAgent(t *testing.T) {
	m := Model{}
	_, ok := m.buildContextSnapshot("NoSuchAgent")
	if ok {
		t.Errorf("ok = true for an unknown agent name, want false")
	}
}

// historyCategoryTokens pulls the "History" category's token count out of a
// snapshot, failing the test if the category is missing.
func historyCategoryTokens(t *testing.T, snap contextSnapshot) int {
	t.Helper()
	for _, c := range snap.Categories {
		if c.Name == "History" {
			return c.Tokens
		}
	}
	t.Fatalf("no History category in snapshot: %+v", snap.Categories)
	return 0
}

// TestBuildContextSnapshot_OrchestratorMode_HistoryReflectsTruncatedWindow is
// the regression test for Fix 1: in orchestrator mode (m.singleAgent == nil),
// the next turn actually sends buildQueryWithHistory(priorHistory, query) —
// which keeps only the last historyMaxTurns (10) turns. With 15 raw turns,
// the History category must reflect that truncated/composed window, not the
// full raw transcript.
func TestBuildContextSnapshot_OrchestratorMode_HistoryReflectsTruncatedWindow(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	model := "gemini-3.5-flash-lite"
	a := agent.NewAgent(&config.AgentDefinition{
		Name:         "Coordinator",
		Role:         "worker",
		Model:        model,
		SystemPrompt: "You coordinate work by spawning subagents.",
	}, nil, tools.NewToolRegistry(), bus, nil)
	orch := agent.NewOrchestrator(&config.OrchestratorConfig{
		Name:     "Orc",
		Strategy: "ReAct",
		Agents:   []string{"Coordinator"},
	}, nil, bus, map[string]agent.Runner{"Coordinator": a})

	// 15 turns > historyMaxTurns (10).
	var history []llm.Message
	for i := 1; i <= 15; i++ {
		history = append(history,
			llm.NewTextMessage("user", fmt.Sprintf("question number %d, please answer in detail", i)),
			llm.NewTextMessage("assistant", fmt.Sprintf("answer number %d, with some detail attached", i)),
		)
	}

	m := Model{
		singleAgent: nil, // orchestrator mode
		runner:      orch,
		history:     history,
	}

	snap, ok := m.buildContextSnapshot("Coordinator")
	if !ok {
		t.Fatalf("buildContextSnapshot(Coordinator) ok=false, want true")
	}
	got := historyCategoryTokens(t, snap)

	wantComposed := tokenCount(buildQueryWithHistory(history, ""), model)
	if got != wantComposed {
		t.Errorf("History tokens = %d, want %d (tokenized composed/truncated window)", got, wantComposed)
	}

	var rawTotal int
	for _, msg := range history {
		rawTotal += tokenCount(msg.AsText(), model)
	}
	if got >= rawTotal {
		t.Errorf("History tokens (%d) should be less than the full raw transcript (%d) once truncated to the last 10 turns", got, rawTotal)
	}
}

// TestBuildContextSnapshot_SingleAgentWithConvMem_UsesComposedHistory is the
// regression test for Fix 1's other branch: single-agent mode with
// conversation memory active folds older turns into a summary via
// ConversationMemory.ComposeHistory before the next turn is sent. The
// History category must reflect that composed result, not raw m.history.
func TestBuildContextSnapshot_SingleAgentWithConvMem_UsesComposedHistory(t *testing.T) {
	bus := telemetry.NewEventBus(8)
	model := "gemini-3.5-flash-lite"
	a := agent.NewAgent(&config.AgentDefinition{
		Name:         "Coordinator",
		Role:         "worker",
		Model:        model,
		SystemPrompt: "You coordinate work by spawning subagents.",
	}, nil, tools.NewToolRegistry(), bus, nil)

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	convMem := memory.NewConversationMemory(store, "sess1", memory.ConversationOptions{KeepRecentTurns: 1})

	// Turns 1-2 carry a lot of padding so they dominate the raw transcript's
	// token count; turn 3 (the verbatim window that survives folding) is
	// short. This makes the fold's effect on the token count unmistakable —
	// not a coincidental match between two independently-sized texts.
	long := strings.Repeat("historical detail that gets folded away and must not be double-counted. ", 20)
	full := []llm.Message{
		llm.NewTextMessage("user", "question 1: "+long),
		llm.NewTextMessage("assistant", "answer 1: "+long),
		llm.NewTextMessage("user", "question 2: "+long),
		llm.NewTextMessage("assistant", "answer 2: "+long),
		llm.NewTextMessage("user", "question 3, short"),
		llm.NewTextMessage("assistant", "answer 3, short"),
	}
	// Fold turns 1-2 into a summary, keeping only the last turn verbatim.
	if err := convMem.Update(context.Background(), full, func(_ context.Context, _ string) (string, error) {
		return "SUMMARY OF EARLIER TURNS", nil
	}); err != nil {
		t.Fatalf("convMem.Update: %v", err)
	}

	m := Model{
		singleAgent: a,
		runner:      a,
		convMem:     convMem,
		history:     full,
	}

	snap, ok := m.buildContextSnapshot("Coordinator")
	if !ok {
		t.Fatalf("buildContextSnapshot(Coordinator) ok=false, want true")
	}
	got := historyCategoryTokens(t, snap)

	composed := convMem.ComposeHistory(full)
	var want int
	for _, msg := range composed {
		want += tokenCount(msg.AsText(), model)
	}
	if got != want {
		t.Errorf("History tokens = %d, want %d (tokenized ComposeHistory result)", got, want)
	}

	var rawTotal int
	for _, msg := range full {
		rawTotal += tokenCount(msg.AsText(), model)
	}
	if got >= rawTotal {
		t.Errorf("History tokens (%d) should be much less than the raw transcript (%d) once the padded turns are folded away", got, rawTotal)
	}
}
