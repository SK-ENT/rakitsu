package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/decision"
)

func (o *Orchestrator) executeDecisionStep(ctx context.Context, step config.PipelineStep, pctx *PipelineContext) (string, *decision.Result, error) {
	spec := step.Decision
	if err := spec.Validate(); err != nil {
		return "", nil, err
	}
	if step.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}

	input := pctx.Query
	branchContext := newPipelineContext(pctx.Query)
	pctx.mu.RLock()
	for _, result := range pctx.Results {
		branchContext.addResult(result)
	}
	source := pctx.byName[spec.Input.Step]
	pctx.mu.RUnlock()
	var evidence *retrievalResult
	if spec.Input.From == "step" || spec.Input.From == "retrieval" {
		if source == nil || source.Error != nil {
			return "", nil, fmt.Errorf("decision %q input step %q has no successful result", step.Name, spec.Input.Step)
		}
		input = source.Output
	}
	if spec.Input.From == "retrieval" {
		evidence = &retrievalResult{}
		if err := json.Unmarshal([]byte(input), evidence); err != nil {
			return "", nil, fmt.Errorf("invalid upstream retrieval result")
		}
		if len(evidence.Passages) == 0 {
			return `{"status":"no_matches","evidence":{"passages":[]}}`, nil, nil
		}
		request, err := json.Marshal(struct {
			Query    string           `json:"query"`
			Evidence *retrievalResult `json:"evidence"`
		}{pctx.Query, evidence})
		if err != nil {
			return "", nil, err
		}
		input = string(request)
	}

	answers, err := decision.Evaluate(ctx, spec, input)
	if err != nil {
		return "", nil, err
	}
	result := &decision.Result{Backend: spec.Backend, Answers: answers}
	if evidence != nil {
		output, err := json.Marshal(struct {
			Decision *decision.Result `json:"decision"`
			Evidence *retrievalResult `json:"evidence"`
		}{result, evidence})
		return string(output), result, err
	}
	branch := spec.Otherwise
	if len(branch) > 0 {
		result.Route = "otherwise"
	}
	for i, route := range spec.Routes {
		if matchesDecision(route.When, answers[route.When.Question]) {
			result.Route = fmt.Sprintf("route %d", i+1)
			branch = route.Steps
			break
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", nil, err
	}
	output := string(encoded)
	branchContext.addResult(&StepResult{Name: step.Name, Output: output})
	for _, child := range branch {
		child.Name = step.Name + "/" + child.Name
		output, err = o.executeStep(ctx, child, branchContext)
		if err != nil {
			return "", result, err
		}
	}
	return output, result, nil
}

func matchesDecision(match config.DecisionMatch, answer decision.Answer) bool {
	switch match.Field {
	case "choice":
		return answer.Choice == match.Equals
	case "probability_true":
		return withinDecisionBounds(answer.ProbabilityTrue, match)
	case "score":
		return withinDecisionBounds(answer.Score, match)
	default:
		return false
	}
}

func withinDecisionBounds(value *float64, match config.DecisionMatch) bool {
	return value != nil && (match.Min == nil || *value >= *match.Min) && (match.Max == nil || *value <= *match.Max)
}
