package agent

import "github.com/SK-ENT/rakitsu/internal/config"

// Default pricing in USD per 1M tokens.
// Used when user config doesn't specify pricing for a model.
var defaultPricing = map[string]config.PricingConfig{
	// OpenAI
	"gpt-4o":      {Input: 2.50, Output: 10.00},
	"gpt-4o-mini": {Input: 0.15, Output: 0.60},
	"gpt-4.1":     {Input: 2.00, Output: 8.00},
	"gpt-4.1-mini": {Input: 0.40, Output: 1.60},
	"gpt-4.1-nano": {Input: 0.10, Output: 0.40},
	"o3":          {Input: 2.00, Output: 8.00},
	"o3-mini":     {Input: 1.10, Output: 4.40},
	"o4-mini":     {Input: 1.10, Output: 4.40},

	// Anthropic
	"claude-sonnet-4-5":   {Input: 3.00, Output: 15.00},
	"claude-opus-4-6":     {Input: 15.00, Output: 75.00},
	"claude-haiku-4-5":    {Input: 0.80, Output: 4.00},

	// Gemini
	"gemini-2.5-pro":            {Input: 1.25, Output: 10.00},
	"gemini-2.5-flash":          {Input: 0.15, Output: 0.60},
	"gemini-3.1-flash-lite-preview": {Input: 0.075, Output: 0.30},
	"gemini-3.5-flash-lite":     {Input: 0.30, Output: 2.50},
	"gemini-3.6-flash":          {Input: 1.50, Output: 7.50},
}

// ResolvePricing returns the pricing for a model, checking user config first,
// then falling back to defaults, and whether any entry was actually found.
// known=false means the returned PricingConfig is a zero-value placeholder,
// not a confirmed "this model is free."
func ResolvePricing(model string, userPricing map[string]config.PricingConfig) (config.PricingConfig, bool) {
	if userPricing != nil {
		if p, ok := userPricing[model]; ok {
			return p, true
		}
	}
	if p, ok := defaultPricing[model]; ok {
		return p, true
	}
	return config.PricingConfig{}, false
}
