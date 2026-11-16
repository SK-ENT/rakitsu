package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type Answer struct {
	Choice          string   `json:"choice,omitempty"`
	ProbabilityTrue *float64 `json:"probability_true,omitempty"`
	Score           *float64 `json:"score,omitempty"`
}

type Result struct {
	Backend string            `json:"backend"`
	Route   string            `json:"route,omitempty"`
	Answers map[string]Answer `json:"answers"`
}

var client = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func Evaluate(ctx context.Context, spec *config.DecisionConfig, input string) (map[string]Answer, error) {
	endpoint := spec.URL
	var key, model string
	var request any
	header := "Authorization"
	switch spec.Backend {
	case "jev":
		endpoint = os.Getenv("RAKITSU_JEV_DECISION_URL")
		if endpoint == "" {
			endpoint = "https://api.typesafe.ai/v1/systemone"
		}
		key = os.Getenv("TYPESAFE_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("Jev requires TYPESAFE_API_KEY")
		}
		model = "jev-latest"
	case "laya":
		if endpoint == "" {
			endpoint = "http://127.0.0.1:8000/v1/systemone"
		}
	case "openai":
		endpoint = endpointFromEnv("RAKITSU_OPENAI_DECISION_URL", "https://api.openai.com/v1/decisions")
		key = os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OpenAI Decisions requires OPENAI_API_KEY")
		}
		request = openAIRequest(spec, input)
	case "gemini":
		endpoint = endpointFromEnv("RAKITSU_GEMINI_DECISION_URL", "https://generativelanguage.googleapis.com/v1beta/decisions")
		key = os.Getenv("GEMINI_API_KEY")
		if key == "" {
			key = os.Getenv("GOOGLE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("Gemini Decisions requires GEMINI_API_KEY or GOOGLE_API_KEY")
		}
		header = "x-goog-api-key"
		model = "gemini-decision-1"
	default:
		return nil, fmt.Errorf("unsupported decision backend %q", spec.Backend)
	}
	if request == nil {
		questions := systemOneQuestions(spec)
		if spec.Backend == "gemini" {
			request = map[string]any{
				"model":     model,
				"input":     []map[string]string{{"type": "text", "text": input}},
				"questions": questions,
			}
		} else {
			request = struct {
				State     string                       `json:"state"`
				Model     string                       `json:"model,omitempty"`
				Questions map[string]systemOneQuestion `json:"questions"`
			}{input, model, questions}
		}
	}
	data, err := postDecision(ctx, spec.Backend, endpoint, header, key, request)
	if err != nil {
		return nil, err
	}
	return decodeAnswers(spec, data)
}

func endpointFromEnv(name, fallback string) string {
	if endpoint := os.Getenv(name); endpoint != "" {
		return endpoint
	}
	return fallback
}

type systemOneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

func systemOneQuestions(spec *config.DecisionConfig) map[string]systemOneQuestion {
	questions := make(map[string]systemOneQuestion, len(spec.Questions))
	for name, q := range spec.Questions {
		wire := systemOneQuestion{Type: q.Type, Instructions: q.Instructions}
		switch q.Type {
		case "predicate":
			wire.Type = "noul"
		case "choice":
			wire.Criteria = q.Choices
		case "score":
			wire.Criteria = q.Levels
		}
		questions[name] = wire
	}
	return questions
}

func postDecision(ctx context.Context, backend, endpoint, header, key string, request any) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Hostname() == "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(endpoint, "#") {
		return nil, fmt.Errorf("invalid decision backend URL")
	}
	// Allow literal loopback for local Laya and operator-controlled fixtures.
	local := net.ParseIP(u.Hostname()).IsLoopback()
	if local {
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("invalid decision backend URL: loopback requires HTTP or HTTPS")
		}
	} else {
		var host string
		switch backend {
		case "jev":
			host = "api.typesafe.ai"
		case "openai":
			host = "api.openai.com"
		case "gemini":
			host = "generativelanguage.googleapis.com"
		}
		if !strings.EqualFold(u.Hostname(), host) || u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
			return nil, fmt.Errorf("invalid decision backend URL: use the provider's HTTPS host or literal loopback")
		}
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid decision request URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		if header == "Authorization" {
			key = "Bearer " + key
		}
		req.Header.Set(header, key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s decision request failed", backend)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s decision request returned HTTP %d", backend, resp.StatusCode)
	}
	const maxResponse = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return nil, fmt.Errorf("could not read bounded decision response")
	}
	return data, nil
}

type wireAnswer struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Choice      string   `json:"choice"`
	Noul        *float64 `json:"noul"`
	Probability *float64 `json:"probability"`
	Score       *float64 `json:"score"`
}

func decodeAnswers(spec *config.DecisionConfig, data []byte) (map[string]Answer, error) {
	var decoded map[string]wireAnswer
	if spec.Backend == "openai" {
		var response struct {
			Answers []wireAnswer `json:"answers"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("invalid decision response JSON")
		}
		decoded = make(map[string]wireAnswer, len(response.Answers))
		for _, answer := range response.Answers {
			if _, exists := decoded[answer.Name]; exists {
				return nil, fmt.Errorf("duplicate decision answer %q", answer.Name)
			}
			answer.Noul = answer.Probability
			decoded[answer.Name] = answer
		}
	} else {
		var response struct {
			Answers map[string]wireAnswer `json:"answers"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("invalid decision response JSON")
		}
		decoded = response.Answers
	}

	answers := make(map[string]Answer, len(spec.Questions))
	for name, q := range spec.Questions {
		wire := decoded[name]
		expectedType := q.Type
		if expectedType == "predicate" && spec.Backend != "openai" {
			expectedType = "noul"
		}
		if wire.Type != "" && wire.Type != expectedType {
			return nil, fmt.Errorf("unexpected decision answer type for %q", name)
		}
		switch q.Type {
		case "predicate":
			if wire.Noul != nil && *wire.Noul >= 0 && *wire.Noul <= 1 {
				answers[name] = Answer{ProbabilityTrue: wire.Noul}
				continue
			}
		case "choice":
			if _, ok := q.Choices[wire.Choice]; ok {
				answers[name] = Answer{Choice: wire.Choice}
				continue
			}
		case "score":
			if wire.Score != nil && *wire.Score >= 0 && *wire.Score <= float64(len(q.Levels)-1) {
				answers[name] = Answer{Score: wire.Score}
				continue
			}
		}
		return nil, fmt.Errorf("missing or invalid decision answer %q", name)
	}
	return answers, nil
}
