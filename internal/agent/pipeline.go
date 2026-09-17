package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// StepResult holds the output of a completed pipeline step.
type StepResult struct {
	Name     string
	Output   string
	Duration time.Duration
	Error    error
}

// PipelineContext accumulates results as pipeline steps execute.
type PipelineContext struct {
	Query   string
	Results []*StepResult // ordered list for deterministic iteration
	byName  map[string]*StepResult
	mu      sync.RWMutex
}

func newPipelineContext(query string) *PipelineContext {
	return &PipelineContext{
		Query:  query,
		byName: make(map[string]*StepResult),
	}
}

func (pc *PipelineContext) addResult(r *StepResult) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.Results = append(pc.Results, r)
	pc.byName[r.Name] = r
}

// clearFrom removes results for all steps in the given levels.
// Used by the debug rerun path to reset pipeline state before re-executing.
func (pc *PipelineContext) clearFrom(levels [][]config.PipelineStep) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	toRemove := make(map[string]bool)
	for _, level := range levels {
		for _, s := range level {
			toRemove[s.Name] = true
		}
	}
	filtered := pc.Results[:0]
	for _, r := range pc.Results {
		if !toRemove[r.Name] {
			filtered = append(filtered, r)
		} else {
			delete(pc.byName, r.Name)
		}
	}
	pc.Results = filtered
}

// buildLevels validates the dependency graph and groups steps into parallel
// execution levels using Kahn's BFS topological sort.
// Steps at the same level have no dependencies between each other and can run concurrently.
// Returns an error if a dependency names an unknown step or a cycle is detected.
func buildLevels(steps []config.PipelineStep) ([][]config.PipelineStep, error) {
	// Inject implicit sequential dependency: steps without explicit depends_on
	// implicitly depend on the previous step so that YAML order = execution order.
	// Users can override with explicit depends_on for DAG parallelism —
	// including declaring a true parallel root with depends_on: [] (a
	// non-nil empty slice, which yaml.v3 decodes distinctly from an omitted
	// field). Checking DependsOn == nil rather than len(...) == 0 is what
	// makes that override actually work: len() can't tell "not specified"
	// (nil) apart from "explicitly declared empty" ([]string{}), so a plain
	// len(...) == 0 check silently overwrote depends_on: [] too, meaning
	// only steps[0] could ever be a zero-dependency DAG root.
	for i := 1; i < len(steps); i++ {
		if steps[i].DependsOn == nil {
			steps[i].DependsOn = []string{steps[i-1].Name}
		}
	}

	// Build lookup and in-degree map
	byName := make(map[string]config.PipelineStep, len(steps))
	inDegree := make(map[string]int, len(steps))
	for _, s := range steps {
		byName[s.Name] = s
		inDegree[s.Name] = 0
	}

	// Validate deps and compute in-degrees
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if dep == s.Name {
				return nil, fmt.Errorf("step %q depends on itself", s.Name)
			}
			if _, ok := byName[dep]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", s.Name, dep)
			}
			inDegree[s.Name]++
		}
	}

	// Kahn's BFS: enqueue steps with in-degree 0
	var levels [][]config.PipelineStep
	remaining := len(steps)

	for remaining > 0 {
		var level []config.PipelineStep
		for _, s := range steps {
			if inDegree[s.Name] == 0 {
				level = append(level, s)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("pipeline has a dependency cycle")
		}

		// Mark these steps as processed
		for _, s := range level {
			inDegree[s.Name] = -1 // sentinel: processed
			remaining--
		}

		// Decrement in-degrees of dependents
		for _, s := range steps {
			if inDegree[s.Name] < 0 {
				continue
			}
			newDeg := 0
			for _, dep := range s.DependsOn {
				if inDegree[dep] >= 0 { // dep not yet processed
					newDeg++
				}
			}
			inDegree[s.Name] = newDeg
		}

		levels = append(levels, level)
	}

	return levels, nil
}

