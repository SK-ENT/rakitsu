package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// ============================================================
// Test stubs
// ============================================================

type okProvider struct {
	name   string
	model  string
	calls  atomic.Int64
	result *GenerateResult
}

func (p *okProvider) Generate(_ context.Context, _ string, _ []Message, _ []ToolDefinition) (*GenerateResult, error) {
	p.calls.Add(1)
	return p.result, nil
}
func (p *okProvider) GetName() string  { return p.name }
func (p *okProvider) GetModel() string { return p.model }

type errProvider struct {
	name  string
	model string
	calls atomic.Int64
	err   error
}

func (p *errProvider) Generate(_ context.Context, _ string, _ []Message, _ []ToolDefinition) (*GenerateResult, error) {
	p.calls.Add(1)
	return nil, p.err
}
func (p *errProvider) GetName() string  { return p.name }
func (p *errProvider) GetModel() string { return p.model }

func okResult(text string) *GenerateResult {
	return &GenerateResult{Response: text, FinishReason: "stop"}
}

// ============================================================
// Unit tests
// ============================================================

func TestFallback_FirstSucceeds_NoFallback(t *testing.T) {
	p0 := &okProvider{name: "p0", model: "m0", result: okResult("hello")}
	p1 := &errProvider{name: "p1", model: "m1", err: errors.New("should not be called")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})
	res, err := fp.Generate(context.Background(), "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "hello" {
		t.Errorf("Response = %q, want %q", res.Response, "hello")
	}
	if p0.calls.Load() != 1 {
		t.Errorf("p0 calls = %d, want 1", p0.calls.Load())
	}
	if p1.calls.Load() != 0 {
		t.Errorf("p1 should not be called, got %d calls", p1.calls.Load())
	}
}

func TestFallback_FirstFails_SecondSucceeds(t *testing.T) {
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("rate limit")}
	p1 := &okProvider{name: "p1", model: "m1", result: okResult("fallback result")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})
	res, err := fp.Generate(context.Background(), "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "fallback result" {
		t.Errorf("Response = %q, want %q", res.Response, "fallback result")
	}
	if p0.calls.Load() != 1 {
		t.Errorf("p0 calls = %d, want 1", p0.calls.Load())
	}
	if p1.calls.Load() != 1 {
		t.Errorf("p1 calls = %d, want 1", p1.calls.Load())
	}
}

func TestFallback_AllFail_ReturnsLastError(t *testing.T) {
	sentinel := errors.New("final error")
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("err0")}
	p1 := &errProvider{name: "p1", model: "m1", err: sentinel}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})
	_, err := fp.Generate(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error does not wrap sentinel: %v", err)
	}
}

// TestFallback_SecondCallAfterFullExhaustion_ReturnsActionableError is a
// regression test: once every provider has been permanently demoted,
// start == len(f.providers) on every subsequent call, so the retry loop
// never executes — lastErr stays nil and the old code wrapped that nil
// error into a malformed "last error: %!w(<nil>)" message instead of
// anything actionable.
func TestFallback_SecondCallAfterFullExhaustion_ReturnsActionableError(t *testing.T) {
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("err0")}
	p1 := &errProvider{name: "p1", model: "m1", err: errors.New("err1")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	// First call exhausts both providers.
	if _, err := fp.Generate(context.Background(), "", nil, nil); err == nil {
		t.Fatal("expected error on first call, got nil")
	}

	// Second call: the loop body never runs (start already == len(providers)).
	_, err := fp.Generate(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error on second call after full exhaustion, got nil")
	}
	if strings.Contains(err.Error(), "<nil>") || strings.Contains(err.Error(), "%!w") {
		t.Errorf("error message is malformed: %v", err)
	}
	// Providers must not be called a third time each — the fast path
	// returns before ever reaching the provider loop.
	if p0.calls.Load() != 1 || p1.calls.Load() != 1 {
		t.Errorf("providers called after exhaustion: p0=%d p1=%d, want 1 each", p0.calls.Load(), p1.calls.Load())
	}
}

func TestFallback_SingleProvider_BehavesLikeDirect(t *testing.T) {
	p0 := &okProvider{name: "p0", model: "m0", result: okResult("solo")}
	fp := NewFallbackProvider([]LLMProvider{p0})

	res, err := fp.Generate(context.Background(), "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "solo" {
		t.Errorf("Response = %q, want %q", res.Response, "solo")
	}
}

func TestFallback_AdvancementPersists(t *testing.T) {
	// After p0 fails once, subsequent calls should start at p1.
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("rate limit")}
	p1 := &okProvider{name: "p1", model: "m1", result: okResult("ok")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	// First call: p0 fails → p1 succeeds
	if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	// Second call: should start at p1 (p0 permanently skipped)
	if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if p0.calls.Load() != 1 {
		t.Errorf("p0 calls = %d, want 1 (skipped on second call)", p0.calls.Load())
	}
	if p1.calls.Load() != 2 {
		t.Errorf("p1 calls = %d, want 2", p1.calls.Load())
	}
}

