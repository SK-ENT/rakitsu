package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// FallbackProvider wraps an ordered list of LLMProviders and automatically
// advances to the next provider on failure. Once a provider fails it is
// skipped for all subsequent calls in this provider's lifetime.
type FallbackProvider struct {
	providers []LLMProvider
	mu        sync.Mutex
	current   int // index of the first provider not yet permanently failed
}

// NewFallbackProvider returns a FallbackProvider backed by the given slice.
// If only one provider is given it behaves identically to using that provider
// directly. Panics if providers is empty.
func NewFallbackProvider(providers []LLMProvider) *FallbackProvider {
	if len(providers) == 0 {
		panic("llm.NewFallbackProvider: providers must not be empty")
	}
	return &FallbackProvider{providers: providers}
}

// Generate tries each provider in order, advancing permanently on failure.
// Returns the first successful result, or the last error if all fail.
func (f *FallbackProvider) Generate(
	ctx context.Context,
	systemPrompt string,
	history []Message,
	tools []ToolDefinition,
) (*GenerateResult, error) {
	f.mu.Lock()
	start := f.current
	f.mu.Unlock()

	if start >= len(f.providers) {
		// Every provider has been permanently demoted already. The loop
		// below would never execute, leaving lastErr nil and producing a
		// malformed "last error: %!w(<nil>)" message forever instead of
		// something actionable.
		return nil, fmt.Errorf("all providers permanently failed on a prior call; no providers left to try")
	}

	var lastErr error
	for i := start; i < len(f.providers); i++ {
		candidateHistory := history
		if hasAudioInput(history) {
			preparer, ok := f.providers[i].(InputPreparer)
			if !ok {
				lastErr = fmt.Errorf("provider %q cannot accept or prepare audio input", f.providers[i].GetName())
				continue
			}
			var err error
			candidateHistory, err = preparer.PrepareInput(ctx, history)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				lastErr = err
				continue
			}
		}
		result, err := f.providers[i].Generate(ctx, systemPrompt, candidateHistory, tools)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// Advance the shared cursor so future calls also skip this provider —
		// but not for a caller-side context cancellation/timeout, which says
		// nothing about this provider's health and shouldn't demote it.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			f.advance(i)
		}
	}
	return nil, fmt.Errorf("all providers failed; last error: %w", lastErr)
}

func (f *FallbackProvider) advance(i int) {
	f.mu.Lock()
	if f.current == i {
		f.current = i + 1
	}
	f.mu.Unlock()
}

func hasAudioInput(history []Message) bool {
	for _, msg := range history {
		for _, block := range msg.Content {
			if block.Type == ContentTypeAudio {
				return true
			}
		}
	}
	return false
}

// GetName returns a descriptive name listing all provider names.
func (f *FallbackProvider) GetName() string {
	names := make([]string, len(f.providers))
	for i, p := range f.providers {
		names[i] = p.GetName()
	}
	return "fallback[" + strings.Join(names, ",") + "]"
}

// GetModel delegates to the currently active provider.
func (f *FallbackProvider) GetModel() string {
	f.mu.Lock()
	idx := f.current
	f.mu.Unlock()
	if idx >= len(f.providers) {
		idx = len(f.providers) - 1
	}
	return f.providers[idx].GetModel()
}
