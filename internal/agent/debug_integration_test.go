package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"

	"github.com/SK-ENT/rakitsu/internal/agent"
)

// recordingProvider is a mock LLM that records each Generate call,
// including what model name and system prompt the agent sent. This lets
// us verify that debug parameter overrides actually reached the LLM call.
//
// It also implements llm.OverrideAware so tests can verify that sampling
// parameter overrides (Temperature/TopP/MaxTokens) reach the provider via
// the GenerateWithOverride dispatch path.
type recordingProvider struct {
	mu       sync.Mutex
	calls    []recordedCall
	scenario []llm.GenerateResult
	idx      int
	name     string
	model    string
}

type recordedCall struct {
	Model        string             // model name visible via GetModel()
	SystemPrompt string             // system prompt passed to Generate()
	Override     *llm.OverrideConfig // non-nil when reached via GenerateWithOverride
}

func (p *recordingProvider) Generate(ctx context.Context, systemPrompt string, history []llm.Message, toolDefs []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return p.recordAndReply(systemPrompt, nil)
}

func (p *recordingProvider) GenerateWithOverride(ctx context.Context, systemPrompt string, history []llm.Message, toolDefs []llm.ToolDefinition, override llm.OverrideConfig) (*llm.GenerateResult, error) {
	return p.recordAndReply(systemPrompt, &override)
}

func (p *recordingProvider) recordAndReply(systemPrompt string, override *llm.OverrideConfig) (*llm.GenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, recordedCall{
		Model:        p.model,
		SystemPrompt: systemPrompt,
		Override:     override,
	})

	if p.idx < len(p.scenario) {
		r := p.scenario[p.idx]
		p.idx++
		return &r, nil
	}
	return &llm.GenerateResult{
		Response:     "done",
		FinishReason: "stop",
		TokenUsage:   &llm.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}, nil
}

func (p *recordingProvider) GetName() string  { return p.name }
func (p *recordingProvider) GetModel() string { return p.model }

func (p *recordingProvider) getCalls() []recordedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]recordedCall, len(p.calls))
	copy(out, p.calls)
	return out
}

