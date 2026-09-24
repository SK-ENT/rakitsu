package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/chat"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/secret"
	anthropicProvider "github.com/SK-ENT/rakitsu/internal/llm/anthropic"
	codexProvider "github.com/SK-ENT/rakitsu/internal/llm/codex"
	geminiProvider "github.com/SK-ENT/rakitsu/internal/llm/gemini"
	openaiProvider "github.com/SK-ENT/rakitsu/internal/llm/openai"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	a2atool "github.com/SK-ENT/rakitsu/internal/tools/a2a"
	clitool "github.com/SK-ENT/rakitsu/internal/tools/cli"
	fstool "github.com/SK-ENT/rakitsu/internal/tools/fs"
	jevtool "github.com/SK-ENT/rakitsu/internal/tools/jev"
	mcptool "github.com/SK-ENT/rakitsu/internal/tools/mcp"
	"github.com/SK-ENT/rakitsu/internal/tools/sessionmsg"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var runCmd = &cobra.Command{
	Use:   "run [config.yaml] [query]",
	Short: "Run an agent with the given configuration and query",
	Long: `Run an agent with the specified YAML configuration file and query.
The agent will execute using the ReAct loop, calling tools as needed.

Config is optional with --interactive: with no config.yaml given, a minimal
keyless (Ollama) chat config is created once at ~/.rakitsu/default-agent.yaml
and reused on every later configless --interactive run — edit that file
directly to add tools or switch provider. One-shot runs (no --interactive)
still need an explicit config, since a single remaining argument would be
ambiguous between a config path and a query.

By default, the CLI auto-connects to a running SSE hub (rakitsu serve) at
http://localhost:9100 to stream events for real-time monitoring. If no hub
is running, the agent runs standalone — no overhead, no errors.

Hub mode (default):
  Events are pushed to the hub in batches. The web UI at http://localhost:9100
  shows all active runs as cards. Click a run to inspect its execution tree
  and graph in real-time. Click "Attach Debugger" to enable breakpoints,
  pause/resume, and parameter overrides — like attaching gdb to a process.
  When the debugger is detached, the CLI returns to normal execution with
  zero debug overhead.

Standalone debug mode (--debug-port):
  Starts a dedicated SSE server with embedded web UI on the given port.
  Opens http://localhost:<port> for single-run debugging. Cannot be used
  with hub mode simultaneously.

Examples:
  rakitsu run agent.yaml "What pods are running?"
  rakitsu run --interactive
  rakitsu run agent.yaml --interactive
  rakitsu run examples/single/01-chat/config.yaml "Hello!"
  rakitsu run agent.yaml "Analyze errors" --trace
  rakitsu run agent.yaml "Debug issue" --verbose
  rakitsu run agent.yaml "Build app" --hub http://myhost:9100
  rakitsu run agent.yaml "Quick test" --no-hub
  rakitsu run agent.yaml "Debug" --debug-port 9200
  rakitsu run agent.yaml "Query" --provider litellm --model gpt-4o`,
	// Zero args is only valid combined with --interactive (falls back to the
	// auto-bootstrapped default config, see ensureDefaultConfig) — one-shot
	// mode still needs an explicit config, cobra has already parsed flags by
	// the time Args runs so interactiveFlag reflects the actual invocation.
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 && interactiveFlag {
			return nil
		}
		return cobra.MinimumNArgs(1)(cmd, args)
	},
	RunE: runAgent,
}

var (
	timeoutSeconds     int
	idleTimeoutSeconds int
	traceEnabled       bool
	debugPort          int
	hubURL             string
	noHub              bool
	resumeSessionID    string
	dryRun             bool
	maxCostFlag        float64
	embeddingProvider  string
	runWorkdir         string
	providerOverride   string
	modelOverride      string
	maxTokensOverride  int
	interactiveFlag    bool
	attachPaths        []string
)

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().IntVarP(&timeoutSeconds, "timeout", "t", defaultTimeoutSeconds, "total execution timeout in seconds, per turn in --interactive (unset or <=0 = no time limit, and agents without max_iterations then have no iteration cap; also via settings.execution.timeout_seconds)")
	runCmd.Flags().IntVar(&idleTimeoutSeconds, "idle-timeout", 0, "cancel the run after N seconds with no streaming activity (0=disabled); a slow-but-progressing model never trips this")
	runCmd.Flags().BoolVar(&traceEnabled, "trace", false, "show real-time agent execution trace on stderr")
	runCmd.Flags().IntVar(&debugPort, "debug-port", 0, "start debug SSE server on this port (e.g. 9100) for web UI inspector")
	runCmd.Flags().StringVar(&hubURL, "hub", "http://localhost:9100", "SSE hub URL for event streaming")
	runCmd.Flags().BoolVar(&noHub, "no-hub", false, "disable hub connection")
	runCmd.Flags().StringVar(&resumeSessionID, "resume", "", "resume a prior session by ID: replays its conversation into the model context (single agent) or resumes the pipeline checkpoint (orchestrator)")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "estimate cost without executing any LLM calls")
	runCmd.Flags().Float64Var(&maxCostFlag, "max-cost", 0, "abort run if total cost exceeds this USD amount (overrides config)")
	runCmd.Flags().StringVarP(&runWorkdir, "workdir", "w", "", "working directory for tool execution (default: current directory)")
	runCmd.Flags().StringVar(&embeddingProvider, "embedding-provider", "", "provider for context retrieval embeddings (e.g. openai, gemini, ollama); overrides config")
	runCmd.Flags().StringVar(&providerOverride, "provider", "", "override default provider for all agents (e.g. litellm, ollama, anthropic)")
	runCmd.Flags().StringVar(&modelOverride, "model", "", "override default model for all agents (e.g. gpt-4o, claude-sonnet-4-20250514)")
	runCmd.Flags().IntVar(&maxTokensOverride, "max-tokens", 0, "override max output tokens for all agents/orchestrators (0=leave config value); raise this for reasoning models like gpt-5-nano, whose hidden reasoning tokens share the same budget as visible output")
	runCmd.Flags().BoolVarP(&interactiveFlag, "interactive", "i", false, "run as interactive chat (overrides config interactive flag)")
	runCmd.Flags().StringArrayVar(&attachPaths, "attach", nil, "attach a local image file to the query (repeatable, e.g. --attach a.png --attach b.png); requires the resolved agent's `vision: true`")
}

// defaultTimeoutSeconds is the --timeout default: off. Long agent operations
// run until they finish unless the user sets a limit.
const defaultTimeoutSeconds = 0

// resolveTimeoutSeconds returns the effective total run timeout in seconds.
// Precedence: explicit --timeout flag > settings.execution.timeout_seconds >
// the --timeout default (off). A returned value <= 0 means "no timeout".
func resolveTimeoutSeconds(cmd *cobra.Command, cfg *config.Config) int {
	if cmd.Flags().Changed("timeout") {
		return timeoutSeconds
	}
	if cfg.Settings.Execution.TimeoutSeconds != 0 {
		return cfg.Settings.Execution.TimeoutSeconds
	}
	return timeoutSeconds
}

// resolveIdleSeconds returns the effective idle (inactivity) timeout in seconds.
// Precedence: explicit --idle-timeout flag > settings.execution.idle_timeout_seconds.
// 0 (or less) disables the idle watchdog.
func resolveIdleSeconds(cmd *cobra.Command, cfg *config.Config) int {
	if cmd.Flags().Changed("idle-timeout") {
		return idleTimeoutSeconds
	}
	return cfg.Settings.Execution.IdleTimeoutSeconds
}

// resolveAgentIterations fills max_iterations for every agent that left it
// unset: settings.execution.max_iterations when set (-1 = no cap), else no
// cap (-1) when the run has no timeout. With neither limit set, a run ends on
// its answer, a budget guard (max_cost / max_total_tokens), the idle timeout,
// or Ctrl+C. An agent's explicit max_iterations is always kept.
func resolveAgentIterations(cfg *config.Config, timeoutSec int) {
	value := cfg.Settings.Execution.MaxIterations
	if value == 0 {
		if timeoutSec > 0 {
			return // agent default (10) applies
		}
		value = -1
	}
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		if a.Settings == nil {
			a.Settings = &config.AgentSettings{}
		}
		if a.Settings.MaxIterations == 0 {
			a.Settings.MaxIterations = value
		}
	}
}

// contextWithOptionalTimeout returns a cancellable context. A non-positive
// timeoutSec applies no deadline, so the run is bounded only by explicit
// cancellation (Ctrl+C / signal / idle watchdog), max_iterations, or budget.
func contextWithOptionalTimeout(parent context.Context, timeoutSec int) (context.Context, context.CancelFunc) {
	if timeoutSec <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
}

