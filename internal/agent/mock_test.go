package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// ============================================================
// sequenceProvider — returns pre-programmed responses in order
// ============================================================

type mockCall struct {
	SystemPrompt string
	History      []llm.Message
	Tools        []llm.ToolDefinition
}

// sequenceProvider returns responses[callIdx] on each Generate call, then increments callIdx.
// Panics if called more times than responses available (indicates test bug).
// Thread-safe.
type sequenceProvider struct {
	mu        sync.Mutex
	responses []llm.GenerateResult
	callIdx   int
	calls     []mockCall
	name      string
}

func newSequenceProvider(responses ...llm.GenerateResult) *sequenceProvider {
	return &sequenceProvider{responses: responses, name: "sequence"}
}

func (p *sequenceProvider) Generate(_ context.Context, systemPrompt string, history []llm.Message, toolDefs []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Record the call
	histCopy := make([]llm.Message, len(history))
	copy(histCopy, history)
	p.calls = append(p.calls, mockCall{
		SystemPrompt: systemPrompt,
		History:      histCopy,
		Tools:        toolDefs,
	})

	if p.callIdx >= len(p.responses) {
		panic(fmt.Sprintf("sequenceProvider: called %d times but only %d responses programmed", p.callIdx+1, len(p.responses)))
	}

	r := p.responses[p.callIdx]
	p.callIdx++
	return &r, nil
}

func (p *sequenceProvider) GetName() string  { return p.name }
func (p *sequenceProvider) GetModel() string { return "mock" }

func (p *sequenceProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callIdx
}

func (p *sequenceProvider) getCall(i int) mockCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[i]
}

// ============================================================
// errorProvider — returns error on Nth call
// ============================================================

type errorProvider struct {
	responses []llm.GenerateResult // returned before error
	err       error
	mu        sync.Mutex
	callIdx   int
}

func (p *errorProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.callIdx < len(p.responses) {
		r := p.responses[p.callIdx]
		p.callIdx++
		return &r, nil
	}
	p.callIdx++
	return nil, p.err
}

func (p *errorProvider) GetName() string  { return "error" }
func (p *errorProvider) GetModel() string { return "mock" }

// ============================================================
// mockTool — returns a fixed string output
// ============================================================

type mockTool struct {
	name   string
	desc   string
	output string
	err    error
	schema map[string]interface{}
	calls  []map[string]interface{} // recorded arguments
	mu     sync.Mutex
}

func newMockTool(name, output string) *mockTool {
	return &mockTool{
		name:   name,
		desc:   name + " tool",
		output: output,
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		},
	}
}

func (t *mockTool) GetName() string                    { return t.name }
func (t *mockTool) GetDescription() string             { return t.desc }
func (t *mockTool) GetParametersSchema() map[string]interface{} { return t.schema }

func (t *mockTool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	t.mu.Lock()
	t.calls = append(t.calls, args)
	t.mu.Unlock()
	// Return both output AND err — this matches the real cli tool's
	// CombinedOutput() behavior where stderr flows into the output string
	// even when the command exits non-zero. Tests that need the old
	// "err means no output" behavior should set output: "" explicitly.
	return t.output, t.err
}

func (t *mockTool) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

// ============================================================
// Response builders — concise helpers for test readability
// ============================================================

func stopResponse(text string) llm.GenerateResult {
	return llm.GenerateResult{
		Response:     text,
		FinishReason: "stop",
		TokenUsage:   &llm.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
	}
}

func toolCallResponse(text string, calls ...llm.ToolCall) llm.GenerateResult {
	return llm.GenerateResult{
		Response:     text,
		ToolCalls:    calls,
		FinishReason: "tool_calls",
		TokenUsage:   &llm.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
	}
}

func tc(name string, args map[string]interface{}) llm.ToolCall {
	return llm.ToolCall{
		ID:        fmt.Sprintf("call_%s", name),
		Name:      name,
		Arguments: args,
	}
}

func stopResponseTokens(text string, in, out int) llm.GenerateResult {
	return llm.GenerateResult{
		Response:     text,
		FinishReason: "stop",
		TokenUsage:   &llm.TokenUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out},
	}
}

