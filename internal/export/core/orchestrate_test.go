package core_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/core"
	"github.com/SK-ENT/rakitsu/internal/export/nemoclaw"
)

// defaultTargets returns both runtime targets for testing.
func defaultTargets() []core.RuntimeTarget {
	return []core.RuntimeTarget{&nemoclaw.OpenShellTarget{}, &core.ComposeTarget{}}
}

func pipelineConfig() *config.Config {
	return &config.Config{
		Name: "Pipeline Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
			},
			Defaults: config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{
			{Name: "Researcher", Role: "worker", Provider: "openai", Model: "gpt-4o"},
			{Name: "Writer", Role: "worker", Provider: "openai", Model: "gpt-4o-mini"},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Agents:   []string{"Researcher", "Writer"},
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{Name: "research", Agent: "Researcher", Task: "Research the topic"},
					{Name: "write", Agent: "Writer", Task: "Write a summary"},
				},
			},
		},
	}
}

func dagConfig() *config.Config {
	return &config.Config{
		Name: "DAG Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
			},
			Defaults: config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{
			{Name: "GoAnalyzer", Role: "worker"},
			{Name: "VueAnalyzer", Role: "worker"},
			{Name: "Synthesizer", Role: "worker"},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Agents:   []string{"GoAnalyzer", "VueAnalyzer", "Synthesizer"},
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{Name: "go-analysis", Agent: "GoAnalyzer", Task: "Analyze Go code"},
					{Name: "vue-analysis", Agent: "VueAnalyzer", Task: "Analyze Vue code", DependsOn: []string{}},
					{Name: "synthesize", Agent: "Synthesizer", Task: "Combine results", DependsOn: []string{"go-analysis", "vue-analysis"}},
				},
			},
		},
	}
}

func reactConfig() *config.Config {
	return &config.Config{
		Name: "ReAct Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderDefinition{
				"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"},
			},
			Defaults: config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{
			{Name: "LogAnalyzer", Role: "worker"},
			{Name: "InfraEngineer", Role: "worker"},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Supervisor",
			Strategy: "ReAct",
			Agents:   []string{"LogAnalyzer", "InfraEngineer"},
		},
	}
}

// --- RuntimeTarget tests ---

func TestComposeTarget_RunAgentCase(t *testing.T) {
	ct := &core.ComposeTarget{}
	cas := ct.RunAgentCase()
	if !strings.Contains(cas, "docker compose exec") {
		t.Error("ComposeTarget should use docker compose exec")
	}
	if !strings.Contains(cas, "openclaw run") {
		t.Error("ComposeTarget should use openclaw run")
	}
}

// --- Functional tests ---

func TestPipelineScript_Sequential(t *testing.T) {
	cfg := pipelineConfig()
	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if !strings.Contains(script, "research") {
		t.Error("script missing research step")
	}
	if !strings.Contains(script, "write") {
		t.Error("script missing write step")
	}

	// Sequential: should NOT have background (&) for these steps
	lines := strings.Split(script, "\n")
	bgCount := 0
	for _, line := range lines {
		if strings.Contains(line, "run_agent") && strings.HasSuffix(strings.TrimSpace(line), "&") {
			bgCount++
		}
	}
	if bgCount > 0 {
		t.Errorf("sequential pipeline should not have background steps, found %d", bgCount)
	}

	if strings.Count(script, "run_agent") < 2 {
		t.Error("expected at least 2 run_agent calls")
	}

	if !strings.Contains(script, "openshell)") {
		t.Error("script should have openshell mode")
	}
	if !strings.Contains(script, "compose)") {
		t.Error("script should have compose mode")
	}
}

func TestPipelineScript_DAG(t *testing.T) {
	cfg := dagConfig()
	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if !strings.Contains(script, "(parallel)") {
		t.Error("script should indicate parallel execution")
	}
	if !strings.Contains(script, "wait") {
		t.Error("parallel steps should have wait commands")
	}
	if !strings.Contains(script, "depends on") {
		t.Error("synthesize step should show dependencies")
	}
}

