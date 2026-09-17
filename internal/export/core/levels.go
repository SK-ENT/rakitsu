package core

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// validatePipelineStepsForExport rejects top-level pipeline steps that
// GeneratePipelineScript cannot safely turn into bash. It runs before
// BuildStepLevels, which does not itself catch any of these problems:
//
//   - A step whose Agent sanitizes to an empty ID falls through the script
//     generator's `agentID == ""` skip in every branch. Checking the raw
//     Agent string for emptiness isn't enough — shared.SanitizeID strips
//     every character outside [a-zA-Z0-9 _-], so a non-empty Agent made
//     entirely of other characters (e.g. punctuation) still sanitizes to
//     "". If the step is a nested type:"parallel"/"loop" container
//     (config.PipelineStep.Steps is a real, actively-implemented runtime
//     feature — internal/agent/pipeline.go recurses into it), that whole
//     sub-pipeline is silently dropped when it lands alone in its level, or
//     triggers an unbound-$PID_ crash under set -u when it shares a parallel
//     level with a sibling step (the launch loop skips it and never assigns
//     PID_<X>, but the wait loop still waits on it unconditionally). A step
//     with nested Steps is rejected regardless of whether Agent is also set:
//     the script generator only ever reads step.Agent, so a step declaring
//     both would silently drop its sub-steps rather than failing. Generating
//     nested-container bash is a separate feature, not implemented here —
//     fail loudly instead of emitting a script that's silently wrong,
//     incomplete, or crashes.
//   - Two steps whose names collide (exact duplicate, or SanitizeID
//     collision) both survive into the same execution level: BuildStepLevels
//     keys its lookup maps by Name, so duplicates collapse to one graph node,
//     but the caller's steps slice still holds both entries. In a parallel
//     level, both then background-write to the same output file (a real
//     race) and are assigned the same PID_<id> bash variable (the second
//     clobbers the first before either wait call runs).
func validatePipelineStepsForExport(steps []config.PipelineStep) error {
	seenIDs := map[string]string{} // sanitized id -> original step name
	for _, s := range steps {
		if len(s.Steps) > 0 {
			stepType := s.Type
			if stepType == "" {
				stepType = "sequential"
			}
			return fmt.Errorf("pipeline step %q: type %q containers with nested sub-steps are not supported by the Pipeline exporter — flatten into top-level steps with explicit depends_on", s.Name, stepType)
		}
		if shared.SanitizeID(s.Agent) == "" {
			if s.Agent == "" {
				return fmt.Errorf("pipeline step %q: no agent specified", s.Name)
			}
			return fmt.Errorf("pipeline step %q: agent %q sanitizes to an empty id — use at least one letter, digit, or underscore", s.Name, s.Agent)
		}
		id := shared.SanitizeID(s.Name)
		if prev, ok := seenIDs[id]; ok {
			return fmt.Errorf("pipeline steps %q and %q both sanitize to the same id %q — rename one", prev, s.Name, id)
		}
		seenIDs[id] = s.Name
	}
	return nil
}

// findSinkStep returns the pipeline's single terminal step — the one step
// that never appears in any other step's DependsOn — for use as the "final
// result" when Synthesis is disabled. A cycle-free DAG always has at least
// one such step (validatePipelineStepsForExport/BuildStepLevels catch cycles
// separately); more than one means the pipeline has independent branches
// with no single final consumer, which is ambiguous for "print the final
// result" purposes.
func findSinkStep(steps []config.PipelineStep) (config.PipelineStep, error) {
	referenced := map[string]bool{}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			referenced[dep] = true
		}
	}
	var sinks []config.PipelineStep
	for _, s := range steps {
		if !referenced[s.Name] {
			sinks = append(sinks, s)
		}
	}
	if len(sinks) != 1 {
		names := make([]string, len(sinks))
		for i, s := range sinks {
			names[i] = s.Name
		}
		return config.PipelineStep{}, fmt.Errorf("pipeline has %d independent final steps %v with synthesis disabled — enable synthesis or add depends_on edges to make the final step unambiguous", len(sinks), names)
	}
	return sinks[0], nil
}

// BuildStepLevels groups pipeline steps into parallel execution levels using
// Kahn's BFS topological sort. Steps at the same level have no dependencies
// between each other and can run concurrently. Only handles top-level sequential
// steps (parallel/loop steps are handled at script generation time).
func BuildStepLevels(steps []config.PipelineStep) ([][]config.PipelineStep, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("no pipeline steps defined")
	}

	// Inject implicit sequential dependency: steps without explicit depends_on
	// implicitly depend on the previous step so that YAML order = execution order.
	// An explicit empty depends_on (DependsOn: []) means "no dependencies" — run in parallel.
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

	// Kahn's BFS
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

		for _, s := range level {
			inDegree[s.Name] = -1 // sentinel: processed
			remaining--
		}

		for _, s := range steps {
			if inDegree[s.Name] < 0 {
				continue
			}
			newDeg := 0
			for _, dep := range s.DependsOn {
				if inDegree[dep] >= 0 {
					newDeg++
				}
			}
			inDegree[s.Name] = newDeg
		}

		levels = append(levels, level)
	}

	return levels, nil
}
