package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/acp"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// convFakeProvider records every Generate call and answers without a network.
type convFakeProvider struct {
	mu    sync.Mutex
	calls []convFakeCall
}

type convFakeCall struct {
	system  string
	history []llm.Message
}

func (p *convFakeProvider) Generate(_ context.Context, system string, history []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, convFakeCall{system: system, history: append([]llm.Message(nil), history...)})
	if strings.HasPrefix(system, "You maintain a rolling summary") {
		return &llm.GenerateResult{Response: "FAKE SUMMARY", FinishReason: "stop"}, nil
	}
	return &llm.GenerateResult{Response: "fake answer", FinishReason: "stop"}, nil
}
func (p *convFakeProvider) GetName() string  { return "convfake" }
func (p *convFakeProvider) GetModel() string { return "convfake-1" }

func convTestConfig(t *testing.T, convOn bool) (*config.Config, *convFakeProvider) {
	t.Helper()
	prov := &convFakeProvider{}
	RegisterProviderFactory("convfake", func(context.Context, *llm.ProviderConfig) (llm.LLMProvider, error) {
		return prov, nil
	})
	t.Cleanup(func() { delete(providerFactories, "convfake") })

	cfg := &config.Config{Name: "conv-test"}
	cfg.Settings.DefaultProvider = "convfake"
	cfg.Settings.APIKeys = map[string]string{"convfake": "test-key"}
	cfg.Settings.Memory.Enabled = true
	cfg.Settings.Memory.Dir = t.TempDir()
	cfg.Settings.Memory.Conversation = config.ConversationMemoryConfig{
		Enabled: convOn, KeepRecentTurns: 1, SummaryMaxChars: 500,
	}
	cfg.Agents = []config.AgentDefinition{{Name: "solo", Role: "worker", Model: "convfake-1", SystemPrompt: "be brief"}}
	return cfg, prov
}

func runConv(t *testing.T, cfg *config.Config, query string, conv *acp.ConvTurn) string {
	t.Helper()
	out, err := executeConfigConv(context.Background(), cfg, telemetry.NewEventBus(100), nil, query, nil, conv)
	if err != nil {
		t.Fatalf("executeConfigConv: %v", err)
	}
	return out
}

func TestExecuteConfigConv_HistoryReachesAgentAndSummarizeIsWired(t *testing.T) {
	cfg, prov := convTestConfig(t, true)
	hist := []llm.Message{
		llm.NewTextMessage("user", "## Conversation summary\nEARLIER FACTS"),
		llm.NewTextMessage("assistant", "ack"),
		llm.NewTextMessage("user", "recent question"),
		llm.NewTextMessage("assistant", "recent answer"),
	}
	conv := &acp.ConvTurn{History: hist}

	if got := runConv(t, cfg, "next question", conv); got != "fake answer" {
		t.Fatalf("response = %q", got)
	}
	if len(prov.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(prov.calls))
	}
	var seen []string
	for _, m := range prov.calls[0].history {
		seen = append(seen, m.AsText())
	}
	joined := strings.Join(seen, "|")
	for _, want := range []string{"EARLIER FACTS", "recent question", "recent answer", "next question"} {
		if !strings.Contains(joined, want) {
			t.Errorf("agent history missing %q: %v", want, seen)
		}
	}
	if strings.Index(joined, "EARLIER FACTS") > strings.Index(joined, "next question") {
		t.Errorf("history out of order: %v", seen)
	}

	if conv.Summarize == nil {
		t.Fatal("conv.Summarize was not set")
	}

	// Drive the real fold: after enough turns the summarizer runs through the
	// agent's provider and the rolling summary is stored.
	cm := memory.NewConversationMemory(openMemoryStore(cfg), "acp-test-session", memory.ConversationOptions{KeepRecentTurns: 1, SummaryMaxChars: 500})
	var transcript []llm.Message
	for _, q := range []string{"one", "two", "three"} {
		transcript = append(transcript, llm.NewTextMessage("user", q), llm.NewTextMessage("assistant", "re "+q))
	}
	if err := cm.Update(context.Background(), transcript, conv.Summarize); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := cm.Summary(); got != "FAKE SUMMARY" {
		t.Fatalf("summary = %q, want FAKE SUMMARY", got)
	}
	last := prov.calls[len(prov.calls)-1]
	if !strings.HasPrefix(last.system, "You maintain a rolling summary") {
		t.Fatalf("summarizer did not go through the agent provider; last system prompt %q", last.system)
	}
}

func TestExecuteConfigConv_NilConvKeepsTextPath(t *testing.T) {
	cfg, prov := convTestConfig(t, false)
	query := "## Conversation so far\nuser: hi\n\nnow this"
	if got := runConv(t, cfg, query, nil); got != "fake answer" {
		t.Fatalf("response = %q", got)
	}
	if len(prov.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(prov.calls))
	}
	h := prov.calls[0].history
	if len(h) != 1 || h[0].Role != "user" || h[0].AsText() != query {
		var seen []string
		for _, m := range h {
			seen = append(seen, m.Role+":"+m.AsText())
		}
		t.Fatalf("text path changed: history = %v, want exactly the composed query", seen)
	}
}
