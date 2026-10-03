package config

import "testing"

func TestClientVersion_GetAndValidate(t *testing.T) {
	cfg := Config{Settings: Settings{Providers: map[string]ProviderDefinition{
		"codex": {Type: "codex", ClientVersion: "0.200.0"},
	}}}
	if got := cfg.GetClientVersion("codex"); got != "0.200.0" {
		t.Fatalf("GetClientVersion = %q", got)
	}
	if got := cfg.GetClientVersion("missing"); got != "" {
		t.Fatalf("GetClientVersion(missing) = %q, want empty", got)
	}
	for _, e := range cfg.Validate() {
		if e.Field == "settings.providers.codex.client_version" {
			t.Fatalf("valid version rejected: %v", e)
		}
	}
	for _, bad := range []string{"1.0.0&x=1", "v1", "1..0", "1.0.0.0.0", " 1.0"} {
		cfg.Settings.Providers["codex"] = ProviderDefinition{Type: "codex", ClientVersion: bad}
		found := false
		for _, e := range cfg.Validate() {
			if e.Field == "settings.providers.codex.client_version" {
				found = true
			}
		}
		if !found {
			t.Errorf("client_version %q not rejected", bad)
		}
	}
}
