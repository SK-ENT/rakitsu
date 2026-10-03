package acp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// turnRecord is what a recording RunFunc saw for one turn.
type turnRecord struct {
	query   string
	conv    *ConvTurn
	history []llm.Message // copy of conv.History at call time
}

type recorder struct {
	mu        sync.Mutex
	turns     []turnRecord
	summaries []string // prompts handed to the fake summarizer
	sumErr    error
}

func (r *recorder) run() RunFunc {
	return func(_ context.Context, _ *config.Config, _ *telemetry.EventBus, _ *debug.DebugController, query string, _ func([]debug.Attachable), conv *ConvTurn) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		rec := turnRecord{query: query, conv: conv}
		if conv != nil {
			rec.history = append([]llm.Message(nil), conv.History...)
			conv.Summarize = func(_ context.Context, prompt string) (string, error) {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.summaries = append(r.summaries, prompt)
				if r.sumErr != nil {
					return "", r.sumErr
				}
				return "SUMMARY-OF-EARLIER", nil
			}
		}
		r.turns = append(r.turns, rec)
		return "answer to " + query, nil
	}
}

func convConfig(t *testing.T, enabled, disableSummary bool) *config.Config {
	t.Helper()
	cfg := loadTestConfig(t)
	cfg.Settings.Memory.Enabled = true
	cfg.Settings.Memory.Dir = t.TempDir()
	cfg.Settings.Memory.Conversation.Enabled = enabled
	cfg.Settings.Memory.Conversation.KeepRecentTurns = 1
	cfg.Settings.Memory.Conversation.DisableSummary = disableSummary
	return cfg
}

func prompt(t *testing.T, srv *Server, id, text string) {
	t.Helper()
	srv.handlePrompt(context.Background(), acpRequest{JSONRPC: "2.0", ID: []byte(`1`), Method: "session/prompt", Params: textPrompt(id, text)})
}

func newConvServer(cfg *config.Config, run RunFunc) (*Server, string) {
	srv := NewServerWithIO(cfg, run, nil, io.Discard)
	srv.sessions.add(newSession("s1"))
	return srv, "s1"
}

func joined(msgs []llm.Message) string {
	var parts []string
	for _, m := range msgs {
		parts = append(parts, m.Role+": "+m.AsText())
	}
	return strings.Join(parts, "\n")
}

func TestConversation_RollingSummaryReplacesFullHistory(t *testing.T) {
	rec := &recorder{}
	srv, id := newConvServer(convConfig(t, true, false), rec.run())

	for _, q := range []string{"q1", "q2", "q3"} {
		prompt(t, srv, id, q)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if got := rec.turns[2].query; got != "q3" {
		t.Errorf("engaged turns must pass the raw query, got %q", got)
	}
	h := joined(rec.turns[2].history)
	if !strings.Contains(h, "SUMMARY-OF-EARLIER") {
		t.Errorf("turn 3 history should carry the rolling summary, got:\n%s", h)
	}
	if strings.Contains(h, "user: q1") || strings.Contains(h, "answer to q1") {
		t.Errorf("turn 3 must not re-feed turn 1 verbatim, got:\n%s", h)
	}
	if !strings.Contains(h, "user: q2") || !strings.Contains(h, "assistant: answer to q2") {
		t.Errorf("turn 3 should keep the most recent turn (keep_recent_turns=1) verbatim, got:\n%s", h)
	}
	if len(rec.summaries) == 0 {
		t.Error("expected at least one summarizer call")
	}
}

func TestConversation_DisableSummaryDropsOldTurnsWithoutSummarizing(t *testing.T) {
	rec := &recorder{}
	srv, id := newConvServer(convConfig(t, true, true), rec.run())
	for _, q := range []string{"q1", "q2", "q3"} {
		prompt(t, srv, id, q)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.summaries) != 0 {
		t.Errorf("disable_summary must not call the summarizer, got %d calls", len(rec.summaries))
	}
	h := joined(rec.turns[2].history)
	if strings.Contains(h, "user: q1") || strings.Contains(h, "Conversation summary") {
		t.Errorf("old turn must be dropped with no summary block, got:\n%s", h)
	}
	if !strings.Contains(h, "user: q2") {
		t.Errorf("recent turn must stay, got:\n%s", h)
	}
}

func TestConversation_SummarizerFailureDegradesToBoundedRecentTurns(t *testing.T) {
	rec := &recorder{sumErr: errors.New("boom")}
	srv, id := newConvServer(convConfig(t, true, false), rec.run())
	n := historyMaxTurns + 5
	for i := 0; i < n; i++ {
		prompt(t, srv, id, "q"+string(rune('A'+i)))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.turns) != n {
		t.Fatalf("every turn must still run despite summarizer failure, ran %d/%d", len(rec.turns), n)
	}
	last := rec.turns[n-1].history
	users := 0
	for _, m := range last {
		if m.Role == "user" {
			users++
		}
	}
	if users != historyMaxTurns {
		t.Errorf("failing summarizer must fall back to the last %d turns, got %d", historyMaxTurns, users)
	}
	if strings.Contains(joined(last), "SUMMARY") {
		t.Error("no summary should appear when summarization failed")
	}
}

func TestConversation_DefaultConfigKeepsTextComposedPath(t *testing.T) {
	memOff := convConfig(t, true, false)
	memOff.Settings.Memory.Enabled = false
	for name, cfg := range map[string]*config.Config{
		"setting absent": convConfig(t, false, false),
		"memory off":     memOff,
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			srv, id := newConvServer(cfg, rec.run())
			prompt(t, srv, id, "first")
			prompt(t, srv, id, "second")
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if rec.turns[1].conv != nil {
				t.Fatal("conv must be nil when conversation memory is not enabled")
			}
			want := "Previous conversation:\nUser: first\nAssistant: answer to first\n\nCurrent question:\nsecond"
			if rec.turns[1].query != want {
				t.Errorf("query = %q, want legacy composition %q", rec.turns[1].query, want)
			}
		})
	}
}

func TestConversation_OrchestratorConfigKeepsTextComposedPath(t *testing.T) {
	cfg := convConfig(t, true, false)
	cfg.Orchestrator = &config.OrchestratorConfig{}
	rec := &recorder{}
	srv, id := newConvServer(cfg, rec.run())
	prompt(t, srv, id, "first")
	prompt(t, srv, id, "second")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.turns[1].conv != nil || !strings.HasPrefix(rec.turns[1].query, "Previous conversation:") {
		t.Errorf("orchestrator configs must keep the text path, got conv=%v query=%q", rec.turns[1].conv, rec.turns[1].query)
	}
}

// Pins that the capped model-visible window never shrinks what the summarizer
// sees: after a run of failures, the first successful fold still covers the
// earliest turns, because Update is fed the full transcript, not the capped one.
func TestConversation_SummarizerRecoveryStillFoldsEarliestTurns(t *testing.T) {
	rec := &recorder{sumErr: errors.New("boom")}
	srv, id := newConvServer(convConfig(t, true, false), rec.run())
	n := historyMaxTurns + 3
	for i := 0; i < n; i++ {
		prompt(t, srv, id, "q"+string(rune('A'+i)))
	}
	rec.mu.Lock()
	rec.sumErr = nil
	rec.summaries = nil
	rec.mu.Unlock()
	prompt(t, srv, id, "recover")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.summaries) == 0 || !strings.Contains(rec.summaries[0], "User: qA") {
		t.Errorf("recovered fold must include the earliest turn qA, prompts: %v", rec.summaries)
	}
}