// executeLevelParallel runs a set of independent steps concurrently and returns
// the merged output. Mirrors executeParallelStep but operates on a flat level slice.
func (o *Orchestrator) executeLevelParallel(ctx context.Context, level []config.PipelineStep, pctx *PipelineContext) (string, error) {
	if dup := o.duplicateAgentInGroup(level); dup != "" {
		return "", fmt.Errorf("pipeline level cannot run concurrently: agent %q is referenced by more than one step in this level, and agents are not safe for concurrent Run() calls — give each step a distinct agent, or add depends_on to make these steps sequential", dup)
	}

	type levelResult struct {
		name   string
		output string
		err    error
	}

	results := make([]levelResult, len(level))
	var wg sync.WaitGroup

	for i, step := range level {
		wg.Add(1)
		go func(idx int, s config.PipelineStep) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = levelResult{name: s.Name, err: fmt.Errorf("step panic: %v", r)}
				}
			}()
			out, err := o.executeStep(ctx, s, pctx)
			results[idx] = levelResult{s.Name, out, err}
		}(i, step)
	}
	wg.Wait()

	// Same "## name\n output\n\n" format as executeParallelStep's merge, so
	// two adjacent non-empty outputs don't get concatenated directly into
	// each other with no separator at all.
	var sb strings.Builder
	var firstErr error
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		if r.output != "" {
			sb.WriteString(fmt.Sprintf("## %s\n%s\n\n", r.name, r.output))
		}
	}
	return sb.String(), firstErr
}

// duplicateAgentInGroup returns the first agent name reachable from more
// than one step in the group (via stepAgents, which already recurses into
// nested sub-steps), or "" if every step in the group touches a distinct
// set of agents. Two steps about to run concurrently that reference the
// same agent would call Run() on the same singleton *Agent instance from
// two goroutines at once — with retrieval enabled and the BM25 backend,
// that reaches an unsynchronized concurrent map read/write in
// BM25Retriever.Query: fatal error: concurrent map read and map write, an
// unrecoverable process crash that no recover() can catch.
func (o *Orchestrator) duplicateAgentInGroup(steps []config.PipelineStep) string {
	seen := make(map[string]bool)
	for _, s := range steps {
		for _, name := range o.stepAgents(s) {
			if seen[name] {
				return name
			}
			seen[name] = true
		}
	}
	return ""
}

