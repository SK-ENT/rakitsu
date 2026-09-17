// Package chathost provides the discovery and delegation tools used by
// the chat-host meta-agent that wraps any interactive config.
//
// The chat-host sits between the user's chat input and the underlying
// root Runner (single agent, pipeline, orchestrator). It exposes:
//
//   - invoke_config(task):      delegate substantive work to the root Runner
//   - list_agents():            discover what agents exist in the config
//   - describe_agent(name):     get one agent's role + system_prompt + tools
//
// This keeps the chat-host's system prompt small (independent of config
// size) while letting it discover capabilities on demand.
package chathost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// InvokeTool resets per-turn loop-guard state via tools.TurnResetter.
var _ tools.TurnResetter = (*InvokeTool)(nil)

// --- invoke_config ---

// InvokeTool delegates a task to the underlying root Runner.
// When forced is true, the tool description drops the "reply directly"
// escape hatch — used with config.ForceDelegation to defeat weak models
// that would otherwise bypass delegation.
type InvokeTool struct {
	root   agent.Runner
	forced bool

	// Loop guards for repeated invoke_config calls within one user turn.
	// A chat agent that fails to recognise a finished answer re-runs the
	// whole orchestration — observed first as repeated byte-identical
	// re-invocations, then, after the same-task guard shipped, as
	// re-invocations with a *rephrased* task. Two guards:
	//
	//   - repeat-after-empty: the root Runner returned empty output
	//     and the chat agent invokes the same task again — return a
	//     stalled-sentinel instead of re-running.
	//   - per-turn answered: once this user turn has produced a
	//     non-empty orchestration result, any further invoke_config call
	//     — same task OR rephrased — returns that result with an explicit
	//     "relay it, don't re-run" instruction. turnAnswered/turnResult
	//     are cleared by ResetTurn at the start of each user turn.
	mu           sync.Mutex
	lastTask     string
	lastWasEmpty bool
	turnAnswered bool
	turnResult   string
}

// ResetTurn clears per-user-turn loop-guard state. The agent loop calls
// this at the start of each Run/RunWithHistory; for the ChatHost agent
// that is exactly one user turn. Implements tools.TurnResetter.
func (t *InvokeTool) ResetTurn() {
	t.mu.Lock()
	t.lastTask = ""
	t.lastWasEmpty = false
	t.turnAnswered = false
	t.turnResult = ""
	t.mu.Unlock()
}

func NewInvokeTool(root agent.Runner, forced bool) *InvokeTool {
	return &InvokeTool{root: root, forced: forced}
}

// stalledSentinel is returned when the chat agent re-invokes a task that
// just returned empty. Its content is non-empty so the chat agent's own
// loop sees a usable result and stops retrying; its phrasing tells the
// user to rephrase rather than implying the system silently failed.
const stalledSentinel = "(The underlying system returned no answer for this request even after retry. Please rephrase your question or ask something different — the supervisor may be unable to synthesize across prior turns in this configuration.)"

// alreadyAnsweredPrefix is prepended to the cached answer when the chat
// agent re-invokes invoke_config within a turn that already produced a
// non-empty result. It hands the prior answer back so the chat
// agent can relay it, with an
// explicit instruction not to re-run the orchestration.
const alreadyAnsweredPrefix = "(invoke_config was already called with this exact task and the underlying system produced the answer below. The work is done — do NOT call invoke_config again for this task. Relay this answer to the user now.)\n\n"

func (t *InvokeTool) GetName() string { return "invoke_config" }

func (t *InvokeTool) GetDescription() string {
	if t.forced {
		return "Delegate the user's request to the underlying configured system (pipeline, orchestrator, or agent). This is the ONLY way to produce an answer for the user. Every user turn must result in exactly one call to this tool. Do not answer from your own knowledge."
	}
	return "Delegate the actual work to the underlying configured system (pipeline, orchestrator, or agent). Use this when the user requests the substantive work this system is designed for — e.g. running the pipeline, performing analysis, generating output. For chitchat or meta questions, reply directly without calling this tool."
}

func (t *InvokeTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task for the underlying system. Pass the user's request (or a rephrased version that matches the system's expected input).",
			},
		},
		"required": []string{"task"},
	}
}

func (t *InvokeTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	task, _ := args["task"].(string)
	if task == "" {
		return "", fmt.Errorf("task is required")
	}

	// Loop guards for a repeated invoke_config call within one user turn.
	// The whole check → run → state-update sequence is held as ONE critical
	// section (the lock is not released around t.root.Run) because the
	// ReAct loop executes every tool call within one LLM turn concurrently,
	// via goroutines (internal/agent/agent.go). Releasing the lock around
	// the run would let two concurrent invoke_config calls both pass the
	// guard while it was still unset and both call root.Run — doubling the
	// orchestration run this guard exists to prevent. Holding the lock the
	// whole time makes a second concurrent call block until the first
	// finishes, then see the now-set guard state and return the
	// cached/stalled result instead of racing past it.
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.turnAnswered {
		// This user turn already produced a non-empty orchestration
		// result. Any further invoke_config call — the same task or a
		// rephrased one — re-runs the whole orchestration and loops the
		// user. Hand the turn's answer back instead. State is held until
		// ResetTurn fires at the next user turn, so the loop is fully
		// contained even if the chat agent ignores the instruction.
		return alreadyAnsweredPrefix + t.turnResult, nil
	}
	if t.lastTask == task && t.lastWasEmpty {
		// The previous run of this exact task returned empty. Return
		// a stalled-sentinel and reset so a later genuine retry re-runs.
		t.lastTask = ""
		t.lastWasEmpty = false
		return stalledSentinel, nil
	}

	result, err := t.root.Run(ctx, task)

	switch {
	case err != nil:
		// Errors may be transient — clear state so a retry re-runs.
		t.lastTask = ""
		t.lastWasEmpty = false
	case strings.TrimSpace(result) == "":
		// Empty result does not "answer" the turn; a rephrased retry is
		// still allowed — this covers the same-task empty repeat.
		t.lastTask = task
		t.lastWasEmpty = true
	default:
		t.lastTask = task
		t.lastWasEmpty = false
		t.turnAnswered = true
		t.turnResult = result
	}

	return result, err
}

