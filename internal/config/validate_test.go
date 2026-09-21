package config

import (
	"math"
	"strings"
	"testing"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := Config{
		Tools:  []ToolDefinition{{Name: "read_file"}, {Name: "write_file"}},
		Skills: []SkillDefinition{{Name: "summarize"}},
		Agents: []AgentDefinition{
			{Name: "Worker", Tools: []string{"read_file"}, Skills: []string{"summarize"}},
			{Name: "Tester", Tools: []string{"write_file"}},
		},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker", "Tester"},
		},
	}
	errs := cfg.Validate()
	if len(errs) > 0 {
		t.Errorf("expected valid config, got %d errors: %v", len(errs), errs)
	}
}

func TestValidate_DuplicateAgentNames(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{
			{Name: "Worker"},
			{Name: "Worker"},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected duplicate agent error")
	}
	if !strings.Contains(errs[0].Message, "duplicate agent name") {
		t.Errorf("expected duplicate message, got %q", errs[0].Message)
	}
}

func TestValidate_DuplicateToolNames(t *testing.T) {
	cfg := Config{
		Tools: []ToolDefinition{{Name: "read"}, {Name: "read"}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected duplicate tool error")
	}
	if !strings.Contains(errs[0].Message, "duplicate tool name") {
		t.Errorf("expected duplicate message, got %q", errs[0].Message)
	}
}

func TestValidate_DuplicateSkillNames(t *testing.T) {
	cfg := Config{
		Skills: []SkillDefinition{{Name: "sum"}, {Name: "sum"}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected duplicate skill error")
	}
}

func TestValidate_DuplicateOrchestratorNames(t *testing.T) {
	cfg := Config{
		Orchestrator:  &OrchestratorConfig{Name: "Lead"},
		Orchestrators: []OrchestratorConfig{{Name: "Lead"}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected duplicate orchestrator error")
	}
	if !strings.Contains(errs[0].Message, "duplicate orchestrator name") {
		t.Errorf("expected duplicate message, got %q", errs[0].Message)
	}
}

func TestValidate_OrchestratorReferencesUnknownAgent(t *testing.T) {
	cfg := Config{
		Agents:       []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{Name: "Lead", Agents: []string{"Worker", "Ghost"}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected unknown agent reference error")
	}
	if !strings.Contains(errs[0].Message, "Ghost") {
		t.Errorf("expected Ghost in error, got %q", errs[0].Message)
	}
}

func TestValidate_PipelineStepReferencesUnknownAgent(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{
					{Name: "s1", Agent: "Missing"},
				},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected unknown step agent error")
	}
	if !strings.Contains(errs[0].Message, "Missing") {
		t.Errorf("expected Missing in error, got %q", errs[0].Message)
	}
}

func TestValidate_PipelineStepRequireToolCallMissingTool(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{
					{Name: "s1", Agent: "Worker", RequireToolCall: &RequireToolCallGate{}},
				},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected error for require_tool_call with no tool name")
	}
	if !strings.Contains(errs[0].Field, "require_tool_call") {
		t.Errorf("expected error field to reference require_tool_call, got %q", errs[0].Field)
	}
}

func TestValidate_PipelineStepRequireToolCallWithTool_NoError(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{
					{Name: "s1", Agent: "Worker", RequireToolCall: &RequireToolCallGate{Tool: "sh", CommandContains: "node dist/index.js"}},
				},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Fatalf("expected no error for a valid require_tool_call gate, got %v", errs)
	}
}

// TestValidate_RequireToolCall_NaNBound_Rejected is the regression test for
// a real finding caught by automated review: a NaN min_value/max_value
// makes every comparison in checkOutputValueBounds false (value < NaN and
// value > NaN are both always false), so the gate would silently accept
// every value while looking fully configured — and the existing
// min>max check can't catch it either, since NaN comparisons are always
// false there too. Must be rejected explicitly at validation time.
func TestValidate_RequireToolCall_NaNBound_Rejected(t *testing.T) {
	nan := math.NaN()
	valid := 0.5
	tests := []struct {
		name string
		gate *RequireToolCallGate
	}{
		{"NaN min_value", &RequireToolCallGate{Tool: "jev", OutputJSONPath: "a.b", MinValue: &nan}},
		{"NaN max_value", &RequireToolCallGate{Tool: "jev", OutputJSONPath: "a.b", MaxValue: &nan}},
		{"NaN min_value with valid max_value", &RequireToolCallGate{Tool: "jev", OutputJSONPath: "a.b", MinValue: &nan, MaxValue: &valid}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Agents: []AgentDefinition{{Name: "Worker"}},
				Orchestrator: &OrchestratorConfig{
					Name:   "Lead",
					Agents: []string{"Worker"},
					Pipeline: &PipelineConfig{
						Steps: []PipelineStep{{Name: "s1", Agent: "Worker", RequireToolCall: tt.gate}},
					},
				},
			}
			errs := cfg.Validate()
			if len(errs) == 0 {
				t.Fatal("expected an error rejecting the NaN bound, got none")
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Message, "NaN") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an error message mentioning NaN, got: %v", errs)
			}
		})
	}
}