// runPipeline executes the orchestrator using a deterministic pipeline.
// Steps may declare depends_on to express DAG dependencies; independent steps
// at the same level are executed concurrently.
func (o *Orchestrator) runPipeline(ctx context.Context, query string) (string, error) {
	if o.pipelineConfig == nil || len(o.pipelineConfig.Steps) == 0 {
		return "", fmt.Errorf("pipeline strategy requires pipeline config with steps")
	}

	levels, err := buildLevels(o.pipelineConfig.Steps)
	if err != nil {
		return "", fmt.Errorf("pipeline DAG error: %w", err)
	}

	pctx := newPipelineContext(query)

	o.eventBus.Emit(o.name, telemetry.EventPipelineStart, telemetry.PipelineStartPayload{
		StepCount: len(o.pipelineConfig.Steps),
		Query:     truncateStr(query, 500),
		Steps:     convertSteps(o.pipelineConfig.Steps),
	})

	var lastOutput string
	levelIdx := 0
	for levelIdx < len(levels) {
		level := levels[levelIdx]

		// RESUME: inject checkpointed results and filter out already-done steps.
		var toRun []config.PipelineStep
		for _, step := range level {
			if o.checkpoint != nil {
				if saved, ok := o.checkpoint.Results[step.Name]; ok {
					pctx.addResult(&StepResult{
						Name:     saved.Name,
						Output:   saved.Output,
						Duration: saved.Duration,
					})
					lastOutput = saved.Output
					continue
				}
			}
			toRun = append(toRun, step)
		}
		if len(toRun) == 0 {
			levelIdx++
			continue
		}

		var output string
		var stepErr error
		if len(toRun) == 1 {
			output, stepErr = o.executeStep(ctx, toRun[0], pctx)
		} else {
			output, stepErr = o.executeLevelParallel(ctx, toRun, pctx)
		}

		if stepErr != nil {
			o.eventBus.Emit(o.name, telemetry.EventPipelineEnd, telemetry.PipelineEndPayload{
				Status:   "error",
				StepsRun: len(pctx.Results),
			})
			return "", fmt.Errorf("pipeline level failed: %w", stepErr)
		}
		lastOutput = output

		// CHECKPOINT: write after each successful level.
		if o.checkpointWriter != nil {
			o.checkpointWriter(buildCheckpoint(pctx, o.checkpoint))
		}

		// DEBUG: check for rerun signal after each successful level.
		if o.debugCtrl != nil {
			select {
			case req := <-o.debugCtrl.RerunCh():
				targetLevel := -1
				for li, lv := range levels {
					for _, s := range lv {
						if s.Name == req.StepName {
							targetLevel = li
							break
						}
					}
					if targetLevel >= 0 {
						break
					}
				}
				if targetLevel >= 0 {
					pctx.clearFrom(levels[targetLevel:])
					// o.checkpoint.Results is a separate cache from
					// pctx.Results, consulted by the "RESUME: inject
					// checkpointed results" block at the top of this loop.
					// Without also removing these entries, the very next
					// iteration finds the rerun target's name still present
					// there and re-injects the stale cached output instead
					// of actually re-executing it — silently defeating the
					// rerun request with no error or warning.
					if o.checkpoint != nil {
						for _, lv := range levels[targetLevel:] {
							for _, s := range lv {
								delete(o.checkpoint.Results, s.Name)
							}
						}
					}
					levelIdx = targetLevel
					continue
				}
			default:
			}
		}

		levelIdx++
	}

	// SYNTHESIS: optional final LLM call to summarize all step results.
	// If the user opted into synthesis (Synthesis=true) and it fails, that's
	// a real failure — the user wanted a synthesized answer, not raw step
	// dumps. Propagate to caller with PIPELINE_END status="synthesis_failed"
	// so exit code reflects reality. The synthesize() call itself
	// retries on transient errors via retryWithBackoff.
	if o.pipelineConfig.Synthesis && o.llmProvider != nil {
		synthResult, err := o.synthesize(ctx, pctx)
		if err != nil {
			o.eventBus.Emit(o.name, telemetry.EventError, telemetry.ErrorPayload{
				ErrorType:   "synthesis_error",
				Message:     err.Error(),
				Recoverable: false,
			})
			o.eventBus.Emit(o.name, telemetry.EventPipelineEnd, telemetry.PipelineEndPayload{
				Status:   "synthesis_failed",
				StepsRun: len(pctx.Results),
			})
			return lastOutput, err
		}
		lastOutput = synthResult
	}

	o.eventBus.Emit(o.name, telemetry.EventPipelineEnd, telemetry.PipelineEndPayload{
		Status:   "success",
		StepsRun: len(pctx.Results),
	})

	return lastOutput, nil
}

const defaultSynthesisPrompt = `You are summarizing the results of a multi-step pipeline.
Review all the outputs below and provide a clear, comprehensive final answer
that addresses the original query. Synthesize findings, resolve any conflicts,
and present a coherent result.`

