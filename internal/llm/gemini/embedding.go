package gemini

import (
	"context"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"google.golang.org/genai"
)

const defaultEmbeddingModel = "text-embedding-004"

// EmbeddingClient implements llm.EmbeddingProvider using the Gemini embedContent API.
// Reuses the same genai.Client construction and auth as NewProvider().
type EmbeddingClient struct {
	client *genai.Client
	model  string
}

// NewEmbeddingClient constructs an EmbeddingClient from a ProviderConfig.
// Uses the same auth priority as NewProvider(): API key > credentials file > ADC.
func NewEmbeddingClient(ctx context.Context, config *llm.ProviderConfig) (*EmbeddingClient, error) {
	// Reuse NewProvider's client construction — same auth, same Vertex AI handling.
	p, err := NewProvider(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("gemini embedding client: %w", err)
	}

	model := config.Model
	if model == "" {
		model = defaultEmbeddingModel
	}

	return &EmbeddingClient{
		client: p.client,
		model:  model,
	}, nil
}

func (c *EmbeddingClient) GetName() string { return "gemini" }

// Embed generates an embedding vector for the given text.
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := c.client.Models.EmbedContent(ctx, c.model, genai.Text(text), nil)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: %w", err)
	}
	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("empty embedding response from gemini")
	}
	return resp.Embeddings[0].Values, nil
}