func TestPipelineScript_Loop(t *testing.T) {
	cfg := &config.Config{
		Name: "Loop Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"}},
			Defaults:        config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{
			{Name: "Writer", Role: "worker"},
			{Name: "Reviewer", Role: "worker"},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{
						Name:            "draft",
						Type:            "loop",
						Agent:           "Writer",
						Task:            "Write a draft",
						ConditionAgent:  "Reviewer",
						ConditionPrompt: "Is this draft good enough? Reply PASS or FAIL.",
						MaxIterations:   3,
					},
				},
			},
		},
	}

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if !strings.Contains(script, "while") {
		t.Error("loop step should generate while loop")
	}
	if !strings.Contains(script, "3") {
		t.Error("should reference max iterations (3)")
	}
	if !strings.Contains(script, "PASS") {
		t.Error("loop should check for PASS condition")
	}
	if !strings.Contains(script, "reviewer") {
		t.Error("should reference reviewer condition agent")
	}

	// Bash syntax check
	if _, err := exec.LookPath("bash"); err == nil {
		tmpFile := filepath.Join(t.TempDir(), "loop.sh")
		os.WriteFile(tmpFile, []byte(script), 0755)
		cmd := exec.Command("bash", "-n", tmpFile)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash syntax check failed: %v\n%s", err, string(output))
		}
	}
}

func TestPipelineScript_Synthesis(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Synthesis = true
	cfg.Orchestrator.Pipeline.SynthesisPrompt = "Combine all results into a final report"

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if !strings.Contains(script, "Synthesis") {
		t.Error("script should have synthesis section")
	}
	if !strings.Contains(script, "input-synthesis.txt") {
		t.Error("script should create synthesis input file")
	}
}

func TestPipelineScript_NestedContainerStep_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{
			Name: "group",
			Type: "parallel",
			Steps: []config.PipelineStep{
				{Name: "sub-a", Agent: "Researcher", Task: "a"},
				{Name: "sub-b", Agent: "Writer", Task: "b"},
			},
		},
	}

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for a top-level nested-container step, got nil")
	}
	if !strings.Contains(err.Error(), "nested sub-steps") {
		t.Errorf("expected error to mention nested sub-steps, got: %v", err)
	}
}

func TestPipelineScript_EmptyAgentStep_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = append(cfg.Orchestrator.Pipeline.Steps,
		config.PipelineStep{Name: "orphan", Task: "no agent set"})

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for a step with no agent, got nil")
	}
	if !strings.Contains(err.Error(), "no agent specified") {
		t.Errorf("expected error to mention no agent specified, got: %v", err)
	}
}

func TestPipelineScript_AgentSanitizesToEmptyID_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	// Agent is non-empty, but shared.SanitizeID strips every character
	// outside [a-zA-Z0-9 _-], so this sanitizes to "" — the same downstream
	// condition GeneratePipelineScript actually checks. A raw `s.Agent == ""`
	// check would have missed this and let it through to the parallel-level
	// wait loop, where it crashes on an unset $PID_ variable under set -u.
	cfg.Orchestrator.Pipeline.Steps = append(cfg.Orchestrator.Pipeline.Steps,
		config.PipelineStep{Name: "orphan", Agent: "!!!", Task: "agent sanitizes to empty"})

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for an agent that sanitizes to an empty id, got nil")
	}
	if !strings.Contains(err.Error(), "sanitizes to an empty id") {
		t.Errorf("expected error to mention the sanitized-to-empty agent id, got: %v", err)
	}
}

func TestPipelineScript_ParallelStepAgentSanitizesToEmptyID_DoesNotCrash(t *testing.T) {
	// Same root cause as above, but specifically in a parallel level (two
	// steps sharing the same DependsOn), which is where the unguarded case
	// used to actually crash the generated script rather than just silently
	// dropping the step.
	cfg := dagConfig()
	for i := range cfg.Orchestrator.Pipeline.Steps {
		if cfg.Orchestrator.Pipeline.Steps[i].Name == "go-analysis" {
			cfg.Orchestrator.Pipeline.Steps[i].Agent = "###"
		}
	}

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for a parallel-level agent that sanitizes to an empty id, got nil")
	}
}

