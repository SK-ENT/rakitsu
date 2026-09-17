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

// TestAgent_B50_FallsBackToThinkingContentWhenResponseEmpty verifies
// that when a reasoning model emits its final answer into the
// reasoning_content channel (ThinkingContent) instead of content
// (Response), the agent does not silently terminate with empty output.
//
// Repro context: nemotron-30B with the reasoning_content_field adapter
// in chat mode emits the entire visible answer into reasoning_content.
// Without this fallback, the chat agent's iter-1 finalAnswer = "" and
// the chat TUI shows nothing, even though the answer streamed correctly
// as TOKEN_CHUNK events.
func TestAgent_B50_FallsBackToThinkingContentWhenResponseEmpty(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	provider := &recordingProvider{
		name:  "test",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response:        "",
				ThinkingContent: "the actual answer the user should see",
				FinishReason:    "stop",
				TokenUsage:      &llm.TokenUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15},
			},
		},
	}

	def := &config.AgentDefinition{
		Name:         "TestAgent",
		Role:         "worker",
		SystemPrompt: "test",
	}
	a := agent.NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)

	result, err := a.Run(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result, "actual answer") {
		t.Errorf("expected fallback to ThinkingContent, got %q", result)
	}
}

// TestAgent_B50_ResponseWinsOverThinkingContent verifies the fallback
// only fires when Response is empty — when both fields have content
// (the normal reasoning-model case), Response remains the source of
// truth. We don't want to leak chain-of-thought into the visible answer
// when the model correctly populated the content channel.
func TestAgent_B50_ResponseWinsOverThinkingContent(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	provider := &recordingProvider{
		name:  "test",
		model: "test-model",
		scenario: []llm.GenerateResult{
			{
				Response:        "the visible answer",
				ThinkingContent: "internal reasoning the user should not see",
				FinishReason:    "stop",
				TokenUsage:      &llm.TokenUsage{InputTokens: 5, OutputTokens: 10, TotalTokens: 15},
			},
		},
	}

	def := &config.AgentDefinition{Name: "TestAgent", Role: "worker", SystemPrompt: "test"}
	a := agent.NewAgent(def, provider, tools.NewToolRegistry(), bus, nil)

	result, err := a.Run(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "the visible answer" {
		t.Errorf("expected Response to win, got %q", result)
	}
	if strings.Contains(result, "internal reasoning") {
		t.Errorf("ThinkingContent leaked into final answer: %q", result)
	}
}
