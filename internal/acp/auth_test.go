package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestTerminalAuthNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, params       string
		wantType, wantMeta bool
	}{
		{"modern", `{"clientCapabilities":{"auth":{"terminal":true}}}`, true, false},
		{"legacy", `{"clientCapabilities":{"_meta":{"terminal-auth":true}}}`, true, false},
		{"unsupported", `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			s := NewServer(&config.Config{}, nil)
			s.out = &out
			s.SetTerminalAuth(func() bool { return false })
			s.handleInitialize(acpRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(tc.params)})
			got := out.String()
			if strings.Contains(got, `"type":"terminal"`) != tc.wantType || strings.Contains(got, `"terminal-auth"`) != tc.wantMeta {
				t.Fatalf("unexpected response: %s", got)
			}
		})
	}
}

func TestAuthRequiredForNewSession(t *testing.T) {
	var out bytes.Buffer
	s := NewServer(&config.Config{}, nil)
	s.out = &out
	s.SetTerminalAuth(func() bool { return false })
	s.handleNewSession(acpRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)})
	if !strings.Contains(out.String(), `"code":-32000`) {
		t.Fatalf("expected auth required: %s", out.String())
	}
}

func TestAuthenticatedNewSession(t *testing.T) {
	var out bytes.Buffer
	s := NewServer(&config.Config{}, nil)
	s.out = &out
	s.SetTerminalAuth(func() bool { return true })
	s.handleNewSession(acpRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)})
	if !strings.Contains(out.String(), `"sessionId"`) {
		t.Fatalf("expected session: %s", out.String())
	}
}