func toolCallResponseTokens(text string, in, out int, calls ...llm.ToolCall) llm.GenerateResult {
	return llm.GenerateResult{
		Response:     text,
		ToolCalls:    calls,
		FinishReason: "tool_calls",
		TokenUsage:   &llm.TokenUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out},
	}
}

// ============================================================
// Agent/orchestrator construction helpers
// ============================================================

func newE2EAgent(name string, provider llm.LLMProvider, bus *telemetry.EventBus, mockTools ...*mockTool) *Agent {
	def := &config.AgentDefinition{
		Name:         name,
		SystemPrompt: "You are " + name,
	}
	registry := tools.NewToolRegistry()
	toolNames := make([]string, 0, len(mockTools))
	for _, mt := range mockTools {
		registry.RegisterTool(mt)
		toolNames = append(toolNames, mt.name)
	}
	def.Tools = toolNames
	return NewAgent(def, provider, registry, bus, nil)
}

func newE2EAgentWithSettings(name string, provider llm.LLMProvider, bus *telemetry.EventBus, settings *config.AgentSettings, mockTools ...*mockTool) *Agent {
	def := &config.AgentDefinition{
		Name:         name,
		SystemPrompt: "You are " + name,
		Settings:     settings,
	}
	registry := tools.NewToolRegistry()
	toolNames := make([]string, 0, len(mockTools))
	for _, mt := range mockTools {
		registry.RegisterTool(mt)
		toolNames = append(toolNames, mt.name)
	}
	def.Tools = toolNames
	return NewAgent(def, provider, registry, bus, nil)
}

func newPipelineOrch(name string, steps []config.PipelineStep, runners map[string]Runner, bus *telemetry.EventBus) *Orchestrator {
	orchConfig := &config.OrchestratorConfig{
		Name:     name,
		Strategy: "Pipeline",
		Agents:   extractRunnerNames(runners),
		Pipeline: &config.PipelineConfig{Steps: steps},
	}
	return NewOrchestrator(orchConfig, &fastProvider{answer: ""}, bus, runners)
}

func newPipelineOrchSynthesis(name string, steps []config.PipelineStep, runners map[string]Runner, bus *telemetry.EventBus, synthProvider llm.LLMProvider) *Orchestrator {
	orchConfig := &config.OrchestratorConfig{
		Name:     name,
		Strategy: "Pipeline",
		Agents:   extractRunnerNames(runners),
		Pipeline: &config.PipelineConfig{Steps: steps, Synthesis: true},
	}
	return NewOrchestrator(orchConfig, synthProvider, bus, runners)
}

func newReActOrch(name string, provider llm.LLMProvider, workers map[string]Runner, bus *telemetry.EventBus) *Orchestrator {
	orchConfig := &config.OrchestratorConfig{
		Name:     name,
		Strategy: "ReAct",
		Agents:   extractRunnerNames(workers),
	}
	return NewOrchestrator(orchConfig, provider, bus, workers)
}

func extractRunnerNames(runners map[string]Runner) []string {
	names := make([]string, 0, len(runners))
	for k := range runners {
		names = append(names, k)
	}
	return names
}

// ============================================================
// Event assertion helpers
// ============================================================

func filterEvents(events []telemetry.AgentEvent, types ...telemetry.EventType) []telemetry.AgentEvent {
	set := make(map[telemetry.EventType]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	var out []telemetry.AgentEvent
	for _, e := range events {
		if set[e.EventType] {
			out = append(out, e)
		}
	}
	return out
}

func eventTypes(events []telemetry.AgentEvent) []telemetry.EventType {
	types := make([]telemetry.EventType, len(events))
	for i, e := range events {
		types[i] = e.EventType
	}
	return types
}

func hasEventType(events []telemetry.AgentEvent, t telemetry.EventType) bool {
	for _, e := range events {
		if e.EventType == t {
			return true
		}
	}
	return false
}

func countEventType(events []telemetry.AgentEvent, t telemetry.EventType) int {
	n := 0
	for _, e := range events {
		if e.EventType == t {
			n++
		}
	}
	return n
}