// waitForState polls until the debug controller reaches the desired state.
func waitForState(dc *debug.DebugController, state debug.DebugState, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return false
		default:
			if dc.GetState() == state {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// waitForPauseAfterResume waits for the state to leave Paused (resume processed),
// then waits for it to return to Paused (next breakpoint hit).
func waitForPauseAfterResume(dc *debug.DebugController, timeout time.Duration) bool {
	deadline := time.After(timeout)
	// Phase 1: wait for state to leave Paused (resume was processed)
	for {
		select {
		case <-deadline:
			return false
		default:
			if dc.GetState() != debug.StatePaused {
				goto phase2
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
phase2:
	// Phase 2: wait for state to return to Paused (next breakpoint)
	for {
		select {
		case <-deadline:
			return false
		default:
			if dc.GetState() == debug.StatePaused {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TestDebug_BreakpointPauseOverrideResume is the critical integration test
// for Rakitsu's Agent Debugger. It verifies the complete real-world flow:
//
//  1. Developer sets a breakpoint on the agent's thought cycle
//  2. Agent runs and pauses at the breakpoint
//  3. Developer inspects and modifies parameters (system prompt, model)
//  4. Developer resumes execution
//  5. The modified parameters are used in the next LLM call
//
// Real-world scenario: debugging a code review agent — notice it's reviewing
// the wrong aspects, swap the system prompt mid-run to focus on security.
func TestDebug_BreakpointPauseOverrideResume(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)

	// Breakpoint: pause before every thought (pre_thought + wildcard agent)
	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "*"})

	// Mock: iteration 0 calls a tool, iteration 1 returns final answer
	mockProvider := &recordingProvider{
		name:  "mock",
		model: "gpt-4o-mini",
		scenario: []llm.GenerateResult{
			{
				ToolCalls: []llm.ToolCall{{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: map[string]interface{}{"path": "/src/main.go"},
				}},
				FinishReason: "tool_calls",
				TokenUsage:   &llm.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
			},
			{
				Response:     "Security audit: no critical issues found.",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150},
			},
		},
	}

	registry := tools.NewToolRegistry()
	registry.RegisterTool(&stubTool{name: "read_file"})

	agentDef := &config.AgentDefinition{
		Name:         "Reviewer",
		Role:         "worker",
		Model:        "gpt-4o-mini",
		SystemPrompt: "You are a general code reviewer. Review for style and correctness.",
		Tools:        []string{"read_file"},
		Settings:     &config.AgentSettings{MaxIterations: 10},
	}

	a := agent.NewAgent(agentDef, mockProvider, registry, bus, nil)
	a.SetDebugController(dc)

	// Track debug events
	var pauseCount int
	var resumeCount int
	var eventMu sync.Mutex
	sub := bus.Subscribe()
	go func() {
		for evt := range sub {
			eventMu.Lock()
			if evt.EventType == telemetry.EventDebugPaused {
				pauseCount++
			} else if evt.EventType == telemetry.EventDebugResumed {
				resumeCount++
			}
			eventMu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var result string
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, runErr = a.Run(ctx, "Review /src/main.go for security issues")
	}()

	// --- Agent hits breakpoint before first thought ---
	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("agent did not pause at breakpoint")
	}

	// Developer inspects the agent state, realizes the prompt is too general.
	// Swaps system prompt + model mid-run to focus on security.
	newModel := "gpt-4o"
	securityPrompt := "You are a security auditor. Focus exclusively on vulnerabilities, injection risks, and auth flaws."
	dc.SetOverrides("Reviewer", &debug.ParamOverride{
		Model:        &newModel,
		SystemPrompt: &securityPrompt,
		Sticky:       false, // one-shot: apply to just this call
	})

	// Clear breakpoints so it runs to completion after resume
	dc.ClearAllBreakpoints()
	dc.Resume(debug.ActionResume)

	wg.Wait()

	if runErr != nil {
		t.Fatalf("agent run failed: %v", runErr)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// --- VERIFY: overrides reached the LLM calls ---
	calls := mockProvider.getCalls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(calls))
	}

	// Call 0 (override applied while paused before this call): security prompt
	if calls[0].SystemPrompt != "You are a security auditor. Focus exclusively on vulnerabilities, injection risks, and auth flaws." {
		t.Errorf("call 0: system prompt should be overridden, got %q", calls[0].SystemPrompt)
	}

	// Call 1 (non-sticky override consumed, reverts to original)
	if calls[1].SystemPrompt != "You are a general code reviewer. Review for style and correctness." {
		t.Errorf("call 1: system prompt should revert to original, got %q", calls[1].SystemPrompt)
	}

	// Verify debug events were emitted. The subscriber goroutine drains events
	// asynchronously; poll briefly so this assertion isn't racy on slow runners.
	readCounts := func() (int, int) {
		eventMu.Lock()
		defer eventMu.Unlock()
		return pauseCount, resumeCount
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p, r := readCounts()
		if p >= 1 && r >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	p, r := readCounts()
	if p < 1 {
		t.Errorf("expected at least 1 DEBUG_PAUSED event, got %d", p)
	}
	if r < 1 {
		t.Errorf("expected at least 1 DEBUG_RESUMED event, got %d", r)
	}
}

// TestDebug_StickyOverridePersistsAcrossIterations verifies that sticky
// overrides persist across ALL remaining LLM calls, not just one.
//
// Real-world scenario: developer sets a sticky system prompt override
// to make the agent adopt a different persona for the rest of the run.
func TestDebug_StickyOverridePersistsAcrossIterations(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)

	// Pause at first thought only
	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "Worker"})

	mockProvider := &recordingProvider{
		name:  "mock",
		model: "base-model",
		scenario: []llm.GenerateResult{
			// 3 iterations: tool → tool → answer
			{
				ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a"}}},
				FinishReason: "tool_calls",
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70},
			},
			{
				ToolCalls:    []llm.ToolCall{{ID: "c2", Name: "read_file", Arguments: map[string]interface{}{"path": "b"}}},
				FinishReason: "tool_calls",
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70},
			},
			{
				Response:     "All done.",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 60, OutputTokens: 10, TotalTokens: 70},
			},
		},
	}

	registry := tools.NewToolRegistry()
	registry.RegisterTool(&stubTool{name: "read_file"})

	agentDef := &config.AgentDefinition{
		Name:         "Worker",
		Role:         "worker",
		SystemPrompt: "Original prompt.",
		Tools:        []string{"read_file"},
		Settings:     &config.AgentSettings{MaxIterations: 10},
	}

	a := agent.NewAgent(agentDef, mockProvider, registry, bus, nil)
	a.SetDebugController(dc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Run(ctx, "Do work")
	}()

	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("timeout waiting for pause")
	}

	// Set STICKY override — persists across ALL remaining iterations
	stickyPrompt := "Sticky: you are now a security auditor for the rest of this run."
	dc.SetOverrides("Worker", &debug.ParamOverride{
		SystemPrompt: &stickyPrompt,
		Sticky:       true,
	})

	dc.ClearAllBreakpoints()
	dc.Resume(debug.ActionResume)

	wg.Wait()

	calls := mockProvider.getCalls()
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 LLM calls, got %d", len(calls))
	}

	// ALL calls after override should use the sticky prompt
	for i, call := range calls {
		if call.SystemPrompt != "Sticky: you are now a security auditor for the rest of this run." {
			t.Errorf("call %d: prompt = %q, want sticky override (should persist)", i, call.SystemPrompt)
		}
	}
}

