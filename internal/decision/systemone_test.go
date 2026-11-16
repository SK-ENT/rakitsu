package decision

import (
	"context"
	"strings"
	"testing"
)

func TestDecisionRejectsUnsafeEndpoints(t *testing.T) {
	for _, tc := range []struct{ backend, endpoint string }{
		{"jev", "https://example.invalid/v1/systemone"},
		{"openai", "https://api.openai.com.example.invalid/v1/decisions"},
		{"gemini", "https://api.openai.com/v1/decisions"},
		{"openai", "https://api.openai.com:8443/v1/decisions"},
		{"openai", "https://api.openai.com/v1/decisions?key=secret"},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/decisions#fragment"},
		{"laya", "https://example.invalid/v1/systemone"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := postDecision(ctx, tc.backend, tc.endpoint, "Authorization", "fixture-key", nil)
			if err == nil || !strings.Contains(err.Error(), "decision backend URL") {
				t.Fatalf("expected URL rejection before the HTTP request, got %v", err)
			}
		})
	}
}
