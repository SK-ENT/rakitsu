package agent

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func TestEndPayload_PricingKnown(t *testing.T) {
	bus := telemetry.NewEventBus(8)

	known := NewAgent(&config.AgentDefinition{Name: "known", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)
	known.SetPricing(config.PricingConfig{Input: 1, Output: 2}, true)
	p := known.endPayload("success", "", 1, 100)
	if !p.PricingKnown {
		t.Errorf("PricingKnown = false, want true")
	}

	unknown := NewAgent(&config.AgentDefinition{Name: "unknown", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)
	unknown.SetPricing(config.PricingConfig{}, false)
	p = unknown.endPayload("success", "", 1, 100)
	if p.PricingKnown {
		t.Errorf("PricingKnown = true, want false")
	}

	// Never called SetPricing at all — must default to false, not panic.
	neverSet := NewAgent(&config.AgentDefinition{Name: "never-set", Role: "worker"}, nil, tools.NewToolRegistry(), bus, nil)
	p = neverSet.endPayload("success", "", 1, 100)
	if p.PricingKnown {
		t.Errorf("PricingKnown = true, want false for an agent that never called SetPricing")
	}
}