// TestDebug_NonStickyOverrideConsumedOnce verifies that non-sticky overrides
// are consumed on a single call and revert to the original on the next call.
//
// Real-world scenario: developer wants to test one call with a different
// prompt, then let the agent return to normal.
func TestDebug_NonStickyOverrideConsumedOnce(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)

	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "Agent"})

	mockProvider := &recordingProvider{
		name:  "mock",
		model: "base",
		scenario: []llm.GenerateResult{
			// 3 iterations: tool → tool → answer
			{
				ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a"}}},
				FinishReason: "tool_calls",
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70},
			},
			{
				ToolCalls:    []llm.ToolCall{{ID: "c2", Name: "read_file", Arguments: map[string]interface{}{"path": "b"}}},
				FinishReason: "tool_calls",
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70},
			},
			{
				Response:     "Done.",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 60, OutputTokens: 10, TotalTokens: 70},
			},
		},
	}

	registry := tools.NewToolRegistry()
	registry.RegisterTool(&stubTool{name: "read_file"})

	agentDef := &config.AgentDefinition{
		Name:         "Agent",
		Role:         "worker",
		SystemPrompt: "Original.",
		Tools:        []string{"read_file"},
		Settings:     &config.AgentSettings{MaxIterations: 10},
	}

	a := agent.NewAgent(agentDef, mockProvider, registry, bus, nil)
	a.SetDebugController(dc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Run(ctx, "Work")
	}()

	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("timeout waiting for pause")
	}

	// Non-sticky override — consumed after one use
	oneShot := "One-shot experimental prompt."
	dc.SetOverrides("Agent", &debug.ParamOverride{
		SystemPrompt: &oneShot,
		Sticky:       false,
	})

	dc.ClearAllBreakpoints()
	dc.Resume(debug.ActionResume)

	wg.Wait()

	calls := mockProvider.getCalls()
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 LLM calls, got %d", len(calls))
	}

	// Call 0: one-shot override applied
	if calls[0].SystemPrompt != "One-shot experimental prompt." {
		t.Errorf("call 0: prompt = %q, want one-shot override", calls[0].SystemPrompt)
	}

	// Calls 1+: should revert to original
	for i := 1; i < len(calls); i++ {
		if calls[i].SystemPrompt != "Original." {
			t.Errorf("call %d: prompt = %q, want original (non-sticky should be consumed)", i, calls[i].SystemPrompt)
		}
	}
}

// TestDebug_StopAction_HaltsExecution verifies the debugger's emergency
// stop: user clicks "Stop" while agent is paused, execution terminates.
func TestDebug_StopAction_HaltsExecution(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)
	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "*"})

	mockProvider := &recordingProvider{
		name:  "mock",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{Response: "Should never reach here", FinishReason: "stop",
				TokenUsage: &llm.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
		},
	}

	agentDef := &config.AgentDefinition{
		Name:         "Victim",
		Role:         "worker",
		SystemPrompt: "Test.",
		Settings:     &config.AgentSettings{MaxIterations: 5},
	}

	a := agent.NewAgent(agentDef, mockProvider, tools.NewToolRegistry(), bus, nil)
	a.SetDebugController(dc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, runErr = a.Run(ctx, "Hello")
	}()

	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("timeout waiting for pause")
	}

	dc.Resume(debug.ActionStop)
	wg.Wait()

	if runErr == nil {
		t.Fatal("expected error from stopped agent, got nil")
	}

	// LLM should NOT have been called — stopped before first Generate
	calls := mockProvider.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 LLM calls (stopped before Generate), got %d", len(calls))
	}
}