func TestValidate_RequireToolCall_FiniteBounds_NoError(t *testing.T) {
	min, max := 0.3, 0.7
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{{Name: "s1", Agent: "Worker", RequireToolCall: &RequireToolCallGate{
					Tool: "jev", OutputJSONPath: "a.b", MinValue: &min, MaxValue: &max,
				}}},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Fatalf("expected no error for finite, well-ordered bounds, got %v", errs)
	}
}

func TestValidate_NestedParallelStepReferences(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: "A"}, {Name: "B"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"A", "B"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{
					{
						Name: "par",
						Type: "parallel",
						Steps: []PipelineStep{
							{Name: "sub1", Agent: "A"},
							{Name: "sub2", Agent: "NoExist"},
						},
					},
				},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected error for nested parallel step reference")
	}
	if !strings.Contains(errs[0].Message, "NoExist") {
		t.Errorf("expected NoExist in error, got %q", errs[0].Message)
	}
}

func TestValidate_AgentReferencesUnknownTool(t *testing.T) {
	cfg := Config{
		Tools:  []ToolDefinition{{Name: "read_file"}},
		Agents: []AgentDefinition{{Name: "Worker", Tools: []string{"read_file", "ghost_tool"}}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected unknown tool reference error")
	}
	if !strings.Contains(errs[0].Message, "ghost_tool") {
		t.Errorf("expected ghost_tool in error, got %q", errs[0].Message)
	}
}

func TestValidate_AgentReferencesUnknownSkill(t *testing.T) {
	cfg := Config{
		Skills: []SkillDefinition{{Name: "summarize"}},
		Agents: []AgentDefinition{{Name: "Worker", Skills: []string{"summarize", "ghost_skill"}}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected unknown skill reference error")
	}
	if !strings.Contains(errs[0].Message, "ghost_skill") {
		t.Errorf("expected ghost_skill in error, got %q", errs[0].Message)
	}
}

func TestValidate_SubOrchestratorAsRunner(t *testing.T) {
	// Main orchestrator references a sub-orchestrator — should be valid
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrators: []OrchestratorConfig{
			{Name: "SubPipeline", Agents: []string{"Worker"}},
		},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"SubPipeline", "Worker"},
		},
	}
	errs := cfg.Validate()
	if len(errs) > 0 {
		t.Errorf("expected valid config (sub-orchestrator as runner), got %d errors: %v", len(errs), errs)
	}
}

func TestValidate_EmptyAgentName(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: ""}},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected error for empty agent name")
	}
	if !strings.Contains(errs[0].Message, "no name") {
		t.Errorf("expected 'no name' in error, got %q", errs[0].Message)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{
			{Name: "A", Tools: []string{"missing_tool"}},
			{Name: "A"}, // duplicate
		},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Ghost"},
		},
	}
	errs := cfg.Validate()
	if len(errs) < 3 {
		t.Errorf("expected at least 3 errors (dup agent, unknown tool, unknown orch ref), got %d: %v", len(errs), errs)
	}
}

func TestValidateYAML_Valid(t *testing.T) {
	yamlStr := `
name: Test
agents:
  - name: Worker
    role: worker
tools:
  - name: read_file
    type: fs
`
	errs, err := ValidateYAML(yamlStr)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("expected valid, got %d errors: %v", len(errs), errs)
	}
}

func TestValidateYAML_InvalidYAML(t *testing.T) {
	_, err := ValidateYAML("not: [valid: yaml: {")
	if err == nil {
		t.Fatal("expected parse error for invalid YAML")
	}
}

