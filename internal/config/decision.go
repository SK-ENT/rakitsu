package config

import (
	"fmt"
	"math"
	"strings"
)

type DecisionConfig struct {
	Backend   string                      `mapstructure:"backend" yaml:"backend"`
	URL       string                      `mapstructure:"url" yaml:"url,omitempty"`
	Input     DecisionInput               `mapstructure:"input" yaml:"input,omitempty"`
	Questions map[string]DecisionQuestion `mapstructure:"questions" yaml:"questions"`
	Routes    []DecisionRoute             `mapstructure:"routes" yaml:"routes,omitempty"`
	Otherwise []PipelineStep              `mapstructure:"otherwise" yaml:"otherwise,omitempty"`
}

type DecisionInput struct {
	From string `mapstructure:"from" yaml:"from"`
	Step string `mapstructure:"step" yaml:"step,omitempty"`
}

type DecisionQuestion struct {
	Type         string            `mapstructure:"type" yaml:"type"`
	Instructions string            `mapstructure:"instructions" yaml:"instructions"`
	Choices      map[string]string `mapstructure:"choices" yaml:"choices,omitempty"`
	Levels       []string          `mapstructure:"levels" yaml:"levels,omitempty"`
}

type DecisionRoute struct {
	When  DecisionMatch  `mapstructure:"when" yaml:"when"`
	Steps []PipelineStep `mapstructure:"steps" yaml:"steps"`
}

type DecisionMatch struct {
	Question string   `mapstructure:"question" yaml:"question"`
	Field    string   `mapstructure:"field" yaml:"field"`
	Equals   string   `mapstructure:"equals" yaml:"equals,omitempty"`
	Min      *float64 `mapstructure:"min" yaml:"min,omitempty"`
	Max      *float64 `mapstructure:"max" yaml:"max,omitempty"`
}

func (d *DecisionConfig) Branches() [][]PipelineStep {
	branches := make([][]PipelineStep, 0, len(d.Routes)+1)
	for _, route := range d.Routes {
		branches = append(branches, route.Steps)
	}
	return append(branches, d.Otherwise)
}

func (d *DecisionConfig) Validate() error {
	if d == nil {
		return fmt.Errorf("decision configuration is required")
	}
	switch d.Backend {
	case "jev", "laya", "openai", "gemini":
	default:
		return fmt.Errorf("decision backend must be jev, laya, openai, or gemini")
	}
	if d.Backend != "laya" && d.URL != "" {
		return fmt.Errorf("decision url is only supported for local Laya")
	}
	namedInput := d.Input.From == "step" || d.Input.From == "retrieval"
	if (namedInput && d.Input.Step == "") ||
		(!namedInput && (d.Input.Step != "" || (d.Input.From != "" && d.Input.From != "query"))) {
		return fmt.Errorf("decision input must be the query or a named step")
	}
	if d.Input.From == "retrieval" && (d.Backend != "laya" || len(d.Routes) != 0 || len(d.Otherwise) != 0) {
		return fmt.Errorf("retrieval evidence requires a route-free local Laya decision")
	}
	if len(d.Questions) == 0 {
		return fmt.Errorf("at least one decision question is required")
	}
	for name, q := range d.Questions {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("decision questions require a name and instructions")
		}
		switch q.Type {
		case "predicate":
			if len(q.Choices) != 0 || len(q.Levels) != 0 {
				return fmt.Errorf("predicate %q cannot have choices or levels", name)
			}
		case "choice":
			if len(q.Choices) < 2 || len(q.Levels) != 0 {
				return fmt.Errorf("choice %q requires at least two choices", name)
			}
			for key, description := range q.Choices {
				if strings.TrimSpace(key) == "" || strings.TrimSpace(description) == "" {
					return fmt.Errorf("choice %q requires named options with descriptions", name)
				}
			}
		case "score":
			if len(q.Levels) < 2 || len(q.Levels) > 10 || len(q.Choices) != 0 {
				return fmt.Errorf("score %q requires 2–10 ordered levels", name)
			}
			for _, level := range q.Levels {
				if strings.TrimSpace(level) == "" {
					return fmt.Errorf("score %q requires nonempty levels", name)
				}
			}
		default:
			return fmt.Errorf("unknown decision question type %q", q.Type)
		}
	}
	if (len(d.Routes) > 0) != (len(d.Otherwise) > 0) {
		return fmt.Errorf("decision routes require an otherwise branch")
	}
	for i, route := range d.Routes {
		match := route.When
		q, exists := d.Questions[match.Question]
		if !exists || len(route.Steps) == 0 {
			return fmt.Errorf("decision route %d needs an existing question and steps", i+1)
		}
		if q.Type == "choice" {
			if _, ok := q.Choices[match.Equals]; !ok || match.Field != "choice" || match.Min != nil || match.Max != nil {
				return fmt.Errorf("decision route %d must match a configured choice", i+1)
			}
			continue
		}
		field, max := "score", float64(len(q.Levels)-1)
		if q.Type == "predicate" {
			field, max = "probability_true", 1
		}
		if match.Field != field || match.Equals != "" || (match.Min == nil && match.Max == nil) ||
			!validDecisionBound(match.Min, max) || !validDecisionBound(match.Max, max) ||
			(match.Min != nil && match.Max != nil && *match.Min > *match.Max) {
			return fmt.Errorf("decision route %d needs valid %s bounds", i+1, field)
		}
	}
	for _, branch := range d.Branches() {
		names := make(map[string]bool)
		for _, step := range branch {
			if strings.TrimSpace(step.Name) == "" || names[step.Name] || step.Agent == "" ||
				(step.Type != "" && step.Type != "sequential") || len(step.Steps) != 0 ||
				step.Decision != nil || step.Retrieval != nil || step.DependsOn != nil {
				return fmt.Errorf("decision branches require uniquely named, ordered agent steps")
			}
			names[step.Name] = true
		}
	}
	return nil
}

func validDecisionBound(value *float64, max float64) bool {
	return value == nil || (!math.IsNaN(*value) && *value >= 0 && *value <= max)
}

// RunsWithoutChatProvider reports whether this pipeline can run without a chat provider.
func (o *OrchestratorConfig) RunsWithoutChatProvider() bool {
	if o.Strategy != "Pipeline" || o.Pipeline == nil || o.Pipeline.Synthesis || len(o.Pipeline.Steps) == 0 {
		return false
	}
	for _, step := range o.Pipeline.Steps {
		if step.Type == "retrieval" && step.Retrieval != nil {
			continue
		}
		if step.Type != "decision" || step.Decision == nil || len(step.Decision.Routes) != 0 || len(step.Decision.Otherwise) != 0 {
			return false
		}
	}
	return true
}