// synthesize makes a final LLM call to summarize all pipeline step results.
func (o *Orchestrator) synthesize(ctx context.Context, pctx *PipelineContext) (string, error) {
	o.eventBus.Emit(o.name, telemetry.EventAgentStart, telemetry.AgentStartPayload{
		Role:  "supervisor",
		Model: o.model,
	})

	prompt := o.pipelineConfig.SynthesisPrompt
	if prompt == "" {
		prompt = defaultSynthesisPrompt
	}

	// Build synthesis input from all step results
	var sb strings.Builder
	sb.WriteString("## Original Query\n")
	sb.WriteString(pctx.Query)
	sb.WriteString("\n\n## Step Results\n\n")

	pctx.mu.RLock()
	for _, r := range pctx.Results {
		if r.Error == nil && r.Output != "" {
			sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", r.Name, r.Output))
		}
	}
	pctx.mu.RUnlock()

	history := []llm.Message{llm.NewTextMessage("user", sb.String())}

	// Wrap the synthesis call in retryWithBackoff so 429s and other
	// transient errors get the same rate-limit-aware backoff curve as
	// per-iteration agent LLM calls. Without this the synthesis path
	// (which is often the largest call in a pipeline — full step results
	// fed in as context) was silently dying on a single 429 even after
	// retry was fixed for normal agent loops.
	//
	// onRetry emits RETRY_ATTEMPT events so a 40s+ rate-limit backoff
	// shows up in the TUI / debugger as a visible wait, not an invisible
	// stall (review feedback Medium #2).
	var result *llm.GenerateResult
	err := retryWithBackoff(ctx, defaultRetryConfig, func() error {
		var rerr error
		result, rerr = o.llmProvider.Generate(ctx, prompt, history, nil)
		return rerr
	}, func(re RetryEvent) {
		o.eventBus.Emit(o.name, telemetry.EventRetryAttempt, telemetry.RetryAttemptPayload{
			Attempt:     re.Attempt,
			MaxAttempts: re.MaxAttempts,
			Error:       re.Err.Error(),
		})
	})
	if err != nil {
		// Invariant: every AGENT_START must have a matching AGENT_END.
		// Without this, the dogfood verifier flags any synthesis-failure
		// run as imbalanced even though the pipeline behaved as designed.
		o.eventBus.Emit(o.name, telemetry.EventAgentEnd, telemetry.AgentEndPayload{
			Status:     "error",
			Iterations: 1,
		})
		return "", fmt.Errorf("synthesis LLM error: %w", err)
	}

	// Emit token usage
	if result.TokenUsage != nil {
		o.eventBus.EmitWithTokenUsage(o.name, telemetry.EventTokenUsage, struct{}{}, telemetry.TokenUsage{
			InputTokens:  result.TokenUsage.InputTokens,
			OutputTokens: result.TokenUsage.OutputTokens,
			TotalTokens:  result.TokenUsage.TotalTokens,
		})
	}

	o.eventBus.Emit(o.name, telemetry.EventAgentEnd, telemetry.AgentEndPayload{
		Status:      "success",
		FinalAnswer: truncateStr(result.Response, 500),
		Iterations:  1,
	})

	return result.Response, nil
}

// executeStep dispatches a pipeline step by type.
func (o *Orchestrator) executeStep(ctx context.Context, step config.PipelineStep, pctx *PipelineContext) (string, error) {
	stepType := step.Type
	if stepType == "" {
		stepType = "sequential"
	}

	// Debug checkpoint before pipeline step
	if o.debugCtrl != nil {
		if err := o.debugCtrl.Check(ctx, "pre_pipeline_step", step.Name, 0, nil); err != nil {
			return "", err
		}
	}

	// Collect agent names for the event
	agents := o.stepAgents(step)

	o.eventBus.Emit(o.name, telemetry.EventPipelineStepStart, telemetry.PipelineStepStartPayload{
		StepName: step.Name,
		StepType: stepType,
		Agents:   agents,
	})

	start := time.Now()
	var output string
	var err error

	switch stepType {
	case "sequential":
		output, err = o.executeSequentialStep(ctx, step, pctx)
	case "parallel":
		output, err = o.executeParallelStep(ctx, step, pctx)
	case "loop":
		output, err = o.executeLoopStep(ctx, step, pctx)
	default:
		err = fmt.Errorf("unknown step type: %q", stepType)
	}

	duration := time.Since(start)

	// Store result
	status := "success"
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status = "timeout"
		} else {
			status = "error"
		}
	}
	pctx.addResult(&StepResult{
		Name:     step.Name,
		Output:   output,
		Duration: duration,
		Error:    err,
	})

	o.eventBus.EmitWithDuration(o.name, telemetry.EventPipelineStepEnd, telemetry.PipelineStepEndPayload{
		StepName: step.Name,
		StepType: stepType,
		Status:   status,
		Output:   truncateStr(output, 500),
	}, duration.Milliseconds())

	return output, err
}

