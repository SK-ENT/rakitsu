package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// TestOrchestrator_B49_EmptySupervisorFallsBackToWorker verifies that
// when the synthetic Hierarchical supervisor returns empty output (e.g.
// because reasoning models like nemotron sometimes finish thinking
// without emitting a final-answer text), the orchestrator falls back
// to the most recent worker output instead of returning "". This is
// the load-bearing half of the fallback fix — without it, the chat agent
// upstream sees empty and retries invoke_config indefinitely.
func TestOrchestrator_B49_EmptySupervisorFallsBackToWorker(t *testing.T) {
	bus := telemetry.NewEventBus(100)

	// Worker provider: produces a single substantive answer with no
	// further tool calls (so the worker's ReAct loop terminates cleanly
	// with a non-empty response).
	workerProvider := &recordingProvider{
		name:  "worker",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response:     "the worker's actual answer to the user's question",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			},
		},
	}
	workerDef := &config.AgentDefinition{
		Name:         "TestWorker",
		Role:         "worker",
		SystemPrompt: "you answer test questions",
		Tools:        nil,
	}
	worker := agent.NewAgent(workerDef, workerProvider, tools.NewToolRegistry(), bus, nil)

	// Supervisor provider: first iteration calls delegate_to_testworker;
	// second iteration produces empty response with no tool calls (the
	// failure mode this fallback patches against — supervisor ran, didn't synthesize,
	// returned empty).
	supervisorProvider := &recordingProvider{
		name:  "supervisor",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response: "I will delegate this.",
				ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "delegate_to_testworker",
					Arguments: map[string]interface{}{"task": "answer the user"},
				}},
			},
			{
				// Empty Response, no tool calls — supervisor "finished" but
				// emitted nothing. This is exactly what nemotron does after
				// a successful delegation when it spent its budget on
				// reasoning tokens and ran out of room to synthesize.
				Response:     "",
				FinishReason: "stop",
			},
		},
	}

	orchConfig := &config.OrchestratorConfig{
		Name:         "TestSupervisor",
		Strategy:     "Hierarchical",
		Model:        "test-model",
		SystemPrompt: "supervise the workers",
		Agents:       []string{"TestWorker"},
	}

	orch := agent.NewOrchestrator(orchConfig, supervisorProvider, bus, map[string]agent.Runner{
		"TestWorker": worker,
	})

	result, err := orch.Run(context.Background(), "user query")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == "" {
		t.Fatalf("supervisor fallback failed: orchestrator returned empty output despite worker producing a real answer (worker calls=%d, supervisor calls=%d)",
			len(workerProvider.getCalls()), len(supervisorProvider.getCalls()))
	}
	if !strings.Contains(result, "actual answer") {
		t.Errorf("expected fallback to worker output, got %q", result)
	}
}

// TestOrchestrator_B49_NoFallbackWhenSupervisorAnswers verifies the
// fallback only fires on empty supervisor output — when the supervisor
// produces a real synthesis text after delegation, that's what the
// orchestrator returns (not the worker output).
func TestOrchestrator_B49_NoFallbackWhenSupervisorAnswers(t *testing.T) {
	bus := telemetry.NewEventBus(100)

	workerProvider := &recordingProvider{
		name:  "worker",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response:     "raw worker output",
				FinishReason: "stop",
				TokenUsage:   &llm.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			},
		},
	}
	workerDef := &config.AgentDefinition{
		Name: "TestWorker", Role: "worker", SystemPrompt: "x",
	}
	worker := agent.NewAgent(workerDef, workerProvider, tools.NewToolRegistry(), bus, nil)

	supervisorProvider := &recordingProvider{
		name:  "supervisor",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response: "delegating",
				ToolCalls: []llm.ToolCall{{
					ID: "c", Name: "delegate_to_testworker",
					Arguments: map[string]interface{}{"task": "go"},
				}},
			},
			{
				Response:     "synthesized supervisor answer",
				FinishReason: "stop",
			},
		},
	}

	orchConfig := &config.OrchestratorConfig{
		Name: "S", Strategy: "Hierarchical", Model: "test-model",
		SystemPrompt: "x", Agents: []string{"TestWorker"},
	}
	orch := agent.NewOrchestrator(orchConfig, supervisorProvider, bus, map[string]agent.Runner{
		"TestWorker": worker,
	})

	result, err := orch.Run(context.Background(), "q")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "synthesized supervisor answer" {
		t.Errorf("supervisor synthesis must win over fallback, got %q", result)
	}
}
