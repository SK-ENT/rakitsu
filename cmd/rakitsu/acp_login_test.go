package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/acp"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/zalando/go-keyring"
)

func TestACPDefaultConfigHasNoTools(t *testing.T) {
	cfg := acpDefaultConfig("synthetic-secret")
	if len(cfg.Tools) != 0 || len(cfg.Agents) != 1 || len(cfg.Agents[0].Tools) != 0 || len(cfg.Agents[0].ToolsInline) != 0 {
		t.Fatal("default configuration must not expose tools")
	}
	if cfg.Agents[0].Model != "gpt-5.6-luna" {
		t.Fatalf("unexpected built-in ACP model: %q", cfg.Agents[0].Model)
	}
	if settings := cfg.Agents[0].ModelConfig; settings == nil || settings.ReasoningEffort != "none" {
		t.Fatal("built-in ACP agent must use explicit reasoning effort")
	}
	if cfg.GetAPIKey("openai") != "synthetic-secret" {
		t.Fatal("missing configured key")
	}
}

func TestManagedACPRunHidesProviderErrors(t *testing.T) {
	run := managedACPRun(func(context.Context, *config.Config, *telemetry.EventBus, *debug.DebugController, string, func([]debug.Attachable), *acp.ConvTurn) (string, error) {
		return "", errors.New("synthetic-secret")
	})
	_, err := run(context.Background(), nil, nil, nil, "", nil, nil)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("provider error leaked: %v", err)
	}
}

