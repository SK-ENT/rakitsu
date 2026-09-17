package mock

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// ProviderFactory creates a mock provider from a ProviderConfig.
// Reads MOCK_LATENCY_MS and MOCK_FAILURE_RATE from env.
func ProviderFactory(ctx context.Context, pc *llm.ProviderConfig) (llm.LLMProvider, error) {
	latency := 10 * time.Millisecond
	if ms, _ := strconv.Atoi(os.Getenv("MOCK_LATENCY_MS")); ms > 0 {
		latency = time.Duration(ms) * time.Millisecond
	}
	failRate := 0.0
	if fr, _ := strconv.ParseFloat(os.Getenv("MOCK_FAILURE_RATE"), 64); fr > 0 {
		failRate = fr
	}

	return NewProvider(Config{
		Name:        "mock",
		Model:       pc.Model,
		Scenario:    SingleToolCall("echo"),
		Latency:     latency,
		FailureRate: failRate,
	}), nil
}
