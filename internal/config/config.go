// Package config provides configuration parsing and management for Rakitsu.
// It defines the YAML schema for defining agents, tools, and orchestrators.
package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure
type Config struct {
	Name               string               `mapstructure:"name"`
	ProjectID          string               `mapstructure:"project_id" yaml:"project_id,omitempty"`
	Version            string               `mapstructure:"version"`
	Description        string               `mapstructure:"description"`
	Interactive        bool                 `mapstructure:"interactive" yaml:"interactive,omitempty"`                 // run as chat TUI instead of one-shot
	InteractiveOverlay *bool                `mapstructure:"interactive_overlay" yaml:"interactive_overlay,omitempty"` // wrap root Runner in chat-host meta-agent (default: true for non-conversational configs)
	ForceDelegation    bool                 `mapstructure:"force_delegation" yaml:"force_delegation,omitempty"`       // ChatHost must call invoke_config on every turn; removes "answer directly" path and discovery tools. Use for weak models that would otherwise bypass delegation.
	Settings           Settings             `mapstructure:"settings"`
	Tools              []ToolDefinition     `mapstructure:"tools"`
	Skills             []SkillDefinition    `mapstructure:"skills"`
	Agents             []AgentDefinition    `mapstructure:"agents"`
	Orchestrator       *OrchestratorConfig  `mapstructure:"orchestrator"`
	Orchestrators      []OrchestratorConfig `mapstructure:"orchestrators" yaml:"orchestrators,omitempty"`
	Workflows          []WorkflowDefinition `mapstructure:"workflows"`

	// settingsEnvRefs maps a settings field (see settingsRefKey) to the
	// ${VAR} text it held before Load expanded it, so Redacted can write the
	// reference back instead of the resolved value. Unexported: never parsed
	// from or written to YAML.
	settingsEnvRefs map[settingsRefKey]string
}

// Settings contains global configuration settings
type Settings struct {
	DefaultProvider  string                        `mapstructure:"default_provider"`
	Providers        map[string]ProviderDefinition `mapstructure:"providers"` // named provider instances
	APIKeys          map[string]string             `mapstructure:"api_keys"`
	BaseURLs         map[string]string             `mapstructure:"base_urls"`         // provider -> custom endpoint URL
	CredentialsFiles map[string]string             `mapstructure:"credentials_files"` // provider -> service account JSON path
	Locations        map[string]string             `mapstructure:"locations"`         // provider -> cloud region (e.g., "global", "us-central1")
	Projects         map[string]string             `mapstructure:"projects"`          // provider -> GCP project ID
	AllowedCommands  []string                      `mapstructure:"allowed_commands"`  // user-defined commands allowed for CLI tools
	Defaults         DefaultSettings               `mapstructure:"defaults"`
	Execution        ExecutionSettings             `mapstructure:"execution"`
	Pricing          map[string]PricingConfig      `mapstructure:"pricing" yaml:"pricing"` // model -> pricing
	Logging          LoggingSettings               `mapstructure:"logging"`
	HubURL           string                        `mapstructure:"hub_url" yaml:"hub_url,omitempty"` // SSE hub URL for rakitsu run (overridden by --hub flag)
	Server           ServerConfig                  `mapstructure:"server"`
	Retry            RetrySettings                 `mapstructure:"retry" yaml:"retry,omitempty"`
	Memory           MemoryConfig                  `mapstructure:"memory" yaml:"memory,omitempty"`
	Spawn            SpawnConfig                   `mapstructure:"spawn" yaml:"spawn,omitempty"`
	AgentChat        AgentChatConfig               `mapstructure:"agent_chat" yaml:"agent_chat,omitempty"`
	SessionMsg       SessionMsgConfig              `mapstructure:"session_msg" yaml:"session_msg,omitempty"`
	RedactKeywords   []string                      `mapstructure:"redact_keywords" yaml:"redact_keywords,omitempty"` // extra credential-shaped keywords for telemetry redaction, on top of the built-in list
}

// SessionMsgConfig gates cross-session messaging for a session: whether it
// accepts messages injected from other live sessions AND whether its agents
// get the send_message / list_sessions tools. Opt-in (default false, plain
// bool like Spawn/Memory, unlike AgentChat's default-on *bool) because an
// inbound message triggers a full agent turn — on a session with cli/fs
// tools that is remote control, so a session must never be reachable unless
// its own config said yes.
type SessionMsgConfig struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
}

// SpawnConfig enables the spawn_agent tool: runtime subagent fan-out. When
// Enabled, every top-level agent (and the interactive ChatHost) gets a
// spawn_agent tool that builds a child agent at runtime and runs it under a
// per-spawn timeout. Children below MaxDepth do not get the tool, so there
// is no recursion by default.
type SpawnConfig struct {
	Enabled        bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	MaxConcurrent  int  `mapstructure:"max_concurrent" yaml:"max_concurrent,omitempty"`   // run-global cap; default 4
	MaxDepth       int  `mapstructure:"max_depth" yaml:"max_depth,omitempty"`             // default 1 (children cannot spawn)
	TimeoutSeconds int  `mapstructure:"timeout_seconds" yaml:"timeout_seconds,omitempty"` // per child; default 300
}

// EffectiveMaxConcurrent returns MaxConcurrent with the documented default.
func (s SpawnConfig) EffectiveMaxConcurrent() int {
	if s.MaxConcurrent > 0 {
		return s.MaxConcurrent
	}
	return 4
}

// EffectiveMaxDepth returns MaxDepth with the documented default.
func (s SpawnConfig) EffectiveMaxDepth() int {
	if s.MaxDepth > 0 {
		return s.MaxDepth
	}
	return 1
}

// EffectiveTimeout returns the per-child timeout with the documented default.
func (s SpawnConfig) EffectiveTimeout() time.Duration {
	if s.TimeoutSeconds > 0 {
		return time.Duration(s.TimeoutSeconds) * time.Second
	}
	return 300 * time.Second
}

// retainNothing is the sentinel a user writes as `max_retained: -1` to turn
// spawned-child retention off entirely while leaving config agents
// addressable. A literal 0 in YAML is indistinguishable from "key absent"
// after mapstructure decoding, so it means "use the default".
const retainNothing = -1

// AgentChatConfig configures directed agent chat: talking to one agent in a
// session directly, separate from the main conversation.
//
// Enabled is a *bool because the feature defaults to ON. A plain bool cannot
// tell "absent" from "explicitly false", and config.Load applies no defaults
// pass — the same reason Config.InteractiveOverlay is a *bool.
type AgentChatConfig struct {
	Enabled            *bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	MaxRetained        int   `mapstructure:"max_retained" yaml:"max_retained,omitempty"`
	MaxTranscriptBytes int   `mapstructure:"max_transcript_bytes" yaml:"max_transcript_bytes,omitempty"`
}

// EffectiveEnabled reports whether directed agent chat is on. Absent = on.
func (a AgentChatConfig) EffectiveEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// EffectiveMaxRetained returns how many agent transcripts to keep, defaulting
// to 20. The retainNothing sentinel (-1) yields 0: nothing is retained, so
// spawned children stop being addressable once they finish, while config
// agents remain addressable with an empty starting history.
func (a AgentChatConfig) EffectiveMaxRetained() int {
	if a.MaxRetained == retainNothing {
		return 0
	}
	if a.MaxRetained > 0 {
		return a.MaxRetained
	}
	return 20
}

// EffectiveMaxTranscriptBytes returns the per-agent transcript size budget,
// defaulting to 256 KiB. A transcript over budget is truncated from the
// front, preserving the system prompt and the most recent turns.
func (a AgentChatConfig) EffectiveMaxTranscriptBytes() int {
	if a.MaxTranscriptBytes > 0 {
		return a.MaxTranscriptBytes
	}
	return 262144
}

// MemoryConfig enables rakitsu's native memory / knowledge-graph store
// (internal/memory): typed knowledge nodes persisted as JSON files per scope
// under Dir, recalled via BM25. When Enabled, every agent gets the memory_*
// tool set (memory_add/query/get/list/link/retire) auto-registered — the
// same pattern as the chat-mode user_input tool. Knowledge is bi-temporal:
// superseding or retiring hides an entry from default recall without
// destroying it, and query/list accept as_of time travel. No external DB;
// single binary.
type MemoryConfig struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled,omitempty"`
	Dir     string `mapstructure:"dir" yaml:"dir,omitempty"` // default ~/.rakitsu/memory
	// Conversation switches chat surfaces from full-history re-feed to a
	// rolling summary + recent verbatim turns (bounded per-turn context).
	Conversation ConversationMemoryConfig `mapstructure:"conversation" yaml:"conversation,omitempty"`
	// AutoRecall injects the top-k memories relevant to each turn's query into
	// the model's context at Run start, so facts surface even when a weak model
	// never calls memory_query (fixes the pure-memory recall fragility seen in
	// the A/B). Opt-in; gated on Enabled.
	AutoRecall AutoRecallConfig `mapstructure:"auto_recall" yaml:"auto_recall,omitempty"`
}

// AutoRecallConfig configures Run-start auto-recall injection of native memory.
// At the start of each agent Run the store is queried for the top-k memories
// relevant to the user's query; the ranked index (same shape as memory_query)
// is injected as a fenced user message. Off by default.
type AutoRecallConfig struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	TopK    int  `mapstructure:"top_k" yaml:"top_k,omitempty"` // default 5
	// Scopes optionally overrides the default session->project->global cascade.
	// Entries may be shorthands ("session","project","global") or literals
	// ("project:foo"). Empty = default cascade.
	Scopes []string `mapstructure:"scopes" yaml:"scopes,omitempty"`
	// NodeType optionally restricts recall to one type (rule|pattern|fact|
	// procedure|gotcha|note). Empty = all types.
	NodeType string `mapstructure:"node_type" yaml:"node_type,omitempty"`
}