func TestManagedACPLoginWithoutRestart(t *testing.T) {
	var mu sync.Mutex
	var stored string
	var loads int
	loader := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		if stored == "" {
			return "", errors.New("not found")
		}
		return stored, nil
	}
	usedKey := make(chan string, 1)
	run := func(_ context.Context, cfg *config.Config, _ *telemetry.EventBus, _ *debug.DebugController, _ string, _ func([]debug.Attachable), _ *acp.ConvTurn) (string, error) {
		usedKey <- cfg.GetAPIKey("openai")
		return "ok", nil
	}
	srv := newManagedACPServer(func() (*config.Config, error) {
		key, err := loader()
		if err != nil {
			return nil, err
		}
		return acpDefaultConfig(key), nil
	}, run)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer inW.Close()
	defer outR.Close()
	go func() { _ = srv.Run(ctx, inR, outW); _ = outW.Close() }()
	dec := json.NewDecoder(outR)
	request := func(payload string) map[string]json.RawMessage {
		t.Helper()
		if _, err := io.WriteString(inW, payload+"\n"); err != nil {
			t.Fatal(err)
		}
		var response map[string]json.RawMessage
		if err := dec.Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := request(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`)
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(first["error"], &rpcErr); err != nil || rpcErr.Code != -32000 {
		t.Fatalf("expected auth required: %s", first["error"])
	}
	mu.Lock()
	stored = "synthetic-key"
	mu.Unlock()
	second := request(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(second["result"], &result); err != nil || result.SessionID == "" {
		t.Fatalf("expected session: %s", second["result"])
	}
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": result.SessionID, "prompt": []map[string]string{{"type": "text", "text": "hello"}}}})
	if _, err := inW.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-usedKey:
		if got != "synthetic-key" {
			t.Fatal("wrong credential passed to provider")
		}
	case <-ctx.Done():
		t.Fatal("prompt did not reach the provider")
	}
	for {
		var response map[string]json.RawMessage
		if err := dec.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if string(response["id"]) == "3" {
			if len(response["result"]) == 0 {
				t.Fatalf("prompt failed: %s", response["error"])
			}
			break
		}
	}
	third := request(`{"jsonrpc":"2.0","id":4,"method":"session/new","params":{}}`)
	if len(third["result"]) == 0 {
		t.Fatalf("expected another session: %s", third["error"])
	}
	mu.Lock()
	defer mu.Unlock()
	if loads != 2 {
		t.Fatalf("expected two credential lookups, got %d", loads)
	}
}

func TestACPProfileSetupAndLoad(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.json")
			stored := map[string]string{}
			setup := acpSetup{profilePath: path, getKey: func(account string) (string, error) {
				key, ok := stored[account]
				if !ok {
					return "", errors.New("missing")
				}
				return key, nil
			}, setKey: func(account, key string) error { stored[account] = key; return nil }, verify: func(context.Context, acpProfile, string) error { return nil }}
			key := "synthetic-secret"
			if provider == "ollama" {
				key = ""
			}
			if err := setup.save(context.Background(), acpProfile{Provider: provider, Model: "fixture-model"}, key); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("synthetic-secret")) {
				t.Fatal("credential in profile")
			}
			cfg, err := setup.load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agents[0].Provider != provider || cfg.Agents[0].Model != "fixture-model" || cfg.GetAPIKey(provider) != key {
				t.Fatal("incorrect loaded config")
			}
			if provider == "ollama" && len(stored) != 0 {
				t.Fatal("ollama used keyring")
			}
		})
	}
}

func TestACPProfileRejectsInvalidBeforeSaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	setup := acpSetup{profilePath: path, setKey: func(string, string) error { t.Fatal("stored rejected key"); return nil }, verify: func(context.Context, acpProfile, string) error { return errors.New("synthetic-secret") }}
	err := setup.save(context.Background(), acpProfile{Provider: "openai", Model: "bad"}, "synthetic-secret")
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("expected sanitized failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected profile was saved")
	}
}

func TestACPLegacyOpenAIProfile(t *testing.T) {
	setup := acpSetup{profilePath: filepath.Join(t.TempDir(), "missing"), getKey: func(account string) (string, error) {
		if account != "openai" {
			t.Fatal("wrong legacy account")
		}
		return "legacy-secret", nil
	}}
	cfg, err := setup.load()
	if err != nil || cfg.GetAPIKey("openai") != "legacy-secret" || cfg.Agents[0].Model != "gpt-5.6-luna" {
		t.Fatal("legacy credential not loaded")
	}
}

func TestACPProfileReplacementRemovesOldCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	keys := map[string]string{"openai": "legacy-secret"}
	setup := acpSetup{
		profilePath: path,
		getKey:      func(account string) (string, error) { return keys[account], nil },
		setKey:      func(account, key string) error { keys[account] = key; return nil },
		deleteKey:   func(account string) error { delete(keys, account); return nil },
		verify:      func(context.Context, acpProfile, string) error { return nil },
	}
	for _, provider := range []string{"openai", "anthropic", "ollama"} {
		key := "fixture-secret-" + provider
		wantKeys := 2
		if provider == "ollama" {
			key = ""
			wantKeys = 1
		}
		if err := setup.save(context.Background(), acpProfile{Provider: provider, Model: "fixture"}, key); err != nil {
			t.Fatal(err)
		}
		cfg, err := setup.load()
		if err != nil || cfg.Settings.DefaultProvider != provider || cfg.GetAPIKey(provider) != key {
			t.Fatalf("could not load the new %s profile: %v", provider, err)
		}
		if len(keys) != wantKeys || keys["openai"] != "legacy-secret" {
			t.Fatalf("unexpected credential count after switching to %s: %d", provider, len(keys))
		}
	}
}

func TestACPProfileConcurrentReplacements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	var keys sync.Map
	setup := acpSetup{
		profilePath: path,
		getKey: func(account string) (string, error) {
			key, _ := keys.Load(account)
			value, _ := key.(string)
			return value, nil
		},
		setKey:    func(account, key string) error { keys.Store(account, key); return nil },
		deleteKey: func(account string) error { keys.Delete(account); return nil },
		verify:    func(context.Context, acpProfile, string) error { return nil },
	}
	results := make(chan error, 8)
	for range cap(results) {
		go func() {
			results <- setup.save(context.Background(), acpProfile{Provider: "openai", Model: "fixture"}, "fixture-key")
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	count := 0
	keys.Range(func(_, _ any) bool { count++; return true })
	if cfg, err := setup.load(); err != nil || cfg.GetAPIKey("openai") != "fixture-key" || count != 1 {
		t.Fatal("concurrent replacements did not leave one usable credential")
	}
}

func TestACPProfileSaveFailureKeepsOldCredential(t *testing.T) {
	for _, tt := range []struct{ provider, key string }{
		{provider: "openai", key: "new-secret"},
		{provider: "ollama"},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			keys := map[string]string{}
			setup := acpSetup{
				profilePath: path,
				getKey:      func(account string) (string, error) { return keys[account], nil },
				setKey:      func(account, key string) error { keys[account] = key; return nil },
				deleteKey:   func(account string) error { delete(keys, account); return nil },
				verify:      func(context.Context, acpProfile, string) error { return nil },
			}
			if err := setup.save(context.Background(), acpProfile{Provider: "openai", Model: "old-model"}, "old-secret"); err != nil {
				t.Fatal(err)
			}
			setup.writeProfile = func(string, acpProfile) error { return errors.New("injected write failure") }
			if err := setup.save(context.Background(), acpProfile{Provider: tt.provider, Model: "new-model"}, tt.key); err == nil {
				t.Fatal("expected failed profile replacement")
			}
			cfg, err := setup.load()
			if err != nil || cfg.Agents[0].Model != "old-model" || cfg.GetAPIKey("openai") != "old-secret" || len(keys) != 1 {
				t.Fatal("old profile or key changed, or new credential was left behind")
			}
		})
	}
}

func TestACPSetupNormalizesGeminiModelID(t *testing.T) {
	const model = "gemini-2.5-flash"
	for _, input := range []string{model, "models/" + model} {
		t.Run(input, func(t *testing.T) {
			setup := acpSetup{
				profilePath: filepath.Join(t.TempDir(), "profile"),
				verify: func(_ context.Context, p acpProfile, _ string) error {
					if p.Model != model {
						t.Fatalf("verification received model %q, want %q", p.Model, model)
					}
					return nil
				},
				setKey: func(string, string) error { return nil },
				getKey: func(string) (string, error) { return "fixture-key", nil },
			}
			if err := setup.save(context.Background(), acpProfile{Provider: "gemini", Model: input}, "fixture-key"); err != nil {
				t.Fatal(err)
			}
			cfg, err := setup.load()
			if err != nil || cfg.Agents[0].Model != model {
				t.Fatalf("saved model did not round-trip as %q: %v", model, err)
			}
		})
	}
}

func TestACPProfileRejectsUnsafeModelNames(t *testing.T) {
	for _, model := range []string{"", "one\nmalicious", "\x1b[2J", "model with spaces"} {
		if validACPProfile(acpProfile{Provider: "ollama", Model: model}) {
			t.Fatal("accepted unsafe model name")
		}
	}
}

func TestACPManagedProfilePinsProviderEndpoints(t *testing.T) {
	tests := []struct{ provider, expected string }{{"anthropic", strings.TrimSuffix(acpAnthropicBaseURL, "/v1")}, {"ollama", acpOllamaBaseURL + "/v1"}, {"gemini", strings.TrimSuffix(acpGeminiBaseURL, "/v1beta")}, {"openai", acpOpenAIBaseURL}}
	for _, tt := range tests {
		cfg := acpConfigForProfile(acpProfile{Provider: tt.provider, Model: "fixture"}, "key")
		if !cfg.Settings.ManagedACP || cfg.GetBaseURL(tt.provider) != tt.expected {
			t.Fatalf("%s endpoint not pinned", tt.provider)
		}
	}
}

func TestACPProfileRetriesCredentialCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile")
	keys := map[string]string{}
	blockDeletion := true
	var oldAccount string
	setup := acpSetup{
		profilePath: path,
		getKey:      func(account string) (string, error) { return keys[account], nil },
		setKey: func(account, key string) error {
			if key == "old-key" {
				oldAccount = account
			}
			keys[account] = key
			return nil
		},
		deleteKey: func(account string) error {
			if account == oldAccount && blockDeletion {
				return errors.New("credential store temporarily unavailable")
			}
			if _, ok := keys[account]; !ok {
				return keyring.ErrNotFound
			}
			delete(keys, account)
			return nil
		},
		verify: func(context.Context, acpProfile, string) error { return nil },
	}
	ctx := context.Background()
	p := acpProfile{Provider: "openai", Model: "fixture"}
	for _, key := range []string{"old-key", "active-key"} {
		if err := setup.save(ctx, p, key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := setup.load()
	if err != nil || cfg.GetAPIKey("openai") != "active-key" || keys[oldAccount] != "old-key" {
		t.Fatal("failed cleanup must leave the new profile usable")
	}
	if err := setup.save(ctx, p, "blocked-key"); err == nil || len(keys) != 2 {
		t.Fatal("setup must retry pending cleanup before staging another key")
	}
	blockDeletion = false
	setup.writeProfile = func(string, acpProfile) error { return errors.New("injected write failure") }
	if err := setup.save(ctx, p, "failed-key"); err == nil {
		t.Fatal("expected profile write to fail after cleanup succeeds")
	}
	saved, err := readACPProfile(path)
	if err != nil || saved.PendingCleanup != oldAccount || len(keys) != 1 || keys[oldAccount] != "" {
		t.Fatal("failed replacement changed the on-disk reference or leaked a credential")
	}
	cfg, err = setup.load()
	if err != nil || cfg.GetAPIKey("openai") != "active-key" {
		t.Fatal("failed replacement did not preserve the active credential")
	}
	setup.writeProfile = nil
	if err := setup.save(ctx, p, "final-key"); err != nil {
		t.Fatal(err)
	}
	cfg, err = setup.load()
	if err != nil || cfg.GetAPIKey("openai") != "final-key" || len(keys) != 1 || keys[oldAccount] != "" {
		t.Fatal("retry did not remove both superseded credentials")
	}
}