// executeSequentialStep runs a single agent with context from prior steps.
func (o *Orchestrator) executeSequentialStep(ctx context.Context, step config.PipelineStep, pctx *PipelineContext) (string, error) {
	stepCtx := ctx
	var cancel context.CancelFunc
	if step.TimeoutSec > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}

	runner, ok := o.agents[step.Agent]
	if !ok {
		return "", fmt.Errorf("agent %q not found", step.Agent)
	}

	// Use step task if defined, otherwise fall back to the CLI query
	stepTask := step.Task
	if stepTask == "" {
		stepTask = pctx.Query
	}

	task := o.buildTaskWithContext(stepTask, pctx)

	// Debug breakpoint: before agent handoff (pipeline mode)
	if o.debugCtrl != nil {
		if err := o.debugCtrl.Check(stepCtx, "pre_agent", runner.GetName(), 0, nil); err != nil {
			return "", err
		}
	}

	// Emit handoff event — include enriched context so the debugger can show
	// what the agent actually receives (prior step results + current task).
	pctx.mu.RLock()
	priorCount := len(pctx.Results)
	pctx.mu.RUnlock()
	o.eventBus.Emit(o.name, telemetry.EventAgentHandoff, telemetry.AgentHandoffPayload{
		FromAgent:      o.name,
		ToAgent:        runner.GetName(),
		Task:           stepTask,
		FullContext:    task,
		ContextSize:    len(task),
		PriorStepCount: priorCount,
	})

	runCtx := stepCtx
	var runToolCallID uint64
	if step.RequireToolCall != nil {
		runCtx, runToolCallID = NewToolCallRunContext(stepCtx)
	}

	result, err := runner.Run(runCtx, task)
	if err != nil {
		// Run itself failed, so checkRequireToolCall below never runs — but
		// a run ID was still minted (and the agent may still have recorded
		// calls against it before erroring), so it must still be released
		// or it leaks in toolCallsByRun for the life of the agent. Discard
		// the calls; nothing reads them on this path.
		releaseToolCallRun(runner, runToolCallID)
		return "", err
	}

	if gateErr := checkRequireToolCall(step, runner, runToolCallID); gateErr != nil {
		return "", gateErr
	}

	// Emit result message
	o.eventBus.Emit(runner.GetName(), telemetry.EventAgentMessage, telemetry.AgentMessagePayload{
		FromAgent: runner.GetName(),
		ToAgent:   o.name,
		Message:   truncateStr(result, 2000),
		Type:      "result",
	})

	return result, nil
}

// releaseToolCallRun discards a run ID's recorded tool calls without
// checking them — used when a gated step's runner.Run itself failed, so
// checkRequireToolCall (which would otherwise consume the entry via
// ToolCallsForRun) never runs. Without this, a run ID minted for a step
// that keeps erroring (e.g. a persistently failing agent, or a cancelled
// context) would never be read back and its entry would leak in
// toolCallsByRun for the life of the agent. No-op for runID 0 (no gate was
// configured, so NewToolCallRunContext — whose counter starts at 1 — was
// never called and there's nothing to release) or a runner that doesn't
// implement ToolCallReporter.
func releaseToolCallRun(runner Runner, runID uint64) {
	if runID == 0 {
		return
	}
	if reporter, ok := runner.(ToolCallReporter); ok {
		reporter.ToolCallsForRun(runID)
	}
}

// checkRequireToolCall mechanically cross-checks a step's require_tool_call
// gate (if any) against the tool calls the step's agent actually made,
// instead of trusting the agent's self-reported final answer. runID is the
// run-scoped ID minted for this specific Run invocation (see
// NewToolCallRunContext) — zero/unused when the step has no gate. Returns
// nil when the step has no gate or when a matching call was found. Fails
// CLOSED otherwise: a runner that doesn't implement ToolCallReporter (e.g.
// a nested *Orchestrator — agents map values aren't limited to *Agent, see
// nested_integration_test.go) can't be verified, and require_tool_call
// exists specifically so a step can't pass on trust alone — so "can't
// verify" is treated the same as "verification failed", not skipped.
func checkRequireToolCall(step config.PipelineStep, runner Runner, runID uint64) error {
	gate := step.RequireToolCall
	if gate == nil {
		return nil
	}
	reporter, ok := runner.(ToolCallReporter)
	if !ok {
		return fmt.Errorf("step %q requires a %q tool call but its runner (%T) doesn't report tool calls, so the gate can't be verified", step.Name, gate.Tool, runner)
	}
	for _, call := range reporter.ToolCallsForRun(runID) {
		if call.Name != gate.Tool {
			continue
		}
		if gate.CommandContains == "" {
			return nil
		}
		if arg, ok := commandArgValue(call, gate.ArgKey); ok && strings.Contains(arg, gate.CommandContains) {
			return nil
		}
	}
	if gate.CommandContains != "" {
		return fmt.Errorf("step %q required a %q tool call matching %q, none occurred", step.Name, gate.Tool, gate.CommandContains)
	}
	return fmt.Errorf("step %q required a %q tool call, none occurred", step.Name, gate.Tool)
}