// ConversationMemoryConfig configures summarized-context chat mode: instead
// of re-feeding the whole transcript every turn, the model sees a rolling
// summary plus the last KeepRecentTurns turns verbatim. The summary is
// maintained by a post-turn summarizer inference and persisted in the
// session scope, so resume keeps the compressed context. Turns are only
// dropped from the model-visible window once they have been folded into the
// summary — a summarizer failure degrades to full history, never data loss.
type ConversationMemoryConfig struct {
	Enabled         bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	KeepRecentTurns int  `mapstructure:"keep_recent_turns" yaml:"keep_recent_turns,omitempty"` // verbatim turns per inference (default 2)
	SummaryMaxChars int  `mapstructure:"summary_max_chars" yaml:"summary_max_chars,omitempty"` // rolling summary size cap (default 2000)
	// DisableSummary switches to pure-memory mode: turns past the verbatim
	// window are dropped WITHOUT a summarizer inference, so context older than
	// KeepRecentTurns is recoverable only via the memory/KG tools. Trades the
	// loss-safety invariant for zero summarizer cost and a strictly flat
	// per-turn context. Default false (rolling summary on).
	DisableSummary bool `mapstructure:"disable_summary" yaml:"disable_summary,omitempty"`
}

// ServerConfig holds configuration for the embedded SSE/HTTP server (rakitsu serve / rakitsu ui).
type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Host string `mapstructure:"host"`
}

// ProviderDefinition defines a named provider instance with its credentials.
// Agents reference providers by instance name (the map key in Settings.Providers).
type ProviderDefinition struct {
	Type            string `mapstructure:"type" yaml:"type"` // underlying provider: openai, anthropic, gemini, ollama
	APIKey          string `mapstructure:"api_key" yaml:"api_key"`
	BaseURL         string `mapstructure:"base_url,omitempty" yaml:"base_url,omitempty"`
	CredentialsFile string `mapstructure:"credentials_file,omitempty" yaml:"credentials_file,omitempty"`
	Location        string `mapstructure:"location,omitempty" yaml:"location,omitempty"`                 // cloud region for Vertex AI (e.g., "us-central1", "global")
	Project         string `mapstructure:"project,omitempty" yaml:"project,omitempty"`                   // GCP project ID for Vertex AI
	DefaultModel    string `mapstructure:"default_model,omitempty" yaml:"default_model,omitempty"`       // default model for this provider; falls back behind agent.Model, ahead of settings.defaults.model
	ResponseFormat  string `mapstructure:"response_format,omitempty" yaml:"response_format,omitempty"`   // optional adapter override: "standard_openai", "reasoning_content_field". Empty = auto-detect from model name.
	RateLimit       int    `mapstructure:"rate_limit,omitempty" yaml:"rate_limit,omitempty"`             // max requests per minute to this provider (0 = unlimited)
	ReasoningEffort string `mapstructure:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"` // OpenAI reasoning_effort sent on every request ("none", "minimal", "low", "medium", "high"); GPT-5.6 models require it with tools
}

// DefaultSettings contains default model configuration
type DefaultSettings struct {
	Model       string  `mapstructure:"model"`
	Temperature float64 `mapstructure:"temperature"`
	MaxTokens   int     `mapstructure:"max_tokens"`
}

// ExecutionSettings contains execution-related settings
type ExecutionSettings struct {
	// MaxIterations is the default for agents that do not set their own
	// max_iterations. -1 = no cap. 0 = unset: no cap when the run has no
	// timeout, else the agent default (10).
	MaxIterations int `mapstructure:"max_iterations"`
	// TimeoutSeconds is the total wall-clock budget for a run (per turn in
	// interactive chat). It overrides the --timeout default (off) when set; the
	// --timeout CLI flag takes precedence when explicitly passed. A negative
	// value means "no timeout" (run until completion / budget / Ctrl+C).
	// 0 = unset (use the flag). With no timeout, agents that leave
	// max_iterations unset get no iteration cap.
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// IdleTimeoutSeconds cancels the run if no telemetry events (token /
	// reasoning chunks, tool calls, ...) arrive for this many seconds. This is
	// an inactivity ("stuck") timeout, distinct from the total TimeoutSeconds:
	// a slow-but-progressing model keeps streaming chunks and never trips it,
	// while a genuinely stalled run does. 0 = disabled (default). Requires a
	// streaming provider; applies to non-interactive runs.
	IdleTimeoutSeconds int     `mapstructure:"idle_timeout_seconds" yaml:"idle_timeout_seconds"`
	RetryAttempts      int     `mapstructure:"retry_attempts"`
	MaxTotalTokens     int     `mapstructure:"max_total_tokens" yaml:"max_total_tokens"` // hard token budget (0=unlimited)
	MaxCost            float64 `mapstructure:"max_cost" yaml:"max_cost"`                 // hard cost budget in USD (0=unlimited)
}

// PricingConfig defines per-model pricing in USD per 1M tokens
type PricingConfig struct {
	Input  float64 `mapstructure:"input" yaml:"input"`
	Output float64 `mapstructure:"output" yaml:"output"`
}

// RetrySettings controls LLM call retry behaviour.
type RetrySettings struct {
	MaxAttempts int    `mapstructure:"max_attempts" yaml:"max_attempts,omitempty"` // max retry attempts (default 3)
	BaseDelay   string `mapstructure:"base_delay" yaml:"base_delay,omitempty"`     // initial backoff duration, e.g. "2s" (default "1s")
	MaxDelay    string `mapstructure:"max_delay" yaml:"max_delay,omitempty"`       // max backoff duration, e.g. "30s" (default "16s")
}

// LoggingSettings contains logging configuration
type LoggingSettings struct {
	Level string `mapstructure:"level"`
	File  string `mapstructure:"file"`
}

// ToolDefinition defines a tool that agents can use
type ToolDefinition struct {
	Name             string               `mapstructure:"name" yaml:"name"`
	Type             string               `mapstructure:"type" yaml:"type"` // cli, fs, mcp_server, a2a
	Description      string               `mapstructure:"description" yaml:"description"`
	Command          string               `mapstructure:"command,omitempty" yaml:"command,omitempty"`
	Operation        string               `mapstructure:"operation,omitempty" yaml:"operation,omitempty"`
	Method           string               `mapstructure:"method,omitempty" yaml:"method,omitempty"`
	Executable       string               `mapstructure:"executable,omitempty" yaml:"executable,omitempty"`
	Script           string               `mapstructure:"script,omitempty" yaml:"script,omitempty"`
	Parameters       map[string]Parameter `mapstructure:"parameters,omitempty" yaml:"parameters,omitempty"`
	Timeout          int                  `mapstructure:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	Env              map[string]string    `mapstructure:"env,omitempty" yaml:"env,omitempty"`
	WorkingDir       string               `mapstructure:"working_dir,omitempty" yaml:"working_dir,omitempty"`
	AllowedPaths     []string             `mapstructure:"allowed_paths,omitempty" yaml:"allowed_paths,omitempty"`
	AllowedExitCodes []int                `mapstructure:"allowed_exit_codes,omitempty" yaml:"allowed_exit_codes,omitempty"`
	Sandbox          *SandboxConfig       `mapstructure:"sandbox,omitempty" yaml:"sandbox,omitempty"`
	// MCP server fields (type: mcp_server)
	Args      []string `mapstructure:"args,omitempty" yaml:"args,omitempty"`
	URL       string   `mapstructure:"url,omitempty"  yaml:"url,omitempty"`
	Transport string   `mapstructure:"transport,omitempty" yaml:"transport,omitempty"` // "stdio" or "http"
	// A2A fields (type: a2a)
	AgentName string `mapstructure:"agent,omitempty" yaml:"agent,omitempty"` // remote agent name to delegate to
	// APIKey authenticates against a peer's /a2a endpoint (RAKITSU_API_TOKEN
	// or any bearer token an A2A server requires) — sent as
	// "Authorization: Bearer <APIKey>". Expanded the same as provider
	// api_key values (${VAR} / ${VAR:-default}); see the expansion loop in
	// Load below.
	APIKey string `mapstructure:"api_key,omitempty" yaml:"api_key,omitempty"`

	// envRefs holds the original ${VAR} text of each url/command/args field
	// expandToolDefEnvVars resolved, so Redacted can write the reference
	// back into a snapshot instead of the resolved (possibly secret) value.
	// Unexported: never parsed from or written to YAML.
	envRefs *toolEnvRefs
}

// toolEnvRefs records pre-expansion ${VAR} references; an empty string
// means that field was not a reference.
type toolEnvRefs struct {
	URL     string
	Command string
	Args    map[int]string
}

// Parameter defines a tool parameter
type Parameter struct {
	Type        string      `mapstructure:"type" yaml:"type"`
	Description string      `mapstructure:"description" yaml:"description"`
	Required    bool        `mapstructure:"required" yaml:"required"`
	Default     interface{} `mapstructure:"default" yaml:"default"`
	Enum        []string    `mapstructure:"enum,omitempty" yaml:"enum,omitempty"`
	// ArgvSplit applies only to a `cli` tool's `command` template, and only
	// when this parameter is the ENTIRE content of one command part (e.g.
	// `command: "gh {{args}}"`, no shell wrapper). The CLI tool execs
	// argv-direct with no shell to split a free-text value on whitespace, so
	// by default the whole value lands as a single argv element — right for
	// a value meant to be one argument (e.g. `python3 -c {{code}}`), wrong
	// for a value meant as a full free-text argument string (e.g. `gh
	// {{args}}` given "repo view --json foo"). Set true to shell-aware split
	// the value into multiple argv tokens instead. Defaults to false so
	// existing configs keep their current (single-token) behavior
	// unchanged. Has no effect when the placeholder sits inside a larger
	// string, e.g. a shell payload passed to `sh -c`/`bash -c` — that stays
	// single-token, substituted before the shell parses it.
	//
	// The split respects only SINGLE-quoted grouping (matching how command
	// templates in YAML are written), not double quotes — a value like
	// `--message "hello world"` splits into `--message`, `"hello`, `world"`
	// with the literal quote characters intact, not a single grouped
	// argument. No shell is invoked so nothing escapes containment, but a
	// double-quoted value (a natural way for a model to quote a multi-word
	// argument) won't group as one token; prefer designing the tool's
	// command/parameters so multi-word values don't need in-value quoting.
	//
	// An UNMATCHED single quote in the value is worse than non-grouping: an
	// odd apostrophe (e.g. "don't" in ordinary free-text like a commit
	// message) is silently DROPPED and everything after it in the value
	// merges into one argv token instead of splitting on whitespace, since
	// the splitter's quote-toggle state never flips back off. E.g.
	// `-m "don't forget --user=x" now` splits into `-m`, `"dont forget
	// --user=x" now` — the apostrophe vanishes and the trailing `now`
	// merges in. Same containment guarantee applies (no shell parses the
	// result), but avoid values with an odd number of single quotes.
	ArgvSplit bool `mapstructure:"argv_split,omitempty" yaml:"argv_split,omitempty"`
}

