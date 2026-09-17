package mock

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// ScenarioStep defines a single LLM response in a sequence.
type ScenarioStep struct {
	Response     string
	ToolCalls    []llm.ToolCall
	TokenUsage   llm.TokenUsage
	FinishReason string // "stop" or "tool_calls"
}

// Scenario is a deterministic sequence of LLM responses.
type Scenario struct {
	Steps []ScenarioStep
	Loop  bool // repeat from beginning when exhausted
	idx   int64
}

// Next returns the next step in the scenario.
func (s *Scenario) Next() *ScenarioStep {
	i := atomic.AddInt64(&s.idx, 1) - 1
	n := int64(len(s.Steps))
	if n == 0 {
		return &ScenarioStep{Response: "empty scenario", FinishReason: "stop"}
	}
	if s.Loop {
		return &s.Steps[i%n]
	}
	if i >= n {
		return &s.Steps[n-1] // repeat last step
	}
	return &s.Steps[i]
}

// Reset resets the scenario index.
func (s *Scenario) Reset() {
	atomic.StoreInt64(&s.idx, 0)
}

func toolCall(name string, args map[string]interface{}) llm.ToolCall {
	return llm.ToolCall{
		ID:        fmt.Sprintf("call_%s_%d", name, 1),
		Name:      name,
		Arguments: args,
	}
}

func tokens(in, out int) llm.TokenUsage {
	return llm.TokenUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

// SimpleAnswer returns a single text response.
func SimpleAnswer() *Scenario {
	return &Scenario{Steps: []ScenarioStep{
		{Response: "The answer is 42.", FinishReason: "stop", TokenUsage: tokens(100, 20)},
	}}
}

// SingleToolCall calls one tool then answers.
func SingleToolCall(toolName string) *Scenario {
	return &Scenario{Steps: []ScenarioStep{
		{
			ToolCalls:    []llm.ToolCall{toolCall(toolName, map[string]interface{}{"input": "test"})},
			FinishReason: "tool_calls",
			TokenUsage:   tokens(150, 50),
		},
		{
			Response:     "Based on the tool result, the answer is complete.",
			FinishReason: "stop",
			TokenUsage:   tokens(200, 30),
		},
	}}
}

// MultiToolChain calls N tools sequentially then answers.
func MultiToolChain(toolName string, n int) *Scenario {
	steps := make([]ScenarioStep, 0, n+1)
	for i := 0; i < n; i++ {
		steps = append(steps, ScenarioStep{
			ToolCalls: []llm.ToolCall{toolCall(toolName, map[string]interface{}{
				"input": fmt.Sprintf("step_%d", i),
			})},
			FinishReason: "tool_calls",
			TokenUsage:   tokens(100+i*20, 40+i*10),
		})
	}
	steps = append(steps, ScenarioStep{
		Response:     fmt.Sprintf("Completed %d tool calls successfully.", n),
		FinishReason: "stop",
		TokenUsage:   tokens(300, 50),
	})
	return &Scenario{Steps: steps}
}

// DeepIteration creates a scenario with many iterations of tool calls.
func DeepIteration(toolName string, iterations int) *Scenario {
	return MultiToolChain(toolName, iterations)
}

// LargeResponse returns a large text response.
func LargeResponse(sizeBytes int) *Scenario {
	text := strings.Repeat("x", sizeBytes)
	return &Scenario{Steps: []ScenarioStep{
		{Response: text, FinishReason: "stop", TokenUsage: tokens(100, sizeBytes/4)},
	}}
}

// DelegationPattern creates a supervisor scenario that delegates to N agents.
func DelegationPattern(agentNames []string) *Scenario {
	steps := make([]ScenarioStep, 0, len(agentNames)+1)
	for _, name := range agentNames {
		steps = append(steps, ScenarioStep{
			ToolCalls: []llm.ToolCall{toolCall(
				"delegate_to_"+name,
				map[string]interface{}{"task": fmt.Sprintf("Do the %s work", name)},
			)},
			FinishReason: "tool_calls",
			TokenUsage:   tokens(200, 60),
		})
	}
	steps = append(steps, ScenarioStep{
		Response:     "All agents completed their tasks.",
		FinishReason: "stop",
		TokenUsage:   tokens(400, 80),
	})
	return &Scenario{Steps: steps}
}

// LoopingToolCall creates an infinite looping scenario (for max_iterations testing).
func LoopingToolCall(toolName string) *Scenario {
	return &Scenario{
		Loop: true,
		Steps: []ScenarioStep{
			{
				ToolCalls:    []llm.ToolCall{toolCall(toolName, map[string]interface{}{"input": "loop"})},
				FinishReason: "tool_calls",
				TokenUsage:   tokens(100, 30),
			},
		},
	}
}