func TestPipelineScript_AgentWithNestedSteps_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	// A step with both a non-empty Agent AND nested Steps used to pass
	// validation (only Agent == "" was checked), then silently drop the
	// sub-steps — the script generator only ever reads step.Agent.
	cfg.Orchestrator.Pipeline.Steps = append(cfg.Orchestrator.Pipeline.Steps,
		config.PipelineStep{
			Name:  "container",
			Agent: "Researcher",
			Type:  "parallel",
			Steps: []config.PipelineStep{{Name: "inner", Agent: "Writer", Task: "x"}},
		})

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for a step with both an agent and nested sub-steps, got nil")
	}
	if !strings.Contains(err.Error(), "nested sub-steps") {
		t.Errorf("expected error to mention nested sub-steps, got: %v", err)
	}
}

func TestPipelineScript_SynthesisWithNoAgents_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Synthesis = true
	cfg.Agents = nil

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for synthesis enabled with no agents, got nil")
	}
	if !strings.Contains(err.Error(), "no agents") {
		t.Errorf("expected error to mention no agents, got: %v", err)
	}
}

func TestPipelineScript_DuplicateStepNames_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "Data A", Agent: "Researcher", Task: "a"},
		{Name: "data-a", Agent: "Writer", Task: "b"},
	}

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for colliding step names, got nil")
	}
	if !strings.Contains(err.Error(), "same id") {
		t.Errorf("expected error to mention the id collision, got: %v", err)
	}
}

func TestPipelineScript_FinalOutput_UsesActualDAGSink_NotArrayOrder(t *testing.T) {
	cfg := pipelineConfig()
	// "final" is declared FIRST in the array but is the true DAG sink —
	// nothing depends on it. "source" is declared LAST but is a dependency
	// of "final", so steps[len(steps)-1] would (incorrectly) pick "source".
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "final", Agent: "Writer", Task: "final step", DependsOn: []string{"source"}},
		{Name: "source", Agent: "Researcher", Task: "source step", DependsOn: []string{}},
	}

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if !strings.Contains(script, `cat "$WORKSPACE/output-final.txt"`) {
		t.Errorf("expected final output to cat the true DAG sink (final), got:\n%s", script)
	}
}

func TestPipelineScript_MultipleIndependentSinks_RejectsExport(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "branch-a", Agent: "Researcher", Task: "a", DependsOn: []string{}},
		{Name: "branch-b", Agent: "Writer", Task: "b", DependsOn: []string{}},
	}
	// Synthesis stays disabled (zero value): two independent branches with
	// no single final consumer is ambiguous for "print the final result".

	_, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err == nil {
		t.Fatal("expected an error for ambiguous final output, got nil")
	}
	if !strings.Contains(err.Error(), "independent final steps") {
		t.Errorf("expected error to mention independent final steps, got: %v", err)
	}
}

func TestPipelineScript_Loop_PreservesDependsOnContextOnRetry(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Agents = append(cfg.Agents, config.AgentDefinition{Name: "Reviewer", Role: "worker"})
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "research", Agent: "Researcher", Task: "Research the topic"},
		{
			Name:           "draft",
			Type:           "loop",
			Agent:          "Writer",
			Task:           "Write a draft",
			DependsOn:      []string{"research"},
			ConditionAgent: "Reviewer",
			MaxIterations:  3,
		},
	}

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if !strings.Contains(script, `cp "$WORKSPACE/input-draft.txt" "$WORKSPACE/input-draft-base.txt"`) {
		t.Error("expected the loop to snapshot its dependency-inclusive input before the loop starts")
	}
	if !strings.Contains(script, `cat "$WORKSPACE/input-draft-base.txt"`) {
		t.Error("expected retry input to be rebuilt from the base snapshot, not the bare task file")
	}
}

func TestPipelineScript_Loop_AnchoredPassCheck_NotBareSubstring(t *testing.T) {
	cfg := &config.Config{
		Name: "Loop Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"}},
			Defaults:        config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{
			{Name: "Writer", Role: "worker"},
			{Name: "Reviewer", Role: "worker"},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{
						Name:           "draft",
						Type:           "loop",
						Agent:          "Writer",
						Task:           "Write a draft",
						ConditionAgent: "Reviewer",
						MaxIterations:  3,
					},
				},
			},
		},
	}

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	if strings.Contains(script, `grep -qi 'PASS'`) {
		t.Error("script should no longer use the bare whole-file substring PASS check")
	}
	if !strings.Contains(script, "head -n1") {
		t.Error("expected the check to read only the first line of the condition result")
	}
	if !strings.Contains(script, "single word on the first line") {
		t.Error("default condition prompt should ask for a single PASS/FAIL word on the first line")
	}
}

