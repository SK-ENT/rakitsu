package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVersionedReferenceExamples(t *testing.T) {
	data, err := os.ReadFile("../../site/data/v0.3.2.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Features []struct {
			ID     string
			Detail []struct {
				Type string `json:"t"`
				Text string
			}
		}
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	strategies := map[string]string{
		"config": "Pipeline", "react-delegation": "ReAct", "pipeline-config": "Pipeline",
		"pipeline-gate": "Pipeline", "hierarchical-demo": "Hierarchical", "pipeline-loop": "Pipeline",
	}
	checked := 0
	for _, feature := range reference.Features {
		for _, block := range feature.Detail {
			if block.Type != "code" || !strings.HasPrefix(block.Text, "name: ") {
				continue
			}
			checked++
			t.Run(feature.ID, func(t *testing.T) {
				problems, err := ValidateYAML(block.Text)
				if err != nil || len(problems) != 0 {
					t.Fatalf("invalid YAML example: %v (%v)", err, problems)
				}
				var cfg struct {
					Orchestrator struct {
						Strategy string
						Pipeline struct {
							Steps []PipelineStep
						}
					}
				}
				if err := yaml.Unmarshal([]byte(block.Text), &cfg); err != nil {
					t.Fatal(err)
				}
				if want := strategies[feature.ID]; want != "" && cfg.Orchestrator.Strategy != want {
					t.Fatalf("strategy = %q, want %q for %s", cfg.Orchestrator.Strategy, want, feature.ID)
				}
				steps := cfg.Orchestrator.Pipeline.Steps
				switch feature.ID {
				case "pipeline-config":
					if len(steps) != 2 || steps[0].Name != "draft" || steps[0].Agent != "Drafter" ||
						steps[1].Name != "edit" || steps[1].Agent != "Editor" ||
						len(steps[1].DependsOn) != 1 || steps[1].DependsOn[0] != "draft" {
						t.Fatal("YAML no longer matches the Drafter → Editor animation")
					}
				case "pipeline-gate":
					if len(steps) != 3 || steps[0].Agent != "Planner" || steps[1].Agent != "Developer" ||
						steps[2].Agent != "Reviewer" || steps[1].RequireToolCall == nil || steps[1].RequireToolCall.Tool != "write_file" {
						t.Fatal("YAML no longer matches the tool-call gate animation")
					}
				case "pipeline-loop":
					if len(steps) != 1 || steps[0].Type != "loop" || steps[0].ConditionAgent != "Judge" ||
						steps[0].MaxIterations != 3 || len(steps[0].Steps) != 1 || steps[0].Steps[0].Agent != "Worker" {
						t.Fatal("YAML no longer matches the loop animation")
					}
				}
			})
		}
	}
	if checked < len(strategies) {
		t.Fatalf("validated %d complete examples, want at least %d", checked, len(strategies))
	}
}