// TestDebug_MaxIterationsOverride verifies that the debugger can change
// max_iterations mid-run — useful when the agent is in an infinite tool loop.
//
// Real-world scenario: agent is stuck calling the same tool repeatedly,
// developer caps iterations to force a final answer.
func TestDebug_MaxIterationsOverride(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)

	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "Looper"})

	callCount := 0
	mockProvider := &recordingProvider{
		name:  "mock",
		model: "model",
		scenario: []llm.GenerateResult{
			// All iterations call a tool — would loop forever without max_iterations
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]interface{}{"path": "a"}}}, FinishReason: "tool_calls", TokenUsage: &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70}},
			{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "read_file", Arguments: map[string]interface{}{"path": "b"}}}, FinishReason: "tool_calls", TokenUsage: &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70}},
			{ToolCalls: []llm.ToolCall{{ID: "c3", Name: "read_file", Arguments: map[string]interface{}{"path": "c"}}}, FinishReason: "tool_calls", TokenUsage: &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70}},
			{ToolCalls: []llm.ToolCall{{ID: "c4", Name: "read_file", Arguments: map[string]interface{}{"path": "d"}}}, FinishReason: "tool_calls", TokenUsage: &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70}},
			{ToolCalls: []llm.ToolCall{{ID: "c5", Name: "read_file", Arguments: map[string]interface{}{"path": "e"}}}, FinishReason: "tool_calls", TokenUsage: &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70}},
		},
	}
	_ = callCount

	registry := tools.NewToolRegistry()
	registry.RegisterTool(&stubTool{name: "read_file"})

	agentDef := &config.AgentDefinition{
		Name:         "Looper",
		Role:         "worker",
		SystemPrompt: "You analyze files.",
		Tools:        []string{"read_file"},
		Settings:     &config.AgentSettings{MaxIterations: 20}, // high limit
	}

	a := agent.NewAgent(agentDef, mockProvider, registry, bus, nil)
	a.SetDebugController(dc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Run(ctx, "Analyze")
	}()

	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("timeout waiting for pause")
	}

	// Developer sees the agent will loop — caps iterations to 2
	maxIter := 2
	dc.SetOverrides("Looper", &debug.ParamOverride{
		MaxIterations: &maxIter,
		Sticky:        true,
	})

	dc.ClearAllBreakpoints()
	dc.Resume(debug.ActionResume)
	wg.Wait()

	// Should have stopped after ~2 iterations (not 20)
	calls := mockProvider.getCalls()
	if len(calls) > 3 {
		t.Errorf("expected max ~3 LLM calls (capped at 2 iterations), got %d", len(calls))
	}
}

// TestDebug_SamplingOverridesReachProvider verifies that Temperature, TopP,
// and MaxTokens overrides actually reach the LLM provider via the
// OverrideAware dispatch path. This was previously broken: the values were
// stored in the wrapper but never made it to the underlying API call.
//
// Real-world scenario: developer is debugging a creative-writing agent that
// is producing deterministic, repetitive output. Mid-run, they raise the
// temperature to encourage variation and verify the change took effect.
func TestDebug_SamplingOverridesReachProvider(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	dc := debug.NewDebugController(bus)

	dc.SetBreakpoint(debug.BreakpointKey{EventType: "pre_thought", AgentName: "Writer"})

	mockProvider := &recordingProvider{
		name:  "mock",
		model: "base-model",
		scenario: []llm.GenerateResult{
			{
				Response:     "Once upon a time...",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 50, OutputTokens: 20, TotalTokens: 70},
			},
		},
	}

	agentDef := &config.AgentDefinition{
		Name:         "Writer",
		Role:         "worker",
		SystemPrompt: "Write creatively.",
		Settings:     &config.AgentSettings{MaxIterations: 5},
	}

	a := agent.NewAgent(agentDef, mockProvider, tools.NewToolRegistry(), bus, nil)
	a.SetDebugController(dc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Run(ctx, "Tell me a story")
	}()

	if !waitForState(dc, debug.StatePaused, 3*time.Second) {
		t.Fatal("timeout waiting for pause")
	}

	temp := 0.95
	topP := 0.85
	maxTok := 512
	dc.SetOverrides("Writer", &debug.ParamOverride{
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   &maxTok,
		Sticky:      false,
	})

	dc.ClearAllBreakpoints()
	dc.Resume(debug.ActionResume)
	wg.Wait()

	calls := mockProvider.getCalls()
	if len(calls) < 1 {
		t.Fatalf("expected at least 1 LLM call, got %d", len(calls))
	}

	// The override-aware dispatch path must have been taken.
	if calls[0].Override == nil {
		t.Fatal("call 0: expected GenerateWithOverride dispatch, got plain Generate")
	}
	if calls[0].Override.Temperature == nil || *calls[0].Override.Temperature != temp {
		t.Errorf("call 0: temperature = %+v, want %v", calls[0].Override.Temperature, temp)
	}
	if calls[0].Override.TopP == nil || *calls[0].Override.TopP != topP {
		t.Errorf("call 0: top_p = %+v, want %v", calls[0].Override.TopP, topP)
	}
	if calls[0].Override.MaxTokens == nil || *calls[0].Override.MaxTokens != maxTok {
		t.Errorf("call 0: max_tokens = %+v, want %v", calls[0].Override.MaxTokens, maxTok)
	}
}

// stubTool implements tools.Tool for debug integration tests.
type stubTool struct {
	name string
}

func (s *stubTool) GetName() string        { return s.name }
func (s *stubTool) GetDescription() string { return "stub tool for testing" }
func (s *stubTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return "file content: function main() { ... }", nil
}
func (s *stubTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
	}
}
