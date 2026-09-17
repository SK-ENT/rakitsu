// Package export converts Rakitsu configs to external agent runtime formats.
//
// Sub-packages:
//   - core:     orchestration engine and RuntimeTarget interface
//   - nemoclaw: NemoClaw/OpenShell deployment exporter
//   - openclaw: OpenClaw config exporter
//   - shared:   utilities shared across export sub-packages
package export

import (
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/nemoclaw"
	ocexport "github.com/SK-ENT/rakitsu/internal/export/openclaw"
)

// Format identifies a target export format.
type Format string

const (
	FormatNemoClaw Format = "nemoclaw"
	FormatOpenClaw Format = "openclaw"
)

// Exporter converts a Rakitsu config into files for an external runtime.
type Exporter interface {
	// Export writes deployment files to outputDir.
	// configPath is the original YAML file path (used to preserve env var references).
	Export(cfg *config.Config, configPath, outputDir string) error
}

// Options configures exporter behavior.
type Options struct {
	// WithBlueprint emits blueprint.yaml (only needed for NVIDIA catalog contributions).
	WithBlueprint bool
}

// OptionsSetter is implemented by exporters that accept runtime options.
type OptionsSetter interface {
	SetOptions(opts Options)
}

// NewExporter returns the exporter for the given format.
func NewExporter(format Format) Exporter {
	switch format {
	case FormatNemoClaw:
		return &nemoClawAdapter{}
	case FormatOpenClaw:
		return &ocexport.Exporter{}
	}
	return nil
}

// nemoClawAdapter wraps nemoclaw.Exporter to implement the root OptionsSetter
// interface, bridging between the root Options type and the nemoclaw package.
type nemoClawAdapter struct {
	inner nemoclaw.Exporter
}

func (a *nemoClawAdapter) Export(cfg *config.Config, configPath, outputDir string) error {
	return a.inner.Export(cfg, configPath, outputDir)
}

func (a *nemoClawAdapter) SetOptions(opts Options) {
	a.inner.SetOptions(nemoclaw.Options{
		WithBlueprint: opts.WithBlueprint,
	})
}
