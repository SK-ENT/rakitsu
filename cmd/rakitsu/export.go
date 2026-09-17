package main

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export"
	"github.com/spf13/cobra"
)

var (
	exportFormat        string
	exportOutput        string
	exportWithBlueprint bool
)

var exportCmd = &cobra.Command{
	Use:   "export [config.yaml]",
	Short: "Export config to OpenClaw or NemoClaw format",
	Long: `Convert a Rakitsu YAML config into deployment files for OpenClaw or NemoClaw.

Formats:
  openclaw    OpenClaw config only — openclaw.json (verified against OpenClaw container)
  nemoclaw    NemoClaw-ready bundle — openclaw.json + sandbox-policy.yaml (verified against OpenShell on DGX Spark)

NemoClaw deployment flow:
  1. On the target host: run 'nemoclaw onboard' to create a sandbox (uses NVIDIA's bundled blueprint)
  2. Copy the exported openclaw.json to ~/.openclaw/openclaw.json
  3. Apply the policy: openshell policy set <sandbox> --policy sandbox-policy.yaml --wait

Flags:
  --with-blueprint    Also emit blueprint.yaml (only for contributing to NVIDIA's blueprint catalog).
                      Regular users don't need this — NemoClaw uses its own bundled blueprint.

For multi-agent configs, generates one subdirectory per agent with independent sandbox configs.

Examples:
  rakitsu export --format openclaw config.yaml
  rakitsu export --format nemoclaw config.yaml
  rakitsu export --format nemoclaw examples/single/05-dev-team/config.yaml --output ./deploy/
  rakitsu export --format nemoclaw config.yaml --with-blueprint`,
	Args: cobra.ExactArgs(1),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVarP(&exportFormat, "format", "f", "openclaw", "Export format: openclaw, nemoclaw")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output directory (default: ./<format>-export/)")
	exportCmd.Flags().BoolVar(&exportWithBlueprint, "with-blueprint", false, "Include blueprint.yaml (for NVIDIA catalog contributions)")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	configPath := args[0]

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("cannot load config %q: %w", configPath, err)
	}

	format := export.Format(exportFormat)
	exporter := export.NewExporter(format)
	if exporter == nil {
		return fmt.Errorf("unknown format: %s (supported: openclaw, nemoclaw)", exportFormat)
	}

	// Apply options
	if opt, ok := exporter.(export.OptionsSetter); ok {
		opt.SetOptions(export.Options{
			WithBlueprint: exportWithBlueprint,
		})
	}

	outDir := exportOutput
	if outDir == "" {
		outDir = fmt.Sprintf("./%s-export", exportFormat)
	}

	if err := exporter.Export(cfg, configPath, outDir); err != nil {
		return fmt.Errorf("export failed: %w", err)
	}

	fmt.Printf("Exported %s config to: %s\n", exportFormat, outDir)
	return nil
}
