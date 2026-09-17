package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// namedStubProvider is a minimal LLMProvider whose GetName/GetModel are
// configurable. Lets us verify that SetLLMProvider actually swaps the in-use
// provider (LLMProvider() returns the new one) without needing a real LLM.
type namedStubProvider struct{ name, model string }

func (p *namedStubProvider) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return &llm.GenerateResult{Response: "ok", FinishReason: "stop"}, nil
}
func (p *namedStubProvider) GetName() string  { return p.name }
func (p *namedStubProvider) GetModel() string { return p.model }

func newSwapTestAgent(t *testing.T, initialProvider string, initialModel string) (*Agent, *telemetry.EventBus) {
	t.Helper()
	bus := telemetry.NewEventBus(16)
	def := &config.AgentDefinition{
		Name:         "Coder",
		Provider:     initialProvider,
		Model:        initialModel,
		SystemPrompt: "test",
	}
	stub := &namedStubProvider{name: "openai-compatible", model: initialModel}
	a := NewAgent(def, stub, tools.NewToolRegistry(), bus, nil)
	return a, bus
}

func TestSetLLMProvider_GettersReflectInitialState(t *testing.T) {
	a, _ := newSwapTestAgent(t, "litellm", "vllm-reasoning-model-a-long")

	if got := a.Model(); got != "vllm-reasoning-model-a-long" {
		t.Errorf("Model() = %q; want %q", got, "vllm-reasoning-model-a-long")
	}
	if got := a.ProviderName(); got != "litellm" {
		t.Errorf("ProviderName() = %q; want %q", got, "litellm")
	}
	if a.LLMProvider() == nil {
		t.Error("LLMProvider() returned nil before swap")
	}
}

func TestSetLLMProvider_SwapsModelAndProvider(t *testing.T) {
	a, _ := newSwapTestAgent(t, "litellm", "vllm-reasoning-model-a-long")
	newStub := &namedStubProvider{name: "openai-compatible", model: "gemini-3.1-flash-lite"}

	a.SetLLMProvider(newStub, "litellm-gemini-flash", "gemini-3.1-flash-lite")

	if got := a.Model(); got != "gemini-3.1-flash-lite" {
		t.Errorf("Model() after swap = %q; want %q", got, "gemini-3.1-flash-lite")
	}
	if got := a.ProviderName(); got != "litellm-gemini-flash" {
		t.Errorf("ProviderName() after swap = %q; want %q", got, "litellm-gemini-flash")
	}
	if a.LLMProvider() != newStub {
		t.Errorf("LLMProvider() after swap returned the OLD provider — pointer identity mismatch")
	}
}

func TestSetLLMProvider_EmitsModelChangedEvent(t *testing.T) {
	a, bus := newSwapTestAgent(t, "litellm", "vllm-reasoning-model-a-long")

	// Subscribe BEFORE the swap so we don't miss the event. EventBus.Emit is
	// asynchronous (publishes to subscriber chans in a goroutine), so we use
	// a short timeout rather than a default-case poll.
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	a.SetLLMProvider(
		&namedStubProvider{name: "openai-compatible", model: "gemini-3.1-flash-lite"},
		"litellm-gemini-flash",
		"gemini-3.1-flash-lite",
	)

	var ev telemetry.AgentEvent
	select {
	case ev = <-sub:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no event received within 500ms after SetLLMProvider")
	}

	if ev.EventType != telemetry.EventModelChanged {
		t.Errorf("event type = %q; want %q", ev.EventType, telemetry.EventModelChanged)
	}
	if ev.AgentName != "Coder" {
		t.Errorf("event AgentName = %q; want %q", ev.AgentName, "Coder")
	}

	var payload telemetry.ModelChangedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.OldModel != "vllm-reasoning-model-a-long" {
		t.Errorf("payload.OldModel = %q; want %q", payload.OldModel, "vllm-reasoning-model-a-long")
	}
	if payload.OldProvider != "litellm" {
		t.Errorf("payload.OldProvider = %q; want %q", payload.OldProvider, "litellm")
	}
	if payload.NewModel != "gemini-3.1-flash-lite" {
		t.Errorf("payload.NewModel = %q; want %q", payload.NewModel, "gemini-3.1-flash-lite")
	}
	if payload.NewProvider != "litellm-gemini-flash" {
		t.Errorf("payload.NewProvider = %q; want %q", payload.NewProvider, "litellm-gemini-flash")
	}
}

// TestSetLLMProvider_RaceFree exercises the mutex with concurrent
// readers and a writer. Run under `go test -race ./internal/agent/...`
// to catch any read/write races on llmProvider, providerName, or model.
func TestSetLLMProvider_RaceFree(t *testing.T) {
	a, _ := newSwapTestAgent(t, "litellm", "m0")

	const readers = 8
	const reads = 500
	done := make(chan struct{}, readers)

	for r := 0; r < readers; r++ {
		go func() {
			for i := 0; i < reads; i++ {
				_ = a.LLMProvider()
				_ = a.Model()
				_ = a.ProviderName()
			}
			done <- struct{}{}
		}()
	}

	// Writer: hammer SetLLMProvider with a series of stubs while readers run.
	for i := 0; i < 100; i++ {
		a.SetLLMProvider(
			&namedStubProvider{name: "stub", model: "model-X"},
			"prov-X",
			"model-X",
		)
	}

	for r := 0; r < readers; r++ {
		<-done
	}
	// Reaching here without -race panicking is the success criterion.
}