// --- list_agents ---

// ListAgentsTool reports a summary of agents defined in the config.
type ListAgentsTool struct {
	cfg *config.Config
}

func NewListAgentsTool(cfg *config.Config) *ListAgentsTool {
	return &ListAgentsTool{cfg: cfg}
}

func (t *ListAgentsTool) GetName() string { return "list_agents" }

func (t *ListAgentsTool) GetDescription() string {
	return "List all agents defined in this config, each with role + a one-line summary. Use describe_agent(name) for the full system_prompt and tool list of a specific agent."
}

func (t *ListAgentsTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

type agentSummary struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Summary string `json:"summary"`
}

func (t *ListAgentsTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	out := make([]agentSummary, 0, len(t.cfg.Agents))
	for i := range t.cfg.Agents {
		a := &t.cfg.Agents[i]
		out = append(out, agentSummary{
			Name:    a.Name,
			Role:    a.Role,
			Summary: firstSentence(a.SystemPrompt),
		})
	}
	// Also list orchestrators (top-level + sub) so the LLM can see the full shape.
	if t.cfg.Orchestrator != nil {
		out = append(out, agentSummary{
			Name:    t.cfg.Orchestrator.Name,
			Role:    "orchestrator:" + t.cfg.Orchestrator.Strategy,
			Summary: fmt.Sprintf("Orchestrates %d agents: %s", len(t.cfg.Orchestrator.Agents), strings.Join(t.cfg.Orchestrator.Agents, ", ")),
		})
	}
	for i := range t.cfg.Orchestrators {
		o := &t.cfg.Orchestrators[i]
		out = append(out, agentSummary{
			Name:    o.Name,
			Role:    "sub-orchestrator:" + o.Strategy,
			Summary: fmt.Sprintf("Sub-orchestrator over: %s", strings.Join(o.Agents, ", ")),
		})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

// --- describe_agent ---

// DescribeAgentTool returns full details for a single agent or orchestrator.
type DescribeAgentTool struct {
	cfg *config.Config
}

func NewDescribeAgentTool(cfg *config.Config) *DescribeAgentTool {
	return &DescribeAgentTool{cfg: cfg}
}

func (t *DescribeAgentTool) GetName() string { return "describe_agent" }

func (t *DescribeAgentTool) GetDescription() string {
	return "Get the full details for a specific agent or orchestrator by name: role, system_prompt, tools list, and (for orchestrators) strategy and member agents. Use this after list_agents to drill down."
}

func (t *DescribeAgentTool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "The name of the agent or orchestrator to describe.",
			},
		},
		"required": []string{"name"},
	}
}

type agentDetail struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Strategy     string   `json:"strategy,omitempty"`
	Agents       []string `json:"agents,omitempty"`
}

func (t *DescribeAgentTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	for i := range t.cfg.Agents {
		a := &t.cfg.Agents[i]
		if a.Name == name {
			d := agentDetail{
				Name:         a.Name,
				Role:         a.Role,
				Provider:     a.Provider,
				Model:        a.Model,
				SystemPrompt: a.SystemPrompt,
				Tools:        a.Tools,
			}
			b, _ := json.MarshalIndent(d, "", "  ")
			return string(b), nil
		}
	}
	if t.cfg.Orchestrator != nil && t.cfg.Orchestrator.Name == name {
		o := t.cfg.Orchestrator
		d := agentDetail{
			Name:         o.Name,
			Role:         "orchestrator",
			Provider:     o.Provider,
			Model:        o.Model,
			SystemPrompt: o.SystemPrompt,
			Strategy:     o.Strategy,
			Agents:       o.Agents,
		}
		b, _ := json.MarshalIndent(d, "", "  ")
		return string(b), nil
	}
	for i := range t.cfg.Orchestrators {
		o := &t.cfg.Orchestrators[i]
		if o.Name == name {
			d := agentDetail{
				Name:         o.Name,
				Role:         "sub-orchestrator",
				Provider:     o.Provider,
				Model:        o.Model,
				SystemPrompt: o.SystemPrompt,
				Strategy:     o.Strategy,
				Agents:       o.Agents,
			}
			b, _ := json.MarshalIndent(d, "", "  ")
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no agent or orchestrator named %q — call list_agents to see available names", name)
}

// --- helpers ---

// firstSentence returns the first line or sentence of a system_prompt as
// a short summary (keeps list_agents output compact).
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// First newline break
	if idx := strings.Index(s, "\n"); idx > 0 {
		s = s[:idx]
	}
	// Cap length
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
