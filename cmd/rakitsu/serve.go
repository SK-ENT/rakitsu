package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"os/signal"
	"syscall"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/session"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/spf13/cobra"
)

var (
	servePort      int
	serveHost      string
	mcpPort        int
	mcpConfig      string
	serveConfigDir string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the hub: web UI, SSE, agent runner, debugger, MCP, A2A",
	Long: `Start the Rakitsu hub — the single server for everything.

Combines the SSE hub, embedded web UI, agent runner, session history,
debugger, MCP server, and A2A endpoint in one process.

How it works:
  1. Start the hub:  rakitsu serve
  2. Run agents:     rakitsu run config.yaml "query"  (auto-connects)
  3. Open browser:   http://localhost:9100

Web UI features:
  • Visual Builder   Drag-and-drop agent flow designer, export to YAML
  • Run Inspector    Real-time execution tree, graph view, token usage
  • Agent Runner     Start runs directly from the browser
  • Debugger         Breakpoints, pause/resume, parameter overrides
  • Session History  Browse and replay completed runs

Hub API endpoints:
  POST /api/hub/register     CLI registers a run session
  POST /api/hub/deregister   CLI deregisters on completion
  GET  /api/hub/sessions     List active sessions
  POST /api/hub/ingest       CLI pushes batched events
  GET  /api/hub/commands     CLI polls for debug commands
  POST /api/hub/debug        UI sends debug commands to CLI
  GET  /events               SSE stream

Examples:
  rakitsu serve                          # Start on default port (9100)
  rakitsu serve --port 8080              # Custom port
  rakitsu serve --host 0.0.0.0           # Bind to all interfaces
  rakitsu serve --config tools.yaml --mcp-port 9200   # Enable MCP server`,
	Run: func(cmd *cobra.Command, args []string) {
		startServe()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 9100, "Port to run the hub on")
	serveCmd.Flags().StringVar(&serveHost, "host", "localhost", "Host to bind to (non-loopback requires RAKITSU_API_TOKEN — see docs/SECURITY.md)")
	serveCmd.Flags().IntVar(&mcpPort, "mcp-port", 0, "Start MCP HTTP server on this port (requires --config)")
	serveCmd.Flags().StringVar(&mcpConfig, "config", "", "YAML config for MCP server and A2A endpoint")
	serveCmd.Flags().StringVar(&serveConfigDir, "config-dir", "", "Extra directory to scan for agent configs in the UI")
}

func startServe() {
	// Refuse to expose the control plane to the network without an API token.
	// The hub can upload configs and start runs (both code-execution paths),
	// so an unauthenticated non-loopback bind is remote RCE.
	if err := server.RequireBindAllowed(serveHost); err != nil {
		log.Fatalf("rakitsu serve: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", serveHost, servePort)

	eventBus := telemetry.NewEventBus(1024)
	sseServer := server.NewSSEServer(eventBus, serveHost, servePort)
	sseServer.SetVersion(Version)
	sseServer.SetLicenseRequired(LicenseRequired == "true")

	// Phase 1 of multi-session debug redesign: a read-only registry that
	// indexes every live one-shot run and chat session by id. Used by the
	// debugger's session picker dropdown.
	sessionRegistry := session.NewRegistry()
	sseServer.SetSessionRegistry(sessionRegistry)

	// Session store — history browsing and run recording
	var sessionStore *store.SessionStore
	if ss, err := store.NewSessionStore(); err != nil {
		log.Printf("Warning: session history unavailable: %v (sessions won't be saved)", err)
	} else {
		sessionStore = ss
		sseServer.SetSessionStore(ss)
	}

	// Config store — scan for available YAML configs for the agent runner
	searchPaths := []string{".", "./examples", "./configs"}
	if serveConfigDir != "" {
		searchPaths = append([]string{serveConfigDir}, searchPaths...)
	}
	if configStore, err := server.NewConfigStore(searchPaths); err != nil {
		log.Printf("Warning: config store unavailable: %v (agent runner disabled)", err)
	} else {
		sseServer.SetConfigStore(configStore)
		defer configStore.Cleanup()

		// Configs here can arrive from the browser, so file: references may
		// only resolve inside the served config directories, the upload
		// directory, and the --config file's own directory.
		roots := configStore.FileRefRoots()
		if mcpConfig != "" {
			roots = append(roots, filepath.Dir(mcpConfig))
		}
		config.SetFileRefRoots(roots)

		// Agent runner — start runs from the browser
		runner := server.NewAgentRunner(eventBus, sessionStore, configStore, sseServer, server.RunFunc(executeConfig))
		sseServer.SetRunner(runner)

		// Chat manager — interactive chat sessions over WebSocket.
		chatMgr := server.NewChatManager(eventBus, configStore, sessionStore, chatBuildFunc)
		// Self-address for the send_message / list_sessions tools —
		// deliberately loopback, not serveHost, so the self-call works even
		// when binding 0.0.0.0.
		chatMgr.SetSelfURL(fmt.Sprintf("http://127.0.0.1:%d", servePort))
		// Wire the /model slash command: chat sessions need to build fresh
		// LLM clients mid-session, and createLLMProvider lives here in
		// cmd/rakitsu (it imports every provider sub-package). The factory
		// adapter forwards to createLLMProvider with the session's ctx+cfg.
		chatMgr.SetSlashLLMFactory(func(ctx context.Context, cfg *config.Config, providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error) {
			return createLLMProvider(ctx, cfg, providerName, model, mc)
		})
		sseServer.SetChatManager(chatMgr)
		defer chatMgr.StopAll()
	}

	mux := sseServer.Mux()

	// MCP + A2A (optional, requires --config)
	if mcpConfig != "" {
		serveCfg, err := config.Load(mcpConfig)
		if err != nil {
			log.Fatalf("rakitsu serve: cannot load config %q: %v", mcpConfig, err)
		}

		if mcpPort > 0 {
			mcpCtx := context.Background()
			registry := createToolRegistry(mcpCtx, serveCfg)
			mcpSrv := server.NewMCPServer(registry, Version)
			mcpHTTP := &http.Server{
				Addr:         fmt.Sprintf("%s:%d", serveHost, mcpPort),
				Handler:      server.MCPListenerHandler(serveHost, mcpSrv),
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 30 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			go func() {
				log.Printf("MCP server running at http://%s:%d/mcp", serveHost, mcpPort)
				if err := mcpHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Printf("MCP server error: %v", err)
				}
			}()
		}

		mux.Handle("/a2a", a2aHandlerFunc(serveCfg, func(ctx context.Context, cfg *config.Config, query string) (string, error) {
			return executeConfig(ctx, cfg, eventBus, nil, query, nil)
		}))
		mux.Handle("/.well-known/agent-card.json", agentCardHandlerFunc(serveCfg, fmt.Sprintf("http://%s/a2a", addr)))
		log.Printf("A2A endpoint ready at http://%s/a2a", addr)
		log.Printf("A2A agent card at http://%s/.well-known/agent-card.json", addr)
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      server.GuardMiddleware(serveHost, server.CorsMiddleware(server.AuthMiddleware(mux))),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Rakitsu hub running at http://%s", addr)
		log.Printf("Agent runner enabled — configs from: %v", searchPaths)
		log.Println("Press Ctrl+C to stop")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("rakitsu serve: %v — is port %d already in use?", err, servePort)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down hub...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Hub stopped")
}