func TestReActScript(t *testing.T) {
	cfg := reactConfig()
	script := core.GenerateReActScript(cfg, defaultTargets())

	if !strings.Contains(script, "ReAct") {
		t.Error("script should mention ReAct strategy")
	}
	if !strings.Contains(script, "delegate.sh") {
		t.Error("script should reference delegate.sh")
	}
	if !strings.Contains(script, "supervisor") {
		t.Error("script should reference supervisor agent")
	}
}

func TestDelegateScript(t *testing.T) {
	cfg := reactConfig()
	script := core.GenerateDelegateScript(cfg, defaultTargets())

	if !strings.Contains(script, "^[a-z0-9][a-z0-9-]*$") {
		t.Error("delegate.sh should validate agent name format")
	}
	if !strings.Contains(script, "log-analyzer") {
		t.Error("delegate.sh should include log-analyzer in known agents")
	}
	if !strings.Contains(script, "infra-engineer") {
		t.Error("delegate.sh should include infra-engineer in known agents")
	}
	if !strings.Contains(script, "openshell") {
		t.Error("delegate.sh should use openshell commands")
	}
}

func TestDelegateScript_NoHallucinatedCommands(t *testing.T) {
	cfg := reactConfig()
	script := core.GenerateDelegateScript(cfg, defaultTargets())

	if strings.Contains(script, "nemoclaw run") {
		t.Error("delegate.sh should NOT contain hallucinated 'nemoclaw run' command")
	}
}

func TestDockerCompose(t *testing.T) {
	cfg := pipelineConfig()
	compose := core.GenerateDockerCompose(cfg)

	if !strings.Contains(compose, "agent-researcher:") {
		t.Error("missing agent-researcher service")
	}
	if !strings.Contains(compose, "agent-writer:") {
		t.Error("missing agent-writer service")
	}
	if !strings.Contains(compose, "context-data:") {
		t.Error("missing context-data volume")
	}
	if !strings.Contains(compose, "internal: true") {
		t.Error("missing internal network configuration")
	}
	if !strings.Contains(compose, ":ro") {
		t.Error("openclaw.json should be mounted read-only")
	}
}

func TestDockerCompose_NoSecretValues(t *testing.T) {
	cfg := pipelineConfig()
	compose := core.GenerateDockerCompose(cfg)

	// GenerateDockerCompose's environment entries are Compose's name-only
	// passthrough form ("- OPENAI_API_KEY", no value — the container inherits
	// the value from the host shell at `docker-compose up` time), never
	// "NAME=value". Any '=' at all is therefore already wrong, whether it's a
	// literal secret ("- OPENAI_API_KEY=sk-abc:xyz") or even a resolved
	// "${VAR}" reference — the old colon/hash heuristic here let both slip
	// through undetected.
	for _, line := range strings.Split(compose, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") && strings.Contains(trimmed, "=") {
			t.Errorf("environment entry should be name-only passthrough (no '='): %s", trimmed)
		}
	}
}

func TestDockerCompose_NoPorts(t *testing.T) {
	cfg := pipelineConfig()
	compose := core.GenerateDockerCompose(cfg)

	if strings.Contains(compose, "ports:") {
		t.Error("docker-compose should not publish ports")
	}
}

func TestBuildStepLevels(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", Agent: "A"},
		{Name: "b", Agent: "B"},
		{Name: "c", Agent: "C"},
	}
	levels, err := core.BuildStepLevels(steps)
	if err != nil {
		t.Fatalf("BuildStepLevels: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(levels))
	}
	if levels[0][0].Name != "a" || levels[1][0].Name != "b" || levels[2][0].Name != "c" {
		t.Error("levels not in expected order")
	}
}

