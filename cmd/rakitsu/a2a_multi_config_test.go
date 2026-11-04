package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/server"
)

func TestA2AConfigHandlerLoadsMultipleAndUpdatedConfigsWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	alphaPath := filepath.Join(dir, "alpha.yaml")
	writeA2ATestConfig(t, alphaPath, "Alpha Config", "Alpha")
	writeA2ATestConfig(t, filepath.Join(dir, "beta.yaml"), "Beta Config", "Beta")

	store, err := server.NewConfigStore([]string{dir})
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	t.Cleanup(store.Cleanup)

	run := func(_ context.Context, cfg *config.Config, _ string) (string, error) {
		return cfg.Name + ":" + cfg.Agents[0].Name, nil
	}
	handler := a2aConfigHandlerFunc(store, run)

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List configs: %v", err)
	}
	entries = entriesWithin(entries, dir)
	if len(entries) != 2 {
		t.Fatalf("List returned %d configs in test directory, want 2", len(entries))
	}
	for _, entry := range entries {
		want := entry.Name + ":" + entry.Agents[0]
		got := sendA2AConfigRequest(t, handler, entry.ID, entry.Agents[0])
		if got != want {
			t.Errorf("config %q result = %q, want %q", entry.ID, got, want)
		}
		card := httptest.NewRecorder()
		cardHandler := a2aConfigAgentCardHandlerFunc(store, "http://fallback.invalid/a2a")
		cardRequest := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json/"+entry.ID, nil)
		cardHandler.ServeHTTP(card, cardRequest)
		if card.Code != http.StatusOK {
			t.Errorf("card for config %q returned %d, want 200", entry.ID, card.Code)
		}
		var parsedCard srvAgentCard
		if err := json.NewDecoder(card.Body).Decode(&parsedCard); err != nil {
			t.Fatalf("decode card: %v", err)
		}
		if len(parsedCard.SupportedInterfaces) != 1 || parsedCard.SupportedInterfaces[0].URL != "http://"+cardRequest.Host+"/a2a/"+entry.ID {
			t.Errorf("card service URL = %+v, want request host with config ID", parsedCard.SupportedInterfaces)
		}
	}

	// Editing a config and adding another file become visible without
	// reconstructing the handler or restarting the server.
	writeA2ATestConfig(t, alphaPath, "Alpha Config Updated", "Alpha")
	writeA2ATestConfig(t, filepath.Join(dir, "gamma.yaml"), "Gamma Config", "Gamma")

	entries, err = store.List()
	if err != nil {
		t.Fatalf("List updated configs: %v", err)
	}
	entries = entriesWithin(entries, dir)
	for _, entry := range entries {
		got := sendA2AConfigRequest(t, handler, entry.ID, entry.Agents[0])
		want := entry.Name + ":" + entry.Agents[0]
		if got != want {
			t.Errorf("updated config %q result = %q, want %q", entry.ID, got, want)
		}
	}
}

