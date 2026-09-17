package server

import (
	"context"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/memory"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// ctxAwareProvider blocks Generate until its context is done, then reports
// that via the canceled channel — used to detect whether cancellation
// actually reached the summarizer's LLM call.
type ctxAwareProvider struct {
	canceled chan struct{}
}

func (p *ctxAwareProvider) Generate(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	<-ctx.Done()
	close(p.canceled)
	return nil, ctx.Err()
}
func (p *ctxAwareProvider) GetName() string  { return "fake" }
func (p *ctxAwareProvider) GetModel() string { return "fake" }

// summarizeTranscript builds n complete user/assistant turns — enough (with
// KeepRecentTurns: 0 below) to make ConversationMemory.Update actually
// invoke the summarizer instead of no-op'ing on "nothing new to fold".
func summarizeTranscript(n int) []llm.Message {
	var out []llm.Message
	for i := 0; i < n; i++ {
		out = append(out, llm.NewTextMessage("user", "question"), llm.NewTextMessage("assistant", "answer"))
	}
	return out
}

// TestStartBackgroundSummarize_StopsOnClose regression-guards: the
// background summarization goroutine used context.WithTimeout(60s) with no
// tie to s.stopCh. If Close() runs shortly after a turn finishes, this
// goroutine kept running (up to 60s) — still holding the agent and making a
// real LLM call — after the session that owns it had already been torn
// down.
func TestStartBackgroundSummarize_StopsOnClose(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	convMem := memory.NewConversationMemory(store, "sess-1", memory.ConversationOptions{KeepRecentTurns: 0})

	prov := &ctxAwareProvider{canceled: make(chan struct{})}
	ag := agent.NewAgent(&config.AgentDefinition{Name: "a1"}, prov, tools.NewToolRegistry(), telemetry.NewEventBus(8), nil)

	sess := &ChatSession{convMem: convMem, stopCh: make(chan struct{})}
	sess.startBackgroundSummarize(ag, summarizeTranscript(3))

	close(sess.stopCh)

	select {
	case <-prov.canceled:
		// Canceled promptly — good, Close() actually stopped this work
		// instead of leaving it to run out its own 60s timeout.
	case <-time.After(2 * time.Second):
		t.Fatal("background summarize's context was not canceled when stopCh closed — it keeps running up to its own 60s timeout regardless of Close()")
	}
}
