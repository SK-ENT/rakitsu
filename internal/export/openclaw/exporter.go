package openclaw

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// Exporter generates openclaw.json configs (without NemoClaw sandbox).
type Exporter struct{}

func (e *Exporter) Export(cfg *config.Config, configPath, outputDir string) error {
	if err := shared.RestoreEnvRefs(cfg, configPath); err != nil {
		return fmt.Errorf("restore env refs: %w", err)
	}

	if len(cfg.Agents) == 0 {
		return fmt.Errorf("no agents defined in config")
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("cannot create output dir: %w", err)
	}

	oc, err := BuildConfig(cfg)
	if err != nil {
		return fmt.Errorf("build config: %w", err)
	}
	return shared.WriteJSON(filepath.Join(outputDir, "openclaw.json"), oc)
}