// SandboxConfig defines security sandbox configuration
type SandboxConfig struct {
	Type         string   `mapstructure:"type" yaml:"type"` // "local_restricted" or "docker"
	Image        string   `mapstructure:"image,omitempty" yaml:"image,omitempty"`
	MountWorkdir bool     `mapstructure:"mount_workdir,omitempty" yaml:"mount_workdir,omitempty"`
	AllowedPaths []string `mapstructure:"allowed_paths,omitempty" yaml:"allowed_paths,omitempty"`
	// NetworkIsolated is kept for backward compatibility: true always maps
	// to --network none. It does nothing else — the docker sandbox network
	// defaults to none regardless (see AllowNetwork).
	NetworkIsolated bool `mapstructure:"network_isolated,omitempty" yaml:"network_isolated,omitempty"`
	// AllowNetwork opts a docker-sandboxed command into network access
	// (skips --network none). Default is no network — a footgun a
	// container shouldn't need for most cli tools (linters, formatters,
	// interpreters), and the safer default when the config wasn't written
	// with network exposure in mind.
	AllowNetwork bool `mapstructure:"allow_network,omitempty" yaml:"allow_network,omitempty"`
	// MountWorkdirWritable makes the mount_workdir bind mount read-write.
	// Default is read-only: a command that only needs to read the
	// workdir (most linters, test runners) can't also modify or delete
	// files there just because MountWorkdir was set for read access.
	MountWorkdirWritable bool `mapstructure:"mount_workdir_writable,omitempty" yaml:"mount_workdir_writable,omitempty"`
	// User sets the docker "--user" the command runs as, "uid:gid" form.
	// Defaults to 65534:65534 (nobody:nogroup) when unset, so a container
	// escape or a compromised tool doesn't get root inside the container.
	User           string         `mapstructure:"user,omitempty" yaml:"user,omitempty"`
	ResourceLimits ResourceLimits `mapstructure:"resource_limits,omitempty" yaml:"resource_limits,omitempty"`
}

// ResourceLimits defines resource constraints for sandboxed execution
type ResourceLimits struct {
	CPULimit    string `mapstructure:"cpu_limit" yaml:"cpu_limit"`
	MemoryLimit string `mapstructure:"memory_limit" yaml:"memory_limit"`
	TimeoutSec  int    `mapstructure:"timeout_sec" yaml:"timeout_sec"`
	// MaxOutputBytes caps the stdout/stderr size returned from a single
	// tool invocation. When 0, the cli tool applies a conservative default
	// (see internal/tools/cli.DefaultMaxOutputBytes). Set to -1 to disable
	// truncation entirely. Output beyond the cap is replaced with a
	// `... [truncated N bytes]` marker so the agent can see that data
	// was elided rather than silently losing context.
	MaxOutputBytes int `mapstructure:"max_output_bytes,omitempty" yaml:"max_output_bytes,omitempty"`
	// PidsLimit caps the number of processes/threads a docker-sandboxed
	// command's container can create (docker run --pids-limit), a floor
	// against a fork bomb or runaway subprocess spawn. When 0, the docker
	// sandbox applies a conservative default (see
	// internal/tools/cli.DefaultPidsLimit). Set to -1 to disable the cap.
	PidsLimit int `mapstructure:"pids_limit,omitempty" yaml:"pids_limit,omitempty"`
}

// SkillDefinition defines a reusable skill template
type SkillDefinition struct {
	Name           string   `mapstructure:"name" yaml:"name"`
	Description    string   `mapstructure:"description" yaml:"description"`
	Tools          []string `mapstructure:"tools" yaml:"tools"`
	PromptTemplate string   `mapstructure:"prompt_template" yaml:"prompt_template"`
}

// ProviderEntry is one element in an agent's ordered provider fallback chain.
// Model is optional; when empty the agent's top-level Model field is used.
type ProviderEntry struct {
	Name  string `mapstructure:"name" yaml:"name"`
	Model string `mapstructure:"model,omitempty" yaml:"model,omitempty"`
}

// AgentDefinition defines an agent configuration
type AgentDefinition struct {
	Name         string           `mapstructure:"name" yaml:"name"`
	Role         string           `mapstructure:"role" yaml:"role"`                               // worker, supervisor
	Provider     string           `mapstructure:"provider" yaml:"provider"`                       // single provider name (backward compat)
	Providers    []ProviderEntry  `mapstructure:"providers,omitempty" yaml:"providers,omitempty"` // ordered fallback chain; overrides Provider when non-empty
	Model        string           `mapstructure:"model" yaml:"model"`
	ModelConfig  *ModelConfig     `mapstructure:"model_config,omitempty" yaml:"model_config,omitempty"`
	SystemPrompt string           `mapstructure:"system_prompt" yaml:"system_prompt"`
	Tools        []string         `mapstructure:"tools,omitempty" yaml:"tools,omitempty"`
	Skills       []string         `mapstructure:"skills,omitempty" yaml:"skills,omitempty"`
	ToolsInline  []ToolDefinition `mapstructure:"tools_inline,omitempty" yaml:"tools_inline,omitempty"`
	Vision       *bool            `mapstructure:"vision,omitempty" yaml:"vision,omitempty"` // image input: true = always, false = never, unset = auto (sent; a model that rejects images falls back to a text note)
	Settings     *AgentSettings   `mapstructure:"settings,omitempty" yaml:"settings,omitempty"`
}

// ModelConfig contains model-specific configuration
type ModelConfig struct {
	Temperature       float64 `mapstructure:"temperature" yaml:"temperature"`
	MaxTokens         int     `mapstructure:"max_tokens" yaml:"max_tokens"`
	TopP              float64 `mapstructure:"top_p,omitempty" yaml:"top_p,omitempty"`
	FrequencyPenalty  float64 `mapstructure:"frequency_penalty,omitempty" yaml:"frequency_penalty,omitempty"`
	PresencePenalty   float64 `mapstructure:"presence_penalty,omitempty" yaml:"presence_penalty,omitempty"`
	TimeoutSec        int     `mapstructure:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`                 // per-request LLM timeout (sent as X-LiteLLM-Timeout header)
	NoStreamTools     bool    `mapstructure:"no_stream_tools" yaml:"no_stream_tools"`                             // disable streaming when tools are present (vLLM/Qwen3 workaround)
	MaxThinkingTokens int     `mapstructure:"max_thinking_tokens,omitempty" yaml:"max_thinking_tokens,omitempty"` // Anthropic extended thinking budget cap (must be >=1024)
	ThinkingOffload   bool    `mapstructure:"thinking_offload" yaml:"thinking_offload"`                           // strip thinking from history and store externally
	ReasoningEffort   string  `mapstructure:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`       // per-agent OpenAI reasoning_effort override (wins over the provider's)
}

// AgentSettings contains agent-specific settings
type AgentSettings struct {
	MaxIterations  int               `mapstructure:"max_iterations" yaml:"max_iterations"`
	MaxTotalTokens int               `mapstructure:"max_total_tokens" yaml:"max_total_tokens"` // per-agent token budget (0=unlimited)
	MaxCost        float64           `mapstructure:"max_cost" yaml:"max_cost"`                 // per-agent cost budget in USD (0=unlimited)
	Verbose        bool              `mapstructure:"verbose" yaml:"verbose"`
	Timeout        int               `mapstructure:"timeout" yaml:"timeout"`
	Reflection     ReflectionConfig  `mapstructure:"reflection" yaml:"reflection"`
	GroundCheck    GroundCheckConfig `mapstructure:"ground_check" yaml:"ground_check"`
	Context        ContextConfig     `mapstructure:"context" yaml:"context"`
	Rollback       RollbackConfig    `mapstructure:"rollback" yaml:"rollback"`
}

// ContextConfig configures how the agent manages conversation context.
type ContextConfig struct {
	Strategy                  string          `mapstructure:"strategy" yaml:"strategy"`                                                 // "full", "sliding_window", "step_log", "auto" (default: "full")
	WindowSize                int             `mapstructure:"window_size" yaml:"window_size"`                                           // sliding_window: number of turns to keep
	KeepRecent                int             `mapstructure:"keep_recent" yaml:"keep_recent"`                                           // step_log: recent steps kept verbatim (default: 3)
	MaxToolOutput             int             `mapstructure:"max_tool_output" yaml:"max_tool_output"`                                   // max chars per tool output, 0=unlimited
	FenceOutputs              bool            `mapstructure:"fence_outputs" yaml:"fence_outputs"`                                       // wrap tool outputs in delimiters for injection defense
	AutoFullThreshold         int             `mapstructure:"auto_full_threshold" yaml:"auto_full_threshold,omitempty"`                 // auto: msgs before switching to sliding_window (default 30)
	AutoCompressThreshold     int             `mapstructure:"auto_compress_threshold" yaml:"auto_compress_threshold,omitempty"`         // auto: msgs before switching to step_log (default 60)
	ContextBudgetThreshold    float64         `mapstructure:"context_budget_threshold" yaml:"context_budget_threshold,omitempty"`       // auto: token pressure to trigger sliding_window (default 0.75)
	ContextRetrievalThreshold float64         `mapstructure:"context_retrieval_threshold" yaml:"context_retrieval_threshold,omitempty"` // auto: token pressure to trigger step_log (default 0.90)
	Retrieval                 RetrievalConfig `mapstructure:"retrieval" yaml:"retrieval,omitempty"`
}

// RetrievalConfig configures per-session semantic retrieval over step history.
// BM25 (zero deps) is the default backend; Ollama provides true embedding-based retrieval.
type RetrievalConfig struct {
	Enabled           bool    `mapstructure:"enabled" yaml:"enabled,omitempty"`
	TopK              int     `mapstructure:"top_k" yaml:"top_k,omitempty"`                           // segments to inject (default 5)
	EmbeddingProvider string  `mapstructure:"embedding_provider" yaml:"embedding_provider,omitempty"` // "ollama" or "" (BM25)
	EmbeddingModel    string  `mapstructure:"embedding_model" yaml:"embedding_model,omitempty"`
	EmbeddingURL      string  `mapstructure:"embedding_url" yaml:"embedding_url,omitempty"` // default http://localhost:11434
	ErrorBias         float64 `mapstructure:"error_bias" yaml:"error_bias,omitempty"`       // weight multiplier for failed steps (default 2.0)
}

