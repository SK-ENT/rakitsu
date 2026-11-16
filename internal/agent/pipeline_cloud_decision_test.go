package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestPipelineCloudDecision(t *testing.T) {
	for _, tc := range []struct {
		backend, endpointVar, keyVar, header, credential, model, response string
	}{
		{"openai", "RAKITSU_OPENAI_DECISION_URL", "OPENAI_API_KEY", "Authorization", "Bearer test-key", "gpt-6-luna", `{"answers":[{"name":"eligible","type":"predicate","probability":0},{"name":"department","type":"choice","choice":"billing"},{"name":"severity","type":"score","score":1}]}`},
		{"gemini", "RAKITSU_GEMINI_DECISION_URL", "GEMINI_API_KEY", "x-goog-api-key", "test-key", "gemini-decision-1", `{"answers":{"eligible":{"type":"noul","noul":0},"department":{"type":"choice","choice":"billing"},"severity":{"type":"score","score":1}}}`},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model     string          `json:"model"`
					Questions json.RawMessage `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if r.Header.Get(tc.header) != tc.credential || request.Model != tc.model {
					t.Error("incorrect model or authentication header")
				}
				if tc.backend == "openai" {
					var questions []struct {
						Name    string              `json:"name"`
						Type    string              `json:"type"`
						Choices []map[string]string `json:"choices"`
						Levels  []map[string]string `json:"levels"`
					}
					if err := json.Unmarshal(request.Questions, &questions); err != nil ||
						len(questions) != 3 || questions[0].Name != "department" || len(questions[0].Choices) != 2 ||
						questions[1].Type != "predicate" || len(questions[2].Levels) != 2 {
						t.Errorf("wrong OpenAI questions: %s", request.Questions)
					}
				} else {
					var questions map[string]struct {
						Type     string          `json:"type"`
						Criteria json.RawMessage `json:"criteria"`
					}
					if err := json.Unmarshal(request.Questions, &questions); err != nil || len(questions) != 3 ||
						questions["eligible"].Type != "noul" || !strings.HasPrefix(string(questions["severity"].Criteria), "[") {
						t.Errorf("wrong Gemini questions: %s", request.Questions)
					}
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			t.Setenv(tc.keyVar, "test-key")
			t.Setenv(tc.endpointVar, server.URL)
			steps := []config.PipelineStep{{Name: "decide", Type: "decision", Decision: &config.DecisionConfig{
				Backend: tc.backend,
				Questions: map[string]config.DecisionQuestion{
					"eligible":   {Type: "predicate", Instructions: "Is the request eligible?"},
					"department": {Type: "choice", Instructions: "Which team handles it?", Choices: map[string]string{"billing": "Invoices", "technical": "Errors"}},
					"severity":   {Type: "score", Instructions: "How severe is it?", Levels: []string{"Low", "High"}},
				},
			}}}
			output, err := newTestOrchestrator(steps, nil).runPipeline(context.Background(), "Example")
			if err != nil || !strings.Contains(output, `"probability_true":0`) || !strings.Contains(output, `"choice":"billing"`) || !strings.Contains(output, `"score":1`) {
				t.Fatalf("output=%q error=%v", output, err)
			}
		})
	}
}
