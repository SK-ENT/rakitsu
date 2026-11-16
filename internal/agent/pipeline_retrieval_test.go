package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestPipelineRetrievalFeedsLaya(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RAKITSU_RETRIEVAL_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Refunds\nDuplicate invoices are refunded within seven days.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Duplicate invoice") || !strings.Contains(string(body), "guide.md") || r.Header.Get("Authorization") != "" {
			t.Error("missing query/evidence or unexpected authentication")
		}
		_, _ = io.WriteString(w, `{"answers":{"supported":{"noul":0.95}}}`)
	}))
	defer server.Close()
	steps := []config.PipelineStep{
		{Name: "search", Type: "retrieval", Retrieval: &config.LocalRetrievalConfig{Directory: ".", TopK: 2}},
		{Name: "judge", Type: "decision", Decision: &config.DecisionConfig{
			Backend: "laya", URL: server.URL, Input: config.DecisionInput{From: "retrieval", Step: "search"},
			Questions: map[string]config.DecisionQuestion{"supported": {Type: "predicate", Instructions: "Does the evidence answer the question?"}},
		}},
	}
	o := newTestOrchestrator(steps, nil)
	run := func(query string, wantCount int) (string, error) {
		t.Helper()
		var output string
		var runErr error
		events := collectEvents(o.eventBus, func() {
			output, runErr = o.runPipeline(context.Background(), query)
		})
		for _, event := range events {
			if event.EventType != telemetry.EventPipelineStepEnd {
				continue
			}
			var payload telemetry.PipelineStepEndPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.StepName != "judge" {
				continue
			}
			if payload.RetrievalCount == nil || *payload.RetrievalCount != wantCount || payload.Output != "" {
				t.Fatalf("want evidence count %d and redacted output in Decision event, got %s", wantCount, event.Payload)
			}
			return output, runErr
		}
		t.Fatal("missing Decision step-end event")
		return output, runErr
	}
	output, err := run("Duplicate invoice", 1)
	if err != nil || !json.Valid([]byte(output)) || !strings.Contains(output, "guide.md") || !strings.Contains(output, "0.95") || calls.Load() != 1 {
		t.Fatalf("output=%q error=%v calls=%d", output, err, calls.Load())
	}
	output, err = run("zqxv", 0)
	if err != nil || !strings.Contains(output, "no_matches") || calls.Load() != 1 {
		t.Fatalf("empty retrieval output=%q error=%v calls=%d", output, err, calls.Load())
	}
}

func TestPipelineRetrievalStaysInsideConfiguredRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	t.Setenv("RAKITSU_RETRIEVAL_ROOT", root)
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("private-needle"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	step := config.PipelineStep{Name: "search", Type: "retrieval", Retrieval: &config.LocalRetrievalConfig{Directory: "."}}
	output, err := executeRetrievalStep(context.Background(), step, "private-needle")
	if err != nil || len(output.Passages) != 0 {
		t.Fatalf("search followed an escaping symlink: %v, %v", output, err)
	}
	for _, directory := range []string{"escape", "../", outside} {
		step.Retrieval.Directory = directory
		if _, err := executeRetrievalStep(context.Background(), step, "private-needle"); err == nil {
			t.Fatalf("accepted invalid retrieval directory %q", directory)
		}
	}
}
