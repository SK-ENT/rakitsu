// Package debug provides debug tooling for agent execution.
// All features are gated by --debug-port > 0; when DebugController is nil,
// agents run with zero overhead.
package debug

import (
	"context"
	"fmt"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// Attachable is implemented by agents and orchestrators that support mid-run debug attach/detach.
type Attachable interface {
	SetDebugController(dc *DebugController)
}

// DebugState represents the current execution state.
type DebugState string

const (
	StateRunning  DebugState = "running"
	StatePaused   DebugState = "paused"
	StateStepping DebugState = "stepping"
)

// ResumeAction defines how to continue after a pause.
type ResumeAction string

const (
	ActionResume   ResumeAction = "resume"
	ActionStep     ResumeAction = "step"
	ActionStepIn   ResumeAction = "step_in"
	ActionStepOut  ResumeAction = "step_out"
	ActionRunUntil ResumeAction = "run_until"
	ActionStop     ResumeAction = "stop"
)

// BreakpointKey identifies a breakpoint by event type and agent name.
// Use "*" as wildcard to match all event types or all agents.
type BreakpointKey struct {
	EventType string `json:"event_type"`
	AgentName string `json:"agent_name"`
}

// CheckpointContext carries execution state at a debug checkpoint.
// Passed to Check() so the frontend can inspect what the agent is doing.
type CheckpointContext struct {
	LastThought    string            `json:"last_thought,omitempty"`
	PendingTools   []PendingToolInfo `json:"pending_tools,omitempty"`
	HistoryLength  int               `json:"history_length"`
	TotalTokensIn  int               `json:"total_tokens_in"`
	TotalTokensOut int               `json:"total_tokens_out"`
	MaxTokens      int               `json:"max_tokens"`
	MaxIterations  int               `json:"max_iterations"`
	ContextWindow       int               `json:"context_window,omitempty"`
	PredictedNextTokens int               `json:"predicted_next_tokens,omitempty"`
	BudgetRatio         float64           `json:"budget_ratio,omitempty"`
}

// PendingToolInfo describes a tool call about to be executed.
type PendingToolInfo struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// ParamOverride holds parameter overrides for an agent's LLM calls and behavior.
type ParamOverride struct {
	// LLM parameters
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Model       *string  `json:"model,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	// Agent-level overrides
	SystemPrompt  *string  `json:"system_prompt,omitempty"`
	MaxIterations *int     `json:"max_iterations,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	Sticky        bool     `json:"sticky"`
}

// RunUntilCondition specifies when to pause during a "run until" action.
// All non-empty fields must match (AND logic). Empty fields are wildcards.
type RunUntilCondition struct {
	Checkpoint string `json:"checkpoint,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Iteration  int    `json:"iteration,omitempty"` // 0 = any
}

// RerunRequest signals the pipeline to restart execution from a named step.
type RerunRequest struct {
	StepName  string
	Overrides map[string]interface{}
}

// pendingResume is what Resume() actually sends on resumeCh. seq ties the
// action to the specific pause it was claimed for (DebugController.pauseSeq
// at claim time) — see the comment on Resume() for why this is needed.
type pendingResume struct {
	action ResumeAction
	seq    uint64
}

// DebugController coordinates breakpoints, pause/resume, and parameter overrides.
type DebugController struct {
	mu          sync.RWMutex
	breakpoints map[BreakpointKey]bool
	overrides   map[string]*ParamOverride // agent name → override
	state       DebugState
	resumeCh    chan pendingResume
	rerunCh     chan RerunRequest
	eventBus    *telemetry.EventBus

	// pauseSeq identifies the current pause instance, incremented every time
	// Check() transitions into StatePaused. Lets Check() tell a resume meant
	// for ITS pause apart from a stale one claimed for an earlier pause that
	// already ended via context cancellation — see Resume()'s doc comment.
	pauseSeq uint64

	// Step In: pause at first checkpoint inside this agent
	stepIntoAgent string
	// Step Out: resume until we leave this agent scope
	stepOutAgent string
	// Run Until: one-shot condition
	runUntilCond *RunUntilCondition
}

// NewDebugController creates a new debug controller.
func NewDebugController(eventBus *telemetry.EventBus) *DebugController {
	return &DebugController{
		breakpoints: make(map[BreakpointKey]bool),
		overrides:   make(map[string]*ParamOverride),
		state:       StateRunning,
		resumeCh:    make(chan pendingResume, 1),
		rerunCh:     make(chan RerunRequest, 1),
		eventBus:    eventBus,
	}
}

// RerunFromStep signals the pipeline to restart execution from the named step.
// Non-blocking: if a signal is already pending, the new one is dropped.
func (dc *DebugController) RerunFromStep(stepName string, overrides map[string]interface{}) {
	select {
	case dc.rerunCh <- RerunRequest{StepName: stepName, Overrides: overrides}:
	default:
	}
}

// RerunCh returns the channel that pipeline runners should select on for rerun signals.
func (dc *DebugController) RerunCh() <-chan RerunRequest {
	return dc.rerunCh
}

// SetBreakpoint adds a breakpoint.
func (dc *DebugController) SetBreakpoint(key BreakpointKey) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.breakpoints[key] = true
}

// ClearBreakpoint removes a breakpoint.
func (dc *DebugController) ClearBreakpoint(key BreakpointKey) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.breakpoints, key)
}

// ClearAllBreakpoints removes all breakpoints.
func (dc *DebugController) ClearAllBreakpoints() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.breakpoints = make(map[BreakpointKey]bool)
}

// ListBreakpoints returns all active breakpoints.
func (dc *DebugController) ListBreakpoints() []BreakpointKey {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	keys := make([]BreakpointKey, 0, len(dc.breakpoints))
	for k := range dc.breakpoints {
		keys = append(keys, k)
	}
	return keys
}

// GetState returns the current debug state.
func (dc *DebugController) GetState() DebugState {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.state
}

// Check inspects whether execution should pause at this checkpoint.
// It blocks until resumed if a breakpoint matches or the controller is in stepping mode.
// cpCtx is optional — when non-nil, its fields are included in the DEBUG_PAUSED event.
// Returns context.Canceled if the user chose to stop.
func (dc *DebugController) Check(ctx context.Context, checkpoint string, agentName string, iteration int, cpCtx *CheckpointContext) error {
	dc.mu.RLock()
	shouldPause, reason := dc.shouldPauseAt(checkpoint, agentName, iteration, cpCtx)
	dc.mu.RUnlock()

	if !shouldPause {
		return nil
	}

	// Transition to paused — clear one-shot state. pauseSeq identifies this
	// specific pause instance; see the resumeCh receive loop below and
	// Resume()'s doc comment for why.
	dc.mu.Lock()
	dc.state = StatePaused
	dc.pauseSeq++
	mySeq := dc.pauseSeq
	dc.stepIntoAgent = ""
	dc.stepOutAgent = ""
	dc.runUntilCond = nil
	dc.mu.Unlock()

	payload := telemetry.DebugPausedPayload{
		AgentName:  agentName,
		Checkpoint: checkpoint,
		Iteration:  iteration,
		Reason:     reason,
	}
	if cpCtx != nil {
		payload.LastThought = cpCtx.LastThought
		payload.PendingTools = convertPendingTools(cpCtx.PendingTools)
		payload.HistoryLength = cpCtx.HistoryLength
		payload.TotalTokensIn = cpCtx.TotalTokensIn
		payload.TotalTokensOut = cpCtx.TotalTokensOut
		payload.MaxTokens = cpCtx.MaxTokens
		payload.MaxIterations = cpCtx.MaxIterations
		payload.ContextWindow = cpCtx.ContextWindow
		payload.PredictedNextTokens = cpCtx.PredictedNextTokens
		payload.BudgetRatio = cpCtx.BudgetRatio
	}
	dc.eventBus.Emit(agentName, telemetry.EventDebugPaused, payload)

	// Block until a resume signal for THIS pause, or context cancellation. A
	// pending message with a mismatched seq is a stale one — claimed by a
	// Resume() call for an earlier pause that already exited via ctx.Done()
	// below before the send landed (see Resume()'s doc comment) — discard it
	// and keep waiting instead of treating it as this pause's resume.
	for {
		select {
		case pending := <-dc.resumeCh:
			if pending.seq != mySeq {
				continue
			}
			action := pending.action
			dc.mu.Lock()
			switch action {
			case ActionStep:
				dc.state = StateStepping
			case ActionStepIn:
				// Pause at first checkpoint inside the next delegated agent
				dc.stepIntoAgent = agentName // track who initiated; Check sees child agents
				dc.state = StateRunning
			case ActionStepOut:
				// Resume until we leave current agent scope
				dc.stepOutAgent = agentName
				dc.state = StateRunning
			case ActionRunUntil:
				// runUntilCond must be set via SetRunUntilCondition before resume
				dc.state = StateRunning
			case ActionStop:
				dc.state = StateRunning
				dc.mu.Unlock()
				dc.eventBus.Emit(agentName, telemetry.EventDebugResumed, telemetry.DebugResumedPayload{
					Action: string(ActionStop),
				})
				return fmt.Errorf("execution stopped by debugger: %w", context.Canceled)
			default:
				dc.state = StateRunning
			}
			dc.mu.Unlock()

			dc.eventBus.Emit(agentName, telemetry.EventDebugResumed, telemetry.DebugResumedPayload{
				Action: string(action),
			})
			return nil

		case <-ctx.Done():
			dc.mu.Lock()
			dc.state = StateRunning
			dc.mu.Unlock()
			return ctx.Err()
		}
	}
}

// Resume sends a resume action to unblock a paused agent. No-op unless the
// controller is currently paused: resumeCh is a buffered channel (capacity
// 1), so a stale, late, or duplicate Resume() call made while nothing is
// paused would otherwise still succeed into that buffer — a buffered send
// doesn't require a waiting receiver — and then get consumed as a spurious
// pre-armed resume the NEXT time Check() actually pauses, skipping that
// pause instead of waiting for a real decision.
//
// The state check and the claim on this pause must happen atomically under
// dc.mu, not as two separate synchronized steps: checking GetState() and
// then sending were previously independent operations, so two Resume()
// calls arriving close together (a UI double-click, a client-side retry)
// could both observe StatePaused before either one's send took effect —
// the second would then buffer a spurious action that Check() reads on its
// *next* pause, the same failure mode this function exists to prevent, just
// reached via two near-simultaneous calls instead of one stale one. Setting
// state away from StatePaused here, under the same lock as the check,
// makes the first caller's claim exclusive: any Resume() racing in after it
// sees a state that's already left StatePaused and returns without
// sending. Check() still owns picking the exact next state (StateStepping
// vs StateRunning, plus the step-into/step-out/run-until bookkeeping) once
// it actually receives the action below — this only needs to be correct
// enough to block a second claim, so StateRunning is a fine placeholder for
// every action including ActionStep, overwritten within a lock-hold of
// Check() consuming it.
//
// A second, independent race remains even with that claim exclusive: this
// function's own send to resumeCh happens outside dc.mu (a channel send
// can't be made atomic with the claim above without risking a blocking send
// while holding the lock), so Check() can lose the race against its own
// ctx.Done() — cancelled after this claim but before the send below lands —
// and return without ever receiving. The send would then still succeed into
// the buffer (nothing requires a waiting receiver) and sit there for
// whichever pause comes next. Tagging the send with the exact pauseSeq
// captured under the claim lock, rather than trying to close that timing
// window directly, is what actually fixes it: Check()'s receive loop above
// only honors a message whose seq matches the pause it's currently in, so a
// send that arrives after its own pause already exited is simply discarded
// by the *next* pause instead of being mistaken for that pause's resume.
func (dc *DebugController) Resume(action ResumeAction) {
	dc.mu.Lock()
	if dc.state != StatePaused {
		dc.mu.Unlock()
		return
	}
	dc.state = StateRunning
	seq := dc.pauseSeq
	dc.mu.Unlock()

	dc.sendResume(pendingResume{action: action, seq: seq})
}

// sendResume performs Resume()'s buffered send, given the seq claimed under
// dc.mu. Split out from Resume() so the delayed-send half of that function's
// own race (see its doc comment) can be driven deterministically in tests.
func (dc *DebugController) sendResume(pending pendingResume) {
	select {
	case dc.resumeCh <- pending:
		return
	default:
	}

	// The buffer's one slot is already occupied. A first version of this
	// fix assumed the occupant must be a stale entry left behind by an
	// earlier pause (nothing else can be mid-send for THIS seq, since the
	// claim in Resume() is exclusive per pause) and unconditionally drained
	// it — but "exclusive per pause" only rules out another sender for
	// *this* seq, not a legitimate, not-yet-consumed message belonging to a
	// *later* pause. This call can itself be the delayed half of an earlier
	// Resume() — claimed, then stalled past its own pause's ctx.Done() exit
	// — arriving here after a brand-new pause has already started AND
	// received its own valid Resume(). Draining unconditionally in that
	// case would destroy that live message and hang the newer pause
	// forever, which is strictly worse than just dropping this stale send.
	//
	// dc.pauseSeq only ever increases, and only increases when a new pause
	// begins — so if it still equals the seq this call claimed, no newer
	// pause has started, and anything already in the buffer really is
	// stale (safe to drain and retry). If it has moved on, this call's own
	// pause has been superseded; abandon rather than risk clobbering
	// whatever the current pause is waiting on.
	dc.mu.Lock()
	current := dc.pauseSeq
	dc.mu.Unlock()
	if current != pending.seq {
		return
	}

	select {
	case <-dc.resumeCh:
	default:
	}
	select {
	case dc.resumeCh <- pending:
	default:
		// Lost a second race for the slot (extremely unlikely: would need
		// another stale send to land in the single-item window between the
		// drain and this send). Nothing further to do without risking a
		// blocking send here.
	}
}

// matchesBreakpoint checks if any breakpoint matches the given checkpoint and agent.
// Must be called with at least a read lock held.
// eventToCheckpoint maps telemetry event type names to debug checkpoint names.
// Users can set breakpoints using either name — both will match.
var eventToCheckpoint = map[string]string{
	"AGENT_START":       "pre_agent",
	"THOUGHT_START":     "pre_thought",
	"THOUGHT_END":       "post_thought",
	"TOOL_CALL_START":   "pre_tool",
	"REFLECTION_START":  "pre_reflection",
	"GROUND_CHECK_START": "pre_ground_check",
	"PIPELINE_STEP_START": "pre_pipeline_step",
}

func (dc *DebugController) matchesBreakpoint(checkpoint, agentName string) bool {
	// Check both the checkpoint name and any event type alias
	names := []string{checkpoint}
	for evt, cp := range eventToCheckpoint {
		if cp == checkpoint {
			names = append(names, evt)
		}
	}
	// Also resolve event type → checkpoint (user set THOUGHT_START, we check pre_thought)
	if alias, ok := eventToCheckpoint[checkpoint]; ok {
		names = append(names, alias)
	}

	for _, name := range names {
		if dc.breakpoints[BreakpointKey{EventType: name, AgentName: agentName}] {
			return true
		}
		if dc.breakpoints[BreakpointKey{EventType: name, AgentName: "*"}] {
			return true
		}
	}
	// Wildcard event type
	if dc.breakpoints[BreakpointKey{EventType: "*", AgentName: agentName}] {
		return true
	}
	// Full wildcard
	if dc.breakpoints[BreakpointKey{EventType: "*", AgentName: "*"}] {
		return true
	}
	return false
}

// SetOverrides sets parameter overrides for an agent.
func (dc *DebugController) SetOverrides(agentName string, ovr *ParamOverride) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.overrides[agentName] = ovr
}

// GetOverrides returns and optionally clears parameter overrides for an agent.
// Non-sticky overrides are cleared after retrieval (apply-once semantics).
func (dc *DebugController) GetOverrides(agentName string) *ParamOverride {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	ovr, ok := dc.overrides[agentName]
	if !ok {
		return nil
	}
	if !ovr.Sticky {
		delete(dc.overrides, agentName)
	}
	return ovr
}

// RequestPause causes the next Check() call to pause (on-demand pause).
func (dc *DebugController) RequestPause() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.state = StateStepping
}

// shouldPauseAt determines if execution should pause at this checkpoint.
// Must be called with at least a read lock held.
// Returns (shouldPause, reason).
func (dc *DebugController) shouldPauseAt(checkpoint, agentName string, iteration int, cpCtx *CheckpointContext) (bool, string) {
	// Normal stepping mode
	if dc.state == StateStepping {
		return true, "step"
	}

	// Step In: pause at first checkpoint in a different (child) agent
	if dc.stepIntoAgent != "" && agentName != dc.stepIntoAgent {
		return true, "step_in"
	}

	// Step Out: skip all checkpoints inside current agent, pause when back at parent
	if dc.stepOutAgent != "" {
		if agentName == dc.stepOutAgent {
			// Still inside the agent — keep running
			return false, ""
		}
		// We're in a different agent (parent) — pause here
		return true, "step_out"
	}

	// Run Until: one-shot condition match
	if dc.runUntilCond != nil && dc.matchesRunUntil(checkpoint, agentName, iteration, cpCtx) {
		return true, "run_until"
	}

	// Regular breakpoints
	if dc.matchesBreakpoint(checkpoint, agentName) {
		return true, "breakpoint"
	}

	return false, ""
}

// matchesRunUntil checks if the current checkpoint matches the run-until condition.
// Must be called with at least a read lock held.
func (dc *DebugController) matchesRunUntil(checkpoint, agentName string, iteration int, cpCtx *CheckpointContext) bool {
	c := dc.runUntilCond
	if c.Checkpoint != "" && !dc.checkpointMatches(c.Checkpoint, checkpoint) {
		return false
	}
	if c.AgentName != "" && c.AgentName != agentName {
		return false
	}
	if c.Iteration > 0 && c.Iteration != iteration {
		return false
	}
	if c.ToolName != "" {
		// Match tool name against pending tools
		if cpCtx == nil {
			return false
		}
		found := false
		for _, t := range cpCtx.PendingTools {
			if t.Name == c.ToolName {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SetRunUntilCondition sets the condition for ActionRunUntil.
// Must be called before Resume(ActionRunUntil).
func (dc *DebugController) SetRunUntilCondition(cond *RunUntilCondition) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.runUntilCond = cond
}

// checkpointMatches returns true if the target matches the actual checkpoint,
// resolving event type aliases (e.g. THOUGHT_START ↔ pre_thought).
func (dc *DebugController) checkpointMatches(target, actual string) bool {
	if target == actual {
		return true
	}
	// target is event name, actual is checkpoint: THOUGHT_START → pre_thought
	if alias, ok := eventToCheckpoint[target]; ok && alias == actual {
		return true
	}
	// target is checkpoint, actual is checkpoint but user set event name
	for evt, cp := range eventToCheckpoint {
		if cp == target && evt == actual {
			return true
		}
	}
	return false
}

// convertPendingTools converts debug PendingToolInfo to telemetry DebugPendingToolInfo.
func convertPendingTools(tools []PendingToolInfo) []telemetry.DebugPendingToolInfo {
	if len(tools) == 0 {
		return nil
	}
	result := make([]telemetry.DebugPendingToolInfo, len(tools))
	for i, t := range tools {
		result[i] = telemetry.DebugPendingToolInfo{
			Name:      t.Name,
			Arguments: t.Arguments,
		}
	}
	return result
}

// ClearOverrides removes parameter overrides for an agent.
func (dc *DebugController) ClearOverrides(agentName string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.overrides, agentName)
}