func TestFallback_ContextCanceled_DoesNotAdvance(t *testing.T) {
	// A caller-side context cancellation/timeout says nothing about the
	// provider's health and must not permanently demote it.
	p0 := &errProvider{name: "p0", model: "m0", err: context.DeadlineExceeded}
	p1 := &okProvider{name: "p1", model: "m1", result: okResult("ok")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	// First call: p0 times out → p1 succeeds, but p0 must NOT be demoted.
	if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	// Second call: p0 should still be tried first.
	if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if p0.calls.Load() != 2 {
		t.Errorf("p0 calls = %d, want 2 (not permanently skipped after a timeout)", p0.calls.Load())
	}
	if got := fp.GetModel(); got != p0.model {
		t.Errorf("GetModel() = %q, want %q (cursor should not have advanced)", got, p0.model)
	}
}

func TestFallback_GetName_ListsAllProviders(t *testing.T) {
	p0 := &okProvider{name: "openai", model: "m0", result: okResult("")}
	p1 := &okProvider{name: "anthropic", model: "m1", result: okResult("")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	name := fp.GetName()
	if name != "fallback[openai,anthropic]" {
		t.Errorf("GetName() = %q, want %q", name, "fallback[openai,anthropic]")
	}
}

func TestFallback_GetModel_DelegatesToCurrentProvider(t *testing.T) {
	p0 := &okProvider{name: "p0", model: "gpt-4o", result: okResult("")}
	fp := NewFallbackProvider([]LLMProvider{p0})

	if m := fp.GetModel(); m != "gpt-4o" {
		t.Errorf("GetModel() = %q, want %q", m, "gpt-4o")
	}
}

func TestFallback_GetModel_AfterAdvancement(t *testing.T) {
	p0 := &errProvider{name: "p0", model: "gpt-4o", err: errors.New("fail")}
	p1 := &okProvider{name: "p1", model: "claude-3-5-sonnet", result: okResult("")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	// Advance current to p1
	fp.Generate(context.Background(), "", nil, nil) //nolint:errcheck

	if m := fp.GetModel(); m != "claude-3-5-sonnet" {
		t.Errorf("GetModel() after advancement = %q, want %q", m, "claude-3-5-sonnet")
	}
}

func TestFallback_EmptyProviders_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty providers, got none")
		}
	}()
	NewFallbackProvider(nil)
}

func TestFallback_ChainOfThree_SkipsFirstTwo(t *testing.T) {
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("e0")}
	p1 := &errProvider{name: "p1", model: "m1", err: errors.New("e1")}
	p2 := &okProvider{name: "p2", model: "m2", result: okResult("third")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1, p2})
	res, err := fp.Generate(context.Background(), "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "third" {
		t.Errorf("Response = %q, want %q", res.Response, "third")
	}
	if p0.calls.Load() != 1 || p1.calls.Load() != 1 || p2.calls.Load() != 1 {
		t.Errorf("unexpected call counts: p0=%d p1=%d p2=%d", p0.calls.Load(), p1.calls.Load(), p2.calls.Load())
	}
}

// ============================================================
// Stress tests
// ============================================================

func TestStress_FallbackConcurrent_FirstAlwaysFails(t *testing.T) {
	// p0 always fails; p1 always succeeds.
	// 100 concurrent calls — all should succeed via p1, race-clean.
	const N = 100
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("rate limit")}
	p1 := &okProvider{name: "p1", model: "m1", result: okResult("ok")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	var wg sync.WaitGroup
	errs := make(chan error, N)

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := fp.Generate(context.Background(), "", nil, nil)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("concurrent call failed: %v", e)
	}

	// p0 should have been called ≥1 time total (possibly once by the first racer)
	if p0.calls.Load() == 0 {
		t.Error("p0 was never called")
	}
}