func TestBuildStepLevels_DAG(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", Agent: "A"},
		{Name: "b", Agent: "B", DependsOn: []string{}},
		{Name: "c", Agent: "C", DependsOn: []string{"a", "b"}},
	}
	levels, err := core.BuildStepLevels(steps)
	if err != nil {
		t.Fatalf("BuildStepLevels: %v", err)
	}
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels (parallel + synthesis), got %d", len(levels))
	}
	if len(levels[0]) != 2 {
		t.Errorf("level 0 should have 2 parallel steps, got %d", len(levels[0]))
	}
	if len(levels[1]) != 1 || levels[1][0].Name != "c" {
		t.Error("level 1 should have step c")
	}
}

func TestBuildStepLevels_CycleDetection(t *testing.T) {
	steps := []config.PipelineStep{
		{Name: "a", Agent: "A", DependsOn: []string{"b"}},
		{Name: "b", Agent: "B", DependsOn: []string{"a"}},
	}
	_, err := core.BuildStepLevels(steps)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle: %v", err)
	}
}

func TestScriptBashSyntax(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found, skipping syntax check")
	}

	cfg := pipelineConfig()
	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}

	tmpFile := filepath.Join(t.TempDir(), "test.sh")
	os.WriteFile(tmpFile, []byte(script), 0755)

	cmd := exec.Command("bash", "-n", tmpFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("bash syntax check failed: %v\n%s", err, string(output))
	}
}

func TestScriptBashSyntax_ReAct(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}

	cfg := reactConfig()

	script := core.GenerateReActScript(cfg, defaultTargets())
	tmpFile := filepath.Join(t.TempDir(), "orchestrate.sh")
	os.WriteFile(tmpFile, []byte(script), 0755)

	cmd := exec.Command("bash", "-n", tmpFile)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("orchestrate.sh syntax check failed: %v\n%s", err, string(output))
	}

	delegate := core.GenerateDelegateScript(cfg, defaultTargets())
	tmpFile2 := filepath.Join(t.TempDir(), "delegate.sh")
	os.WriteFile(tmpFile2, []byte(delegate), 0755)

	cmd2 := exec.Command("bash", "-n", tmpFile2)
	if output, err := cmd2.CombinedOutput(); err != nil {
		t.Errorf("delegate.sh syntax check failed: %v\n%s", err, string(output))
	}
}

// --- Injection regression tests ---
//
// These assert the generated script does not EXECUTE injected commands —
// bash -n (syntax-only) is not enough, since a successful injection produces
// a syntactically valid script.

// fakeSuccessTarget is a minimal RuntimeTarget whose run_agent case always
// succeeds without any real external binary (docker/openshell aren't
// available in the test sandbox), so a full multi-level pipeline script can
// run to completion and reach every step's input-preparation code, where the
// injected content under test lives.
type fakeSuccessTarget struct{}

func (fakeSuccessTarget) Name() string                           { return "test" }
func (fakeSuccessTarget) RunAgentCase() string                   { return `      echo "ok" > "$output_file"` }
func (fakeSuccessTarget) SetupCommands(agentIDs []string) string { return "" }
func (fakeSuccessTarget) TeardownCommands() string               { return "" }
func (fakeSuccessTarget) DelegateCase() string                   { return `  echo "ok"` }

// runGeneratedScript writes script to a temp file, executes it against
// fakeSuccessTarget's "test" mode, and returns whether canaryPath was
// created — i.e. whether injected content ran as a real command instead of
// staying inert data.
func runGeneratedScript(t *testing.T, script, canaryPath string) (ran bool) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found, skipping injection check")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "orchestrate.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("bash", scriptPath, "test")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		// fakeSuccessTarget's run_agent case always succeeds, so a
		// correctly-quoted script has no legitimate reason to exit non-zero.
		// Treating this as merely informational let an unrelated crash (e.g.
		// a bug that kills the script before it reaches the injected
		// content) report "safe" for the wrong reason — the canary was never
		// created because the script never got that far, not because the
		// injection was neutralized. Fail loudly instead.
		t.Fatalf("generated script failed unexpectedly: %v\n%s", err, output)
	}

	_, err := os.Stat(canaryPath)
	return err == nil
}

