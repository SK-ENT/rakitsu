// Package mock provides a mock LLM provider for stress testing.
package mock

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Provider is a mock LLM provider with configurable behavior.
type Provider struct {
	name        string
	model       string
	scenario    *Scenario
	latency     time.Duration
	failureRate float64
	callCount   int64
}

// Config configures a mock provider.
type Config struct {
	Name        string
	Model       string
	Scenario    *Scenario
	Latency     time.Duration
	FailureRate float64 // 0.0-1.0
}

// NewProvider creates a new mock provider.
func NewProvider(cfg Config) *Provider {
	if cfg.Name == "" {
		cfg.Name = "mock"
	}
	if cfg.Model == "" {
		cfg.Model = "mock-model"
	}
	if cfg.Scenario == nil {
		cfg.Scenario = SimpleAnswer()
	}
	return &Provider{
		name:        cfg.Name,
		model:       cfg.Model,
		scenario:    cfg.Scenario,
		latency:     cfg.Latency,
		failureRate: cfg.FailureRate,
	}
}

func (p *Provider) Generate(ctx context.Context, systemPrompt string, history []llm.Message, tools []llm.ToolDefinition) (*llm.GenerateResult, error) {
	atomic.AddInt64(&p.callCount, 1)

	// Simulate latency
	if p.latency > 0 {
		select {
		case <-time.After(p.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Random failures
	if p.failureRate > 0 && rand.Float64() < p.failureRate {
		return nil, fmt.Errorf("mock LLM transient error")
	}

	step := p.scenario.Next()
	return &llm.GenerateResult{
		Response:     step.Response,
		ToolCalls:    step.ToolCalls,
		TokenUsage:   &step.TokenUsage,
		FinishReason: step.FinishReason,
	}, nil
}

func (p *Provider) GetName() string  { return p.name }
func (p *Provider) GetModel() string { return p.model }

// CallCount returns the number of Generate calls made.
func (p *Provider) CallCount() int64 {
	return atomic.LoadInt64(&p.callCount)
}

// GenerateStream implements StreamingProvider for mock streaming tests.
func (p *Provider) GenerateStream(ctx context.Context, systemPrompt string, history []llm.Message, tools []llm.ToolDefinition) (*llm.StreamResult, error) {
	result, err := p.Generate(ctx, systemPrompt, history, tools)
	if err != nil {
		errCh := make(chan error, 1)
		errCh <- err
		return &llm.StreamResult{
			Chunks: make(<-chan llm.StreamChunk),
			Final:  make(<-chan *llm.GenerateResult),
			Err:    errCh,
		}, nil
	}

	chunks := make(chan llm.StreamChunk, 10)
	final := make(chan *llm.GenerateResult, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(chunks)
		// Stream response in small pieces
		text := result.Response
		chunkSize := 20
		for i := 0; i < len(text); i += chunkSize {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			select {
			case chunks <- llm.StreamChunk{Text: text[i:end]}:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
		chunks <- llm.StreamChunk{Done: true}
		final <- result
	}()

	return &llm.StreamResult{
		Chunks: chunks,
		Final:  final,
		Err:    errCh,
	}, nil
}
