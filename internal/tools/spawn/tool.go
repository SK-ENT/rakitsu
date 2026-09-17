package spawn

import (
	"context"
	"fmt"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Tool is the spawn_agent tool. Execute is called concurrently by the ReAct
// loop when the LLM emits several spawn_agent calls in one turn — that IS the
// parallel fan-out, so everything here must be goroutine-safe.
type Tool struct {
	factory   Factory
	cfg       config.SpawnConfig
	parent    string
	depth     int
	state     *RunState
	bus       *telemetry.EventBus
	templates []string
}

// NewTool builds a spawn tool bound to one parent agent.
func NewTool(f Factory, cfg config.SpawnConfig, parent string, depth int, state *RunState, bus *telemetry.EventBus, templates []string) *Tool {
	return &Tool{factory: f, cfg: cfg, parent: parent, depth: depth, state: state, bus: bus, templates: templates}
}

// GetName returns the tool's name.
func (t *Tool) GetName() string { return "spawn_agent" }

// GetDescription returns the tool's description for the LLM.
func (t *Tool) GetDescription() string {
	desc := "Spawn a subagent to work on a task and return its final result. " +
		"To fan out, call spawn_agent multiple times in the SAME response — calls in one turn run in parallel. " +
		"The subagent does not see this conversation, so make the task self-contained. " +
		"Do not ask the user for confirmation or permission before calling this tool — just call it."
	if len(t.templates) > 0 {
		desc += " Available agent templates: " + strings.Join(t.templates, ", ") + "."
	}
	return desc
}

// GetParametersSchema returns the JSON Schema for the tool's parameters.
func (t *Tool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Complete, self-contained task for the subagent, including any context it needs.",
			},
			"agent": map[string]interface{}{
				"type":        "string",
				"description": "Optional: config-defined agent to use as template. Omit for a general-purpose worker.",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Optional short display label for this subagent.",
			},
		},
		"required": []string{"task"},
	}
}

// Execute spawns one child and blocks until it finishes, times out, or the
// parent context is cancelled. On failure it returns BOTH a descriptive
// string (so the [TOOL ERROR] history marker and the parent LLM's next turn
// can react to readable text, matching the sibling invoke_config tool) AND a
// real Go error — the same registry convention every other tool follows —
// so repeated identical spawn failures still trip the repeated-failure
// hint and the rollback trigger instead of retrying forever unnoticed.
func (t *Tool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	task, _ := args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("spawn_agent needs a non-empty task")
	}
	template, _ := args["agent"].(string)
	label, _ := args["name"].(string)

	// Queue for a run-global slot BEFORE starting the child's timeout clock,
	// so queued children are not penalized for waiting.
	select {
	case t.state.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-t.state.sem }()

	base := label
	if base == "" {
		base = template
	}
	if base == "" {
		base = "subagent"
	}
	spec := Spec{
		Task:          task,
		TemplateAgent: template,
		Name:          t.state.uniqueName(base),
		Parent:        t.parent,
		Depth:         t.depth + 1,
	}

	// The configured per-child timeout bounds the factory call too — a
	// slow/hanging Factory (network calls, provider handshakes) would
	// otherwise have no deadline at all.
	cctx, cancel := context.WithTimeout(ctx, t.cfg.EffectiveTimeout())
	defer cancel()

	child, cleanup, err := t.factory(cctx, spec)
	if err != nil {
		return fmt.Sprintf("spawn_agent failed: %v", err), fmt.Errorf("spawn_agent failed: %w", err)
	}
	defer cleanup()

	t.bus.Emit(t.parent, telemetry.EventAgentHandoff, telemetry.AgentHandoffPayload{
		FromAgent: t.parent,
		ToAgent:   child.GetName(),
		Task:      task,
	})

	result, err := child.Run(cctx, task)
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			msg := fmt.Sprintf("subagent %q timed out after %ds", child.GetName(), int(t.cfg.EffectiveTimeout().Seconds()))
			return msg, fmt.Errorf("%s", msg)
		}
		msg := fmt.Sprintf("subagent %q failed: %v", child.GetName(), err)
		return msg, fmt.Errorf("subagent %q failed: %w", child.GetName(), err)
	}
	return result, nil
}
