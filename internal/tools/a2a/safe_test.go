package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestNewA2AToolRejectsUserinfoAndBadScheme(t *testing.T) {
	for _, u := range []string{"http://u:p@example.com", "ftp://example.com", "file:///x"} {
		if _, err := NewA2ATool(&config.ToolDefinition{Name: "x", URL: u, AgentName: "a"}); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestA2ARedirectNotFollowedAndTokenNotForwarded(t *testing.T) {
	var otherHits, sameAuthed atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a2a":
			http.Redirect(w, r, "/elsewhere/a2a", http.StatusTemporaryRedirect)
		case "/elsewhere/a2a":
			if r.Header.Get("Authorization") != "" {
				sameAuthed.Add(1)
			}
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()
	tool, err := NewA2ATool(&config.ToolDefinition{Name: "x", URL: srv.URL, AgentName: "a", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_ = tool.call(context.Background(), "SendMessage", nil, &struct{}{})
	if sameAuthed.Load() != 0 {
		t.Fatal("bearer token forwarded on same-host redirect")
	}
	// Cross-origin redirect refused.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv2.Close()
	tool2, _ := NewA2ATool(&config.ToolDefinition{Name: "x", URL: srv2.URL, AgentName: "a", APIKey: "secret"})
	if err := tool2.call(context.Background(), "SendMessage", nil, &struct{}{}); err == nil {
		t.Fatal("expected error")
	}
	if otherHits.Load() != 0 {
		t.Fatal("cross-origin redirect followed")
	}
}

func TestA2ADefaultClientDeniesOtherLoopbackPorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/a2a", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	tool, _ := NewA2ATool(&config.ToolDefinition{Name: "x", URL: srv.URL, AgentName: "a"})
	if err := tool.call(context.Background(), "SendMessage", nil, &struct{}{}); err == nil {
		t.Fatal("expected error")
	}
}
