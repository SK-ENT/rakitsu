package openai

import (
	"context"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/llm"
	openai "github.com/sashabaranov/go-openai"
)

// EmbeddingClient implements llm.EmbeddingProvider using the OpenAI embeddings API.
// Works for OpenAI, LiteLLM proxy, Ollama (/v1), or any OpenAI-compatible endpoint.
type EmbeddingClient struct {
	client *openai.Client
	model  string
	name   string
}

// NewEmbeddingClient constructs an EmbeddingClient from a ProviderConfig.
// Reuses the same client construction pattern as NewProvider().
func NewEmbeddingClient(config *llm.ProviderConfig) *EmbeddingClient {
	clientConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		clientConfig.BaseURL = config.BaseURL
	}

	name := "openai"
	if config.BaseURL != "" {
		name = "openai-compatible"
	}

	model := config.Model
	if model == "" {
		// Default embedding models by provider hint
		switch name {
		case "ollama", "openai-compatible":
			model = "nomic-embed-text"
		default:
			model = "text-embedding-3-small"
		}
	}

	return &EmbeddingClient{
		client: openai.NewClientWithConfig(clientConfig),
		model:  model,
		name:   name,
	}
}

func (c *EmbeddingClient) GetName() string { return c.name }

// Embed generates an embedding vector for the given text.
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := c.client.CreateEmbeddings(ctx, openai.EmbeddingRequestStrings{
		Input: []string{text},
		Model: openai.EmbeddingModel(c.model),
	})
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response from %s", c.name)
	}
	return resp.Data[0].Embedding, nil
}