// startIdleWatchdog cancels the run (and sets *fired) if no events arrive on the
// bus for `idle`. A non-positive idle, or a nil bus, disables it. The watchdog
// subscribes to the same fan-out bus the agent emits TOKEN_CHUNK /
// REASONING_CHUNK / tool events on, so any streaming activity resets the timer;
// only a genuinely stalled run trips it. It exits when ctx is done or the bus
// subscription closes. Requires a streaming provider to be useful.
func startIdleWatchdog(ctx context.Context, bus *telemetry.EventBus, idle time.Duration, cancel context.CancelFunc, fired *atomic.Bool) {
	if idle <= 0 || bus == nil {
		return
	}
	ch := bus.Subscribe()
	go func() {
		defer bus.Unsubscribe(ch)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				// Cancel first, then publish the flag, so an observer that sees
				// fired==true can rely on ctx already being cancelled.
				cancel()
				fmt.Fprintf(os.Stderr, "\nNo activity for %s — cancelling run (idle timeout).\n", idle)
				fired.Store(true)
				return
			}
		}
	}()
}

// defaultConfigPath returns the location of the auto-bootstrapped configless
// --interactive config. Same os.UserHomeDir()+filepath.Join(home, ".rakitsu",
// ...) pattern already used for the session store and memory dir (see
// internal/store/store.go, internal/memory/memory.go) — no new shared helper,
// just matching how this repo already does it in two other places.
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rakitsu", "default-agent.yaml"), nil
}

// ensureDefaultConfig writes a minimal, keyless (Ollama) chat config — with
// read-only fs tools scoped to the current directory, no shell/write access —
// to path if nothing exists there yet. Never overwrites an existing file —
// once created, it's a real file the user can edit (add tools, switch provider),
// not something regenerated on every run.
func ensureDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	const defaultConfigYAML = `# Auto-created by 'rakitsu run --interactive' the first time it ran with no
# config given. Edit freely — add tools, switch provider — this file is
# yours now and won't be regenerated. For something more capable to start
# from instead, see 'rakitsu scaffold --list' or 'rakitsu quickstart'.
#
# Runs keyless against local Ollama by default (https://ollama.com):
#   ollama pull llama3.1:8b
# Prefer a cloud model? Uncomment a provider under settings.providers below.

name: Default Assistant
version: "1.0"
description: Conversational assistant with read-only access to the current directory, including images.
interactive: true

settings:
  default_provider: ollama
  providers:
    ollama:
      type: ollama
      base_url: http://localhost:11434/v1
    # Alternatives — uncomment one and set default_provider + defaults.model above:
    # openai:
    #   type: openai
    #   api_key: ${OPENAI_API_KEY}
    # anthropic:
    #   type: anthropic
    #   api_key: ${ANTHROPIC_API_KEY}
    # litellm:
    #   type: litellm
    #   api_key: ${LITELLM_API_KEY:-not-needed}
    #   base_url: ${LITELLM_BASE_URL:-http://localhost:4000}
  defaults:
    model: llama3.1:8b
    temperature: 0.7
    max_tokens: 2048

tools:
  - name: list_files
    type: fs
    operation: list
    allowed_paths:
      - "."
    description: List files in a directory
    parameters:
      path: { type: string, description: "Directory path to list", required: true }

  - name: read_file
    type: fs
    operation: read
    allowed_paths:
      - "."
    description: Read file contents
    parameters:
      path: { type: string, description: "File path to read", required: true }

  - name: search_files
    type: fs
    operation: search
    allowed_paths:
      - "."
    description: Search for text patterns across files
    parameters:
      pattern: { type: string, description: "Search pattern (regex)", required: true }
      path: { type: string, description: "Directory to search in", required: true }

  # Lets the model see an image file (png/jpg/gif/webp). Needs a model that
  # accepts images (e.g. a vision model in Ollama, or most cloud models);
  # with a text-only model the agent gets a note instead and says so.
  - name: read_image
    type: fs
    operation: read_image
    allowed_paths:
      - "."
    description: Load an image file so you can see it
    parameters:
      path: { type: string, description: "Image file path", required: true }

agents:
  - name: Assistant
    role: worker
    system_prompt: |
      You are a helpful, friendly assistant. Answer questions clearly and concisely.
      If you don't know something, say so honestly. You have read-only access to
      files in the current directory via list_files, read_file, and search_files —
      use them when a question is about a local file; you cannot write, delete, or
      run commands. To look at an image file, call read_image; if its result says
      the image was omitted, tell the user you cannot see it with this model.
`
	return os.WriteFile(path, []byte(defaultConfigYAML), 0o644)
}

