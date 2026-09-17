package agent

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
)

func TestResolvePricing_KnownFromUserConfig(t *testing.T) {
	user := map[string]config.PricingConfig{"my-model": {Input: 1, Output: 2}}
	p, known := ResolvePricing("my-model", user)
	if !known {
		t.Errorf("known = false, want true for a user-configured model")
	}
	if p.Input != 1 || p.Output != 2 {
		t.Errorf("p = %+v, want {1 2}", p)
	}
}

func TestResolvePricing_KnownFromDefaults(t *testing.T) {
	p, known := ResolvePricing("gpt-4o-mini", nil)
	if !known {
		t.Errorf("known = false, want true for a built-in default model")
	}
	if p.Input != 0.15 {
		t.Errorf("p.Input = %v, want 0.15", p.Input)
	}
}

// The gemini-3.x routes are the LiteLLM dogfood models, so a missing entry
// here renders as "$0.00 (pricing not set)" in /usage on every run.
// Rates are Google's list prices per 1M tokens (ai.google.dev/gemini-api/docs/pricing).
func TestResolvePricing_Gemini3Routes(t *testing.T) {
	for _, tc := range []struct {
		model         string
		input, output float64
	}{
		{"gemini-3.5-flash-lite", 0.30, 2.50},
		{"gemini-3.6-flash", 1.50, 7.50},
	} {
		p, known := ResolvePricing(tc.model, nil)
		if !known {
			t.Errorf("known = false, want true for %q", tc.model)
			continue
		}
		if p.Input != tc.input || p.Output != tc.output {
			t.Errorf("%s: p = %+v, want {%v %v} USD per 1M tokens", tc.model, p, tc.input, tc.output)
		}
	}
}

// Pricing matches exactly, never by prefix — unlike llm.LookupContextWindow,
// which prefix-matches because a model family shares one window size. Prices
// vary by an order of magnitude within a family, so inheriting a sibling's
// rate would report a confidently wrong dollar figure instead of "not set".
func TestResolvePricing_NoPrefixInheritance(t *testing.T) {
	for _, model := range []string{"gemini-3.5-pro", "gemini-3.5-flash-lite-preview"} {
		if _, known := ResolvePricing(model, nil); known {
			t.Errorf("known = true for %q, want false — pricing must not match by prefix", model)
		}
	}
}

func TestResolvePricing_UnknownModel(t *testing.T) {
	p, known := ResolvePricing("some-custom-litellm-route", nil)
	if known {
		t.Errorf("known = true, want false for an unmapped model")
	}
	if p != (config.PricingConfig{}) {
		t.Errorf("p = %+v, want zero value", p)
	}
}
