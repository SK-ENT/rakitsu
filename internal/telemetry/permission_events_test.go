package telemetry

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPermissionEventNames(t *testing.T) {
	if EventPermissionRequested != "PERMISSION_REQUESTED" || EventPermissionDecided != "PERMISSION_DECIDED" {
		t.Fatal("event names changed")
	}
}

func TestRedact_PermissionRequested_RedactsArgv(t *testing.T) {
	p, _ := json.Marshal(PermissionRequestedPayload{RequestID: "r1", Argv: []string{"curl", "-H", "Authorization: Bearer SYNTH_TOKEN_123"}})
	out := RedactEventPayload(EventPermissionRequested, p)
	if bytes.Contains(out, []byte("SYNTH_TOKEN_123")) {
		t.Fatalf("argv not redacted: %s", out)
	}
	if !bytes.Contains(out, []byte(`"request_id":"r1"`)) {
		t.Fatalf("non-secret field lost: %s", out)
	}
}

func TestRedact_PermissionRequested_FlagValueForms(t *testing.T) {
	for _, argv := range [][]string{
		{"tool", "--token", "SYNTH_TOKEN_123"},
		{"tool", "--password=SYNTH_TOKEN_123"},
		{"tool", "https://user:SYNTH_TOKEN_123@host/x"},
	} {
		p, _ := json.Marshal(PermissionRequestedPayload{RequestID: "r1", Argv: argv})
		out := RedactEventPayload(EventPermissionRequested, p)
		if bytes.Contains(out, []byte("SYNTH_TOKEN_123")) {
			t.Fatalf("argv %v not redacted: %s", argv, out)
		}
	}
}

func TestRedact_PermissionRequested_KeepsPlainArgv(t *testing.T) {
	p, _ := json.Marshal(PermissionRequestedPayload{RequestID: "r1", Argv: []string{"ls", "-la", "/tmp"}, Options: []string{"once", "deny"}})
	out := RedactEventPayload(EventPermissionRequested, p)
	if !bytes.Contains(out, []byte(`"argv":["ls","-la","/tmp"]`)) {
		t.Fatalf("plain argv changed: %s", out)
	}
}

func TestPermissionDecidedJSON(t *testing.T) {
	b, _ := json.Marshal(PermissionDecidedPayload{RequestID: "r1", Decision: "allow", Scope: "once", Source: "client_selected"})
	for _, k := range []string{`"request_id":"r1"`, `"decision":"allow"`, `"scope":"once"`, `"source":"client_selected"`} {
		if !bytes.Contains(b, []byte(k)) {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
	if out := RedactEventPayload(EventPermissionDecided, b); !bytes.Equal(out, b) {
		t.Fatalf("decided payload altered: %s", out)
	}
}