func runAgent(cmd *cobra.Command, args []string) (runErr error) {
	var configPath string
	query := ""
	if len(args) == 0 {
		// Only reachable when --interactive is set — runCmd.Args already
		// enforced that. Bootstraps once, reused (not regenerated) on every
		// later configless run so a user's edits to it stick.
		path, err := defaultConfigPath()
		if err != nil {
			return fmt.Errorf("cannot determine default config path: %w", err)
		}
		if err := ensureDefaultConfig(path); err != nil {
			return fmt.Errorf("cannot create default config at %s: %w", path, err)
		}
		fmt.Printf("No config given — using the default at %s (edit it directly, or run "+
			"'rakitsu scaffold'/'rakitsu quickstart' for something more capable).\n", path)
		configPath = path
	} else {
		configPath = args[0]
		if len(args) >= 2 {
			query = args[1]
		}
	}

	// Load configuration
	fmt.Printf("Loading configuration from %s...\n", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("cannot load config %q: %w", configPath, err)
	}

	fmt.Printf("Loaded: %s (v%s)\n", cfg.Name, cfg.Version)

	// Extend telemetry's credential-keyword redaction with any
	// operator-supplied keywords from this run's config, before any
	// agent starts and emits events. Deliberately called here (the
	// primary run entry point), not inside config.Load itself — Load is
	// also used to read secondary/nested configs (e.g. serve's MCP
	// sub-config), and letting every Load call silently overwrite this
	// process-global redaction state would let an incidental later load
	// wipe out keywords the main run config set.
	telemetry.SetExtraRedactKeywords(cfg.Settings.RedactKeywords)

	// CLI --interactive is tri-state: absent = honor YAML; present = authoritative.
	// This is the "force off" escape hatch: `rakitsu run cfg.yaml "q" --interactive=false`
	// runs one-shot even when cfg.yaml declares interactive: true (useful for scripted
	// invocations of a config designed for humans).
	if cmd.Flags().Changed("interactive") {
		cfg.Interactive = interactiveFlag
	}

	// Non-interactive configs require a query argument.
	if !cfg.Interactive && query == "" {
		return fmt.Errorf("query required for non-interactive runs (or set interactive: true in config)")
	}

	// Apply CLI provider/model overrides
	if providerOverride != "" {
		cfg.Settings.DefaultProvider = providerOverride
		// Inject provider definition from env vars if not already in config
		if _, ok := cfg.Settings.Providers[strings.ToLower(providerOverride)]; !ok {
			envPrefix := strings.ToUpper(providerOverride)
			// Well-known OpenAI-compatible vendor aliases: map vendor shorthand
			// to the `openai` provider type with a default base URL, so
			// `--provider nvidia` works without requiring NVIDIA_BASE_URL in env.
			providerType := strings.ToLower(providerOverride)
			defaultBaseURL := ""
			switch providerType {
			case "nvidia":
				providerType = "openai"
				defaultBaseURL = "https://integrate.api.nvidia.com/v1"
			}
			pd := config.ProviderDefinition{
				Type:   providerType,
				APIKey: os.Getenv(envPrefix + "_API_KEY"),
			}
			if baseURL := os.Getenv(envPrefix + "_BASE_URL"); baseURL != "" {
				pd.BaseURL = baseURL
			} else if defaultBaseURL != "" {
				pd.BaseURL = defaultBaseURL
			}
			if cfg.Settings.Providers == nil {
				cfg.Settings.Providers = make(map[string]config.ProviderDefinition)
			}
			cfg.Settings.Providers[strings.ToLower(providerOverride)] = pd
		}
		// Clobber explicit per-agent and per-orchestrator provider fields
		// so "override default provider for all agents" actually means ALL.
		for i := range cfg.Agents {
			cfg.Agents[i].Provider = providerOverride
			cfg.Agents[i].Providers = nil
		}
		if cfg.Orchestrator != nil {
			cfg.Orchestrator.Provider = providerOverride
		}
		for i := range cfg.Orchestrators {
			cfg.Orchestrators[i].Provider = providerOverride
		}
		fmt.Fprintf(os.Stderr, "Provider override: %s\n", providerOverride)
	}
	if modelOverride != "" {
		cfg.Settings.Defaults.Model = modelOverride
		// Clobber explicit per-agent and per-orchestrator model fields too.
		for i := range cfg.Agents {
			cfg.Agents[i].Model = modelOverride
		}
		if cfg.Orchestrator != nil {
			cfg.Orchestrator.Model = modelOverride
		}
		for i := range cfg.Orchestrators {
			cfg.Orchestrators[i].Model = modelOverride
		}
		fmt.Fprintf(os.Stderr, "Model override: %s\n", modelOverride)
	}
	if maxTokensOverride > 0 {
		cfg.Settings.Defaults.MaxTokens = maxTokensOverride
		// Unlike Model (a plain string field), MaxTokens lives inside the
		// nilable ModelConfig pointer on both AgentDefinition and
		// OrchestratorConfig — allocate it before setting the field so a
		// config with no model_config block still gets clobbered. Mirrors
		// the modelOverride clobber above.
		for i := range cfg.Agents {
			if cfg.Agents[i].ModelConfig == nil {
				cfg.Agents[i].ModelConfig = &config.ModelConfig{}
			}
			cfg.Agents[i].ModelConfig.MaxTokens = maxTokensOverride
		}
		if cfg.Orchestrator != nil {
			if cfg.Orchestrator.ModelConfig == nil {
				cfg.Orchestrator.ModelConfig = &config.ModelConfig{}
			}
			cfg.Orchestrator.ModelConfig.MaxTokens = maxTokensOverride
		}
		for i := range cfg.Orchestrators {
			if cfg.Orchestrators[i].ModelConfig == nil {
				cfg.Orchestrators[i].ModelConfig = &config.ModelConfig{}
			}
			cfg.Orchestrators[i].ModelConfig.MaxTokens = maxTokensOverride
		}
		fmt.Fprintf(os.Stderr, "Max tokens override: %d\n", maxTokensOverride)
	}

	// Apply workdir to all tools that don't have their own. Made absolute
	// first: tools anchor relative allowed_paths on an absolute working_dir
	// (so allowed_paths: ["."] means the workdir), but treat a relative one
	// as cwd-relative like every other relative path (SK-ENT/rakitsu#28).
	if runWorkdir != "" {
		if abs, err := filepath.Abs(runWorkdir); err == nil {
			runWorkdir = abs
		}
		for i := range cfg.Tools {
			if cfg.Tools[i].WorkingDir == "" {
				cfg.Tools[i].WorkingDir = runWorkdir
			}
		}
		for i := range cfg.Agents {
			for j := range cfg.Agents[i].ToolsInline {
				if cfg.Agents[i].ToolsInline[j].WorkingDir == "" {
					cfg.Agents[i].ToolsInline[j].WorkingDir = runWorkdir
				}
			}
		}
	}

	// --attach: local files only for this pass (no remote URL, no File API
	// upload). Loaded and validated here, before session/hub/event bus
	// setup, so a bad path or vision-gate rejection fails fast with no side
	// effects.
	var attachments []llm.ContentBlock
	if len(attachPaths) > 0 {
		if cfg.Interactive {
			return fmt.Errorf("--attach is not supported with --interactive yet — drop --interactive or attach without it")
		}
		if dryRun {
			return fmt.Errorf("--attach is not supported with --dry-run (image cost estimation is not implemented in this release)")
		}
		if cfg.Orchestrator != nil && len(cfg.Agents) > 0 {
			return fmt.Errorf("--attach is not supported with multi-agent/orchestrator configs yet — use a single-agent config")
		}
		if len(cfg.Agents) == 0 {
			return fmt.Errorf("no agents defined in %q — add at least one entry under the 'agents:' key", configPath)
		}
		// Same "use the first agent" resolution the single-agent execution
		// path below applies.
		visionAgent := &cfg.Agents[0]
		if visionAgent.Vision == nil || !*visionAgent.Vision {
			return fmt.Errorf("agent %q does not accept image input (set `vision: true` in its config) — cannot use --attach", visionAgent.Name)
		}
		for _, p := range attachPaths {
			block, err := llm.LoadImageAttachment(p)
			if err != nil {
				return fmt.Errorf("cannot attach %q: %w", p, err)
			}
			attachments = append(attachments, block)
		}
	}

	// Use config hub_url as default when --hub flag was not explicitly passed.
	// Applied before the interactive branch so the chat TUI's hub registration
	// (cross-session messaging) honors it too.
	if !cmd.Flags().Changed("hub") && cfg.Settings.HubURL != "" {
		hubURL = cfg.Settings.HubURL
	}

	timeoutSec := resolveTimeoutSeconds(cmd, cfg)
	resolveAgentIterations(cfg, timeoutSec)

	// Interactive mode: hand off to chat TUI (after provider/model/workdir overrides).
	if cfg.Interactive {
		// The TUI has no history-replay path yet — reject instead of
		// silently ignoring the flag and starting a fresh conversation.
		if resumeSessionID != "" {
			return fmt.Errorf("--resume is not supported with interactive chat yet — resume with history via the web UI (rakitsu serve → Sessions → Resume), or drop --interactive")
		}
		// The timeout applies to each chat turn, not to the whole session:
		// the session context also owns providers and MCP servers, which must
		// outlive any single turn's deadline.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var turnTimeout time.Duration
		if timeoutSec > 0 {
			turnTimeout = time.Duration(timeoutSec) * time.Second
		}
		return runInteractive(ctx, cfg, query, turnTimeout)
	}

	// Dry-run: estimate cost and exit before any LLM calls or session recording.
	if dryRun {
		return dryRunEstimate(cfg, query)
	}

	// Set up context with timeout and cancellation. A non-positive effective
	// timeout disables the deadline (run until completion / max_iterations /
	// budget / Ctrl+C) — see contextWithOptionalTimeout.
	ctx, cancel := contextWithOptionalTimeout(context.Background(), timeoutSec)
	defer cancel()

	// idleTimedOut is set by the idle watchdog (started after the event bus is
	// up) so the session-end defer can classify an inactivity cancellation as a
	// timeout rather than a generic error.
	var idleTimedOut atomic.Bool

	// Handle interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived interrupt signal, cancelling...")
		cancel()
	}()

	// Create event bus
	eventBus := telemetry.NewEventBus(1024)

	// Idle watchdog: cancel the run if no events stream for the configured idle
	// window. Subscribed to the same bus the agent emits TOKEN_CHUNK /
	// REASONING_CHUNK on, so a slow-but-progressing model keeps it alive and
	// only a genuinely stalled run trips it. No-op when idle <= 0.
	startIdleWatchdog(ctx, eventBus, time.Duration(resolveIdleSeconds(cmd, cfg))*time.Second, cancel, &idleTimedOut)

	// Start session persistence (always-on, failure-tolerant)
	var sessionStore *store.SessionStore
	if ss, err := store.NewSessionStore(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session persistence unavailable: %v\n", err)
	} else {
		sessionStore = ss
		agentNames := make([]string, len(cfg.Agents))
		for i, a := range cfg.Agents {
			agentNames[i] = a.Name
		}
		configYAML := ""
		if yamlBytes, err := yaml.Marshal(config.Redacted(cfg)); err == nil {
			configYAML = string(yamlBytes)
		}
		if err := sessionStore.StartSession(store.SessionMeta{
			Name:       cfg.Name,
			Query:      query,
			ConfigPath: configPath,
			ConfigYAML: configYAML,
			Agents:     agentNames,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not start session recording: %v\n", err)
			sessionStore = nil
		} else {
			fmt.Printf("Session ID: %s\n", sessionStore.CurrentSessionID())
		}
	}
	if sessionStore != nil {
		eventCh := eventBus.Subscribe()
		var subWG sync.WaitGroup
		subWG.Add(1)
		go func() {
			defer subWG.Done()
			for event := range eventCh {
				sessionStore.WriteEvent(event)
			}
		}()
		defer func() {
			// Drain the subscriber goroutine BEFORE closing the
			// session, otherwise trailing AGENT_END/EXECUTION_COMPLETE
			// events emitted by Agent.RunWithHistory's defer may arrive
			// at WriteEvent after EndSession has nil'd the file pointer
			// and get silently dropped (store.go:113 guards on file==nil).
			// Unsubscribe closes the channel; the goroutine drains any
			// buffered events and exits its range loop.
			eventBus.Unsubscribe(eventCh)
			subWG.Wait()

			status := store.SessionSuccess
			if runErr != nil {
				status = store.SessionError
			}
			if ctx.Err() == context.DeadlineExceeded || idleTimedOut.Load() {
				status = store.SessionTimeout
			}
			sessionStore.EndSession(status)
		}()
	}

	// Start console tracer if requested
	if traceEnabled {
		tracer := telemetry.NewConsoleTracer()
		tracer.Start(eventBus)
		defer tracer.Stop()
	}

	// Debug controller — only created when debug port is active
	var debugCtrl *debug.DebugController

	// Start debug SSE server if requested
	if debugPort > 0 {
		debugCtrl = debug.NewDebugController(eventBus)

		sseServer := server.NewSSEServer(eventBus, "localhost", debugPort)
		sseServer.SetDebugController(debugCtrl)
		if sessionStore != nil {
			sseServer.SetSessionStore(sessionStore)
		}

		// Collect agent names for session info
		agentNames := make([]string, len(cfg.Agents))
		for i, a := range cfg.Agents {
			agentNames[i] = a.Name
		}
		sseServer.SetSessionInfo(server.SessionInfo{
			Name:       cfg.Name,
			Query:      query,
			ConfigPath: configPath,
			Agents:     agentNames,
			StartTime:  time.Now().Format(time.RFC3339),
		})

		go func() {
			if err := sseServer.Start(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "Debug SSE server error: %v\n", err)
			}
		}()
		defer sseServer.Stop()

		// Give the server a moment to start
		time.Sleep(50 * time.Millisecond)
	}

	// Steerable one-shot: queue only ever created on a successful hub
	// registration (assigned below), so a nil channel here means "no hub" —
	// enqueue/hook/drain and the tool registration all no-op end to end.
	var steerCh chan chat.InboundSessionMsg

	// Pipeline-strategy orchestrator runs never build the ReAct supervisor
	// (they route through Pipeline steps, not agent.Orchestrator's ReAct
	// loop), so a steering drain hook has nowhere to inject — registering
	// accepts_messages:true for one would advertise a target that silently
	// discards every steer.
	steerable := cfg.Settings.SessionMsg.Enabled && !(cfg.Orchestrator != nil && cfg.Orchestrator.Strategy == "Pipeline")
	if cfg.Settings.SessionMsg.Enabled && !steerable {
		fmt.Fprintln(os.Stderr, "session_msg: Pipeline strategy runs cannot be steered mid-run — registering as non-messageable")
	}

	// Connect to hub for event streaming (unless explicitly disabled)
	var hubClient *telemetry.HubClient
	var hubSessionID string
	if !noHub {
		agentNames := make([]string, len(cfg.Agents))
		for i, a := range cfg.Agents {
			agentNames[i] = a.Name
		}
		hubSessionID = fmt.Sprintf("%d", time.Now().UnixNano())
		hc := telemetry.NewHubClient(hubURL, hubSessionID, eventBus)
		if err := hc.Register(cfg.Name, query, configPath, agentNames, "run", steerable); err != nil {
			fmt.Fprintf(os.Stderr, "Hub not available at %s, running standalone\n", hubURL)
		} else {
			hubClient = hc
			fmt.Fprintf(os.Stderr, "Connected to hub at %s (session %s)\n", hubURL, hubSessionID)

			// Queue only created when the config opts in AND is actually
			// steerable (settings.session_msg.enabled minus Pipeline runs)
			// AND the hub connection is live — must be set before the
			// OnCommand closure below, which captures it.
			if steerable {
				steerCh = make(chan chat.InboundSessionMsg, steerQueueCap)
			}

			// Handle debug commands from hub (runtime attach/detach)
			hubClient.OnCommand = func(action string, data map[string]interface{}) {
				switch action {
				case "debug_enable":
					if debugCtrl == nil {
						debugCtrl = debug.NewDebugController(eventBus)
						fmt.Fprintf(os.Stderr, "Debug attached from hub\n")
					}
				case "debug_disable":
					if debugCtrl != nil {
						debugCtrl.Resume(debug.ActionResume)
						debugCtrl = nil
						fmt.Fprintf(os.Stderr, "Debug detached from hub\n")
					}
				case "resume":
					if debugCtrl != nil {
						resumeAction := debug.ActionResume
						if a, ok := data["action"].(string); ok {
							resumeAction = debug.ResumeAction(a)
						}
						// Set run-until condition before resuming
						if resumeAction == debug.ActionRunUntil {
							cond := &debug.RunUntilCondition{}
							if v, ok := data["checkpoint"].(string); ok {
								cond.Checkpoint = v
							}
							if v, ok := data["agent_name"].(string); ok {
								cond.AgentName = v
							}
							if v, ok := data["tool_name"].(string); ok {
								cond.ToolName = v
							}
							if v, ok := data["iteration"].(float64); ok {
								cond.Iteration = int(v)
							}
							debugCtrl.SetRunUntilCondition(cond)
						}
						debugCtrl.Resume(resumeAction)
					}
				case "set_breakpoint":
					if debugCtrl != nil {
						bp := debug.BreakpointKey{}
						if et, ok := data["event_type"].(string); ok {
							bp.EventType = et
						}
						if an, ok := data["agent_name"].(string); ok {
							bp.AgentName = an
						}
						debugCtrl.SetBreakpoint(bp)
					}
				case "clear_breakpoint":
					if debugCtrl != nil {
						bp := debug.BreakpointKey{}
						if et, ok := data["event_type"].(string); ok {
							bp.EventType = et
						}
						if an, ok := data["agent_name"].(string); ok {
							bp.AgentName = an
						}
						debugCtrl.ClearBreakpoint(bp)
					}
				case "clear_all_breakpoints":
					if debugCtrl != nil {
						debugCtrl.ClearAllBreakpoints()
					}
				case "set_params":
					if debugCtrl != nil {
						agentName, _ := data["agent"].(string)
						if agentName != "" {
							ovr := &debug.ParamOverride{}
							if t, ok := data["temperature"].(float64); ok {
								ovr.Temperature = &t
							}
							if mt, ok := data["max_tokens"].(float64); ok {
								v := int(mt)
								ovr.MaxTokens = &v
							}
							if m, ok := data["model"].(string); ok {
								ovr.Model = &m
							}
							if s, ok := data["sticky"].(bool); ok {
								ovr.Sticky = s
							}
							debugCtrl.SetOverrides(agentName, ovr)
						}
					}
				case "clear_params":
					if debugCtrl != nil {
						agentName, _ := data["agent"].(string)
						if agentName != "" {
							debugCtrl.ClearOverrides(agentName)
						}
					}
				case "pause":
					if debugCtrl != nil {
						debugCtrl.RequestPause()
					}
				case "rerun_from_step":
					if debugCtrl != nil {
						stepName, _ := data["step_name"].(string)
						overrides, _ := data["overrides"].(map[string]interface{})
						debugCtrl.RerunFromStep(stepName, overrides)
					}
				case "session_message":
					if steerCh == nil {
						return
					}
					text, _ := data["text"].(string)
					if text == "" {
						return
					}
					fromID, _ := data["from_session_id"].(string)
					fromName, _ := data["from_name"].(string)
					enqueueSteer(steerCh, chat.InboundSessionMsg{FromSessionID: fromID, FromName: fromName, Text: text},
						func(old chat.InboundSessionMsg) {
							eventBus.Emit(rootRunnerName(cfg), telemetry.EventSessionMsgReceived, telemetry.SessionMsgReceivedPayload{
								FromSessionID: old.FromSessionID, FromName: old.FromName,
								Text: chat.TruncateSessionMsg(old.Text), Status: "dropped_overflow",
							})
						})
				}
			}

			hubClient.Start()
			defer hubClient.Stop("completed")
			defer drainSteerAtExit(steerCh, eventBus, rootRunnerName(cfg))
		}
	}

	// Create tool registry
	toolRegistry := createToolRegistry(ctx, cfg)
	defer toolRegistry.CloseAll()

	// Callback for late-attaching debug controller to running agents
	var liveAgents []*agent.Agent
	var liveAgentsMu sync.Mutex
	registerDebugAttach := func() {
		if hubClient != nil {
			origOnCommand := hubClient.OnCommand
			hubClient.OnCommand = func(action string, data map[string]interface{}) {
				// Call original handler first (creates/destroys debugCtrl)
				if origOnCommand != nil {
					origOnCommand(action, data)
				}
				// Propagate debug controller to all live agents
				if action == "debug_enable" || action == "debug_disable" {
					liveAgentsMu.Lock()
					for _, ag := range liveAgents {
						ag.SetDebugController(debugCtrl)
					}
					liveAgentsMu.Unlock()
				}
			}
		}
	}

	// Check if we have an orchestrator (multi-agent mode)
	if cfg.Orchestrator != nil && len(cfg.Agents) > 0 {
		// Standalone runs (failed/disabled hub registration) get no session
		// id or hub URL, so BuildRunner's sessionmsg gate stays off even if
		// the config opted in.
		mhURL, mhSessionID := hubURL, hubSessionID
		if hubClient == nil {
			mhURL, mhSessionID = "", ""
		}
		return runMultiAgent(ctx, cfg, toolRegistry, eventBus, debugCtrl, query, sessionStore, resumeSessionID,
			steerHook(steerCh, eventBus, rootRunnerName(cfg)), mhURL, mhSessionID,
			func(agents []*agent.Agent) {
				liveAgentsMu.Lock()
				liveAgents = agents
				liveAgentsMu.Unlock()
				registerDebugAttach()
			})
	}

	// Single agent mode
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("no agents defined in %q — add at least one entry under the 'agents:' key", configPath)
	}

	// Use the first agent
	agentDef := &cfg.Agents[0]

	// Resume: replay the parent session's user/assistant turns into
	// the model context. Loaded before any provider setup so a bad session ID
	// fails fast, and hard-erroring on missing history keeps the flag honest —
	// a silent fresh-context "resume" was the original bug.
	var priorHistory []llm.Message
	replayTurns := 0
	if resumeSessionID != "" {
		if sessionStore == nil {
			return fmt.Errorf("cannot resume: session store is unavailable")
		}
		h, err := server.HistoryForResume(sessionStore, resumeSessionID)
		if err != nil {
			return fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		priorHistory = h
		fmt.Printf("Resuming session %s — replaying %d prior message(s) into context\n",
			resumeSessionID, len(priorHistory))

		// Re-emit the replayed turns as CHAT_TURN events so this forked
		// session's own record is self-contained: resuming a resumed session
		// replays the whole chain, not just its last hop. AgentName carries
		// the session ID, mirroring ChatSession's turn events.
		sid := sessionStore.CurrentSessionID()
		for _, m := range priorHistory {
			switch m.Role {
			case "user":
				replayTurns++
				eventBus.Emit(sid, telemetry.EventChatTurnStart, telemetry.ChatTurnStartPayload{
					Turn: replayTurns, Text: m.AsText(),
				})
			case "assistant":
				if replayTurns == 0 {
					continue
				}
				eventBus.Emit(sid, telemetry.EventChatTurnEnd, telemetry.ChatTurnEndPayload{
					Turn: replayTurns, Final: m.AsText(),
				})
			}
		}
	}

	llmProvider, err := createAgentProvider(ctx, cfg, agentDef)
	if err != nil {
		return fmt.Errorf("cannot create LLM provider for agent %q: %w\n  hint: check your API key and provider settings in the config", agentDef.Name, err)
	}

	memStore := openMemoryStore(cfg)
	registerMemoryTools(toolRegistry, cfg, memStore, "", agentDef.Name, eventBus)
	rateLimiters := buildRateLimiters(cfg)
	if sb := newSpawnRuntime(ctx, cfg, eventBus, memStore, nil, rateLimiters, debugCtrl); sb != nil {
		defer sb.cleanupAll() // closes the global tool registry spawned children share (MCP subprocesses etc.)
		sb.registerSpawnTool(toolRegistry, agentDef.Name, 0)
	}
	ep := createEmbeddingProvider(ctx, cfg, agentRetrievalConfig(agentDef))
	ag := agent.NewAgent(agentDef, llmProvider, toolRegistry, eventBus, ep)
	if hook := steerHook(steerCh, eventBus, agentDef.Name); hook != nil {
		ag.SetSteering(hook)
	}
	if cfg.Settings.SessionMsg.Enabled && hubClient != nil {
		for _, t := range sessionmsg.NewTools(sessionmsg.Deps{
			HubURL:    hubURL,
			SessionID: hubSessionID,
			AgentName: agentDef.Name,
		}) {
			toolRegistry.RegisterTool(t)
		}
	}
	wireStorageClaimGuard(ag, memStore)
	wireAutoRecall(ag, cfg, memStore, "", agentDef.Name, eventBus)
	if debugCtrl != nil {
		ag.SetDebugController(debugCtrl)
	}
	wireAgentRetryAndRateLimit(ag, cfg, agentDef, rateLimiters)
	liveAgents = []*agent.Agent{ag}
	registerDebugAttach()

	// Print verbose info
	if verbose {
		fmt.Printf("\nAgent: %s\n", ag.GetName())
		fmt.Printf("Role: %s\n", ag.GetRole())
		fmt.Printf("Provider: %s\n", llmProvider.GetName())
		fmt.Printf("Model: %s\n", llmProvider.GetModel())
		fmt.Printf("Query: %s\n\n", query)
	}

	// Run the agent
	fmt.Println("Executing agent...")
	startTime := time.Now()

	// A resumed run wraps its own turn in CHAT_TURN events too, completing the
	// self-contained record started by the replay above. Plain (non-resume)
	// runs keep their historical event shape untouched.
	if resumeSessionID != "" && sessionStore != nil {
		eventBus.Emit(sessionStore.CurrentSessionID(), telemetry.EventChatTurnStart,
			telemetry.ChatTurnStartPayload{Turn: replayTurns + 1, Text: query})
	}
	var result string
	if len(attachments) > 0 {
		for i, block := range attachments {
			sizeBytes, _ := block.Metadata["size_bytes"].(int64)
			eventBus.Emit(agentDef.Name, telemetry.EventMediaAttached, telemetry.MediaAttachedPayload{
				Modality:  string(block.Type),
				Via:       "cli_attach",
				Path:      attachPaths[i],
				SizeBytes: sizeBytes,
				MIMEType:  block.MIMEType,
			})
		}
		result, err = ag.RunWithAttachments(ctx, query, priorHistory, attachments)
	} else {
		result, err = ag.RunWithHistory(ctx, query, priorHistory)
	}
	if resumeSessionID != "" && sessionStore != nil {
		endPayload := telemetry.ChatTurnEndPayload{Turn: replayTurns + 1, Final: result}
		if err != nil {
			endPayload.Interrupted = true
			endPayload.Err = err.Error()
		}
		eventBus.Emit(sessionStore.CurrentSessionID(), telemetry.EventChatTurnEnd, endPayload)
	}
	if err != nil {
		return fmt.Errorf("agent %q execution failed: %w", agentDef.Name, err)
	}

	duration := time.Since(startTime)

	// Print result
	fmt.Printf("\n%s\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Result (took %v):\n", duration.Round(time.Millisecond))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println(result)
	fmt.Printf("%s\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

// runMultiAgent runs the orchestrator with multiple agents
func runMultiAgent(
	ctx context.Context,
	cfg *config.Config,
	globalToolRegistry *tools.ToolRegistry,
	eventBus *telemetry.EventBus,
	debugCtrl *debug.DebugController,
	query string,
	sessionStore *store.SessionStore,
	resumeID string,
	steering func() []llm.Message,
	hubURL, hubSessionID string,
	onAgentsReady func([]*agent.Agent),
) error {
	fmt.Println("\nMulti-agent mode enabled")

	br, err := BuildRunner(ctx, cfg, eventBus, BuildOptions{
		DebugCtrl:       debugCtrl,
		SessionID:       hubSessionID,
		HubURL:          hubURL,
		MaxCostOverride: maxCostFlag,
		OnAgentsReady:   onAgentsReady,
	})
	if err != nil {
		return err
	}
	defer br.Cleanup()

	if br.RootOrch == nil {
		return fmt.Errorf("runMultiAgent requires a config with an orchestrator")
	}
	orch := br.RootOrch
	if steering != nil {
		orch.SetSteering(steering)
	}
	_ = globalToolRegistry // no longer needed — per-agent registries built by BuildRunner

	if verbose {
		for name, ag := range br.Agents {
			fmt.Printf("  - Created agent: %s (tools: %v)\n", name, ag.GetTools())
		}
	}

	// Checkpoint: always register writer when store is available
	if sessionStore != nil {
		sessionID := sessionStore.CurrentSessionID()
		orch.SetCheckpointWriter(func(data agent.CheckpointData) {
			data.SessionID = sessionID
			sd := store.StoreCheckpointData{
				SessionID:      data.SessionID,
				Query:          data.Query,
				CompletedSteps: data.CompletedSteps,
				Results:        make(map[string]store.StoreStepResult, len(data.Results)),
			}
			for k, v := range data.Results {
				sd.Results[k] = store.StoreStepResult{
					Name:       v.Name,
					Output:     v.Output,
					DurationNs: int64(v.Duration),
				}
			}
			_ = sessionStore.WriteCheckpoint(sd)
		})
	}

	// Resume: load prior checkpoint and inject into orchestrator
	if resumeID != "" {
		if sessionStore == nil {
			return fmt.Errorf("cannot resume: session store is unavailable")
		}
		cp, err := sessionStore.LoadCheckpoint(resumeID)
		if err != nil {
			return fmt.Errorf("cannot resume session %s: %w", resumeID, err)
		}
		agentCP := &agent.CheckpointData{
			SessionID:      cp.SessionID,
			Query:          cp.Query,
			CompletedSteps: cp.CompletedSteps,
			Results:        make(map[string]agent.CheckpointStep, len(cp.Results)),
		}
		for k, v := range cp.Results {
			agentCP.Results[k] = agent.CheckpointStep{
				Name:     v.Name,
				Output:   v.Output,
				Duration: time.Duration(v.DurationNs),
			}
		}
		orch.SetCheckpoint(agentCP)
		fmt.Printf("Resuming from checkpoint: %d step(s) already completed — %v\n",
			len(cp.CompletedSteps), cp.CompletedSteps)
	}

	// Print verbose info
	if verbose {
		fmt.Printf("\nOrchestrator: %s\n", cfg.Orchestrator.Name)
		fmt.Printf("Strategy: %s\n", cfg.Orchestrator.Strategy)
		fmt.Printf("Workers: %v\n", cfg.Orchestrator.Agents)
		fmt.Printf("Query: %s\n\n", query)
	}

	// Run the orchestrator
	fmt.Println("Executing orchestrator...")
	startTime := time.Now()

	result, err := orch.Run(ctx, query)
	if err != nil {
		return fmt.Errorf("orchestrator %q execution failed: %w", cfg.Orchestrator.Name, err)
	}

	duration := time.Since(startTime)

	// Print result
	fmt.Printf("\n%s\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Result (took %v):\n", duration.Round(time.Millisecond))
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Println(result)
	fmt.Printf("%s\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

// createLLMProvider creates an LLM provider based on the provider name and config.
// Provider resolution: explicit providerName → settings.default_provider → "openai".
// Model resolution: explicit model → provider.default_model → settings.defaults.model → "gpt-4o-mini".
// Named providers (settings.providers map) are resolved to their underlying type.
func createLLMProvider(ctx context.Context, cfg *config.Config, providerName, model string, modelConfig *config.ModelConfig) (llm.LLMProvider, error) {
	if providerName == "" {
		providerName = cfg.Settings.DefaultProvider
	}
	if providerName == "" {
		providerName = "openai"
	}

	if model == "" {
		model = cfg.GetDefaultModel(providerName)
	}
	if model == "" {
		model = cfg.Settings.Defaults.Model
	}

	// Resolve the underlying provider type (e.g., "litellm-fast" → "openai")
	providerType := strings.ToLower(cfg.GetProviderType(providerName))

	// The codex provider reads its model from ~/.codex/config.toml when none
	// is configured; the OpenAI-flavoured fallback would be rejected there.
	if model == "" && providerType != "codex" {
		model = "gpt-4o-mini"
	}

	pc := &llm.ProviderConfig{
		APIKey:          secret.New(cfg.GetAPIKey(providerName)),
		Model:           model,
		BaseURL:         cfg.GetBaseURL(providerName),
		CredentialsFile: cfg.GetCredentialsFile(providerName),
		Location:        cfg.GetLocation(providerName),
		Project:         cfg.GetProject(providerName),
		ResponseFormat:  cfg.GetResponseFormat(providerName),
		ReasoningEffort: cfg.GetReasoningEffort(providerName),
		Temperature:     cfg.Settings.Defaults.Temperature,
		MaxTokens:       cfg.Settings.Defaults.MaxTokens,
	}

	// Apply per-agent model config overrides
	if modelConfig != nil {
		if modelConfig.Temperature > 0 {
			pc.Temperature = modelConfig.Temperature
		}
		if modelConfig.MaxTokens > 0 {
			pc.MaxTokens = modelConfig.MaxTokens
		}
		if modelConfig.TopP > 0 {
			pc.TopP = modelConfig.TopP
		}
		if modelConfig.TimeoutSec > 0 {
			pc.TimeoutSec = modelConfig.TimeoutSec
		}
		if modelConfig.MaxThinkingTokens > 0 {
			pc.MaxThinkingTokens = modelConfig.MaxThinkingTokens
		}
		if modelConfig.ReasoningEffort != "" {
			pc.ReasoningEffort = modelConfig.ReasoningEffort
		}
	}

	// Ollama defaults — reuses OpenAI-compatible provider
	if providerType == "ollama" {
		if pc.APIKey.IsEmpty() {
			pc.APIKey = secret.New("ollama")
		}
		if pc.BaseURL == "" {
			pc.BaseURL = "http://localhost:11434/v1"
		}
	}

	// LiteLLM proxies manage their own API keys — skip validation
	if providerType == "litellm" && pc.APIKey.IsEmpty() {
		pc.APIKey = secret.New("not-needed") // placeholder for OpenAI client
	}

	// Validate API key (except for ollama, gemini, litellm, and codex — codex uses the ChatGPT login)
	if providerType != "ollama" && providerType != "gemini" && providerType != "litellm" && providerType != "codex" && pc.APIKey.IsEmpty() {
		envVar := strings.ToUpper(providerName) + "_API_KEY"
		return nil, fmt.Errorf("%s API key not set — export %s or add api_key to settings.providers.%s", providerName, envVar, providerName)
	}

	// Check plugin registry first (by instance name, then by type)
	if factory, ok := GetProviderFactory(providerName); ok {
		return factory(ctx, pc)
	}
	if providerType != providerName {
		if factory, ok := GetProviderFactory(providerType); ok {
			return factory(ctx, pc)
		}
	}

	// Built-in providers (resolved by type)
	switch providerType {
	case "openai", "ollama", "litellm":
		return openaiProvider.NewProvider(pc), nil
	case "anthropic":
		return anthropicProvider.NewProvider(pc), nil
	case "gemini":
		return geminiProvider.NewProvider(ctx, pc)
	case "codex":
		return codexProvider.NewProvider(pc)
	default:
		return nil, fmt.Errorf("unknown provider type %q for %q — supported: openai, anthropic, gemini, codex, ollama, litellm", providerType, providerName)
	}
}

// createEmbeddingProvider resolves an llm.EmbeddingProvider from config.
// rc is the agent-level RetrievalConfig (EmbeddingProvider, EmbeddingModel, EmbeddingURL).
// Resolution order:
//  1. --embedding-provider CLI flag (overrides all)
//  2. rc.EmbeddingProvider from agent config
//  3. cfg.Settings.DefaultProvider
//  4. Auto-detect Ollama at localhost:11434 (or rc.EmbeddingURL)
//  5. nil → BM25 fallback in NewRetriever
func createEmbeddingProvider(ctx context.Context, cfg *config.Config, rc config.RetrievalConfig) llm.EmbeddingProvider {
	// CLI flag overrides config
	explicitProvider := embeddingProvider
	if explicitProvider == "" {
		explicitProvider = rc.EmbeddingProvider
	}

	resolve := func(providerName, model string) llm.EmbeddingProvider {
		providerType := cfg.GetProviderType(providerName)
		pc := &llm.ProviderConfig{
			APIKey:          secret.New(cfg.GetAPIKey(providerName)),
			BaseURL:         cfg.GetBaseURL(providerName),
			CredentialsFile: cfg.GetCredentialsFile(providerName),
			Location:        cfg.GetLocation(providerName),
			Project:         cfg.GetProject(providerName),
			Model:           model,
		}
		switch providerType {
		case "openai", "ollama", "litellm", "":
			if providerType == "ollama" {
				if pc.APIKey.IsEmpty() {
					pc.APIKey = secret.New("ollama")
				}
				if pc.BaseURL == "" {
					pc.BaseURL = "http://localhost:11434/v1"
				}
			}
			return openaiProvider.NewEmbeddingClient(pc)
		case "gemini":
			ep, err := geminiProvider.NewEmbeddingClient(ctx, pc)
			if err != nil {
				return nil
			}
			return ep
		case "anthropic":
			return nil // no embedding API
		default:
			return nil
		}
	}

	// 1+2. Explicit provider (flag or config)
	if explicitProvider != "" {
		if ep := resolve(explicitProvider, rc.EmbeddingModel); ep != nil {
			return ep
		}
	}

	// 3. Default LLM provider
	if cfg.Settings.DefaultProvider != "" {
		if ep := resolve(cfg.Settings.DefaultProvider, rc.EmbeddingModel); ep != nil {
			return ep
		}
	}

	// 4. Auto-detect Ollama
	ollamaBase := rc.EmbeddingURL
	if ollamaBase == "" {
		ollamaBase = "http://localhost:11434"
	}
	if isOllamaRunning(ollamaBase) {
		model := rc.EmbeddingModel
		if model == "" {
			model = "nomic-embed-text"
		}
		return openaiProvider.NewEmbeddingClient(&llm.ProviderConfig{
			APIKey:  secret.New("ollama"),
			BaseURL: ollamaBase + "/v1",
			Model:   model,
		})
	}

	return nil // BM25 fallback
}

// agentRetrievalConfig safely returns the RetrievalConfig for an agent definition.
func agentRetrievalConfig(def *config.AgentDefinition) config.RetrievalConfig {
	if def.Settings != nil {
		return def.Settings.Context.Retrieval
	}
	return config.RetrievalConfig{}
}

// isOllamaRunning checks if an Ollama server is reachable at baseURL.
func isOllamaRunning(baseURL string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// createAgentProvider builds an LLMProvider for an AgentDefinition.
// When the agent has Providers (fallback chain), it creates a FallbackProvider;
// otherwise it falls back to the single Provider field.
func createAgentProvider(ctx context.Context, cfg *config.Config, def *config.AgentDefinition) (llm.LLMProvider, error) {
	if len(def.Providers) <= 1 {
		providerName := def.Provider
		model := def.Model
		if len(def.Providers) == 1 {
			providerName = def.Providers[0].Name
			if def.Providers[0].Model != "" {
				model = def.Providers[0].Model
			}
		}
		return createLLMProvider(ctx, cfg, providerName, model, def.ModelConfig)
	}
	providers := make([]llm.LLMProvider, 0, len(def.Providers))
	for _, entry := range def.Providers {
		model := def.Model // agent-level default
		if entry.Model != "" {
			model = entry.Model // per-entry override
		}
		p, err := createLLMProvider(ctx, cfg, entry.Name, model, def.ModelConfig)
		if err != nil {
			return nil, fmt.Errorf("cannot create fallback provider %q: %w", entry.Name, err)
		}
		providers = append(providers, p)
	}
	return llm.NewFallbackProvider(providers), nil
}

// ProviderFactory is a function that creates an LLM provider from config.
// External plugins implement this to register custom providers.
type ProviderFactory func(ctx context.Context, config *llm.ProviderConfig) (llm.LLMProvider, error)

var providerFactories = map[string]ProviderFactory{}

// RegisterProviderFactory registers a custom LLM provider factory.
// Use this to add support for providers not built into rakitsu.
func RegisterProviderFactory(name string, factory ProviderFactory) {
	providerFactories[name] = factory
}

// GetProviderFactory returns a registered provider factory, if any.
func GetProviderFactory(name string) (ProviderFactory, bool) {
	f, ok := providerFactories[name]
	return f, ok
}

// executeConfig creates agents/orchestrator from config and runs the query.
// This is the core execution logic shared between CLI (runAgent) and web runner.
// executeConfig runs a config one-shot. It is the single non-interactive
// entry point used by the web AgentRunner, A2A handler, and MCP server.
// Chat mode (runInteractive / ChatHost / user_input tool) is deliberately
// absent here — cfg.Interactive is ignored. If you ever need to honor it,
// audit every caller: interactive semantics don't make sense in RPC/web-run
// contexts where no human is attached to the process.
func executeConfig(ctx context.Context, cfg *config.Config, eventBus *telemetry.EventBus, debugCtrl *debug.DebugController, query string, registrar func([]debug.Attachable)) (string, error) {
	toolRegistry := createToolRegistry(ctx, cfg)
	defer toolRegistry.CloseAll()

	memStore := openMemoryStore(cfg)

	// Create root guard from global execution settings
	var rootGuard *agent.CompositeGuard
	if cfg.Settings.Execution.MaxTotalTokens > 0 || cfg.Settings.Execution.MaxCost > 0 {
		rootTG := agent.NewTokenGuard(cfg.Settings.Execution.MaxTotalTokens, cfg.Settings.Execution.MaxCost)
		rootGuard = agent.NewCompositeGuard(nil, rootTG)
	}
	// Note: executeConfig is called from the web runner and does not have access to CLI flags,
	// so --max-cost is not applied here. Use settings.execution.max_cost in YAML instead.
	rateLimiters := buildRateLimiters(cfg)

	// spawn_agent wiring for this run (nil when settings.spawn is disabled). Shares
	// this run's rootGuard/rateLimiters/debugCtrl so spawned children fall under the
	// same budget cap, rate limits, and debugger visibility as top-level agents.
	spawnRT := newSpawnRuntime(ctx, cfg, eventBus, memStore, rootGuard, rateLimiters, debugCtrl)
	if spawnRT != nil {
		defer spawnRT.cleanupAll() // closes the global tool registry spawned children share (MCP subprocesses etc.)
	}

	if cfg.Orchestrator != nil && len(cfg.Agents) > 0 {
		agents := make(map[string]*agent.Agent)
		// Each agent below gets its own registry (separate from the shared
		// toolRegistry above) so per-agent tool sets don't collide. Only the
		// shared one is deferred at the top of this function — close these
		// too, or MCP subprocesses/connections registered on them leak on
		// every multi-agent run.
		var agentToolRegistries []*tools.ToolRegistry
		defer func() {
			for _, r := range agentToolRegistries {
				r.CloseAll()
			}
		}()
		for i := range cfg.Agents {
			agentDef := &cfg.Agents[i]
			agentToolRegistry := tools.NewToolRegistry()
			agentToolRegistries = append(agentToolRegistries, agentToolRegistry)
			for _, toolName := range agentDef.Tools {
				if tool := toolRegistry.GetTool(toolName); tool != nil {
					agentToolRegistry.RegisterTool(tool)
				}
			}
			for _, inlineTool := range agentDef.ToolsInline {
				switch inlineTool.Type {
				case "cli":
					t := clitool.NewTool(&inlineTool, cfg.Settings.AllowedCommands)
					agentToolRegistry.RegisterTool(t)
				case "fs":
					t := fstool.NewTool(&inlineTool)
					agentToolRegistry.RegisterTool(t)
				case "jev":
					t := jevtool.NewTool(&inlineTool)
					agentToolRegistry.RegisterTool(t)
				case "mcp_server":
					mcpTools, closer, err := mcptool.NewMCPServer(ctx, &inlineTool)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: MCP server %q init failed: %v\n", inlineTool.Name, err)
						continue
					}
					for _, t := range mcpTools {
						agentToolRegistry.RegisterTool(t)
					}
					agentToolRegistry.AddCloser(closer)
				case "a2a":
					t, err := a2atool.NewA2ATool(&inlineTool)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: A2A tool %q init failed: %v\n", inlineTool.Name, err)
						continue
					}
					agentToolRegistry.RegisterTool(t)
				}
			}
			registerMemoryTools(agentToolRegistry, cfg, memStore, "", agentDef.Name, eventBus)
			if spawnRT != nil {
				spawnRT.registerSpawnTool(agentToolRegistry, agentDef.Name, 0)
			}
			agentProvider, err := createAgentProvider(ctx, cfg, agentDef)
			if err != nil {
				return "", fmt.Errorf("cannot create provider for agent %q: %w\n  hint: verify the provider is defined under settings.providers", agentDef.Name, err)
			}
			agentEP := createEmbeddingProvider(ctx, cfg, agentRetrievalConfig(agentDef))
			ag := agent.NewAgent(agentDef, agentProvider, agentToolRegistry, eventBus, agentEP)
			wireStorageClaimGuard(ag, memStore)
			wireAutoRecall(ag, cfg, memStore, "", agentDef.Name, eventBus)
			if debugCtrl != nil {
				ag.SetDebugController(debugCtrl)
			}
			// Wire guard
			var agMaxTok int
			var agMaxCost float64
			if agentDef.Settings != nil {
				agMaxTok = agentDef.Settings.MaxTotalTokens
				agMaxCost = agentDef.Settings.MaxCost
			}
			if agMaxTok > 0 || agMaxCost > 0 || rootGuard != nil {
				tg := agent.NewTokenGuard(agMaxTok, agMaxCost)
				ag.SetTokenGuard(tg)
				ag.SetGuard(agent.NewCompositeGuard(rootGuard, tg))
				pricing, pricingKnown := agent.ResolvePricing(agentProvider.GetModel(), cfg.Settings.Pricing)
				ag.SetPricing(pricing, pricingKnown)
			}
			wireAgentRetryAndRateLimit(ag, cfg, agentDef, rateLimiters)
			agents[agentDef.Name] = ag
		}

		// Build runners map: agents + sub-orchestrators
		runners := make(map[string]agent.Runner, len(agents)+len(cfg.Orchestrators))
		for name, ag := range agents {
			runners[name] = ag
		}
		for i := range cfg.Orchestrators {
			subCfg := &cfg.Orchestrators[i]
			subRunners := make(map[string]agent.Runner)
			for _, name := range subCfg.Agents {
				if r, ok := runners[name]; ok {
					subRunners[name] = r
				}
			}
			subMC := subCfg.ModelConfig
			if subMC == nil {
				subMC = &config.ModelConfig{Temperature: 0.3}
			}
			subProv, err := createLLMProvider(ctx, cfg, subCfg.Provider, subCfg.Model, subMC)
			if err != nil {
				return "", fmt.Errorf("cannot create provider for sub-orchestrator %q: %w\n  hint: verify the provider is defined under settings.providers", subCfg.Name, err)
			}
			subOrch := agent.NewOrchestrator(subCfg, subProv, eventBus, subRunners)
			if debugCtrl != nil {
				subOrch.SetDebugController(debugCtrl)
			}
			if rootGuard != nil {
				subPricing, subPricingKnown := agent.ResolvePricing(subProv.GetModel(), cfg.Settings.Pricing)
				subOrch.SetRootGuard(rootGuard, subPricing, subPricingKnown)
			}
			runners[subCfg.Name] = subOrch
		}

		// Auto-include agents and sub-orchestrators (same logic as runMultiAgent)
		{
			claimed := make(map[string]bool)
			for _, name := range cfg.Orchestrator.Agents {
				claimed[name] = true
			}
			for _, sub := range cfg.Orchestrators {
				for _, name := range sub.Agents {
					claimed[name] = true
				}
			}
			existing := make(map[string]bool, len(cfg.Orchestrator.Agents))
			for _, name := range cfg.Orchestrator.Agents {
				existing[name] = true
			}
			for _, sub := range cfg.Orchestrators {
				if !existing[sub.Name] {
					cfg.Orchestrator.Agents = append(cfg.Orchestrator.Agents, sub.Name)
					existing[sub.Name] = true
				}
			}
			for _, ag := range cfg.Agents {
				if !claimed[ag.Name] && !existing[ag.Name] {
					cfg.Orchestrator.Agents = append(cfg.Orchestrator.Agents, ag.Name)
					existing[ag.Name] = true
				}
			}
		}

		orchModelConfig := cfg.Orchestrator.ModelConfig
		if orchModelConfig == nil {
			orchModelConfig = &config.ModelConfig{Temperature: 0.3}
		}
		orchProvider, err := createLLMProvider(ctx, cfg, cfg.Orchestrator.Provider, cfg.Orchestrator.Model, orchModelConfig)
		if err != nil {
			return "", fmt.Errorf("cannot create orchestrator provider %q: %w\n  hint: verify the provider is defined under settings.providers", cfg.Orchestrator.Provider, err)
		}
		orch := agent.NewOrchestrator(cfg.Orchestrator, orchProvider, eventBus, runners)
		if debugCtrl != nil {
			orch.SetDebugController(debugCtrl)
		}
		if rootGuard != nil {
			orchPricing, orchPricingKnown := agent.ResolvePricing(orchProvider.GetModel(), cfg.Settings.Pricing)
			orch.SetRootGuard(rootGuard, orchPricing, orchPricingKnown)
		}
		// Register all agents/orchestrators for mid-run debug attach
		if registrar != nil {
			attachables := make([]debug.Attachable, 0, len(agents)+len(cfg.Orchestrators)+1)
			for _, ag := range agents {
				attachables = append(attachables, ag)
			}
			for _, name := range runners {
				if o, ok := name.(*agent.Orchestrator); ok {
					attachables = append(attachables, o)
				}
			}
			attachables = append(attachables, orch)
			registrar(attachables)
		}
		return orch.Run(ctx, query)
	}

	if len(cfg.Agents) == 0 {
		return "", fmt.Errorf("no agents defined in configuration — add at least one entry under the 'agents:' key")
	}

	agentDef := &cfg.Agents[0]
	provider, err := createAgentProvider(ctx, cfg, agentDef)
	if err != nil {
		return "", fmt.Errorf("cannot create LLM provider for agent %q: %w", agentDef.Name, err)
	}
	registerMemoryTools(toolRegistry, cfg, memStore, "", agentDef.Name, eventBus)
	if spawnRT != nil {
		spawnRT.registerSpawnTool(toolRegistry, agentDef.Name, 0)
	}
	ep := createEmbeddingProvider(ctx, cfg, agentRetrievalConfig(agentDef))
	ag := agent.NewAgent(agentDef, provider, toolRegistry, eventBus, ep)
	wireStorageClaimGuard(ag, memStore)
	wireAutoRecall(ag, cfg, memStore, "", agentDef.Name, eventBus)
	if debugCtrl != nil {
		ag.SetDebugController(debugCtrl)
	}
	// Wire guard for single-agent mode
	var agMaxTok int
	var agMaxCost float64
	if agentDef.Settings != nil {
		agMaxTok = agentDef.Settings.MaxTotalTokens
		agMaxCost = agentDef.Settings.MaxCost
	}
	if agMaxTok > 0 || agMaxCost > 0 || rootGuard != nil {
		tg := agent.NewTokenGuard(agMaxTok, agMaxCost)
		ag.SetTokenGuard(tg)
		ag.SetGuard(agent.NewCompositeGuard(rootGuard, tg))
		pricing, pricingKnown := agent.ResolvePricing(provider.GetModel(), cfg.Settings.Pricing)
		ag.SetPricing(pricing, pricingKnown)
	}
	wireAgentRetryAndRateLimit(ag, cfg, agentDef, rateLimiters)
	if registrar != nil {
		registrar([]debug.Attachable{ag})
	}
	return ag.Run(ctx, query)
}

// createToolRegistry creates and populates a tool registry from configuration
func createToolRegistry(ctx context.Context, cfg *config.Config) *tools.ToolRegistry {
	registry := tools.NewToolRegistry()

	// Register tools from configuration
	for _, toolDef := range cfg.Tools {
		switch toolDef.Type {
		case "cli":
			tool := clitool.NewTool(&toolDef, cfg.Settings.AllowedCommands)
			registry.RegisterTool(tool)
		case "fs":
			tool := fstool.NewTool(&toolDef)
			registry.RegisterTool(tool)
		case "jev":
			tool := jevtool.NewTool(&toolDef)
			registry.RegisterTool(tool)
		case "mcp_server":
			mcpTools, closer, err := mcptool.NewMCPServer(ctx, &toolDef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: MCP server %q init failed: %v\n", toolDef.Name, err)
				continue
			}
			for _, t := range mcpTools {
				registry.RegisterTool(t)
			}
			registry.AddCloser(closer)
		case "a2a":
			t, err := a2atool.NewA2ATool(&toolDef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: A2A tool %q init failed: %v\n", toolDef.Name, err)
				continue
			}
			registry.RegisterTool(t)
		}
	}

	return registry
}

// dryRunEstimate prints a per-agent cost estimate and exits without calling any LLM.
// It uses a character-based token heuristic (1 token ≈ 4 chars) and the same
// ResolvePricing() used at runtime, so user pricing overrides are respected.
func dryRunEstimate(cfg *config.Config, query string) error {
	fmt.Println("Dry-run cost estimate (no LLM calls made)")
	fmt.Println(strings.Repeat("─", 64))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tMODEL\tESTIM. INPUT TOKENS\tESTIM. COST (USD)")

	var totalTokens int
	var totalCost float64

	for i := range cfg.Agents {
		agentDef := &cfg.Agents[i]

		model := agentDef.Model
		if model == "" {
			model = cfg.Settings.Defaults.Model
		}

		// Estimate input: system prompt + query + ~200 tokens per tool definition
		inputText := agentDef.SystemPrompt + "\n" + query
		inputTokens := llm.EstimateTokens(inputText) + len(agentDef.Tools)*200 + len(agentDef.ToolsInline)*200
		// Assume output ≈ 20% of input (conservative single-turn estimate)
		outputTokens := inputTokens / 5

		pricing, _ := agent.ResolvePricing(model, cfg.Settings.Pricing)

		// Scale by max_iterations (how many ReAct turns the agent may take)
		maxIter := 10
		if agentDef.Settings != nil && agentDef.Settings.MaxIterations > 0 {
			maxIter = agentDef.Settings.MaxIterations
		}
		scaledInput := inputTokens * maxIter
		scaledOutput := outputTokens * maxIter

		cost := float64(scaledInput)/1e6*pricing.Input + float64(scaledOutput)/1e6*pricing.Output

		fmt.Fprintf(w, "%s\t%s\t%d\t$%.6f\n", agentDef.Name, model, scaledInput, cost)
		totalTokens += scaledInput
		totalCost += cost
	}

	fmt.Fprintln(w, strings.Repeat("─", 64))
	fmt.Fprintf(w, "TOTAL\t\t%d\t$%.6f\n", totalTokens, totalCost)
	w.Flush()

	fmt.Println("\nNote: estimate assumes 10 iterations per agent; actual usage will vary.")
	fmt.Println("      Models without known pricing show $0.000000 — set settings.pricing in YAML.")
	return nil
}

// buildRateLimiters creates provider-scoped rate limiters from config.
// Multiple agents sharing a provider share the same limiter instance.
func buildRateLimiters(cfg *config.Config) map[string]*agent.RateLimiter {
	limiters := make(map[string]*agent.RateLimiter)
	for name, prov := range cfg.Settings.Providers {
		if rl := agent.NewRateLimiter(prov.RateLimit); rl != nil {
			limiters[name] = rl
		}
	}
	return limiters
}

// resolveAgentProviderName returns the provider name an agent will use.
func resolveAgentProviderName(cfg *config.Config, def *config.AgentDefinition) string {
	if len(def.Providers) > 0 {
		return def.Providers[0].Name
	}
	if def.Provider != "" {
		return def.Provider
	}
	return cfg.Settings.DefaultProvider
}

// wireAgentRetryAndRateLimit attaches retry config and rate limiter to an agent.
func wireAgentRetryAndRateLimit(ag *agent.Agent, cfg *config.Config, def *config.AgentDefinition, limiters map[string]*agent.RateLimiter) {
	rc := agent.RetryConfigFromSettings(cfg.Settings.Retry, cfg.Settings.Execution.RetryAttempts)
	ag.SetRetryConfig(rc)
	provName := resolveAgentProviderName(cfg, def)
	if rl, ok := limiters[provName]; ok {
		ag.SetRateLimiter(rl)
	}
}
