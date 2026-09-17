package shared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Researcher", "researcher"},
		{"Log Analyzer", "log-analyzer"},
		{"BackendReviewer", "backend-reviewer"},
		{"api-specialist", "api-specialist"},
	}
	for _, tt := range tests {
		got := SanitizeID(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShellQuote_Metacharacters(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"semi;colon", "'semi;colon'"},
		{"pipe|char", "'pipe|char'"},
		{"dollar$var", "'dollar$var'"},
		{"back`tick", "'back`tick'"},
		{"single'quote", `'single'\''quote'`},
		{"$(command)", "'$(command)'"},
		{`"double"`, `'"double"'`},
	}
	for _, tt := range tests {
		got := ShellQuote(tt.input)
		if got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeComment(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"single line", "single line"},
		{"line one\nline two", "line one line two"},
		{"cr\rand\r\nnewline", "cr and  newline"},
	}
	for _, tt := range tests {
		got := SanitizeComment(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeComment(%q) = %q, want %q", tt.input, got, tt.want)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("SanitizeComment(%q) = %q still contains a newline", tt.input, got)
		}
	}
}

func TestIsEnvRef(t *testing.T) {
	if !IsEnvRef("${OPENAI_API_KEY}") {
		t.Error("expected true for ${OPENAI_API_KEY}")
	}
	if IsEnvRef("sk-raw-key") {
		t.Error("expected false for sk-raw-key")
	}
}

func TestEnvVarName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"${OPENAI_API_KEY}", "OPENAI_API_KEY"},
		{"${MY_VAR_123}", "MY_VAR_123"},
		{"${_PRIVATE}", "_PRIVATE"},
		{"${EVIL!@#$}", "${EVIL!@#$}"},
		{"${123BAD}", "${123BAD}"},
		{"${}", "${}"},
		{"${a b}", "${a b}"},
		{"sk-raw-key", "sk-raw-key"},
	}
	for _, tt := range tests {
		got := EnvVarName(tt.input)
		if got != tt.want {
			t.Errorf("EnvVarName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRawProviderEnvVars_Error(t *testing.T) {
	_, err := RawProviderEnvVars("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for unreadable config, got nil")
	}
}

func TestExtractHostPort(t *testing.T) {
	tests := []struct {
		input    string
		wantHost string
		wantPort int
	}{
		{"https://api.openai.com/v1", "api.openai.com", 0},
		{"http://localhost:4000", "localhost", 4000},
		{"api.anthropic.com", "api.anthropic.com", 0},
		{"https://llm.example.com:4443/v1", "llm.example.com", 4443},
		// Regression: userinfo must not leak into the host.
		{"https://user:pass@example.com:8443/mcp", "example.com", 8443},
		// Regression: bare IPv6 literals, with and without an explicit port.
		{"http://[::1]", "::1", 0},
		{"http://[::1]:8080", "::1", 8080},
		// Regression: scheme stripping must not be case-sensitive.
		{"HTTPS://Example.com", "Example.com", 0},
		// A ${VAR} placeholder that reached this function unexpanded must
		// degrade to ("", 0), not a garbage value — see the doc comment.
		{"https://${DGX_HOST}:4443/v1", "", 0},
	}
	for _, tt := range tests {
		host, port := ExtractHostPort(tt.input)
		if host != tt.wantHost {
			t.Errorf("ExtractHostPort(%q) host = %q, want %q", tt.input, host, tt.wantHost)
		}
		if port != tt.wantPort {
			t.Errorf("ExtractHostPort(%q) port = %d, want %d", tt.input, port, tt.wantPort)
		}
	}
}

// Regression: RestoreEnvRefs used to do an exact-string lookup into
// cfg.Settings.Providers, which Viper always lowercases. A raw-YAML provider
// block with any uppercase letter in its name silently failed to restore.
func TestRestoreEnvRefs_CaseInsensitiveProviderName(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
settings:
  providers:
    DGX:
      type: ollama
      api_key: "${DGX_API_KEY}"
      base_url: "${DGX_BASE_URL}"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulates what config.Load() actually produces: the provider map is
	// keyed lowercase, and the values are already-resolved (real secrets),
	// not the ${VAR} form.
	cfg := &config.Config{
		Settings: config.Settings{
			Providers: map[string]config.ProviderDefinition{
				"dgx": {Type: "ollama", APIKey: "sk-real-resolved-secret", BaseURL: "http://10.0.0.1:1234"},
			},
		},
	}

	if err := RestoreEnvRefs(cfg, configPath); err != nil {
		t.Fatalf("RestoreEnvRefs: %v", err)
	}

	pd, ok := cfg.Settings.Providers["dgx"]
	if !ok {
		t.Fatal("provider \"dgx\" missing after RestoreEnvRefs")
	}
	if pd.APIKey != "${DGX_API_KEY}" {
		t.Errorf("APIKey = %q, want the restored ${DGX_API_KEY} reference, not the resolved secret", pd.APIKey)
	}
	if pd.BaseURL != "${DGX_BASE_URL}" {
		t.Errorf("BaseURL = %q, want the restored ${DGX_BASE_URL} reference", pd.BaseURL)
	}
}

func TestQualifiedModel(t *testing.T) {
	got := QualifiedModel("openai", "gpt-4o")
	if got != "openai/gpt-4o" {
		t.Errorf("QualifiedModel = %q, want %q", got, "openai/gpt-4o")
	}
}

func TestDedup(t *testing.T) {
	got := Dedup([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Dedup = %v, want [a b c]", got)
	}
}