func TestStress_FallbackConcurrent_AllFail(t *testing.T) {
	// All providers fail — all goroutines should get an error, no deadlock.
	const N = 50
	p0 := &errProvider{name: "p0", model: "m0", err: errors.New("e0")}
	p1 := &errProvider{name: "p1", model: "m1", err: errors.New("e1")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	var wg sync.WaitGroup
	var errCount atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if errCount.Load() != N {
		t.Errorf("expected %d errors, got %d", N, errCount.Load())
	}
}

func TestStress_FallbackConcurrent_MidFlightAdvancement(t *testing.T) {
	// p0 fails for the first 10 calls, then succeeds.
	// Tests that advancement is safe even when some goroutines observe the transition.
	const N = 60
	const threshold = 10

	var p0calls atomic.Int64
	p0 := &conditionalProvider{
		name:  "p0",
		model: "m0",
		fn: func() (*GenerateResult, error) {
			n := p0calls.Add(1)
			if n <= threshold {
				return nil, fmt.Errorf("capacity error %d", n)
			}
			return okResult("p0-ok"), nil
		},
	}
	p1 := &okProvider{name: "p1", model: "m1", result: okResult("p1-ok")}

	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	var wg sync.WaitGroup
	errs := make(chan error, N)

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := fp.Generate(context.Background(), "", nil, nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("unexpected error: %v", e)
	}
}

// conditionalProvider calls fn() on each Generate invocation.
type conditionalProvider struct {
	name  string
	model string
	fn    func() (*GenerateResult, error)
}

func (p *conditionalProvider) Generate(_ context.Context, _ string, _ []Message, _ []ToolDefinition) (*GenerateResult, error) {
	return p.fn()
}
func (p *conditionalProvider) GetName() string  { return p.name }
func (p *conditionalProvider) GetModel() string { return p.model }

// ============================================================
// Audio-aware (InputPreparer) tests
// ============================================================

func audioHistory() []Message {
	return []Message{{Role: "user", Content: []ContentBlock{{Type: ContentTypeAudio, MIMEType: "audio/wav"}}}}
}

// preparingProvider implements InputPreparer and records the history it
// actually received in Generate, so tests can assert the prepared (not
// original) history was passed through.
type preparingProvider struct {
	name         string
	model        string
	result       *GenerateResult
	prepareErr   error
	generateErr  error
	receivedText string // Content[0].Text of the history Generate() was called with
}

func (p *preparingProvider) PrepareInput(_ context.Context, history []Message) ([]Message, error) {
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	out := make([]Message, len(history))
	for i := range history {
		out[i] = Message{Role: history[i].Role, Content: []ContentBlock{{Type: ContentTypeText, Text: "prepared:" + p.name}}}
	}
	return out, nil
}

func (p *preparingProvider) Generate(_ context.Context, _ string, history []Message, _ []ToolDefinition) (*GenerateResult, error) {
	if len(history) > 0 && len(history[0].Content) > 0 {
		p.receivedText = history[0].Content[0].Text
	}
	if p.generateErr != nil {
		return nil, p.generateErr
	}
	return p.result, nil
}
func (p *preparingProvider) GetName() string  { return p.name }
func (p *preparingProvider) GetModel() string { return p.model }

func TestFallback_AudioInput_PassesPreparedHistoryToGenerate(t *testing.T) {
	p0 := &preparingProvider{name: "p0", model: "m0", result: okResult("ok")}
	fp := NewFallbackProvider([]LLMProvider{p0})

	if _, err := fp.Generate(context.Background(), "", audioHistory(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p0.receivedText != "prepared:p0" {
		t.Errorf("Generate received %q, want the prepared history", p0.receivedText)
	}
}

func TestFallback_AudioInput_ProviderWithoutInputPreparerIsSkipped(t *testing.T) {
	p0 := &okProvider{name: "p0", model: "m0", result: okResult("should not be used")}
	p1 := &preparingProvider{name: "p1", model: "m1", result: okResult("from p1")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	res, err := fp.Generate(context.Background(), "", audioHistory(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "from p1" {
		t.Errorf("Response = %q, want %q", res.Response, "from p1")
	}
	if p0.calls.Load() != 0 {
		t.Errorf("p0 (no InputPreparer) should never be called for audio input, got %d calls", p0.calls.Load())
	}
}

func TestFallback_AudioInput_AllProvidersLackInputPreparer_ReturnsActionableError(t *testing.T) {
	p0 := &okProvider{name: "p0", model: "m0", result: okResult("")}
	fp := NewFallbackProvider([]LLMProvider{p0})

	_, err := fp.Generate(context.Background(), "", audioHistory(), nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot accept or prepare audio input") {
		t.Errorf("error = %v, want it to name the audio-input gap", err)
	}
}

func TestFallback_AudioInput_PrepareInputErrorFallsThroughToNextProvider(t *testing.T) {
	p0 := &preparingProvider{name: "p0", model: "m0", prepareErr: errors.New("transcription failed")}
	p1 := &preparingProvider{name: "p1", model: "m1", result: okResult("from p1")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	res, err := fp.Generate(context.Background(), "", audioHistory(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "from p1" {
		t.Errorf("Response = %q, want %q", res.Response, "from p1")
	}
}

func TestFallback_AudioInput_PrepareInputContextCanceledStopsImmediately(t *testing.T) {
	p0 := &preparingProvider{name: "p0", model: "m0", prepareErr: context.Canceled}
	p1 := &preparingProvider{name: "p1", model: "m1", result: okResult("should not run")}
	fp := NewFallbackProvider([]LLMProvider{p0, p1})

	_, err := fp.Generate(context.Background(), "", audioHistory(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestFallback_NoAudioInput_InputPreparerNeverCalled(t *testing.T) {
	// preparingProvider.Generate would echo "prepared:p0" if PrepareInput ran;
	// with no audio blocks it must see the original text history untouched.
	p0 := &preparingProvider{name: "p0", model: "m0", result: okResult("ok")}
	fp := NewFallbackProvider([]LLMProvider{p0})

	history := []Message{{Role: "user", Content: []ContentBlock{{Type: ContentTypeText, Text: "hello"}}}}
	if _, err := fp.Generate(context.Background(), "", history, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p0.receivedText != "hello" {
		t.Errorf("Generate received %q, want the original text unchanged", p0.receivedText)
	}
}