func TestA2AConfigHandlerRejectsUnknownConfigID(t *testing.T) {
	store, err := server.NewConfigStore([]string{t.TempDir()})
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	t.Cleanup(store.Cleanup)
	handler := a2aConfigHandlerFunc(store, stubRun("unused", "", nil))
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "request",
		"method":  "SendMessage",
		"params": srvSendMessageParams{
			Tenant:  "Researcher",
			Message: srvA2AMessage{MessageID: "message", Role: roleUser, Parts: []srvA2APart{{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/a2a/unknown-config", bytes.NewReader(body)))
	var result testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Error == nil || result.Error.Code != -32602 {
		t.Fatalf("response error = %+v, want invalid-config error -32602", result.Error)
	}
}

func TestA2AConfigHandlerScopesTasksToConfigEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeA2ATestConfig(t, filepath.Join(dir, "alpha.yaml"), "Alpha Config", "Alpha")
	writeA2ATestConfig(t, filepath.Join(dir, "beta.yaml"), "Beta Config", "Beta")

	store, err := server.NewConfigStore([]string{dir})
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	t.Cleanup(store.Cleanup)
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List configs: %v", err)
	}
	entries = entriesWithin(entries, dir)
	ids := make(map[string]string, len(entries))
	for _, entry := range entries {
		ids[entry.Agents[0]] = entry.ID
	}
	alphaID, betaID := ids["Alpha"], ids["Beta"]
	if alphaID == "" || betaID == "" {
		t.Fatalf("config IDs missing: %v", ids)
	}

	started := make(chan struct{})
	canceled := make(chan struct{})
	handler := a2aConfigHandlerFunc(store, func(ctx context.Context, _ *config.Config, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	})

	sent := postA2APath(t, handler, "/a2a/"+alphaID, "SendMessage", "send", srvSendMessageParams{
		Tenant:  "Alpha",
		Message: srvA2AMessage{MessageID: "message", Role: roleUser, Parts: []srvA2APart{{Text: "hold until canceled"}}},
	})
	if sent.Error != nil {
		t.Fatalf("SendMessage error: %+v", sent.Error)
	}
	var sendResult srvSendMessageResult
	if err := json.Unmarshal(sent.Result, &sendResult); err != nil {
		t.Fatalf("decode SendMessage result: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent run did not start")
	}

	otherGet := postA2APath(t, handler, "/a2a/"+betaID, "GetTask", "other-get", srvGetTaskParams{ID: sendResult.Task.ID})
	if otherGet.Error == nil || otherGet.Error.Code != -32001 {
		t.Errorf("GetTask through another config = %+v, want task-not-found error -32001", otherGet.Error)
	}
	otherCancel := postA2APath(t, handler, "/a2a/"+betaID, "CancelTask", "other-cancel", srvCancelTaskParams{ID: sendResult.Task.ID})
	if otherCancel.Error == nil || otherCancel.Error.Code != -32001 {
		t.Errorf("CancelTask through another config = %+v, want task-not-found error -32001", otherCancel.Error)
	}

	originGet := postA2APath(t, handler, "/a2a/"+alphaID, "GetTask", "origin-get", srvGetTaskParams{ID: sendResult.Task.ID})
	if originGet.Error != nil {
		t.Errorf("GetTask through originating config: %+v", originGet.Error)
	} else {
		var task srvA2ATask
		if err := json.Unmarshal(originGet.Result, &task); err != nil {
			t.Fatalf("decode originating task: %v", err)
		}
		if task.Status.State != taskStateWorking {
			t.Errorf("task state after cross-config requests = %q, want %q", task.Status.State, taskStateWorking)
		}
	}

	originCancel := postA2APath(t, handler, "/a2a/"+alphaID, "CancelTask", "origin-cancel", srvCancelTaskParams{ID: sendResult.Task.ID})
	if originCancel.Error != nil {
		t.Fatalf("CancelTask through originating config: %+v", originCancel.Error)
	}
	var canceledTask srvA2ATask
	if err := json.Unmarshal(originCancel.Result, &canceledTask); err != nil {
		t.Fatalf("decode canceled task: %v", err)
	}
	if canceledTask.Status.State != taskStateCanceled {
		t.Errorf("originating CancelTask state = %q, want %q", canceledTask.Status.State, taskStateCanceled)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Error("originating CancelTask did not stop the agent run")
	}
}

func postA2APath(t *testing.T, handler http.HandlerFunc, path, method, requestID string, params interface{}) testA2AResponse {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	var result testA2AResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	return result
}

func TestA2AReloadingHandlerPicksUpConfigEditsWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team.yaml")
	writeA2ATestConfig(t, path, "Team v1", "Writer")
	handler := a2aReloadingHandlerFunc(func() (*config.Config, error) {
		return config.Load(path)
	}, func(_ context.Context, cfg *config.Config, _ string) (string, error) {
		return cfg.Name, nil
	})

	if got := sendA2ARequest(t, handler, "/a2a", "Writer"); got != "Team v1" {
		t.Fatalf("first result = %q, want %q", got, "Team v1")
	}
	cardHandler := agentCardReloadingHandlerFunc(func() (*config.Config, error) {
		return config.Load(path)
	}, "http://fallback.invalid/a2a")
	card := httptest.NewRecorder()
	cardHandler.ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	var parsedCard srvAgentCard
	if err := json.NewDecoder(card.Body).Decode(&parsedCard); err != nil {
		t.Fatalf("decode initial card: %v", err)
	}
	if parsedCard.Name != "Team v1" {
		t.Fatalf("initial card name = %q, want %q", parsedCard.Name, "Team v1")
	}
	writeA2ATestConfig(t, path, "Team v2", "Writer")
	if got := sendA2ARequest(t, handler, "/a2a", "Writer"); got != "Team v2" {
		t.Fatalf("result after editing config = %q, want %q", got, "Team v2")
	}
	card = httptest.NewRecorder()
	cardHandler.ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if err := json.NewDecoder(card.Body).Decode(&parsedCard); err != nil {
		t.Fatalf("decode updated card: %v", err)
	}
	if parsedCard.Name != "Team v2" {
		t.Fatalf("updated card name = %q, want %q", parsedCard.Name, "Team v2")
	}
}

func entriesWithin(entries []server.ConfigEntry, dir string) []server.ConfigEntry {
	filtered := make([]server.ConfigEntry, 0, len(entries))
	for _, entry := range entries {
		if filepath.Dir(entry.Path) == dir {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func sendA2AConfigRequest(t *testing.T, handler http.HandlerFunc, configID, agent string) string {
	t.Helper()
	return sendA2ARequest(t, handler, "/a2a/"+configID, agent)
}

func sendA2ARequest(t *testing.T, handler http.HandlerFunc, endpoint, agent string) string {
	t.Helper()
	requestBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "request",
		"method":  "SendMessage",
		"params": srvSendMessageParams{
			Tenant:  agent,
			Message: srvA2AMessage{MessageID: "message", Role: roleUser, Parts: []srvA2APart{{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(requestBody)))
	var response testA2AResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("A2A error: %+v", response.Error)
	}
	var result srvSendMessageResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode SendMessage result: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pollBody, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "poll",
			"method":  "GetTask",
			"params":  srvGetTaskParams{ID: result.Task.ID},
		})
		poll := httptest.NewRecorder()
		handler.ServeHTTP(poll, httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(pollBody)))
		var polled testA2AResponse
		if err := json.NewDecoder(poll.Body).Decode(&polled); err != nil {
			t.Fatalf("decode GetTask: %v", err)
		}
		var task srvA2ATask
		if err := json.Unmarshal(polled.Result, &task); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		if task.Status.State == taskStateCompleted {
			return task.Artifacts[0].Parts[0].Text
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("A2A task did not finish")
	return ""
}

func writeA2ATestConfig(t *testing.T, path, name, agent string) {
	t.Helper()
	content := "name: " + name + "\nversion: \"1.0\"\nagents:\n  - name: " + agent + "\n    role: worker\n    system_prompt: \"test agent\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}
