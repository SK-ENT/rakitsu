package chathost

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
)

func testCfg() *config.Config {
	return &config.Config{
		Name:        "Content Publishing Pipeline",
		Description: "Multi-stage content pipeline.",
		Agents: []config.AgentDefinition{
			{Name: "Researcher", Role: "worker", SystemPrompt: "Research the topic thoroughly.\nGather facts.", Tools: []string{"web_search"}},
			{Name: "Drafter", Role: "worker", SystemPrompt: "Write a compelling draft."},
		},
		Orchestrator: &config.OrchestratorConfig{
			Name:     "Publisher",
			Strategy: "Pipeline",
			Agents:   []string{"Researcher", "Drafter"},
		},
	}
}

func TestListAgents(t *testing.T) {
	tool := NewListAgentsTool(testCfg())
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var list []agentSummary
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	// 2 agents + 1 orchestrator
	if len(list) != 3 {
		t.Errorf("got %d entries, want 3", len(list))
	}
	// Summary should be the first line
	for _, s := range list {
		if s.Name == "Researcher" && !strings.Contains(s.Summary, "Research the topic") {
			t.Errorf("Researcher summary = %q, want first line", s.Summary)
		}
	}
}

func TestDescribeAgent_Agent(t *testing.T) {
	tool := NewDescribeAgentTool(testCfg())
	out, err := tool.Execute(context.Background(), map[string]interface{}{"name": "Researcher"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var d agentDetail
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if d.Name != "Researcher" {
		t.Errorf("name = %q", d.Name)
	}
	if !strings.Contains(d.SystemPrompt, "Gather facts") {
		t.Errorf("full system_prompt should be returned, got %q", d.SystemPrompt)
	}
}

func TestDescribeAgent_Orchestrator(t *testing.T) {
	tool := NewDescribeAgentTool(testCfg())
	out, err := tool.Execute(context.Background(), map[string]interface{}{"name": "Publisher"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var d agentDetail
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if d.Strategy != "Pipeline" {
		t.Errorf("strategy = %q", d.Strategy)
	}
	if len(d.Agents) != 2 {
		t.Errorf("agents = %v", d.Agents)
	}
}

func TestDescribeAgent_NotFound(t *testing.T) {
	tool := NewDescribeAgentTool(testCfg())
	_, err := tool.Execute(context.Background(), map[string]interface{}{"name": "Nonexistent"})
	if err == nil {
		t.Error("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "list_agents") {
		t.Errorf("error should hint at list_agents tool, got %v", err)
	}
}

func TestDescribeAgent_MissingName(t *testing.T) {
	tool := NewDescribeAgentTool(testCfg())
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestInvokeTool_Description_Default(t *testing.T) {
	tool := NewInvokeTool(nil, false)
	desc := tool.GetDescription()
	if !strings.Contains(desc, "reply directly") {
		t.Errorf("default description should preserve the chitchat escape hatch, got %q", desc)
	}
	if strings.Contains(desc, "Every user turn") {
		t.Errorf("default description should not contain forced-mode mandate, got %q", desc)
	}
}

func TestInvokeTool_Description_Forced(t *testing.T) {
	tool := NewInvokeTool(nil, true)
	desc := tool.GetDescription()
	if strings.Contains(desc, "reply directly") {
		t.Errorf("forced description must NOT contain the chitchat escape hatch, got %q", desc)
	}
	if !strings.Contains(desc, "Every user turn") {
		t.Errorf("forced description should mandate delegation on every turn, got %q", desc)
	}
	if !strings.Contains(desc, "ONLY way") {
		t.Errorf("forced description should state invoke_config is the only answer path, got %q", desc)
	}
}

// fakeRunner satisfies agent.Runner with scriptable Run output. Used to
// drive InvokeTool's retry-guard logic without standing up a real
// orchestrator.
type fakeRunner struct {
	mu      sync.Mutex
	calls   int
	outputs []string // outputs[i] returned on the i-th call; falls back to last value when exhausted
	errs    []error  // errs[i] returned on the i-th call when present (else nil)
	delay   time.Duration
}

func (f *fakeRunner) Run(ctx context.Context, query string) (string, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := ""
	if f.calls < len(f.outputs) {
		out = f.outputs[f.calls]
	} else if len(f.outputs) > 0 {
		out = f.outputs[len(f.outputs)-1]
	}
	var err error
	if f.calls < len(f.errs) {
		err = f.errs[f.calls]
	}
	f.calls++
	return out, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRunner) GetName() string                             { return "fake" }
func (f *fakeRunner) GetRole() agent.AgentRole                    { return agent.RoleWorker }
func (f *fakeRunner) GetTools() []string                          { return nil }
func (f *fakeRunner) SetDebugController(_ *debug.DebugController) {}

func TestInvokeTool_RetryGuard_BreaksLoopOnRepeatedEmpty(t *testing.T) {
	// Two consecutive calls with the same task and empty result on the
	// first call should return the stalled-sentinel on the second call,
	// without re-running the underlying root. This is the repeat-after-empty guard.
	r := &fakeRunner{outputs: []string{"", "should never be reached"}}
	tool := NewInvokeTool(r, true)

	out, err := tool.Execute(context.Background(), map[string]interface{}{"task": "summarize previous result"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if out != "" {
		t.Fatalf("first call should pass through empty result, got %q", out)
	}
	if r.calls != 1 {
		t.Fatalf("first call should hit underlying root once, got %d", r.calls)
	}

	out, err = tool.Execute(context.Background(), map[string]interface{}{"task": "summarize previous result"})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !strings.Contains(out, "no answer") {
		t.Errorf("second call should return stalled sentinel, got %q", out)
	}
	if r.calls != 1 {
		t.Errorf("second call should NOT hit underlying root (guard fired), got %d total calls", r.calls)
	}
}

func TestInvokeTool_RepeatAfterNonEmpty_ReturnsCachedAnswer(t *testing.T) {
	// Once a task produces a non-empty answer, re-invoking the SAME
	// task must NOT re-run the orchestration — it returns the cached
	// answer with a relay instruction. This is the loop that hung during
	// dogfood testing (ChatHost re-invoked one turn 8x).
	r := &fakeRunner{outputs: []string{"actual answer", "should never be reached"}}
	tool := NewInvokeTool(r, true)

	out1, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if out1 != "actual answer" {
		t.Fatalf("first call: got %q, want %q", out1, "actual answer")
	}
	out2, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if r.calls != 1 {
		t.Errorf("second call must NOT re-run the orchestration, got %d total calls", r.calls)
	}
	if !strings.Contains(out2, "actual answer") {
		t.Errorf("second call should hand back the cached answer, got %q", out2)
	}
	if !strings.Contains(out2, "do NOT call invoke_config again") {
		t.Errorf("second call should carry the relay instruction, got %q", out2)
	}
}

func TestInvokeTool_RepeatAfterNonEmpty_StaysShortCircuited(t *testing.T) {
	// The per-turn-answered guard must NOT reset after firing — every subsequent
	// identical call keeps short-circuiting, so a model that ignores the
	// relay instruction still cannot re-run the orchestration.
	r := &fakeRunner{outputs: []string{"the answer", "leaked re-run"}}
	tool := NewInvokeTool(r, true)

	for i := 0; i < 5; i++ {
		out, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "same"})
		if i == 0 {
			continue
		}
		if r.calls != 1 {
			t.Fatalf("call %d re-ran the orchestration (calls=%d) — guard reset", i+1, r.calls)
		}
		if !strings.Contains(out, "the answer") {
			t.Errorf("call %d should keep returning the cached answer, got %q", i+1, out)
		}
	}
}

func TestInvokeTool_RephrasedTaskSameTurn_ReturnsCachedAnswer(t *testing.T) {
	// The rephrased-task loop: after a
	// non-empty answer the chat agent re-invoked with a REPHRASED task,
	// so the same-task guard could not see it. The per-turn guard must
	// catch it — a different task within the SAME turn (no ResetTurn in
	// between) still returns the turn's cached answer, not a re-run.
	r := &fakeRunner{outputs: []string{"answer A", "should never be reached"}}
	tool := NewInvokeTool(r, true)

	out1, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "weakest tested area — list the packages"})
	if out1 != "answer A" {
		t.Fatalf("first call: got %q", out1)
	}
	out2, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "based on the previous answers, the weakest tested area"})
	if r.calls != 1 {
		t.Errorf("rephrased re-invocation must NOT re-run the orchestration, got %d calls", r.calls)
	}
	if !strings.Contains(out2, "answer A") {
		t.Errorf("rephrased call should return the turn's cached answer, got %q", out2)
	}
}

func TestInvokeTool_ConcurrentCallsDoNotDoubleRun(t *testing.T) {
	// The ReAct loop executes every tool call within one LLM turn
	// concurrently, so two invoke_config calls in the same response run as
	// concurrent goroutines. Without the guard held across the whole
	// check-run-update section, both could pass the guard while it was
	// still unset and both call root.Run.
	r := &fakeRunner{outputs: []string{"actual answer"}, delay: 20 * time.Millisecond}
	tool := NewInvokeTool(r, true)

	var wg sync.WaitGroup
	outs := make([]string, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
			outs[i] = out
		}(i)
	}
	close(start)
	wg.Wait()

	if got := r.callCount(); got != 1 {
		t.Fatalf("root.Run called %d times for 2 concurrent invoke_config calls in one turn, want 1", got)
	}
	for i, out := range outs {
		if !strings.Contains(out, "actual answer") {
			t.Errorf("goroutine %d output = %q, want it to contain the answer (direct or cached)", i, out)
		}
	}
}

func TestInvokeTool_ResetTurn_AllowsReRunNextTurn(t *testing.T) {
	// ResetTurn (called by the agent loop at each user turn) clears the
	// per-turn guard so a genuine new turn re-runs the orchestration —
	// even when the user repeats the exact same question.
	r := &fakeRunner{outputs: []string{"turn 1 answer", "turn 2 answer"}}
	tool := NewInvokeTool(r, true)

	out1, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "q"})
	if out1 != "turn 1 answer" {
		t.Fatalf("turn 1: got %q", out1)
	}
	// Same task again within the same turn → cached, no re-run.
	_, _ = tool.Execute(context.Background(), map[string]interface{}{"task": "q"})
	if r.calls != 1 {
		t.Fatalf("same turn: expected 1 underlying run, got %d", r.calls)
	}
	// New user turn.
	tool.ResetTurn()
	out2, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "q"})
	if out2 != "turn 2 answer" {
		t.Errorf("after ResetTurn the same task should re-run, got %q", out2)
	}
	if r.calls != 2 {
		t.Errorf("expected 2 underlying runs across 2 turns, got %d", r.calls)
	}
}

