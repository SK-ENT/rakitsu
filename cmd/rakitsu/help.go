package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/SK-ENT/rakitsu/internal/brand"
	"github.com/spf13/cobra"
)

// installHelpBanner makes the root command's help (`rakitsu --help`, `rakitsu
// -h`, or bare `rakitsu`) open with the brand mark + version — the same
// identity shown at chat TUI startup (internal/chat/banner.go), so the CLI's
// front door carries it too. Subcommand help (`rakitsu run --help`) is left
// alone: showing the mark on every subcommand would just be noise.
func installHelpBanner(root *cobra.Command) {
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd.Parent() == nil {
			fmt.Fprintln(cmd.OutOrStdout(), renderCLIBanner())
		}
		defaultHelp(cmd, args)
	})
}

// renderCLIBanner draws the mark beside "rakitsu <version>", vertically
// centered against the mark's height.
func renderCLIBanner() string {
	version := lipgloss.NewStyle().Bold(true).Render("rakitsu " + versionString())
	return lipgloss.JoinHorizontal(lipgloss.Center, brand.Render(), "   ", version) + "\n"
}
