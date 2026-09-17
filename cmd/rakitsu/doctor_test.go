package main

import (
	"os"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestCheckEnvVarReferences_NoneFound(t *testing.T) {
	raw := []byte("name: test\nversion: \"1.0\"\nagents:\n  - name: a\n    model: gpt-4\n")
	findings := checkEnvVarReferences(raw)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].sev != sevInfo {
		t.Errorf("severity = %v, want sevInfo", findings[0].sev)
	}
	if !strings.Contains(findings[0].msg, "no ${VAR}") {
		t.Errorf("msg = %q, want mention of no refs", findings[0].msg)
	}
}

func TestCheckEnvVarReferences_PartitionsSetUnsetEmpty(t *testing.T) {
	// Mix of three states: a set var, an unset var, an empty-string var.
	t.Setenv("DOCTOR_TEST_SET", "value-here")
	t.Setenv("DOCTOR_TEST_EMPTY", "")
	os.Unsetenv("DOCTOR_TEST_UNSET")

	raw := []byte(`
settings:
  providers:
    p1:
      api_key: ${DOCTOR_TEST_SET}
      base_url: ${DOCTOR_TEST_EMPTY}
      timeout: ${DOCTOR_TEST_UNSET:-30}
`)
	findings := checkEnvVarReferences(raw)

	var sawSet, sawEmpty, sawUnset bool
	for _, f := range findings {
		switch {
		case strings.Contains(f.label, "set") && strings.Contains(f.msg, "DOCTOR_TEST_SET"):
			sawSet = true
			if f.sev != sevOK {
				t.Errorf("set vars expected sevOK, got %v", f.sev)
			}
		case strings.Contains(f.label, "empty") && strings.Contains(f.msg, "DOCTOR_TEST_EMPTY"):
			sawEmpty = true
			if f.sev != sevWarn {
				t.Errorf("empty vars expected sevWarn, got %v", f.sev)
			}
		case strings.Contains(f.label, "unset") && strings.Contains(f.msg, "DOCTOR_TEST_UNSET"):
			sawUnset = true
			if f.sev != sevErr {
				t.Errorf("unset vars expected sevErr, got %v", f.sev)
			}
		}
	}
	if !sawSet {
		t.Error("missing finding for set var")
	}
	if !sawEmpty {
		t.Error("missing finding for empty var")
	}
	if !sawUnset {
		t.Error("missing finding for unset var")
	}
}

func TestCheckEnvVarReferences_DedupsAndHandlesDefaultSyntax(t *testing.T) {
	// Same var referenced twice; should appear once. Default-value syntax
	// ${VAR:-fallback} should still be detected as a reference.
	t.Setenv("DOCTOR_TEST_DUP", "v")
	raw := []byte(`
a: ${DOCTOR_TEST_DUP}
b: ${DOCTOR_TEST_DUP}
c: ${DOCTOR_TEST_DUP:-fallback}
`)
	findings := checkEnvVarReferences(raw)
	for _, f := range findings {
		if strings.Contains(f.label, "set") {
			occurrences := strings.Count(f.msg, "DOCTOR_TEST_DUP")
			if occurrences != 1 {
				t.Errorf("DOCTOR_TEST_DUP appeared %d times in set-findings msg, want 1: %q", occurrences, f.msg)
			}
			return
		}
	}
	t.Error("expected at least one set finding for DOCTOR_TEST_DUP")
}

func TestContainsCaseInsensitive(t *testing.T) {
	cases := []struct {
		haystack []string
		needle   string
		want     bool
	}{
		{[]string{"gpt-4", "claude-3-opus"}, "gpt-4", true},
		{[]string{"gpt-4", "claude-3-opus"}, "GPT-4", true},
		{[]string{"vllm-nemotron-elastic-30b"}, "vllm-nemotron-30b", false}, // the recent rename caught by doctor in dogfood
		{[]string{}, "anything", false},
		{[]string{"x"}, "", false},
	}
	for i, tc := range cases {
		got := containsCaseInsensitive(tc.haystack, tc.needle)
		if got != tc.want {
			t.Errorf("case %d: containsCaseInsensitive(%v, %q) = %v, want %v", i, tc.haystack, tc.needle, got, tc.want)
		}
	}
}

