package agent

import (
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Step represents a single ReAct iteration's execution data.
type Step struct {
	Iteration   int
	Thought     string
	ToolCalls   []llm.ToolCall
	ToolResults []ToolOutput
	Reflection  string
	TokensIn    int
	TokensOut   int
}

// ToolOutput captures a single tool call result.
type ToolOutput struct {
	CallID   string
	ToolName string
	// RawOutput is the full untruncated output. Preserved for telemetry events.
	// The ContextMonitor may truncate/fence the version sent to the LLM.
	RawOutput string
	Error     string
	Duration  int64
	Metadata  map[string]interface{}
}

// StepLog is the external execution state store.
// It holds the full record of all steps so the ContextMonitor can build
// compact context for the LLM without losing information.
type StepLog struct {
	Query string
	Steps []Step
}

// NewStepLog creates a new step log for the given query.
func NewStepLog(query string) *StepLog {
	return &StepLog{Query: query}
}

// AddStep appends a completed step to the log.
func (sl *StepLog) AddStep(s Step) {
	sl.Steps = append(sl.Steps, s)
}

// LastStep returns the most recent step, or nil if empty.
func (sl *StepLog) LastStep() *Step {
	if len(sl.Steps) == 0 {
		return nil
	}
	return &sl.Steps[len(sl.Steps)-1]
}

// Len returns the number of recorded steps.
func (sl *StepLog) Len() int {
	return len(sl.Steps)
}