// commandArgValue returns the string argument a require_tool_call gate's
// CommandContains should be checked against, and whether one could be
// unambiguously determined. If argKey is set, only that argument is
// considered. Otherwise, the call must have exactly one string-valued
// argument — the common shape for a single-parameter tool like "sh" — or
// this returns false rather than guessing, so a match can never be
// satisfied by an unrelated field (a path, an env value, metadata) that
// happens to contain the target substring.
func commandArgValue(call llm.ToolCall, argKey string) (string, bool) {
	if argKey != "" {
		s, ok := call.Arguments[argKey].(string)
		return s, ok
	}
	var found string
	count := 0
	for _, v := range call.Arguments {
		if s, ok := v.(string); ok {
			found = s
			count++
		}
	}
	if count != 1 {
		return "", false
	}
	return found, true
}

// executeParallelStep runs sub-steps concurrently and merges results.
func (o *Orchestrator) executeParallelStep(ctx context.Context, step config.PipelineStep, pctx *PipelineContext) (string, error) {
	if dup := o.duplicateAgentInGroup(step.Steps); dup != "" {
		return "", fmt.Errorf("parallel step %q cannot run: agent %q is referenced by more than one sub-step, and agents are not safe for concurrent Run() calls — give each sub-step a distinct agent, or use type: sequential instead", step.Name, dup)
	}

	stepCtx := ctx
	if step.TimeoutSec > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}

	type subResult struct {
		name   string
		output string
		err    error
	}

	results := make([]subResult, len(step.Steps))
	var wg sync.WaitGroup

	for i, sub := range step.Steps {
		wg.Add(1)
		go func(idx int, s config.PipelineStep) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = subResult{name: s.Name, err: fmt.Errorf("step panic: %v", r)}
				}
			}()
			// Dispatch via executeStep, not executeSequentialStep directly,
			// so a sub-step nested inside a parallel group can itself be
			// type: loop or type: parallel — config.PipelineStep.Steps is a
			// recursive structure that structurally permits this nesting,
			// but executeSequentialStep only knows how to run a leaf step
			// (step.Agent != ""); a composite sub-step reaching it failed
			// with the misleading error `agent "" not found` instead of
			// actually recursing. executeStep also owns emitting
			// PIPELINE_STEP_START/END and calling pctx.addResult for this
			// sub-step (same as executeLoopStep already does for its own
			// sub-steps), so this goroutine's result is only used for
			// merging output below, not stored again.
			out, err := o.executeStep(stepCtx, s, pctx)
			results[idx] = subResult{name: s.Name, output: out, err: err}
		}(i, sub)
	}
	wg.Wait()

	// Merge output — each sub-step's result was already recorded on pctx by
	// executeStep above.
	var sb strings.Builder
	var firstErr error
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("## %s\n%s\n\n", r.name, r.output))
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
	}

	return sb.String(), firstErr
}

// loopConditionPassRe matches a leading "PASS" verdict in a condition
// agent's (uppercased, trimmed) response. Anchored to the start rather than
// a bare strings.Contains: the condition prompt only asks the model to
// "answer PASS or FAIL with reasons" — free text, no structured marker — so
// a response explaining a failure in prose (e.g. "The step FAILED, it does
// NOT PASS the requirements") contains the substring "PASS" too, and a
// naive Contains check would misread that as passing, silently accepting
// unfinished or failing work as complete. Requiring the verdict to lead the
// response is the standard shape for an explicit PASS/FAIL answer; if a
// model answers unconventionally (verdict buried after prose) this errs on
// the safe side — the loop just keeps iterating instead of exiting early.
var loopConditionPassRe = regexp.MustCompile(`^PASS\b`)

