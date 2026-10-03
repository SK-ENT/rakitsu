package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// DefaultClientVersion is the default Codex backend client version used when
// querying the model catalog. The backend filters newer models (e.g., gpt-6.1-sol,
// gpt-6-luna) for older versions, so this must be current to expose the full model list.
const DefaultClientVersion = "1.0.0"

// ModelInfo is the subset of the Codex model catalog entry rakitsu shows.
type ModelInfo struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"` // "list" for models the user should see
}

// ListModels fetches the model catalog the subscription backend offers this
// login. config.CredentialsFile and config.BaseURL are honored like in
// NewProvider; config.Model is not needed. config.ClientVersion overrides
// the default version sent in the /models request.
func ListModels(ctx context.Context, config *llm.ProviderConfig) ([]ModelInfo, error) {
	authPath := config.CredentialsFile
	if authPath == "" {
		authPath = DefaultAuthFile
	}
	baseURL := DefaultBaseURL
	if config.BaseURL != "" {
		baseURL = config.BaseURL
	}
	clientVersion := DefaultClientVersion
	if config.ClientVersion != "" {
		clientVersion = config.ClientVersion
	}
	client := &http.Client{Timeout: 15 * time.Second}
	access, account, err := newTokenSource(authPath).token(ctx, client)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models?client_version="+clientVersion, nil)
	if err != nil {
		return nil, fmt.Errorf("codex: build models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if account != "" {
		req.Header.Set("chatgpt-account-id", account)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "rakitsu")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: models request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, newStatusError(resp.StatusCode, "codex: models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("codex: decode models: %w", err)
	}
	return out.Models, nil
}

// ListedSlugs returns the slugs of models meant to be shown to the user,
// in catalog order. Entries with no visibility are kept.
func ListedSlugs(models []ModelInfo) []string {
	var slugs []string
	for _, m := range models {
		if m.Slug == "" || (m.Visibility != "" && m.Visibility != "list") {
			continue
		}
		slugs = append(slugs, m.Slug)
	}
	return slugs
}