// ReflectionConfig configures agent self-reflection behavior
type ReflectionConfig struct {
	Enabled   bool   `mapstructure:"enabled" yaml:"enabled"`
	Mode      string `mapstructure:"mode" yaml:"mode"`           // "after_tool", "before_answer", "both"
	Frequency string `mapstructure:"frequency" yaml:"frequency"` // "always", "on_error", "every_n" (default: "always")
	EveryN    int    `mapstructure:"every_n" yaml:"every_n"`     // reflect every N iterations (when frequency="every_n")
	Prompt    string `mapstructure:"prompt" yaml:"prompt"`       // custom reflection prompt (optional)
}

// GroundCheckConfig configures agent ground-check validation
type GroundCheckConfig struct {
	Enabled             bool    `mapstructure:"enabled" yaml:"enabled"`
	ConfidenceThreshold float64 `mapstructure:"confidence_threshold" yaml:"confidence_threshold"` // 0.0-1.0, default 0.7
	Prompt              string  `mapstructure:"prompt" yaml:"prompt"`                             // custom check prompt (optional)
	MaxRetries          int     `mapstructure:"max_retries" yaml:"max_retries"`                   // retry on low confidence, default 1
}

// RollbackConfig configures runtime self-correction. When an agent iteration
// hits a dead end, the agent rewinds its conversation history to before that
// iteration and retries — so the failed attempt's tool errors never pollute
// the LLM context. Off by default; opt in per agent. The agent re-reads this
// block at the start of every Run, so edits hot-reload on the next turn.
//
// Triggers (deterministic, always evaluated) — recognized values:
//   - "tool_error"            — every tool call in the iteration failed
//   - "repeated_tool_failure" — a tool hit the 3x-identical-failure ceiling
//
// An unrecognized trigger is ignored. Empty Triggers defaults to ["tool_error"].
//
// LLMSelfJudge adds an opt-in secondary trigger: when no deterministic trigger
// fires, the agent asks the LLM whether the step was a dead end. It is a hint
// only (one extra LLM call per tool iteration) and is off by default — weak
// local models lack the meta-cognition to judge this reliably.
type RollbackConfig struct {
	Enabled      bool     `mapstructure:"enabled" yaml:"enabled"`
	MaxRollbacks int      `mapstructure:"max_rollbacks" yaml:"max_rollbacks,omitempty"` // cap per Run (default 3)
	Triggers     []string `mapstructure:"triggers" yaml:"triggers,omitempty"`
	LLMSelfJudge bool     `mapstructure:"llm_self_judge" yaml:"llm_self_judge,omitempty"`
}

// OrchestratorConfig defines the orchestrator configuration
type OrchestratorConfig struct {
	Name         string          `mapstructure:"name"`
	Role         string          `mapstructure:"role"`
	Strategy     string          `mapstructure:"strategy"` // ReAct, PlanAndExecute, Hierarchical
	Provider     string          `mapstructure:"provider"` // openai, anthropic, gemini, ollama
	Model        string          `mapstructure:"model"`
	ModelConfig  *ModelConfig    `mapstructure:"model_config,omitempty"`
	SystemPrompt string          `mapstructure:"system_prompt"`
	Agents       []string        `mapstructure:"agents"`
	Routing      *RoutingConfig  `mapstructure:"routing,omitempty"`
	Handoff      *HandoffConfig  `mapstructure:"handoff,omitempty"`
	Pipeline     *PipelineConfig `mapstructure:"pipeline,omitempty"`
}

// RoutingConfig defines routing rules for the orchestrator
type RoutingConfig struct {
	AutoDelegateTools bool          `mapstructure:"auto_delegate_tools"`
	Rules             []RoutingRule `mapstructure:"rules,omitempty"`
}

// RoutingRule defines a routing rule
type RoutingRule struct {
	Condition  string `mapstructure:"condition"`
	DelegateTo string `mapstructure:"delegate_to"`
}

// HandoffConfig defines handoff behavior between agents
type HandoffConfig struct {
	IncludeContext       bool `mapstructure:"include_context"`
	MaxContextLength     int  `mapstructure:"max_context_length"`
	AllowCrossAgentCalls bool `mapstructure:"allow_cross_agent_calls"`
}

// PipelineConfig defines a deterministic execution pipeline for the orchestrator.
// Used when orchestrator strategy is "Pipeline".
type PipelineConfig struct {
	Steps           []PipelineStep `mapstructure:"steps" yaml:"steps"`
	Synthesis       bool           `mapstructure:"synthesis" yaml:"synthesis,omitempty"`               // make a final LLM call to summarize all step results
	SynthesisPrompt string         `mapstructure:"synthesis_prompt" yaml:"synthesis_prompt,omitempty"` // custom prompt for synthesis (optional)
}

// PipelineStep defines a step in the pipeline.
// Type defaults to "sequential" (single agent). "parallel" and "loop" contain nested steps.
type PipelineStep struct {
	Name            string         `mapstructure:"name" yaml:"name"`
	Type            string         `mapstructure:"type,omitempty" yaml:"type,omitempty"`                         // "sequential" (default), "parallel", "loop"
	Agent           string         `mapstructure:"agent,omitempty" yaml:"agent,omitempty"`                       // sequential: agent name
	Task            string         `mapstructure:"task,omitempty" yaml:"task,omitempty"`                         // task description
	Steps           []PipelineStep `mapstructure:"steps,omitempty" yaml:"steps,omitempty"`                       // parallel/loop: sub-steps
	MaxIterations   int            `mapstructure:"max_iterations,omitempty" yaml:"max_iterations,omitempty"`     // loop: max iterations (default 5)
	ConditionAgent  string         `mapstructure:"condition_agent,omitempty" yaml:"condition_agent,omitempty"`   // loop: agent to evaluate pass/fail
	ConditionPrompt string         `mapstructure:"condition_prompt,omitempty" yaml:"condition_prompt,omitempty"` // loop: prompt for pass/fail check
	TimeoutSec      int            `mapstructure:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`           // per-step timeout in seconds
	DependsOn       []string       `mapstructure:"depends_on,omitempty" yaml:"depends_on,omitempty"`             // names of steps that must complete before this one

	// RequireToolCall is a mechanical cross-check: if set, the step's agent
	// run must have actually invoked the named tool (matching
	// CommandContains, when set) or the step is forced to fail regardless of
	// what the agent's own final answer or a condition agent's verdict says.
	// Guards against a worker/condition agent self-reporting success without
	// doing the work its prompt asked for.
	RequireToolCall *RequireToolCallGate `mapstructure:"require_tool_call,omitempty" yaml:"require_tool_call,omitempty"`
}

// RequireToolCallGate names a tool call that must have occurred during a
// pipeline step's agent run for the step to be allowed to pass.
type RequireToolCallGate struct {
	Tool            string `mapstructure:"tool" yaml:"tool"`                                             // required tool name, e.g. "sh"
	CommandContains string `mapstructure:"command_contains,omitempty" yaml:"command_contains,omitempty"` // if set, a substring the checked argument must contain
	// ArgKey names which of the call's arguments CommandContains is checked
	// against, e.g. "cmd" for the built-in cli tool. If unset, the gate only
	// matches a call whose arguments have exactly one string-valued field
	// (the common case for a single-parameter tool like "sh") and checks
	// that one — a call with zero or multiple string arguments is treated
	// as not matching rather than guessed at, so CommandContains can never
	// be satisfied by an unrelated field (a path, an env value, metadata).
	ArgKey string `mapstructure:"arg_key,omitempty" yaml:"arg_key,omitempty"`

	// OutputJSONPath, when set, extends the gate beyond "was the tool
	// called" to "did its own JSON response contain a numeric value within
	// bounds" — checked against the matching call's actual output, not the
	// agent's self-reported summary of it. A dot-separated path into the
	// tool's JSON output, e.g. "answers.severity.score" for a Jev score
	// question. At least one of MinValue/MaxValue must be set alongside it.
	OutputJSONPath string `mapstructure:"output_json_path,omitempty" yaml:"output_json_path,omitempty"`
	// MinValue/MaxValue bound the extracted value, inclusive. Either or
	// both may be set. A step whose matching call's extracted value falls
	// outside the given bound(s) fails the gate, even if the agent's own
	// final answer claims success.
	MinValue *float64 `mapstructure:"min_value,omitempty" yaml:"min_value,omitempty"`
	MaxValue *float64 `mapstructure:"max_value,omitempty" yaml:"max_value,omitempty"`
}

// WorkflowDefinition defines a pre-configured workflow (legacy, use PipelineConfig instead)
type WorkflowDefinition struct {
	Name           string           `mapstructure:"name"`
	Description    string           `mapstructure:"description"`
	Steps          []WorkflowStep   `mapstructure:"steps"`
	FinalSynthesis *SynthesisConfig `mapstructure:"final_synthesis,omitempty"`
}

// WorkflowStep defines a step in a workflow
type WorkflowStep struct {
	Agent     string   `mapstructure:"agent"`
	Task      string   `mapstructure:"task"`
	DependsOn []string `mapstructure:"depends_on,omitempty"`
	Condition string   `mapstructure:"condition,omitempty"`
}

// SynthesisConfig defines the final synthesis step
type SynthesisConfig struct {
	Agent  string `mapstructure:"agent"`
	Prompt string `mapstructure:"prompt"`
}

// envOverrides holds run-scoped env vars that take priority over os.Getenv.
// envOverridesMu guards every read/write of envOverrides itself (including
// from lookupEnv, reached by plain Load calls too — not just LoadWithEnv).
// loadWithEnvMu serializes every load end-to-end — LoadWithEnv AND plain
// Load — so one call's overrides can never leak into another: a Load that
// ran while a LoadWithEnv had its overrides installed used to resolve
// ${VAR} from them (on a hub, one request's API key in another request's
// config). It must stay distinct from envOverridesMu, which lookupEnv takes
// inside the load.
var (
	envOverrides   map[string]string
	envOverridesMu sync.RWMutex
	loadWithEnvMu  sync.Mutex
)

