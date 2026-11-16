package config

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/permission"
)

type FocusConfig struct {
	Mode           permission.Focus   `mapstructure:"mode" yaml:"mode,omitempty"`
	AvailableModes []permission.Focus `mapstructure:"available_modes" yaml:"available_modes,omitempty"`
}

func (s Settings) PolicyConfig() permission.PolicyConfig {
	mode := s.Permissions.Mode
	if mode == "" {
		mode = permission.ModeLegacy
	}
	return permission.PolicyConfig{DefaultFocus: s.Focus.Mode, AvailableFocus: append([]permission.Focus(nil), s.Focus.AvailableModes...), DefaultMode: mode, AvailableModes: append([]permission.Mode(nil), s.Permissions.AvailableModes...)}
}

// ResolvePolicy combines trusted runtime inputs with selectors loaded from
// YAML. Entry points provide the operator/workflow projection before tools exist.
func ResolvePolicy(s Settings, selection permission.Selection, parent *permission.EffectivePolicy, inputs permission.PolicyInputs) (permission.EffectivePolicy, error) {
	resolved, err := s.PolicyConfig().ResolveSelection(selection)
	if err != nil {
		return permission.EffectivePolicy{}, err
	}
	switch resolved.Mode {
	case permission.ModeAsk:
		if !askModeEnabled {
			return permission.EffectivePolicy{}, fmt.Errorf("permission mode ask is not enabled in this build")
		}
	case permission.ModeSemiAuto:
		if !semiAutoModeEnabled {
			return permission.EffectivePolicy{}, fmt.Errorf("permission mode semi_auto is not enabled in this build")
		}
	case permission.ModeAuto:
		if !autoModeEnabled {
			return permission.EffectivePolicy{}, fmt.Errorf("permission mode auto is not enabled in this build")
		}
	}
	return permission.ResolvePolicy(s.PolicyConfig(), resolved, parent, inputs)
}
