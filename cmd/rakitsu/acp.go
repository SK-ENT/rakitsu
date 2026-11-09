package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/SK-ENT/rakitsu/internal/acp"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/spf13/cobra"
)

var acpCmd = &cobra.Command{
	Use:   "acp [config.yaml]",
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
	Args: cobra.MaximumNArgs(1),
	RunE: runACP,
}

var (
	acpTimeoutSeconds int
	acpMaxSessions    int
	acpSessionIdle    time.Duration
	acpLoginFlag      bool
)

func init() {
	rootCmd.AddCommand(acpCmd)
	acpCmd.Flags().BoolVar(&acpLoginFlag, "login", false, "set up the built-in ACP provider")
	acpCmd.Flags().IntVar(&acpTimeoutSeconds, "timeout", 0,
		"override settings.execution.timeout_seconds for every session/prompt turn (<=0 disables it); unset uses the pinned config, defaulting to 300s")
	acpCmd.Flags().IntVar(&acpMaxSessions, "max-sessions", 256,
		"max sessions retained in memory; the least recently used idle session is evicted past this (<=0 = unlimited; sessions with a running prompt are never evicted)")
	acpCmd.Flags().DurationVar(&acpSessionIdle, "session-idle-timeout", 24*time.Hour,
		"evict a session unused for this long (<=0 = never); a later prompt on it gets \"session not found\"")
}

func runACP(cmd *cobra.Command, args []string) error {
	if acpLoginFlag {
		if len(args) != 0 {
			return fmt.Errorf("--login cannot be used with a config")
		}
		return acpLogin()
	}
	var cfg *config.Config
	if len(args) == 1 {
		var err error
		cfg, err = config.Load(args[0])
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	} else {
		cfg = acpDefaultConfig("")
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

	var srv *acp.Server
	if len(args) == 0 {
		srv = newManagedACPServer(defaultACPSetup().load, executeConfigConv)
	} else {
		srv = acp.NewServer(cfg, executeConfigConv)
	}
	srv.SetSessionRetention(acpMaxSessions, acpSessionIdle)
	if cmd.Flags().Changed("timeout") {
		srv.SetTimeoutOverride(acpTimeoutSeconds)
	}

	fmt.Fprintln(os.Stderr, "rakitsu ACP server ready (stdin/stdout)")

	return srv.Run(ctx, os.Stdin, os.Stdout)
}

// managedACPRun keeps provider error bodies out of the ACP response.
func managedACPRun(run acp.RunFunc) acp.RunFunc {
	return func(ctx context.Context, cfg *config.Config, bus *telemetry.EventBus, dc *debug.DebugController, query string, registrar func([]debug.Attachable), conv *acp.ConvTurn) (string, error) {
		result, err := run(ctx, cfg, bus, dc, query, registrar, conv)
		if err != nil {
			return "", fmt.Errorf("provider request failed; check your credentials and network connection")
		}
		return result, nil
	}
}

// newManagedACPServer retries credential lookup until login succeeds. The
// resulting configuration is immutable and shared by all subsequent turns.
func newManagedACPServer(loadConfig func() (*config.Config, error), run acp.RunFunc) *acp.Server {
	var mu sync.Mutex
	var readyConfig *config.Config
	ready := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if readyConfig != nil {
			return true
		}
		cfg, err := loadConfig()
		if err != nil || cfg == nil {
			return false
		}
		readyConfig = cfg
		return true
	}
	managedRun := managedACPRun(func(ctx context.Context, _ *config.Config, bus *telemetry.EventBus, dc *debug.DebugController, query string, registrar func([]debug.Attachable), conv *acp.ConvTurn) (string, error) {
		mu.Lock()
		cfg := readyConfig
		mu.Unlock()
		if cfg == nil {
			return "", fmt.Errorf("authentication required")
		}
		return run(ctx, cfg, bus, dc, query, registrar, conv)
	})
	srv := acp.NewServer(acpDefaultConfig(""), managedRun)
	srv.SetTerminalAuth(ready)
	return srv
}
