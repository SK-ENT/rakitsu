package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestTranscriptSinkFiresWithFullHistory asserts the sink receives the
// agent's complete conversation — the seeded prior history, the new user
// message, and the assistant reply — after a run that answers directly.
func TestTranscriptSinkFiresWithFullHistory(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(stopResponse("42"))
	ag := newE2EAgent("assistant", provider, bus)

	var got []llm.Message
	ag.SetTranscriptSink(func(h []llm.Message, _ error) {
		got = append([]llm.Message(nil), h...)
	})

	prior := []llm.Message{llm.NewTextMessage("user", "earlier"), llm.NewTextMessage("assistant", "ok")}
	if _, err := ag.RunWithHistory(context.Background(), "what is the answer?", prior); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) < 4 {
		t.Fatalf("sink got %d messages, want at least 4 (prior 2 + new user + assistant reply)", len(got))
	}
	if got[0].AsText() != "earlier" || got[1].AsText() != "ok" {
		t.Errorf("prior history not preserved: %+v", got[:2])
	}
	if got[2].Role != "user" || got[2].AsText() != "what is the answer?" {
		t.Errorf("new user message missing: %+v", got[2])
	}
	last := got[len(got)-1]
	if last.Role != "assistant" || last.AsText() != "42" {
		t.Errorf("transcript does not end with the agent's answer: %+v", last)
	}
}

// TestTranscriptSinkFiresOnToolCallRun asserts the sink captures the ReAct
// transcript — the assistant's tool call and the tool result — which is what
// makes "why did you do X?" answerable after the fact.
func TestTranscriptSinkFiresOnToolCallRun(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	mt := &mockTool{name: "peek", output: "the README only"}
	provider := newSequenceProvider(
		toolCallResponse("", tc("peek", map[string]interface{}{})),
		stopResponse("I read the README"),
	)
	ag := newE2EAgent("assistant", provider, bus, mt)

	var got []llm.Message
	ag.SetTranscriptSink(func(h []llm.Message, _ error) { got = append([]llm.Message(nil), h...) })

	if _, err := ag.Run(context.Background(), "what did you read?"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sawToolResult, sawToolCall bool
	for _, m := range got {
		if m.AsText() == "the README only" {
			sawToolResult = true
		}
		if m.Role == "assistant" {
			for _, call := range m.ToolCalls {
				if call.Name == "peek" {
					sawToolCall = true
				}
			}
		}
	}
	if !sawToolResult {
		t.Errorf("tool result missing from captured transcript: %+v", got)
	}
	if !sawToolCall {
		t.Errorf("assistant tool-call message missing from captured transcript: %+v", got)
	}

	last := got[len(got)-1]
	if last.Role != "assistant" || last.AsText() != "I read the README" {
		t.Errorf("transcript does not end with the agent's final answer: %+v", last)
	}
}

// TestTranscriptSinkUnsetIsNoOp asserts an agent with no sink runs normally.
func TestTranscriptSinkUnsetIsNoOp(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	ag := newE2EAgent("assistant", newSequenceProvider(stopResponse("fine")), bus)
	out, err := ag.Run(context.Background(), "hello")
	if err != nil || out != "fine" {
		t.Fatalf("Run = (%q, %v), want (fine, nil)", out, err)
	}
}

// TestTranscriptSinkOnMaxIterations pins the max_iterations exit path's own
// transcript append (agent.go's "Max iterations reached" branch). Mutation
// MUT-A — deleting that whole `if a.transcriptSink != nil` block — left the
// entire suite green: nothing exercised it. Without the append the captured
// transcript ends mid-turn on a role:"tool" message, which is both a
// dishonest record of the run and, replayed by a later directed chat, an
// unanswered-call history.
func TestTranscriptSinkOnMaxIterations(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	mt := &mockTool{name: "peek", output: "still looking"}
	// Never stops: every iteration asks for another tool call, so the agent
	// runs out of iterations instead of answering. The third response feeds
	// the forced-synthesis pass at the max_iterations exit.
	provider := newSequenceProvider(
		toolCallResponse("", tc("peek", map[string]interface{}{})),
		toolCallResponse("", tc("peek", map[string]interface{}{})),
		stopResponse("here is what I found"),
	)
	ag := newE2EAgentWithSettings("assistant", provider, bus,
		&config.AgentSettings{MaxIterations: 2}, mt)

	var got []llm.Message
	ag.SetTranscriptSink(func(h []llm.Message, _ error) { got = append([]llm.Message(nil), h...) })

	out, err := ag.Run(context.Background(), "find it")
	if err != nil {
		t.Fatalf("max_iterations must degrade gracefully, got error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("transcript sink never fired on the max_iterations path")
	}
	last := got[len(got)-1]
	if last.Role != "assistant" {
		t.Fatalf("transcript ends on a %q message, want the agent's own max_iterations answer: %+v", last.Role, last)
	}
	if last.AsText() != out {
		t.Errorf("transcript's final message = %q, want the returned answer %q", last.AsText(), out)
	}
}

// TestTranscriptSinkReportsMaxIterations: the sink's runErr is what a consumer
// labels the stored transcript with, and a run that burned its whole iteration
// budget did not finish. Reporting nil there told the roster the agent was
// done, and the picker offered it as "done — N turns" — a guess presented as
// fact, the same failure StatusFailed was introduced to stop.
//
// Run/RunWithHistory still return the partial answer with a nil error: this is
// the sink's channel only, so the graceful-degradation contract every other
// caller depends on is unchanged.
func TestTranscriptSinkReportsMaxIterations(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	mt := &mockTool{name: "peek", output: "still looking"}
	// The third response feeds the forced-synthesis pass. Even when it
	// produces an answer, the run still stopped on its iteration budget, so
	// the sink must keep reporting ErrMaxIterations — the synthesized text is
	// best-effort output, not a finished run.
	provider := newSequenceProvider(
		toolCallResponse("", tc("peek", map[string]interface{}{})),
		toolCallResponse("", tc("peek", map[string]interface{}{})),
		stopResponse("best-effort summary"),
	)
	ag := newE2EAgentWithSettings("assistant", provider, bus,
		&config.AgentSettings{MaxIterations: 2}, mt)

	var sinkErr error
	var fired bool
	ag.SetTranscriptSink(func(_ []llm.Message, runErr error) { sinkErr, fired = runErr, true })

	if _, err := ag.Run(context.Background(), "find it"); err != nil {
		t.Fatalf("max_iterations must still degrade gracefully for the caller, got: %v", err)
	}
	if !fired {
		t.Fatal("transcript sink never fired on the max_iterations path")
	}
	if !errors.Is(sinkErr, ErrMaxIterations) {
		t.Errorf("sink runErr = %v, want ErrMaxIterations — a run that ran out of iterations must not be recorded as a clean finish", sinkErr)
	}
}

// TestTranscriptSinkReportsNilOnSuccess is the other half: only a run that
// really did not finish gets an error, or the distinction is worthless.
func TestTranscriptSinkReportsNilOnSuccess(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(stopResponse("fine"))
	ag := newE2EAgentWithSettings("assistant", provider, bus, &config.AgentSettings{MaxIterations: 3})

	var sinkErr error
	var fired bool
	ag.SetTranscriptSink(func(_ []llm.Message, runErr error) { sinkErr, fired = runErr, true })

	if _, err := ag.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fired {
		t.Fatal("transcript sink never fired")
	}
	if sinkErr != nil {
		t.Errorf("sink runErr = %v on a successful run, want nil", sinkErr)
	}
}
