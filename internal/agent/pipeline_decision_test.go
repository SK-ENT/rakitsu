package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

func TestPipelineDecisionRunsOnlySelectedBranch(t *testing.T) {
	for _, backend := range []string{"jev", "laya"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", "fixture-key")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				wantAuth := ""
				if backend == "jev" {
					wantAuth = "Bearer fixture-key"
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Error("unexpected authorization header")
				}
				_, _ = w.Write([]byte(`{"answers":{"department":{"choice":"billing"}}}`))
			}))
			defer server.Close()
			bus := telemetry.NewEventBus(32)
			billing := newSequenceProvider(llm.GenerateResult{Response: "billing ran", FinishReason: "stop"})
			review := newSequenceProvider(llm.GenerateResult{Response: "review ran", FinishReason: "stop"})
			endpoint := server.URL
			if backend == "jev" {
				t.Setenv("RAKITSU_JEV_DECISION_URL", endpoint)
				endpoint = ""
			}
			steps := []config.PipelineStep{{Name: "triage", Type: "decision", Decision: &config.DecisionConfig{
				Backend: backend, URL: endpoint,
				Questions: map[string]config.DecisionQuestion{"department": {Type: "choice", Instructions: "Which department?", Choices: map[string]string{"billing": "Payments", "technical": "Errors"}}},
				Routes:    []config.DecisionRoute{{When: config.DecisionMatch{Question: "department", Field: "choice", Equals: "billing"}, Steps: []config.PipelineStep{{Name: "bill", Agent: "billing"}}}},
				Otherwise: []config.PipelineStep{{Name: "review", Agent: "review"}},
			}}}
			o := newTestOrchestrator(steps, map[string]*Agent{
				"billing": newTestAgent("billing", billing, bus),
				"review":  newTestAgent("review", review, bus),
			})
			var checkpoint CheckpointData
			o.SetCheckpointWriter(func(cp CheckpointData) { checkpoint = cp })
			out, err := o.runPipeline(context.Background(), "duplicate charge")
			if err != nil || out != "billing ran" || billing.callCount() != 1 || review.callCount() != 0 {
				t.Fatalf("output=%q error=%v billing=%d review=%d", out, err, billing.callCount(), review.callCount())
			}
			saved := checkpoint.Results["triage"]
			if len(checkpoint.Results) != 1 || saved.Decision == nil || saved.Decision.Route != "route 1" {
				t.Fatalf("checkpoint lacks completed decision: %#v", checkpoint)
			}
			o.SetCheckpoint(&checkpoint)
			out, err = o.runPipeline(context.Background(), "duplicate charge")
			if err != nil || out != "billing ran" || billing.callCount() != 1 || requests.Load() != 1 {
				t.Fatalf("resume reran work or lost output: %q, %v, requests=%d", out, err, requests.Load())
			}
		})
	}
}

func TestPipelineDecisionFallbackAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantReview     int
		wantError      bool
	}{
		{name: "fallback", response: `{"answers":{"department":{"choice":"technical"}}}`, wantReview: 1},
		{name: "invalid", response: `{"answers":{"department":{"choice":"unknown"}}}`, wantError: true},
		{name: "missing", response: `{"answers":{}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			bus := telemetry.NewEventBus(32)
			review := newSequenceProvider(llm.GenerateResult{Response: "review ran", FinishReason: "stop"})
			steps := []config.PipelineStep{{Name: "triage", Type: "decision", Decision: &config.DecisionConfig{
				Backend: "laya", URL: server.URL,
				Questions: map[string]config.DecisionQuestion{"department": {Type: "choice", Instructions: "Which department?", Choices: map[string]string{"billing": "Payments", "technical": "Errors"}}},
				Routes:    []config.DecisionRoute{{When: config.DecisionMatch{Question: "department", Field: "choice", Equals: "billing"}, Steps: []config.PipelineStep{{Name: "bill", Agent: "billing"}}}},
				Otherwise: []config.PipelineStep{{Name: "review", Agent: "review"}},
			}}}
			o := newTestOrchestrator(steps, map[string]*Agent{"review": newTestAgent("review", review, bus)})
			_, err := o.runPipeline(context.Background(), "request")
			if (err != nil) != tc.wantError || review.callCount() != tc.wantReview {
				t.Fatalf("error=%v review=%d, want error=%v review=%d", err, review.callCount(), tc.wantError, tc.wantReview)
			}
		})
	}
}

func TestDecisionRetrievalInputDependencies(t *testing.T) {
	for _, tc := range []struct {
		name, source, wantError string
		deps                    []string
	}{
		{"missing", "typo", "unknown input step", nil},
		{"parallel", "search", "upstream dependency", []string{}},
		{"wrong type", "agent", "must reference a retrieval step", nil},
		{"transitive", "search", "", []string{"agent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []config.PipelineStep{
				{Name: "search", Type: "retrieval"},
				{Name: "agent"},
				{Name: "decide", Type: "decision", DependsOn: tc.deps, Decision: &config.DecisionConfig{}},
			}
			steps[2].Decision.Input.From = "retrieval"
			steps[2].Decision.Input.Step = tc.source
			_, err := buildLevels(steps)
			if tc.wantError == "" && err == nil {
				return
			}
			if err == nil || tc.wantError == "" || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error %q, got %v", tc.wantError, err)
			}
		})
	}
}
