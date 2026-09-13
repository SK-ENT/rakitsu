// Package main provides the CLI entry point for Rakitsu
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/paupawsan/rakitsu/internal/dotenv"
	"github.com/spf13/cobra"
)

var (
	// Version and LicenseRequired are set at build time via -ldflags.
	// Version embeds the build component (commit hash on main, commit
	// count on dev branches) — see the Makefile.
	Version         = "dev"
	BuildCommit     = "" // release builds set the bare tag as Version and pass the commit here
	LicenseRequired = "" // set to "true" via -ldflags for official release builds

	// Global flags
	cfgFile string
	verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "rakitsu",
	Short: "Rakitsu - A sophisticated CLI-based agentic system",
	Long: `Rakitsu is a sophisticated, globally installable CLI-based agentic
system with visual tooling. It allows you to define multi-agent systems
using YAML configuration and execute them from the command line.

Features:
  • Multi-agent orchestration with ReAct loop and reflection
  • Multi-provider support: OpenAI, Anthropic, Gemini, Ollama (per-agent)
  • Visual builder for designing agent flows (rakitsu serve)
  • Real-time monitoring hub for multiple concurrent runs (rakitsu serve)
  • Runtime debug attach: breakpoints, pause/resume, parameter overrides
  • Session history with replay and config export
  • Secure tool execution with whitelist sandboxing
  • Pipeline and hierarchical orchestration strategies

Quick start:
  rakitsu run --interactive                 Chat right now — no config needed
  rakitsu run agent.yaml "your query"       Run a single agent
  rakitsu run agent.yaml --interactive      Chat interactively with your own config
  rakitsu scaffold <use-case>               Generate a config from a use-case preset
  rakitsu quickstart                        Interactive wizard — no YAML needed
  rakitsu serve                             Start the web UI + monitoring hub
  rakitsu doctor agent.yaml                 Check config + provider health

  Progressive sample configs (single-file and modular) live under
  examples/ in the repo — see examples/README.md.

Architecture:
  rakitsu serve    Web UI + standalone SSE hub — visual builder, run launcher,
                 inspector, and debugger. Open http://localhost:9100 to see all
                 active runs, select one to inspect its execution tree/graph,
                 and attach the debugger at runtime (breakpoints, pause/resume,
                 param overrides).

  rakitsu run      Executes agents. Auto-connects to a running hub (default
                 http://localhost:9100) to push events. If no hub is running,
                 the agent runs standalone with no overhead. Use --no-hub to
                 explicitly disable, or --debug-port for single-run debugging.

  rakitsu doctor   Diagnoses a config before you run it: provider reachability,
                 auth, model-in-catalog, and ${VAR} resolution.`,
	Version: versionString(),
	// A runtime error (missing API key, unreachable provider) is not a
	// usage mistake — printing the full flag usage block after it buries
	// the actionable message. Cobra still prints the error itself.
	SilenceUsage: true,
	// Load a project-local .env (if any) before any subcommand runs, so a
	// quickstart-generated project's saved API key is available to `run`/
	// `serve`/`doctor` without the user exporting anything by hand. Never
	// overrides a variable already set in the real shell environment —
	// see internal/dotenv.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return dotenv.Load(".env")
	},
}

// versionString is the user-facing version. Release builds set Version to the
// bare git tag and pass the commit separately as BuildCommit; dev/Makefile
// builds already embed the build component in Version itself, so only append
// the commit when it isn't already there.
func versionString() string {
	if BuildCommit != "" && !strings.Contains(Version, BuildCommit) {
		return Version + " (" + BuildCommit + ")"
	}
	return Version
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("{{.Name}} version %s\nLicense: BSL 1.1 — see LICENSE\n", versionString()))
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is ./agent.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	installHelpBanner(rootCmd)
}

func main() {
	// cobra already prints the error (Error: …) — don't print it twice.
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	var exitErr doctorExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.Code)
	}
	os.Exit(1)
}
