package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/acp"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestACPLoginStoreOnlyAfterValidation(t *testing.T) {
	for _, valid := range []bool{false, true} {
		stored := ""
		err := saveACPKey(context.Background(), "synthetic-secret", func(context.Context, string) error {
			if !valid {
				return errors.New("synthetic-secret")
			}
			return nil
		}, func(key string) error { stored = key; return nil })
		if valid && (err != nil || stored != "synthetic-secret") {
			t.Fatalf("valid key: stored=%q err=%v", stored, err)
		}
		if !valid && (err == nil || stored != "" || strings.Contains(err.Error(), "synthetic-secret")) {
			t.Fatalf("invalid key: stored=%q err=%v", stored, err)
		}
	}
}

func TestACPDefaultConfigHasNoTools(t *testing.T) {
	cfg := acpDefaultConfig("synthetic-secret")
	if len(cfg.Tools) != 0 || len(cfg.Agents) != 1 || len(cfg.Agents[0].Tools) != 0 || len(cfg.Agents[0].ToolsInline) != 0 {
		t.Fatal("default configuration must not expose tools")
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
	srv := newManagedACPServer(loader, run)
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
	err := saveACPKey(ctx, "synthetic-key", func(context.Context, string) error { return nil }, func(key string) error {
		mu.Lock()
		defer mu.Unlock()
		stored = key
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