func TestPipelineScript_StepNameDoubleQuoteInjection_DoesNotExecute(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "INJECTED")
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: `evil"; touch ` + canary + `; echo "`, Type: "loop", Agent: "Researcher", Task: "x", MaxIterations: 1},
	}

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if runGeneratedScript(t, script, canary) {
		t.Fatal("step name with an embedded double-quote executed injected content")
	}
}

func TestPipelineScript_DependsOnSingleQuoteInjection_DoesNotExecute(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "INJECTED")
	cfg := dagConfig()
	maliciousDepName := `go-analysis'; touch ` + canary + `; echo '`
	cfg.Orchestrator.Pipeline.Steps[0].Name = maliciousDepName
	cfg.Orchestrator.Pipeline.Steps[2].DependsOn = []string{maliciousDepName, "vue-analysis"}

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if runGeneratedScript(t, script, canary) {
		t.Fatal("DependsOn entry with an embedded single-quote executed injected content")
	}
}

func TestPipelineScript_TaskHeredocCollision_DoesNotExecute(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "INJECTED")
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "research", Agent: "Researcher", Task: "cover text\nTASK_EOF\ntouch " + canary + "\ncat <<'DUMMY'\ntrailer"},
	}

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if strings.Contains(script, "<<'TASK_EOF'") {
		t.Error("script should no longer use a fixed-delimiter heredoc for step.Task")
	}
	if runGeneratedScript(t, script, canary) {
		t.Fatal("task text containing a line matching the old heredoc delimiter executed injected content")
	}
}

func TestPipelineScript_SynthesisStepNameSingleQuoteInjection_DoesNotExecute(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "INJECTED")
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Synthesis = true
	cfg.Orchestrator.Pipeline.Steps[0].Name = `research'; touch ` + canary + `; echo '`

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if runGeneratedScript(t, script, canary) {
		t.Fatal("synthesis step name with an embedded single-quote executed injected content")
	}
}

func TestPipelineScript_CommentNewlineInjection_StaysCommented(t *testing.T) {
	cfg := pipelineConfig()
	cfg.Orchestrator.Pipeline.Steps = []config.PipelineStep{
		{Name: "research\ntouch /tmp/should-not-run-as-a-command", Agent: "Researcher", Task: "x"},
	}

	script, err := core.GeneratePipelineScript(cfg, defaultTargets())
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "touch /tmp/should-not-run-as-a-command") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Errorf("a step name containing a newline broke out of its comment: %q", line)
		}
	}
}

// --- Bash-identifier regression tests ---
//
// shared.SanitizeID produces kebab-case output (dashes) — fine as a filename
// component, but invalid inside a bash variable name. Any multi-word step
// name used to break the generated LOOP_ITER_/PID_ variables.

func TestPipelineScript_MultiWordLoopStepName_ValidBashIdentifier(t *testing.T) {
	cfg := &config.Config{
		Name: "Loop Test",
		Settings: config.Settings{
			DefaultProvider: "openai",
			Providers:       map[string]config.ProviderDefinition{"openai": {Type: "openai", APIKey: "${OPENAI_API_KEY}"}},
			Defaults:        config.DefaultSettings{Model: "gpt-4o-mini"},
		},
		Agents: []config.AgentDefinition{{Name: "Writer", Role: "worker"}},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Lead",
			Strategy: "Pipeline",
			Pipeline: &config.PipelineConfig{
				Steps: []config.PipelineStep{
					{Name: "Draft Response", Type: "loop", Agent: "Writer", Task: "Write a draft", MaxIterations: 1},
				},
			},
		},
	}

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if strings.Contains(script, "DRAFT-RESPONSE") {
		t.Error("a bash variable name contains a dash from the kebab-case step ID")
	}
	if _, err := exec.LookPath("bash"); err == nil {
		tmpFile := filepath.Join(t.TempDir(), "orchestrate.sh")
		os.WriteFile(tmpFile, []byte(script), 0755) //nolint:errcheck
		cmd := exec.Command("bash", tmpFile, "test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("multi-word loop step name broke the script: %v\n%s", err, out)
		}
	}
}