// LoadWithEnv loads config with run-scoped env var overrides.
// Overrides take priority over real environment variables.
// Concurrent calls are serialized using a package-level mutex, so overrides
// from one call can never leak into or be clobbered by another.
func LoadWithEnv(configPath string, overrides map[string]string) (*Config, error) {
	loadWithEnvMu.Lock()
	defer loadWithEnvMu.Unlock()

	envOverridesMu.Lock()
	envOverrides = overrides
	envOverridesMu.Unlock()
	defer func() {
		envOverridesMu.Lock()
		envOverrides = nil
		envOverridesMu.Unlock()
	}()

	return load(configPath)
}

// Load loads configuration from a YAML file. It is serialized with
// LoadWithEnv (see loadWithEnvMu).
func Load(configPath string) (*Config, error) {
	loadWithEnvMu.Lock()
	defer loadWithEnvMu.Unlock()
	return load(configPath)
}

// load does the actual work; callers hold loadWithEnvMu.
func load(configPath string) (*Config, error) {
	v := viper.New()

	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	// Also read environment variables
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Auto-generate project ID if not set
	if config.ProjectID == "" {
		config.ProjectID = uuid.New().String()
	}

	// Viper's Unmarshal doesn't properly decode map[string]struct for nested providers.
	// Viper lowercases all keys, so we must match case-insensitively.
	if rawProviders := v.GetStringMap("settings.providers"); len(rawProviders) > 0 {
		// Build case-insensitive lookup for existing providers
		origNames := make(map[string]string) // lowercase → original
		for k := range config.Settings.Providers {
			origNames[strings.ToLower(k)] = k
		}
		for name, raw := range rawProviders {
			origName := origNames[name]
			if origName == "" {
				origName = name
			}
			existing := config.Settings.Providers[origName]
			if m, ok := raw.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t != "" {
					existing.Type = t
				}
				if k, ok := m["api_key"].(string); ok && k != "" {
					existing.APIKey = k
				}
				if u, ok := m["base_url"].(string); ok && u != "" {
					existing.BaseURL = u
				}
				if c, ok := m["credentials_file"].(string); ok && c != "" {
					existing.CredentialsFile = c
				}
				if l, ok := m["location"].(string); ok && l != "" {
					existing.Location = l
				}
				if p, ok := m["project"].(string); ok && p != "" {
					existing.Project = p
				}
				if d, ok := m["default_model"].(string); ok && d != "" {
					existing.DefaultModel = d
				}
			}
			if config.Settings.Providers == nil {
				config.Settings.Providers = make(map[string]ProviderDefinition)
			}
			config.Settings.Providers[origName] = existing
		}
	}

	// Expand environment variables in API keys, base URLs, and credentials files
	config.Settings.APIKeys = expandEnvVars(config.Settings.APIKeys)
	config.recordSettingsMapRefs("base_urls", config.Settings.BaseURLs)
	config.Settings.BaseURLs = expandEnvVars(config.Settings.BaseURLs)
	config.recordSettingsMapRefs("credentials_files", config.Settings.CredentialsFiles)
	config.Settings.CredentialsFiles = expandEnvVars(config.Settings.CredentialsFiles)
	config.recordSettingsMapRefs("locations", config.Settings.Locations)
	config.Settings.Locations = expandEnvVars(config.Settings.Locations)
	config.recordSettingsMapRefs("projects", config.Settings.Projects)
	config.Settings.Projects = expandEnvVars(config.Settings.Projects)

	// Expand environment variables in named provider definitions
	for name, pd := range config.Settings.Providers {
		config.recordProviderRef(name, "base_url", pd.BaseURL)
		config.recordProviderRef(name, "credentials_file", pd.CredentialsFile)
		config.recordProviderRef(name, "location", pd.Location)
		config.recordProviderRef(name, "project", pd.Project)
		config.recordProviderRef(name, "default_model", pd.DefaultModel)
		pd.APIKey = expandEnvVar(pd.APIKey)
		pd.BaseURL = expandEnvVar(pd.BaseURL)
		pd.CredentialsFile = expandEnvVar(pd.CredentialsFile)
		pd.Location = expandEnvVar(pd.Location)
		pd.Project = expandEnvVar(pd.Project)
		pd.DefaultModel = expandEnvVar(pd.DefaultModel)
		config.Settings.Providers[name] = pd
	}

	// Expand environment variables in defaults
	config.recordSettingsRef(settingsRefKey{section: "defaults", field: "model"}, config.Settings.Defaults.Model)
	config.Settings.Defaults.Model = expandEnvVar(config.Settings.Defaults.Model)

	// Filter out $ref placeholders (entries with no name from modular YAML references)
	config.filterRefPlaceholders()

	// Auto-discover definitions from conventional directories
	configDir := filepath.Dir(configPath)
	if err := config.autoDiscoverDefinitions(configDir); err != nil {
		return nil, fmt.Errorf("failed to auto-discover definitions: %w", err)
	}

	// Resolve file references in text fields (file: prefix + auto-detect)
	if err := config.resolveFileReferences(configDir); err != nil {
		return nil, fmt.Errorf("failed to resolve file references: %w", err)
	}

	// Expand environment variables in tool definitions' credentials (a2a's
	// api_key). Runs after auto-discovery so tools loaded from conventional
	// directories get expanded too, not just inline ones.
	for i := range config.Tools {
		expandToolDefEnvVars(&config.Tools[i])
	}
	for ai := range config.Agents {
		for ti := range config.Agents[ai].ToolsInline {
			expandToolDefEnvVars(&config.Agents[ai].ToolsInline[ti])
		}
	}

	// Merge each agent's referenced skills' prompt_template into its
	// effective SystemPrompt — in memory only, same convention as the
	// expandEnvVar/resolveFileReferences mutations above. Must run after
	// resolveFileReferences (which resolves a skill's own prompt_template
	// file: ref) so the text being merged in is already resolved. Before
	// this, `skills:` was validated and exported but never actually
	// affected an agent's behavior.
	for i := range config.Agents {
		config.Agents[i].SystemPrompt = config.EffectiveSystemPrompt(&config.Agents[i])
	}

	// Validate the config. Errors block load; warnings are logged and ignored.
	if errs := config.Validate(); len(errs) > 0 {
		for _, e := range errs {
			if e.IsWarning() {
				fmt.Fprintf(os.Stderr, "config warning: %s\n", e.Error())
			}
		}
		for _, e := range errs {
			if e.IsError() {
				return &config, fmt.Errorf("config validation: %w", e)
			}
		}
	}

	return &config, nil
}

// lookupEnv checks run-scoped overrides first, then falls back to os.Getenv.
func lookupEnv(key string) string {
	envOverridesMu.RLock()
	overrides := envOverrides
	envOverridesMu.RUnlock()
	if overrides != nil {
		if v, ok := overrides[key]; ok {
			return v
		}
	}
	return os.Getenv(key)
}

// expandEnvVars expands environment variables in a map
func expandEnvVars(m map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
			envVar := v[2 : len(v)-1]
			if idx := strings.Index(envVar, ":-"); idx != -1 {
				varName := envVar[:idx]
				defaultVal := envVar[idx+2:]
				result[k] = getEnvWithDefault(varName, defaultVal)
			} else {
				result[k] = lookupEnv(envVar)
			}
		} else {
			result[k] = v
		}
	}
	return result
}

// expandEnvVar expands a single environment variable reference like ${VAR} or ${VAR:-default}
func expandEnvVar(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		envVar := v[2 : len(v)-1]
		if idx := strings.Index(envVar, ":-"); idx != -1 {
			return getEnvWithDefault(envVar[:idx], envVar[idx+2:])
		}
		return lookupEnv(envVar)
	}
	return v
}

// expandToolDefEnvVars expands ${VAR} / ${VAR:-default} references in a
// ToolDefinition's credential and mcp_server connection fields (api_key,
// url, command, args) in place.
func expandToolDefEnvVars(t *ToolDefinition) {
	refs := &toolEnvRefs{}
	if isEnvRef(t.URL) {
		refs.URL = t.URL
	}
	if isEnvRef(t.Command) {
		refs.Command = t.Command
	}
	for i, a := range t.Args {
		if isEnvRef(a) {
			if refs.Args == nil {
				refs.Args = map[int]string{}
			}
			refs.Args[i] = a
		}
	}
	t.envRefs = refs

	t.APIKey = expandEnvVar(t.APIKey)
	t.URL = expandEnvVar(t.URL)
	t.Command = expandEnvVar(t.Command)
	for i := range t.Args {
		t.Args[i] = expandEnvVar(t.Args[i])
	}
}

// isEnvRef reports whether v is a whole-value ${VAR} reference, the only
// form expandEnvVar resolves.
func isEnvRef(v string) bool {
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}")
}

// settingsRefKey identifies one expanded settings field. Provider names and
// settings-map keys are free-form (they may contain "/"), so they are kept
// as separate fields rather than joined into a path string.
type settingsRefKey struct {
	section string // "providers", "defaults", or a settings map name ("base_urls", ...)
	name    string // provider name or map key; empty for defaults
	field   string // provider field ("base_url", ...) or "model"; empty for maps
}

// recordSettingsRef remembers v under key if it is a ${VAR} reference.
func (c *Config) recordSettingsRef(key settingsRefKey, v string) {
	if !isEnvRef(v) {
		return
	}
	if c.settingsEnvRefs == nil {
		c.settingsEnvRefs = map[settingsRefKey]string{}
	}
	c.settingsEnvRefs[key] = v
}

// recordProviderRef records a named provider's field.
func (c *Config) recordProviderRef(name, field, v string) {
	c.recordSettingsRef(settingsRefKey{section: "providers", name: name, field: field}, v)
}

// recordSettingsMapRefs records each ${VAR} value of a settings map
// (base_urls, credentials_files, ...).
func (c *Config) recordSettingsMapRefs(section string, m map[string]string) {
	for k, v := range m {
		c.recordSettingsRef(settingsRefKey{section: section, name: k}, v)
	}
}

