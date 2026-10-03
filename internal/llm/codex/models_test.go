package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

func TestListModels_ClientVersionDefault(t *testing.T) {
	var capturedQuery *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{
					"slug":         "gpt-4",
					"display_name": "GPT-4",
					"visibility":   "list",
				},
			},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	access := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeAuth(t, dir, access, "r1", "", "acct-9")

	config := &llm.ProviderConfig{
		CredentialsFile: path,
		BaseURL:         srv.URL,
	}
	_, err := ListModels(context.Background(), config)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	// Verify the default client_version=1.0.0 is sent
	if capturedQuery == nil {
		t.Fatal("captured query is nil")
	}
	clientVersion := capturedQuery.Query().Get("client_version")
	if clientVersion != "1.0.0" {
		t.Fatalf("expected client_version=1.0.0, got %q", clientVersion)
	}
}

func TestListModels_ClientVersionOverride(t *testing.T) {
	var capturedQuery *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{
					"slug":         "gpt-4",
					"display_name": "GPT-4",
					"visibility":   "list",
				},
			},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	access := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeAuth(t, dir, access, "r1", "", "acct-9")

	config := &llm.ProviderConfig{
		CredentialsFile: path,
		BaseURL:         srv.URL,
		ClientVersion:   "2.0.0",
	}
	_, err := ListModels(context.Background(), config)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	// Verify the overridden client_version=2.0.0 is sent
	if capturedQuery == nil {
		t.Fatal("captured query is nil")
	}
	clientVersion := capturedQuery.Query().Get("client_version")
	if clientVersion != "2.0.0" {
		t.Fatalf("expected client_version=2.0.0, got %q", clientVersion)
	}
}

func TestListModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{
					"slug":         "gpt-4",
					"display_name": "GPT-4",
					"visibility":   "list",
				},
				{
					"slug":         "gpt-3.5-turbo",
					"display_name": "GPT-3.5 Turbo",
					"visibility":   "list",
				},
			},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	access := fakeJWT(t, map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeAuth(t, dir, access, "r1", "", "acct-9")

	config := &llm.ProviderConfig{
		CredentialsFile: path,
		BaseURL:         srv.URL,
	}
	models, err := ListModels(context.Background(), config)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Slug != "gpt-4" {
		t.Fatalf("expected first model slug to be 'gpt-4', got %q", models[0].Slug)
	}
}

func TestListedSlugs(t *testing.T) {
	models := []ModelInfo{
		{Slug: "gpt-4", DisplayName: "GPT-4", Visibility: "list"},
		{Slug: "gpt-3.5", DisplayName: "GPT-3.5", Visibility: ""},
		{Slug: "hidden", DisplayName: "Hidden", Visibility: "hidden"},
		{Slug: "", DisplayName: "NoSlug", Visibility: "list"},
	}
	slugs := ListedSlugs(models)
	if len(slugs) != 2 {
		t.Fatalf("expected 2 slugs, got %d: %v", len(slugs), slugs)
	}
	if slugs[0] != "gpt-4" || slugs[1] != "gpt-3.5" {
		t.Fatalf("unexpected slugs: %v", slugs)
	}
}