func TestInvokeTool_RetryGuard_RepeatAfterErrorReRuns(t *testing.T) {
	// An errored run clears guard state — errors may be transient, so a
	// repeat of the same task must re-run rather than short-circuit on a
	// stale cached result.
	r := &fakeRunner{
		outputs: []string{"", "recovered answer"},
		errs:    []error{context.DeadlineExceeded, nil},
	}
	tool := NewInvokeTool(r, true)

	_, err1 := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if err1 == nil {
		t.Fatalf("first call should surface the underlying error")
	}
	out2, err2 := tool.Execute(context.Background(), map[string]interface{}{"task": "x"})
	if err2 != nil {
		t.Fatalf("second call: %v", err2)
	}
	if out2 != "recovered answer" {
		t.Errorf("repeat-after-error should re-run, got %q", out2)
	}
	if r.calls != 2 {
		t.Errorf("expected 2 underlying calls, got %d", r.calls)
	}
}

func TestInvokeTool_RetryGuard_DoesNotFireOnTaskChange(t *testing.T) {
	// Empty result on task A followed by a different task B should NOT
	// trigger the guard — only repeat-after-empty on the SAME task does.
	r := &fakeRunner{outputs: []string{"", "answer for B"}}
	tool := NewInvokeTool(r, true)

	_, _ = tool.Execute(context.Background(), map[string]interface{}{"task": "A"})
	out, _ := tool.Execute(context.Background(), map[string]interface{}{"task": "B"})
	if out != "answer for B" {
		t.Errorf("task change should bypass guard, got %q", out)
	}
	if r.calls != 2 {
		t.Errorf("expected both tasks to hit underlying root, got %d", r.calls)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   single line   ", "single line"},
		{"first line\nsecond line", "first line"},
		{strings.Repeat("x", 200), strings.Repeat("x", 117) + "..."},
	}
	for _, tc := range cases {
		if got := firstSentence(tc.in); got != tc.want {
			t.Errorf("firstSentence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
