package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/SK-ENT/rakitsu/internal/acp"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/spf13/cobra"
)

var acpCmd = &cobra.Command{
	Use:   "acp <config.yaml>",
	Short: "Start ACP (Agent Client Protocol) stdio server",
	Long: `Start an ACP (Agent Client Protocol) server over stdin/stdout.

ACP is the JSON-RPC 2.0 protocol Zed and other editors use to talk to coding
agents. Running this command registers rakitsu as an ACP agent an editor can
discover and invoke — every session it serves runs the config given here,
the same way "rakitsu run <config.yaml> <query>" does for a one-shot run.

Protocol flow:
  editor  -> rakitsu:  initialize
  editor  -> rakitsu:  session/new {"cwd": "...", "mcpServers": []}
  editor  -> rakitsu:  session/prompt {"sessionId": "...", "prompt": [{"type":"text","text":"..."}]}
  rakitsu -> editor:   session/update {...}          (streamed during execution)
  rakitsu -> editor:   (session/prompt response) {"stopReason": "end_turn"}
  editor  -> rakitsu:  session/cancel {"sessionId": "..."}   (notification, no response)

Example (Zed settings.json):
  "agent_servers": {
    "rakitsu": { "command": "/path/to/rakitsu", "args": ["acp", "/path/to/agent.yaml"] }
  }

Examples:
  rakitsu acp agent.yaml               # start ACP server (IDE connects via stdio)
  echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | rakitsu acp agent.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runACP,
}

var acpTimeoutSeconds int

func init() {
	rootCmd.AddCommand(acpCmd)
	acpCmd.Flags().IntVar(&acpTimeoutSeconds, "timeout", 0,
		"override settings.execution.timeout_seconds for every session/prompt turn (<=0 disables it); unset uses the pinned config, defaulting to 300s")
}

func runACP(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(args[0])
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Same reasoning as run.go: an ACP session runs agents and emits the
	// same tool-call telemetry events, so it needs the same operator
	// keyword extension applied before any session starts.
	telemetry.SetExtraRedactKeywords(cfg.Settings.RedactKeywords)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals gracefully.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	srv := acp.NewServer(cfg, executeConfig)
	if cmd.Flags().Changed("timeout") {
		srv.SetTimeoutOverride(acpTimeoutSeconds)
	}

	fmt.Fprintln(os.Stderr, "rakitsu ACP server ready (stdin/stdout)")

	return srv.Run(ctx, os.Stdin, os.Stdout)
}