func TestPipelineScript_MultiWordParallelStepName_ValidBashIdentifier(t *testing.T) {
	cfg := dagConfig()
	cfg.Orchestrator.Pipeline.Steps[0].Name = "Go Analysis"
	cfg.Orchestrator.Pipeline.Steps[1].Name = "Vue Analysis"
	cfg.Orchestrator.Pipeline.Steps[2].DependsOn = []string{"Go Analysis", "Vue Analysis"}

	script, err := core.GeneratePipelineScript(cfg, []core.RuntimeTarget{fakeSuccessTarget{}})
	if err != nil {
		t.Fatalf("GeneratePipelineScript: %v", err)
	}
	if strings.Contains(script, "GO-ANALYSIS") || strings.Contains(script, "VUE-ANALYSIS") {
		t.Error("a PID_ variable name contains a dash from the kebab-case step ID")
	}
	if _, err := exec.LookPath("bash"); err == nil {
		tmpFile := filepath.Join(t.TempDir(), "orchestrate.sh")
		os.WriteFile(tmpFile, []byte(script), 0755) //nolint:errcheck
		cmd := exec.Command("bash", tmpFile, "test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("multi-word parallel step name broke the script: %v\n%s", err, out)
		}
	}
}

// --- Hallucination guard tests ---

func TestGeneratedScripts_NoHallucinatedCommands(t *testing.T) {
	hallucinated := []string{
		"nemoclaw run",
		`nemoclaw "agent-`,
	}

	targets := defaultTargets()
	configs := []*config.Config{pipelineConfig(), dagConfig(), reactConfig()}

	for _, cfg := range configs {
		var scripts []string

		if cfg.Orchestrator.Strategy == "Pipeline" {
			s, err := core.GeneratePipelineScript(cfg, targets)
			if err != nil {
				t.Fatalf("GeneratePipelineScript: %v", err)
			}
			scripts = append(scripts, s)
		} else {
			scripts = append(scripts, core.GenerateReActScript(cfg, targets))
			scripts = append(scripts, core.GenerateDelegateScript(cfg, targets))
		}

		for _, script := range scripts {
			for _, h := range hallucinated {
				if strings.Contains(script, h) {
					t.Errorf("config %q: script contains hallucinated command %q", cfg.Name, h)
				}
			}
		}
	}
}

// --- Integration: NemoClaw multi-agent export with orchestration ---

func TestNemoClawMultiAgent_Orchestration(t *testing.T) {
	dir := t.TempDir()
	cfg := pipelineConfig()

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	for _, name := range []string{"orchestrate.sh", "docker-compose.yaml", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing file: %s", name)
		}
	}

	info, err := os.Stat(filepath.Join(dir, "orchestrate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0100 == 0 {
		t.Error("orchestrate.sh should be executable")
	}

	for _, name := range []string{"agent-researcher", "agent-writer"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing agent dir: %s", name)
		}
	}

	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(readme), "orchestrate.sh") {
		t.Error("README should reference orchestrate.sh")
	}

	script, _ := os.ReadFile(filepath.Join(dir, "orchestrate.sh"))
	scriptStr := string(script)
	if strings.Contains(scriptStr, "nemoclaw run") || strings.Contains(scriptStr, "nemoclaw start") {
		t.Error("orchestrate.sh should NOT contain hallucinated NemoClaw commands")
	}
	if !strings.Contains(scriptStr, "openshell sandbox") {
		t.Error("orchestrate.sh should use openshell sandbox commands")
	}
}

func TestNemoClawMultiAgent_ReAct_HasDelegate(t *testing.T) {
	dir := t.TempDir()
	cfg := reactConfig()

	exp := &nemoclaw.Exporter{}
	if err := exp.Export(cfg, "", dir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "delegate.sh")); err != nil {
		t.Error("ReAct export missing delegate.sh")
	}

	info, err := os.Stat(filepath.Join(dir, "delegate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0100 == 0 {
		t.Error("delegate.sh should be executable")
	}

	script, _ := os.ReadFile(filepath.Join(dir, "delegate.sh"))
	if strings.Contains(string(script), "nemoclaw run") {
		t.Error("delegate.sh should NOT contain hallucinated 'nemoclaw run'")
	}
}
