package acp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type configSchemaPin struct {
	SourceRelease   string                     `json:"sourceRelease"`
	ProtocolVersion int                        `json:"protocolVersion"`
	AgentMethods    map[string]string          `json:"agentMethods"`
	ClientMethods   map[string]string          `json:"clientMethods"`
	Defs            map[string]json.RawMessage `json:"$defs"`
}

func TestACPConfigOptionSchemaPin(t *testing.T) {
	b, err := os.ReadFile("testdata/acp-schema-v1.24.1/schema-excerpt.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin configSchemaPin
	if err := json.Unmarshal(b, &pin); err != nil {
		t.Fatal(err)
	}
	if pin.SourceRelease != "schema-v1.24.1" || pin.ProtocolVersion != 1 {
		t.Fatalf("schema pin = %q, protocol %d; want schema-v1.24.1, protocol 1", pin.SourceRelease, pin.ProtocolVersion)
	}
	if pin.AgentMethods["session_set_config_option"] != "session/set_config_option" {
		t.Fatalf("session config method = %q", pin.AgentMethods["session_set_config_option"])
	}
	if pin.AgentMethods["session_new"] != "session/new" || pin.ClientMethods["session_update"] != "session/update" {
		t.Fatal("pinned session method names changed")
	}

	for _, check := range []struct{ def, property string }{
		{"NewSessionResponse", "configOptions"},
		{"ClientSessionCapabilities", "configOptions"},
		{"SessionConfigOption", "type"},
		{"SessionConfigSelect", "currentValue"},
		{"SessionConfigSelect", "options"},
		{"SetSessionConfigOptionRequest", "sessionId"},
		{"SetSessionConfigOptionRequest", "configId"},
		{"SetSessionConfigOptionRequest", "value"},
		{"SetSessionConfigOptionResponse", "configOptions"},
		{"ConfigOptionUpdate", "configOptions"},
	} {
		if !schemaHasProperty(t, pin.Defs, check.def, check.property) {
			t.Errorf("%s no longer contains %q", check.def, check.property)
		}
	}
	if !strings.Contains(string(pin.Defs["SessionUpdate"]), `"config_option_update"`) {
		t.Fatal("SessionUpdate no longer includes config_option_update")
	}
}

func schemaHasProperty(t *testing.T, defs map[string]json.RawMessage, def, property string) bool {
	t.Helper()
	raw, ok := defs[def]
	if !ok {
		t.Errorf("schema definition %s is missing", def)
		return false
	}
	type branch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
		AnyOf      []branch                   `json:"anyOf"`
		OneOf      []branch                   `json:"oneOf"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Errorf("decode schema definition %s: %v", def, err)
		return false
	}
	if _, ok := shape.Properties[property]; ok {
		return true
	}
	for _, alternatives := range [][]branch{shape.AnyOf, shape.OneOf} {
		for _, alternative := range alternatives {
			if _, ok := alternative.Properties[property]; ok {
				return true
			}
		}
	}
	return false
}