func TestCheckModelsInCatalog_DetectsMissing(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{
			DefaultProvider: "litellm",
		},
		Agents: []config.AgentDefinition{
			{Name: "A1", Model: "vllm-nemotron-retired-99b", Provider: "litellm"},
			{Name: "A2", Model: "vllm-nemotron-elastic-30b", Provider: "litellm"},
		},
	}
	catalogs := map[string][]string{
		"litellm": {"vllm-nemotron-elastic-30b", "vllm-nemotron-elastic-30b-long", "gemini-3.1-flash-lite"},
	}
	findings := checkModelsInCatalog(cfg, catalogs)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (one per agent), got %d", len(findings))
	}

	var a1err, a2ok bool
	for _, f := range findings {
		if f.subj == "A1" {
			if f.sev != sevErr {
				t.Errorf("A1 sev = %v, want sevErr (model not in catalog)", f.sev)
			}
			if !strings.Contains(f.msg, "vllm-nemotron-retired-99b") {
				t.Errorf("A1 msg should mention the missing model: %q", f.msg)
			}
			if !strings.Contains(f.msg, "available:") {
				t.Errorf("A1 msg should include available-models hint: %q", f.msg)
			}
			a1err = true
		}
		if f.subj == "A2" {
			if f.sev != sevOK {
				t.Errorf("A2 sev = %v, want sevOK (model present)", f.sev)
			}
			a2ok = true
		}
	}
	if !a1err {
		t.Error("missing finding for A1")
	}
	if !a2ok {
		t.Error("missing finding for A2")
	}
}

func TestCheckModelsInCatalog_HonorsCaseInsensitiveProviderLookup(t *testing.T) {
	// Viper lowercases all map keys; agent.Provider in YAML is preserved.
	// Doctor must match case-insensitively so a "LiteLLM" agent provider
	// still finds the "litellm" catalog.
	cfg := &config.Config{
		Settings: config.Settings{DefaultProvider: "litellm"},
		Agents: []config.AgentDefinition{
			{Name: "A1", Model: "m1", Provider: "LiteLLM"},
		},
	}
	catalogs := map[string][]string{"litellm": {"m1"}}
	findings := checkModelsInCatalog(cfg, catalogs)
	if len(findings) != 1 || findings[0].sev != sevOK {
		t.Errorf("expected single OK finding, got %+v", findings)
	}
}

func TestCheckModelsInCatalog_SkipsUnprobedProvider(t *testing.T) {
	// Anthropic/Gemini providers aren't probed by MVP; agents on those
	// providers should produce INFO findings, not errors.
	cfg := &config.Config{
		Settings: config.Settings{DefaultProvider: "anthropic"},
		Agents: []config.AgentDefinition{
			{Name: "A1", Model: "claude-3-opus", Provider: "anthropic"},
		},
	}
	catalogs := map[string][]string{} // no probe results
	findings := checkModelsInCatalog(cfg, catalogs)
	if len(findings) != 1 || findings[0].sev != sevInfo {
		t.Errorf("expected single INFO finding (catalog not probed), got %+v", findings)
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		in   []finding
		want int
	}{
		{"all OK", []finding{{sev: sevOK}, {sev: sevOK}}, 0},
		{"info only", []finding{{sev: sevInfo}, {sev: sevOK}}, 0},
		{"warn present", []finding{{sev: sevOK}, {sev: sevWarn}}, 1},
		{"error present", []finding{{sev: sevWarn}, {sev: sevErr}}, 2},
		{"empty", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitCodeFor(tc.in)
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSnippet(t *testing.T) {
	got := snippet([]byte("hello world this is too long"), 10)
	if got != "hello worl…" {
		t.Errorf("got %q, want %q", got, "hello worl…")
	}
	got2 := snippet([]byte("short"), 100)
	if got2 != "short" {
		t.Errorf("got %q, want %q", got2, "short")
	}
}