// executeLoopStep iterates sub-steps until condition passes or max iterations reached.
func (o *Orchestrator) executeLoopStep(ctx context.Context, step config.PipelineStep, pctx *PipelineContext) (string, error) {
	loopCtx := ctx
	if step.TimeoutSec > 0 {
		var cancel context.CancelFunc
		loopCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}

	maxIter := step.MaxIterations
	if maxIter <= 0 {
		maxIter = 5
	}

	var lastOutput string
	for i := 1; i <= maxIter; i++ {
		iterName := fmt.Sprintf("%s_iteration_%d", step.Name, i)
		iterStart := time.Now()

		// Emit loop iteration info
		o.eventBus.Emit(o.name, telemetry.EventPipelineStepStart, telemetry.PipelineStepStartPayload{
			StepName: iterName,
			StepType: "loop_iteration",
		})

		// Execute all sub-steps in sequence
		var iterErr error
		for _, sub := range step.Steps {
			out, err := o.executeStep(loopCtx, sub, pctx)
			if err != nil {
				iterErr = err
				break
			}
			lastOutput = out
		}

		// Emit PIPELINE_STEP_END for this loop iteration
		iterStatus := "success"
		if iterErr != nil {
			iterStatus = "error"
		}
		o.eventBus.EmitWithDuration(o.name, telemetry.EventPipelineStepEnd, telemetry.PipelineStepEndPayload{
			StepName: iterName,
			StepType: "loop_iteration",
			Status:   iterStatus,
			Output:   truncateStr(lastOutput, 500),
		}, time.Since(iterStart).Milliseconds())

		if iterErr != nil {
			return "", iterErr
		}

		// Check condition if configured
		if step.ConditionAgent != "" {
			condAgent, ok := o.agents[step.ConditionAgent]
			if !ok {
				return "", fmt.Errorf("condition agent %q not found", step.ConditionAgent)
			}

			condTask := o.buildTaskWithContext(step.ConditionPrompt, pctx)
			condResult, err := condAgent.Run(loopCtx, condTask)
			if err != nil {
				return "", fmt.Errorf("condition check failed: %w", err)
			}

			if loopConditionPassRe.MatchString(strings.ToUpper(strings.TrimSpace(condResult))) {
				break
			}
		}
	}

	return lastOutput, nil
}

// buildTaskWithContext assembles a task string with the original query and all prior results.
func (o *Orchestrator) buildTaskWithContext(task string, pctx *PipelineContext) string {
	pctx.mu.RLock()
	defer pctx.mu.RUnlock()

	// If task is the same as the query (no step-level task), skip the "Original Query" section
	if task == pctx.Query && len(pctx.Results) == 0 {
		return task
	}

	var sb strings.Builder

	// Only include original query when the step has its own task
	if task != pctx.Query {
		sb.WriteString("## Original Query\n")
		sb.WriteString(pctx.Query)
		sb.WriteString("\n\n")
	}

	if len(pctx.Results) > 0 {
		sb.WriteString("## Previous Results\n\n")
		for _, r := range pctx.Results {
			if r.Error == nil && r.Output != "" {
				sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", r.Name, r.Output))
			}
		}
	}

	sb.WriteString("## Current Task\n")
	sb.WriteString(task)

	return sb.String()
}

// stepAgents collects agent names from a step (including sub-steps).
func (o *Orchestrator) stepAgents(step config.PipelineStep) []string {
	if step.Agent != "" {
		return []string{step.Agent}
	}
	var agents []string
	for _, sub := range step.Steps {
		agents = append(agents, o.stepAgents(sub)...)
	}
	return agents
}

// convertSteps converts config pipeline steps to telemetry step info for UI pre-rendering.
func convertSteps(steps []config.PipelineStep) []telemetry.PipelineStepInfo {
	var result []telemetry.PipelineStepInfo
	for _, s := range steps {
		stepType := s.Type
		if stepType == "" {
			stepType = "sequential"
		}
		info := telemetry.PipelineStepInfo{
			Name: s.Name,
			Type: stepType,
		}
		if s.Agent != "" {
			info.Agents = []string{s.Agent}
		}
		if len(s.Steps) > 0 {
			info.Steps = convertSteps(s.Steps)
			// Collect agents from sub-steps
			for _, sub := range info.Steps {
				info.Agents = append(info.Agents, sub.Agents...)
			}
		}
		result = append(result, info)
	}
	return result
}

// truncateStr truncates a string to maxLen, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