// restoreSettingsEnvRefs writes src's recorded settings ${VAR} references
// back over dst's resolved values. dst is a deep copy of src.
func restoreSettingsEnvRefs(dst *Config, src *Config) {
	for key, ref := range src.settingsEnvRefs {
		switch key.section {
		case "providers":
			pd, ok := dst.Settings.Providers[key.name]
			if !ok {
				continue
			}
			switch key.field {
			case "base_url":
				pd.BaseURL = ref
			case "credentials_file":
				pd.CredentialsFile = ref
			case "location":
				pd.Location = ref
			case "project":
				pd.Project = ref
			case "default_model":
				pd.DefaultModel = ref
			}
			dst.Settings.Providers[key.name] = pd
		case "defaults":
			dst.Settings.Defaults.Model = ref
		default:
			var m map[string]string
			switch key.section {
			case "base_urls":
				m = dst.Settings.BaseURLs
			case "credentials_files":
				m = dst.Settings.CredentialsFiles
			case "locations":
				m = dst.Settings.Locations
			case "projects":
				m = dst.Settings.Projects
			}
			if _, ok := m[key.name]; ok {
				m[key.name] = ref
			}
		}
	}
}

// restoreToolEnvRefs writes src's recorded ${VAR} references back over
// dst's resolved values. dst is a deep copy of src (same field layout).
func restoreToolEnvRefs(dst *ToolDefinition, src *ToolDefinition) {
	r := src.envRefs
	if r == nil {
		return
	}
	if r.URL != "" {
		dst.URL = r.URL
	}
	if r.Command != "" {
		dst.Command = r.Command
	}
	for i, ref := range r.Args {
		if i < len(dst.Args) {
			dst.Args[i] = ref
		}
	}
}

// getEnvWithDefault gets an environment variable with a default value
func getEnvWithDefault(key, defaultVal string) string {
	if val := lookupEnv(key); val != "" {
		return val
	}
	return defaultVal
}

// findProvider does case-insensitive lookup in the Providers map.
// Viper lowercases all map keys, so "DGX" in YAML becomes "dgx" in the map.
func (c *Config) findProvider(name string) (ProviderDefinition, bool) {
	if pd, ok := c.Settings.Providers[name]; ok {
		return pd, true
	}
	lower := strings.ToLower(name)
	if pd, ok := c.Settings.Providers[lower]; ok {
		return pd, true
	}
	return ProviderDefinition{}, false
}

// GetProviderDefinition returns a named provider instance, if defined.
func (c *Config) GetProviderDefinition(name string) (ProviderDefinition, bool) {
	return c.findProvider(name)
}

// GetProviderType returns the underlying provider type for a name.
// Checks named providers first, then treats the name itself as the type.
func (c *Config) GetProviderType(name string) string {
	if pd, ok := c.findProvider(name); ok && pd.Type != "" {
		return pd.Type
	}
	return name
}

// GetBaseURL returns the base URL override for a provider.
// Checks named providers first, then falls back to flat base_urls map.
func (c *Config) GetBaseURL(provider string) string {
	if pd, ok := c.findProvider(provider); ok && pd.BaseURL != "" {
		return pd.BaseURL
	}
	if url, ok := c.Settings.BaseURLs[provider]; ok {
		return url
	}
	return ""
}

// GetCredentialsFile returns the credentials file path for a provider.
// Checks named providers first, then falls back to flat credentials_files map.
func (c *Config) GetCredentialsFile(provider string) string {
	if pd, ok := c.findProvider(provider); ok && pd.CredentialsFile != "" {
		return pd.CredentialsFile
	}
	if path, ok := c.Settings.CredentialsFiles[provider]; ok {
		return path
	}
	return ""
}

// GetLocation returns the cloud region for a provider.
// Checks named providers first, then falls back to flat locations map.
func (c *Config) GetLocation(provider string) string {
	if pd, ok := c.findProvider(provider); ok && pd.Location != "" {
		return pd.Location
	}
	if loc, ok := c.Settings.Locations[provider]; ok {
		return loc
	}
	return ""
}

// GetResponseFormat returns the explicit response-format adapter override
// for a provider, or empty string if none set (meaning: auto-detect from model name).
func (c *Config) GetResponseFormat(provider string) string {
	if pd, ok := c.findProvider(provider); ok {
		return pd.ResponseFormat
	}
	return ""
}

// GetReasoningEffort returns the provider-level OpenAI reasoning_effort
// setting, or empty string if none set (meaning: omit the field).
func (c *Config) GetReasoningEffort(provider string) string {
	if pd, ok := c.findProvider(provider); ok {
		return pd.ReasoningEffort
	}
	return ""
}

// GetProject returns the GCP project ID for a provider.
// Checks named providers first, then falls back to flat projects map.
func (c *Config) GetProject(provider string) string {
	if pd, ok := c.findProvider(provider); ok && pd.Project != "" {
		return pd.Project
	}
	if proj, ok := c.Settings.Projects[provider]; ok {
		return proj
	}
	return ""
}

// GetDefaultModel returns the default_model declared on a named provider, if any.
func (c *Config) GetDefaultModel(provider string) string {
	if pd, ok := c.findProvider(provider); ok {
		return pd.DefaultModel
	}
	return ""
}

// GetAPIKey returns the API key for a provider.
// Checks named providers first, then falls back to flat api_keys map.
func (c *Config) GetAPIKey(provider string) string {
	if pd, ok := c.findProvider(provider); ok && pd.APIKey != "" {
		return pd.APIKey
	}
	if key, ok := c.Settings.APIKeys[provider]; ok {
		return key
	}
	return ""
}

// GetAgent returns an agent by name
func (c *Config) GetAgent(name string) *AgentDefinition {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			return &c.Agents[i]
		}
	}
	return nil
}

// GetTool returns a tool by name
func (c *Config) GetTool(name string) *ToolDefinition {
	for i := range c.Tools {
		if c.Tools[i].Name == name {
			return &c.Tools[i]
		}
	}
	return nil
}

// GetSkill returns a skill by name
func (c *Config) GetSkill(name string) *SkillDefinition {
	for i := range c.Skills {
		if c.Skills[i].Name == name {
			return &c.Skills[i]
		}
	}
	return nil
}

// EffectiveSystemPrompt composes def's own SystemPrompt with the
// prompt_template of every skill def references (in declaration order),
// joined by blank lines. An unresolvable skill name is skipped here rather
// than treated as an error — Validate() already reports an unknown skill
// reference separately, and this function needs to stay usable even when
// called before validation runs.
func (c *Config) EffectiveSystemPrompt(def *AgentDefinition) string {
	prompt := def.SystemPrompt
	for _, ref := range def.Skills {
		skill := c.GetSkill(ref)
		if skill == nil || strings.TrimSpace(skill.PromptTemplate) == "" {
			continue
		}
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += skill.PromptTemplate
	}
	return prompt
}

// ============================================================
// REDACTION
// ============================================================

// Redacted returns a deep copy of c with resolved secret values replaced
// by a fixed mask string ("[REDACTED]"). Use before persisting or
// serializing a config to any trust boundary — session snapshots written
// to ~/.rakitsu/sessions/<id>.jsonl, debug exports, shared replay files.
//
// Fields redacted:
//   - Settings.Providers[*].APIKey
//   - Settings.APIKeys[*] values
//   - Tools[*].APIKey and every Agents[*].ToolsInline[*].APIKey (a2a tool
//     credentials)
//
// URLs, credentials-file paths, model names, and agent prompts are not
// redacted — they are configuration structure, not secrets.
//
// Redacted never returns nil. If c is nil, it returns a pointer to a
// zero-value Config.
func Redacted(c *Config) (out *Config) {
	if c == nil {
		return &Config{}
	}
	// Fail closed: on a round-trip error return only the name, never the
	// original — callers write the result into session files. yaml.v3
	// panics (rather than erroring) on an unmarshalable value, so recover
	// that too.
	defer func() {
		if recover() != nil {
			out = &Config{Name: c.Name}
		}
	}()
	data, err := yaml.Marshal(c)
	if err != nil {
		return &Config{Name: c.Name}
	}
	var cp Config
	if err := yaml.Unmarshal(data, &cp); err != nil {
		return &Config{Name: c.Name}
	}
	const mask = "[REDACTED]"
	restoreSettingsEnvRefs(&cp, c)
	if len(cp.Tools) == len(c.Tools) {
		for i := range cp.Tools {
			restoreToolEnvRefs(&cp.Tools[i], &c.Tools[i])
		}
	}
	if len(cp.Agents) == len(c.Agents) {
		for ai := range cp.Agents {
			if len(cp.Agents[ai].ToolsInline) != len(c.Agents[ai].ToolsInline) {
				continue
			}
			for ti := range cp.Agents[ai].ToolsInline {
				restoreToolEnvRefs(&cp.Agents[ai].ToolsInline[ti], &c.Agents[ai].ToolsInline[ti])
			}
		}
	}
	if cp.Settings.Providers != nil {
		for name, p := range cp.Settings.Providers {
			if p.APIKey != "" {
				p.APIKey = mask
				cp.Settings.Providers[name] = p
			}
		}
	}
	for k, v := range cp.Settings.APIKeys {
		if v != "" {
			cp.Settings.APIKeys[k] = mask
		}
	}
	for i := range cp.Tools {
		if cp.Tools[i].APIKey != "" {
			cp.Tools[i].APIKey = mask
		}
	}
	for ai := range cp.Agents {
		for ti := range cp.Agents[ai].ToolsInline {
			if cp.Agents[ai].ToolsInline[ti].APIKey != "" {
				cp.Agents[ai].ToolsInline[ti].APIKey = mask
			}
		}
	}
	return &cp
}

// ============================================================
// VALIDATION
// ============================================================

