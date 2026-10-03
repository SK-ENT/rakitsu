package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWakeConfigYAMLRoundTrip(t *testing.T) {
	src := `
wake:
  enabled: true
  interval_seconds: 60
  checks:
    - name: btc
      type: http_json
      url: http://localhost:1/x
      field: bitcoin.usd
      alarm_if: { below: 55000 }
      label_band: { upper: 1.0, lower: -1.0, exit: 0.5 }
`
	var s Settings
	if err := yaml.Unmarshal([]byte(src), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Wake.Enabled || len(s.Wake.Checks) != 1 {
		t.Fatalf("wake not parsed: %+v", s.Wake)
	}
	c := s.Wake.Checks[0]
	if c.AlarmIf == nil || c.AlarmIf.Below == nil || *c.AlarmIf.Below != 55000 {
		t.Fatalf("alarm_if.below not parsed: %+v", c.AlarmIf)
	}
	if c.LabelBand == nil || c.LabelBand.Exit != 0.5 {
		t.Fatalf("label_band not parsed: %+v", c.LabelBand)
	}
}

func TestWakeDefaultsOff(t *testing.T) {
	var s Settings
	if s.Wake.Enabled {
		t.Fatal("wake must default to disabled")
	}
}