func TestValidateYAML_DuplicateNames(t *testing.T) {
	yamlStr := `
name: Test
agents:
  - name: Worker
  - name: Worker
`
	errs, err := ValidateYAML(yamlStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected duplicate name error")
	}
}

func TestValidate_ConditionAgentReference(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{{Name: "Worker"}},
		Orchestrator: &OrchestratorConfig{
			Name:   "Lead",
			Agents: []string{"Worker"},
			Pipeline: &PipelineConfig{
				Steps: []PipelineStep{
					{
						Name:           "loop",
						Type:           "loop",
						ConditionAgent: "NonExistent",
						Steps:          []PipelineStep{{Name: "s", Agent: "Worker"}},
					},
				},
			},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected error for unknown condition agent")
	}
	if !strings.Contains(errs[0].Message, "NonExistent") {
		t.Errorf("expected NonExistent in error, got %q", errs[0].Message)
	}
}

// TestValidate_B31_HierarchicalSupervisorWarning covers B31: user-declared
// role:supervisor agents alongside a Hierarchical orchestrator are ignored
// at runtime (the orchestrator synthesizes its own supervisor). Emit a
// warning severity so existing configs still load.
func TestValidate_B31_HierarchicalSupervisorWarning(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{
			{Name: "Boss", Role: "supervisor"},
			{Name: "Worker1", Role: "worker"},
			{Name: "Worker2", Role: "worker"},
		},
		Orchestrator: &OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Hierarchical",
			Agents:   []string{"Boss", "Worker1", "Worker2"},
		},
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected B31 warning for supervisor-role agent under Hierarchical")
	}
	var got *ValidationError
	for _, e := range errs {
		if strings.Contains(e.Message, "role=supervisor") {
			got = e
			break
		}
	}
	if got == nil {
		t.Fatalf("missing B31 warning; errs=%v", errs)
	}
	if !got.IsWarning() {
		t.Errorf("B31 should be Severity=warning, got %q", got.Severity)
	}
	if got.IsError() {
		t.Error("B31 must not block config load")
	}
	if !strings.Contains(got.Message, "Boss") {
		t.Errorf("warning should name the offending agent, got %q", got.Message)
	}
}

// TestValidate_B31_NoWarningForNonHierarchical confirms the check is strategy-
// scoped: supervisor-role agents under Pipeline (or no orchestrator) do not
// trigger B31.
func TestValidate_B31_NoWarningForNonHierarchical(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{
			{Name: "Boss", Role: "supervisor"},
			{Name: "W", Role: "worker"},
		},
		Orchestrator: &OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Agents:   []string{"Boss", "W"},
		},
	}
	for _, e := range cfg.Validate() {
		if strings.Contains(e.Message, "role=supervisor") {
			t.Errorf("unexpected B31 warning under Pipeline strategy: %q", e.Message)
		}
	}
}

// TestValidate_B31_SubOrchestratorHierarchical confirms the check picks up
// Hierarchical strategy on sub-orchestrators too, not just the root.
func TestValidate_B31_SubOrchestratorHierarchical(t *testing.T) {
	cfg := Config{
		Agents: []AgentDefinition{
			{Name: "Boss", Role: "supervisor"},
			{Name: "W", Role: "worker"},
		},
		Orchestrator: &OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Agents:   []string{"Boss", "W"},
		},
		Orchestrators: []OrchestratorConfig{
			{Name: "SubHub", Strategy: "Hierarchical", Agents: []string{"Boss", "W"}},
		},
	}
	found := false
	for _, e := range cfg.Validate() {
		if strings.Contains(e.Message, "role=supervisor") && e.IsWarning() {
			found = true
		}
	}
	if !found {
		t.Error("expected B31 warning when a sub-orchestrator uses Hierarchical strategy")
	}
}

// TestValidationError_SeverityDefaults confirms the IsError/IsWarning helpers
// preserve back-compat for the existing checks that don't set Severity.
func TestValidationError_SeverityDefaults(t *testing.T) {
	e := &ValidationError{Field: "x", Message: "y"}
	if !e.IsError() {
		t.Error("empty Severity must default to IsError=true for back-compat")
	}
	if e.IsWarning() {
		t.Error("empty Severity must not be a warning")
	}
	w := &ValidationError{Field: "x", Message: "y", Severity: "warning"}
	if w.IsError() {
		t.Error("Severity=warning must not be an error")
	}
	if !w.IsWarning() {
		t.Error("Severity=warning must be IsWarning=true")
	}
}