// ValidationError describes a single config validation issue.
// Severity is "error" (blocks load) or "warning" (logged, does not block).
// Empty Severity defaults to "error" for backward compatibility.
type ValidationError struct {
	Field    string `json:"field"`
	Message  string `json:"message"`
	Severity string `json:"severity,omitempty"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// IsError returns true if the issue must block config load.
// Default (empty Severity) is treated as an error for backward compatibility
// with existing checks that predate the severity field.
func (e *ValidationError) IsError() bool {
	return e.Severity == "" || e.Severity == "error"
}

// IsWarning returns true if the issue is advisory only.
func (e *ValidationError) IsWarning() bool {
	return e.Severity == "warning"
}

// Validate checks the config for duplicate names, broken references, and
// structural issues. Returns a slice of all errors found (empty = valid).
func (c *Config) Validate() []*ValidationError {
	var errs []*ValidationError
	add := func(field, msg string) {
		errs = append(errs, &ValidationError{Field: field, Message: msg})
	}

	c.validateToolSecurity(add)

	// 1. Duplicate agent names
	agentNames := make(map[string]int)
	for i, a := range c.Agents {
		if a.Name == "" {
			add(fmt.Sprintf("agents[%d]", i), "agent has no name")
			continue
		}
		if prev, ok := agentNames[a.Name]; ok {
			add(fmt.Sprintf("agents[%d]", i), fmt.Sprintf("duplicate agent name %q (first at agents[%d])", a.Name, prev))
		}
		agentNames[a.Name] = i
	}

	// 2. Duplicate tool names
	toolNames := make(map[string]int)
	for i, t := range c.Tools {
		if t.Name == "" {
			continue
		}
		if prev, ok := toolNames[t.Name]; ok {
			add(fmt.Sprintf("tools[%d]", i), fmt.Sprintf("duplicate tool name %q (first at tools[%d])", t.Name, prev))
		}
		toolNames[t.Name] = i
	}

	// 3. Duplicate skill names
	skillNames := make(map[string]int)
	for i, s := range c.Skills {
		if s.Name == "" {
			continue
		}
		if prev, ok := skillNames[s.Name]; ok {
			add(fmt.Sprintf("skills[%d]", i), fmt.Sprintf("duplicate skill name %q (first at skills[%d])", s.Name, prev))
		}
		skillNames[s.Name] = i
	}

	// 4. Duplicate orchestrator names (across orchestrator + orchestrators)
	orchNames := make(map[string]string) // name → location
	if c.Orchestrator != nil && c.Orchestrator.Name != "" {
		orchNames[c.Orchestrator.Name] = "orchestrator"
	}
	for i, o := range c.Orchestrators {
		if o.Name == "" {
			add(fmt.Sprintf("orchestrators[%d]", i), "sub-orchestrator has no name")
			continue
		}
		if loc, ok := orchNames[o.Name]; ok {
			add(fmt.Sprintf("orchestrators[%d]", i), fmt.Sprintf("duplicate orchestrator name %q (first at %s)", o.Name, loc))
		}
		orchNames[o.Name] = fmt.Sprintf("orchestrators[%d]", i)
	}

	// Build set of all valid runner names (agents + sub-orchestrators)
	runners := make(map[string]bool)
	for name := range agentNames {
		runners[name] = true
	}
	for name := range orchNames {
		runners[name] = true
	}

	// 5. Orchestrator agents must reference valid runners
	if c.Orchestrator != nil {
		for _, ref := range c.Orchestrator.Agents {
			if !runners[ref] {
				add("orchestrator.agents", fmt.Sprintf("references unknown agent or sub-orchestrator %q", ref))
			}
		}
		// 6. Pipeline step agents must reference valid runners
		if c.Orchestrator.Pipeline != nil {
			validatePipelineSteps(c.Orchestrator.Pipeline.Steps, runners, "orchestrator.pipeline", &errs)
		}
	}
	for i, o := range c.Orchestrators {
		for _, ref := range o.Agents {
			if !runners[ref] {
				add(fmt.Sprintf("orchestrators[%d].agents", i), fmt.Sprintf("references unknown agent %q", ref))
			}
		}
		if o.Pipeline != nil {
			validatePipelineSteps(o.Pipeline.Steps, runners, fmt.Sprintf("orchestrators[%d].pipeline", i), &errs)
		}
	}

	// 7. Agent tool references must match defined tools
	for i, a := range c.Agents {
		for _, ref := range a.Tools {
			if _, ok := toolNames[ref]; !ok {
				add(fmt.Sprintf("agents[%d].tools", i), fmt.Sprintf("agent %q references unknown tool %q", a.Name, ref))
			}
		}
		for _, ref := range a.Skills {
			if _, ok := skillNames[ref]; !ok {
				add(fmt.Sprintf("agents[%d].skills", i), fmt.Sprintf("agent %q references unknown skill %q", a.Name, ref))
			}
		}
	}

	// 8. Warn when a role:supervisor agent exists alongside a Hierarchical
	// orchestrator. The Hierarchical strategy synthesizes its own supervisor
	// (named after the orchestrator) in internal/agent/orchestrator.go:117-128
	// and silently ignores user-declared role:supervisor agents. The user's
	// supervisor ends up a regular worker registered via delegation tools;
	// its system_prompt and per-agent settings are dropped. Not a hard error —
	// the config still runs — but a foot-gun worth flagging.
	hierStrategy := func(strategy string) bool { return strategy == "Hierarchical" }
	hasHierarchical := (c.Orchestrator != nil && hierStrategy(c.Orchestrator.Strategy))
	if !hasHierarchical {
		for _, o := range c.Orchestrators {
			if hierStrategy(o.Strategy) {
				hasHierarchical = true
				break
			}
		}
	}
	if hasHierarchical {
		for i, a := range c.Agents {
			if strings.EqualFold(string(a.Role), "supervisor") {
				errs = append(errs, &ValidationError{
					Field:    fmt.Sprintf("agents[%d].role", i),
					Message:  fmt.Sprintf("agent %q declares role=supervisor but a Hierarchical orchestrator is present — Hierarchical synthesizes its own supervisor from the orchestrator definition and will ignore this agent's system_prompt + settings. Either change role to worker, or switch orchestrator.strategy away from Hierarchical.", a.Name),
					Severity: "warning",
				})
			}
		}
	}

	return errs
}

// validatePipelineSteps checks that all step.agent references are valid runners.
func validatePipelineSteps(steps []PipelineStep, runners map[string]bool, prefix string, errs *[]*ValidationError) {
	for i, s := range steps {
		path := fmt.Sprintf("%s.steps[%d]", prefix, i)
		if s.Agent != "" && !runners[s.Agent] {
			*errs = append(*errs, &ValidationError{
				Field:   path,
				Message: fmt.Sprintf("step %q references unknown agent or sub-orchestrator %q", s.Name, s.Agent),
			})
		}
		if len(s.Steps) > 0 {
			validatePipelineSteps(s.Steps, runners, path, errs)
		}
		if s.ConditionAgent != "" && !runners[s.ConditionAgent] {
			*errs = append(*errs, &ValidationError{
				Field:   path + ".condition_agent",
				Message: fmt.Sprintf("step %q references unknown condition agent %q", s.Name, s.ConditionAgent),
			})
		}
		if s.RequireToolCall != nil && s.RequireToolCall.Tool == "" {
			*errs = append(*errs, &ValidationError{
				Field:   path + ".require_tool_call.tool",
				Message: fmt.Sprintf("step %q has require_tool_call but no tool name", s.Name),
			})
		}
		if gate := s.RequireToolCall; gate != nil {
			hasBound := gate.MinValue != nil || gate.MaxValue != nil
			if gate.OutputJSONPath != "" && !hasBound {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.output_json_path",
					Message: fmt.Sprintf("step %q sets output_json_path but neither min_value nor max_value", s.Name),
				})
			}
			if hasBound && gate.OutputJSONPath == "" {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.min_value",
					Message: fmt.Sprintf("step %q sets min_value/max_value but no output_json_path to extract", s.Name),
				})
			}
			if gate.MinValue != nil && gate.MaxValue != nil && *gate.MinValue > *gate.MaxValue {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.min_value",
					Message: fmt.Sprintf("step %q has min_value %v greater than max_value %v", s.Name, *gate.MinValue, *gate.MaxValue),
				})
			}
			// A NaN bound makes every comparison in checkOutputValueBounds
			// false (value < NaN and value > NaN are both always false),
			// silently disabling the gate — every value would pass — while
			// the config still looks fully configured. The min>max check
			// above can't catch this either, since NaN comparisons are
			// always false there too. Reject explicitly rather than let a
			// malformed YAML `.nan` bound quietly defeat a safety gate.
			if gate.MinValue != nil && math.IsNaN(*gate.MinValue) {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.min_value",
					Message: fmt.Sprintf("step %q has min_value NaN, which would make the gate accept every value", s.Name),
				})
			}
			if gate.MaxValue != nil && math.IsNaN(*gate.MaxValue) {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.max_value",
					Message: fmt.Sprintf("step %q has max_value NaN, which would make the gate accept every value", s.Name),
				})
			}
			// An infinite bound defeats the gate the same way NaN does:
			// `value < -Inf` and `value > +Inf` are always false for any
			// finite value, so `min_value: -.inf` or `max_value: .inf`
			// makes that side of the check a no-op while the config still
			// looks fully configured. There's no legitimate use for this —
			// omitting the field entirely (nil) already means "don't check
			// this side" — so reject explicitly, same as the NaN case.
			if gate.MinValue != nil && math.IsInf(*gate.MinValue, 0) {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.min_value",
					Message: fmt.Sprintf("step %q has min_value %v, which would make the gate accept every value on this side — omit min_value instead if no lower bound is wanted", s.Name, *gate.MinValue),
				})
			}
			if gate.MaxValue != nil && math.IsInf(*gate.MaxValue, 0) {
				*errs = append(*errs, &ValidationError{
					Field:   path + ".require_tool_call.max_value",
					Message: fmt.Sprintf("step %q has max_value %v, which would make the gate accept every value on this side — omit max_value instead if no upper bound is wanted", s.Name, *gate.MaxValue),
				})
			}
		}
	}
}

// ValidateYAML parses raw YAML and validates it without loading file references.
// Used by the /api/config/validate endpoint.
func ValidateYAML(yamlContent string) ([]*ValidationError, error) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	cfg.filterRefPlaceholders()
	return cfg.Validate(), nil
}

// ============================================================
// FILE REFERENCE RESOLUTION
// ============================================================

// fileRefExtensions are extensions that trigger auto-detection of file references.
var fileRefExtensions = []string{".md", ".txt", ".prompt"}

// resolveFileReferences replaces file references with file contents.
// Supports explicit "file:path" prefix and auto-detection by extension.
func (c *Config) resolveFileReferences(baseDir string) error {
	for i := range c.Agents {
		if err := resolveFileRef(baseDir, &c.Agents[i].SystemPrompt); err != nil {
			return fmt.Errorf("agent %q system_prompt: %w", c.Agents[i].Name, err)
		}
		if c.Agents[i].Settings != nil {
			if err := resolveFileRef(baseDir, &c.Agents[i].Settings.Reflection.Prompt); err != nil {
				return fmt.Errorf("agent %q reflection.prompt: %w", c.Agents[i].Name, err)
			}
			if err := resolveFileRef(baseDir, &c.Agents[i].Settings.GroundCheck.Prompt); err != nil {
				return fmt.Errorf("agent %q ground_check.prompt: %w", c.Agents[i].Name, err)
			}
		}
	}
	for i := range c.Skills {
		if err := resolveFileRef(baseDir, &c.Skills[i].PromptTemplate); err != nil {
			return fmt.Errorf("skill %q prompt_template: %w", c.Skills[i].Name, err)
		}
	}
	if c.Orchestrator != nil {
		if err := resolveFileRef(baseDir, &c.Orchestrator.SystemPrompt); err != nil {
			return fmt.Errorf("orchestrator system_prompt: %w", err)
		}
	}
	for i := range c.Orchestrators {
		if err := resolveFileRef(baseDir, &c.Orchestrators[i].SystemPrompt); err != nil {
			return fmt.Errorf("sub-orchestrator %q system_prompt: %w", c.Orchestrators[i].Name, err)
		}
	}
	for i := range c.Workflows {
		if c.Workflows[i].FinalSynthesis != nil {
			if err := resolveFileRef(baseDir, &c.Workflows[i].FinalSynthesis.Prompt); err != nil {
				return fmt.Errorf("workflow %q final_synthesis.prompt: %w", c.Workflows[i].Name, err)
			}
		}
	}
	return nil
}

// resolveFileRef resolves a file reference in a string field.
// Explicit "file:path" always loads the file (error if missing).
// Auto-detect: single-line value ending in .md/.txt/.prompt loads if file exists.
func resolveFileRef(baseDir string, field *string) error {
	if field == nil || *field == "" {
		return nil
	}
	val := *field

	// Explicit file: prefix
	if strings.HasPrefix(val, "file:") {
		return readFileInto(baseDir, strings.TrimPrefix(val, "file:"), field)
	}

	// Auto-detect: single-line value with known extension
	if !strings.Contains(val, "\n") && looksLikeFilePath(val) {
		absPath := filepath.Join(baseDir, val)
		if _, err := os.Stat(absPath); err == nil {
			return readFileInto(baseDir, val, field)
		}
	}

	return nil
}

// looksLikeFilePath checks if a value ends with a known text file extension.
func looksLikeFilePath(val string) bool {
	lower := strings.ToLower(val)
	for _, ext := range fileRefExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// fileRefRoots, when non-empty, confines file references (file: prefix and
// auto-detected prompt paths) to these directories. The CLI leaves it empty:
// a local config author can already read any file they own, and shipped
// examples use file:../_shared/... . `rakitsu serve` sets it, because there
// the config can come from a web request and a reference like
// file:../../home/me/.ssh/id_rsa would pull a server file into the prompt
// and the session snapshot.
var (
	fileRefRootsMu sync.RWMutex
	fileRefRoots   []string
)

// SetFileRefRoots confines file references to roots (symlinks resolved).
// An empty list removes the restriction.
func SetFileRefRoots(roots []string) {
	var resolved []string
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		resolved = append(resolved, abs)
	}
	fileRefRootsMu.Lock()
	fileRefRoots = resolved
	fileRefRootsMu.Unlock()
}

// checkFileRefAllowed returns an error if roots are set and path (after
// resolving symlinks) is not inside one of them.
func checkFileRefAllowed(path string) error {
	fileRefRootsMu.RLock()
	roots := fileRefRoots
	fileRefRootsMu.RUnlock()
	if len(roots) == 0 {
		return nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if rel, err := filepath.Rel(root, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("file reference %s is outside the allowed config directories", path)
}

// readFileInto reads a file and stores its contents in the target field.
func readFileInto(baseDir, relPath string, field *string) error {
	absPath := filepath.Join(baseDir, relPath)
	if err := checkFileRefAllowed(absPath); err != nil {
		return fmt.Errorf("reading %s: %w", relPath, err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", relPath, err)
	}
	*field = string(data)
	return nil
}

// filterRefPlaceholders removes entries that came from $ref YAML references
// but were not resolved (they have no name). Auto-discovery will add the real ones.
func (c *Config) filterRefPlaceholders() {
	filtered := c.Agents[:0]
	for _, a := range c.Agents {
		if a.Name != "" {
			filtered = append(filtered, a)
		}
	}
	c.Agents = filtered

	filteredTools := c.Tools[:0]
	for _, t := range c.Tools {
		if t.Name != "" {
			filteredTools = append(filteredTools, t)
		}
	}
	c.Tools = filteredTools

	filteredSkills := c.Skills[:0]
	for _, s := range c.Skills {
		if s.Name != "" {
			filteredSkills = append(filteredSkills, s)
		}
	}
	c.Skills = filteredSkills

	filteredOrchestrators := c.Orchestrators[:0]
	for _, o := range c.Orchestrators {
		if o.Name != "" {
			filteredOrchestrators = append(filteredOrchestrators, o)
		}
	}
	c.Orchestrators = filteredOrchestrators
}

// ============================================================
// DIRECTORY AUTO-DISCOVERY
// ============================================================

// autoDiscoverDefinitions scans conventional directories for agent, skill,
// and tool definitions. Inline definitions take precedence over discovered ones.
func (c *Config) autoDiscoverDefinitions(baseDir string) error {
	// Discover agents
	if err := c.discoverAgents(filepath.Join(baseDir, "agents")); err != nil {
		return err
	}
	// Discover skills
	if err := c.discoverSkills(filepath.Join(baseDir, "skills")); err != nil {
		return err
	}
	// Discover tools
	if err := c.discoverTools(filepath.Join(baseDir, "tools")); err != nil {
		return err
	}
	// Discover sub-orchestrators
	if err := c.discoverOrchestrators(filepath.Join(baseDir, "orchestrators")); err != nil {
		return err
	}
	return nil
}

func (c *Config) discoverAgents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // directory doesn't exist — not an error
	}
	for _, entry := range entries {
		if entry.IsDir() || !isDefinitionFile(entry.Name()) {
			continue
		}
		agent, err := parseAgentFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("agents/%s: %w", entry.Name(), err)
		}
		if c.GetAgent(agent.Name) == nil {
			c.Agents = append(c.Agents, *agent)
		}
	}
	return nil
}

func (c *Config) discoverSkills(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !isDefinitionFile(entry.Name()) {
			continue
		}
		skill, err := parseSkillFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("skills/%s: %w", entry.Name(), err)
		}
		if c.GetSkill(skill.Name) == nil {
			c.Skills = append(c.Skills, *skill)
		}
	}
	return nil
}

func (c *Config) discoverTools(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLDefinitionFile(entry.Name()) {
			continue
		}
		tool, err := parseToolFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("tools/%s: %w", entry.Name(), err)
		}
		if c.GetTool(tool.Name) == nil {
			c.Tools = append(c.Tools, *tool)
		}
	}
	return nil
}

func (c *Config) discoverOrchestrators(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // directory doesn't exist — not an error
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLDefinitionFile(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("orchestrators/%s: %w", entry.Name(), err)
		}
		var orch OrchestratorConfig
		if err := yaml.Unmarshal(data, &orch); err != nil {
			return fmt.Errorf("orchestrators/%s: %w", entry.Name(), err)
		}
		if orch.Name == "" {
			continue
		}
		// Skip if already defined inline
		found := false
		for _, existing := range c.Orchestrators {
			if existing.Name == orch.Name {
				found = true
				break
			}
		}
		if !found {
			c.Orchestrators = append(c.Orchestrators, orch)
		}
	}
	return nil
}

// isDefinitionFile returns true if the file has a supported definition
// extension for kinds that also support markdown with YAML front matter
// (agents, skills).
func isDefinitionFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml" || ext == ".md"
}

// isYAMLDefinitionFile returns true for YAML-only definition kinds (tools,
// orchestrators) — unlike agents/skills, their parsers don't understand
// markdown front matter, so a stray .md file must not be picked up here.
func isYAMLDefinitionFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// ============================================================
// FILE PARSERS
// ============================================================

func parseAgentFile(path string) (*AgentDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(path)) == ".md" {
		return parseMarkdownAgent(data)
	}
	var agent AgentDefinition
	if err := yaml.Unmarshal(data, &agent); err != nil {
		return nil, err
	}
	return &agent, nil
}

func parseMarkdownAgent(data []byte) (*AgentDefinition, error) {
	frontMatter, body, err := splitFrontMatter(data)
	if err != nil {
		return nil, err
	}
	var agent AgentDefinition
	if err := yaml.Unmarshal(frontMatter, &agent); err != nil {
		return nil, err
	}
	if agent.SystemPrompt == "" && len(body) > 0 {
		agent.SystemPrompt = strings.TrimSpace(string(body))
	}
	return &agent, nil
}

func parseSkillFile(path string) (*SkillDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(path)) == ".md" {
		return parseMarkdownSkill(data)
	}
	var skill SkillDefinition
	if err := yaml.Unmarshal(data, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

func parseMarkdownSkill(data []byte) (*SkillDefinition, error) {
	frontMatter, body, err := splitFrontMatter(data)
	if err != nil {
		return nil, err
	}
	var skill SkillDefinition
	if err := yaml.Unmarshal(frontMatter, &skill); err != nil {
		return nil, err
	}
	if skill.PromptTemplate == "" && len(body) > 0 {
		skill.PromptTemplate = strings.TrimSpace(string(body))
	}
	return &skill, nil
}

func parseToolFile(path string) (*ToolDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Tools are YAML-only (markdown doesn't make sense for tool definitions)
	var tool ToolDefinition
	if err := yaml.Unmarshal(data, &tool); err != nil {
		return nil, err
	}
	return &tool, nil
}

// splitFrontMatter splits "---\nyaml\n---\nbody" into front matter and body.
func splitFrontMatter(data []byte) ([]byte, []byte, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return nil, data, fmt.Errorf("no YAML front matter found (file must start with ---)")
	}
	rest := s[3:]
	// Skip optional newline after opening ---
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, data, fmt.Errorf("unterminated YAML front matter (missing closing ---)")
	}
	frontMatter := rest[:idx]
	body := rest[idx+4:] // skip \n---
	// Skip optional newline after closing ---
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}
	return []byte(frontMatter), []byte(body), nil
}
