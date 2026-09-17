package main

import (
	"context"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
	"github.com/SK-ENT/rakitsu/internal/tools/chathost"
	"github.com/SK-ENT/rakitsu/internal/tools/sessionmsg"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// buildChatHostAgent creates a synthetic conversational agent that wraps
// the underlying root Runner. It has a small static system prompt plus
// three discovery/delegation tools (invoke_config, list_agents,
// describe_agent). Token-efficient for deeply nested configs — the LLM
// only pays to learn about the parts it actually asks about.
func buildChatHostAgent(
	ctx context.Context,
	cfg *config.Config,
	root agent.Runner,
	eventBus *telemetry.EventBus,
	userInputReqCh chan userinput.InputRequest,
	userInputRespCh chan string,
	spawnToolFor func(parentName string, depth int) tools.Tool,
	sessionID string,
	hubURL string,
) (*agent.Agent, error) {
	// Register tools. When force_delegation is set, the discovery tools
	// (list_agents / describe_agent) are omitted so weak models can't be
	// distracted into "let me look around first" and then answer from
	// training — invoke_config becomes the only useful action.
	reg := tools.NewToolRegistry()
	reg.RegisterTool(chathost.NewInvokeTool(root, cfg.ForceDelegation))
	if !cfg.ForceDelegation {
		reg.RegisterTool(chathost.NewListAgentsTool(cfg))
		reg.RegisterTool(chathost.NewDescribeAgentTool(cfg))
	}
	if userInputReqCh != nil && userInputRespCh != nil {
		reg.RegisterTool(userinput.NewTool(userInputReqCh, userInputRespCh, "ChatHost", eventBus))
	}

	// spawn_agent — lets interactive chat fan out subagents directly. Counts
	// as a delegation signal under force_delegation (see turnguard.go).
	spawnRegistered := false
	if spawnToolFor != nil {
		if st := spawnToolFor("ChatHost", 0); st != nil {
			reg.RegisterTool(st)
			spawnRegistered = true
		}
	}

	// send_message / list_sessions — cross-session messaging, opt-in via
	// settings.session_msg (mirrors the auto-registration in
	// buildAgentToolRegistryDepth: this registry is hand-built, not built
	// through the runtime builder).
	sessionMsgTools := []tools.Tool(nil)
	if cfg.Settings.SessionMsg.Enabled {
		sessionMsgTools = sessionmsg.NewTools(sessionmsg.Deps{
			HubURL:    hubURL,
			SessionID: sessionID,
			AgentName: "ChatHost",
		})
		for _, t := range sessionMsgTools {
			reg.RegisterTool(t)
		}
	}

	// Small, scale-independent system prompt. The LLM discovers the
	// underlying system on demand via list_agents / describe_agent
	// (unless force_delegation is set, in which case the prompt mandates
	// invoke_config on every turn).
	systemPrompt := chatHostSystemPrompt(cfg)

	// Synthetic agent definition — lives only in memory, not from YAML.
	toolNames := []string{"invoke_config"}
	if !cfg.ForceDelegation {
		toolNames = append(toolNames, "list_agents", "describe_agent")
	}
	def := &config.AgentDefinition{
		Name:         "ChatHost",
		Role:         "worker",
		SystemPrompt: systemPrompt,
		Tools:        toolNames,
	}
	if userInputReqCh != nil {
		def.Tools = append(def.Tools, "user_input")
	}
	if spawnRegistered {
		def.Tools = append(def.Tools, "spawn_agent")
	}
	for _, t := range sessionMsgTools {
		def.Tools = append(def.Tools, t.GetName())
	}

	// Provider comes from config defaults.
	provider, err := createLLMProvider(ctx, cfg, cfg.Settings.DefaultProvider, cfg.Settings.Defaults.Model, nil)
	if err != nil {
		return nil, fmt.Errorf("chat-host needs settings.default_provider + settings.defaults.model: %w", err)
	}

	ep := createEmbeddingProvider(ctx, cfg, config.RetrievalConfig{})
	ag := agent.NewAgent(def, provider, reg, eventBus, ep)

	// Interim guard: under force_delegation, reject a turn that produced
	// no invoke_config call (one corrective retry, then a [NO-DELEGATION]
	// marker). The ChatHost registers no memory tools, so the storage-claim
	// check stays off here. No-op when force_delegation is unset.
	ag.SetTurnGuard(agent.NewTurnGuard(agent.TurnGuardConfig{
		ForceDelegation: cfg.ForceDelegation,
	}))

	// Apply the same retry + rate limiting the other agents get.
	rateLimiters := buildRateLimiters(cfg)
	wireAgentRetryAndRateLimit(ag, cfg, def, rateLimiters)

	return ag, nil
}

// chatHostSystemPrompt returns the small static prompt for the chat host.
// When cfg.ForceDelegation is set, a stricter variant is emitted that
// mandates invoke_config on every turn (a mitigation for weak models).
func chatHostSystemPrompt(cfg *config.Config) string {
	name := cfg.Name
	if name == "" {
		name = "this system"
	}
	description := cfg.Description
	if description == "" {
		description = "(no description provided)"
	}

	if cfg.ForceDelegation {
		return fmt.Sprintf(`You are the chat interface to %q.

About this system: %s

You have exactly one tool:
  - invoke_config(task):       Delegate the user's request to the underlying system.

Hard rules:
  - Every user turn MUST produce exactly one call to invoke_config. No exceptions.
  - Never answer from your own knowledge. The underlying system is the source of truth.
  - Never refuse to delegate, never summarize without delegating, never offer
    to answer "briefly" without the tool. If the user asks anything, call invoke_config.
  - Pass the user's request to invoke_config verbatim, or lightly rephrase it to
    match the system's expected input format. Do not answer in the same turn.
  - If the user greets you or asks meta questions ("who are you?"), still call
    invoke_config with the user's message — the underlying system handles it.`,
			name, description)
	}

	return fmt.Sprintf(`You are the chat interface to %q.

About this system: %s

You have four tools:
  - list_agents():             See the agents and orchestrators this config defines.
  - describe_agent(name):      Get the full role, system_prompt, and tools of one.
  - invoke_config(task):       Delegate substantive work to the underlying system.
  - user_input(question):      Ask the user for clarification if needed.

Behavior:
  - For greetings, chitchat, meta questions ("what can you do?", "who are you?"):
    reply directly using what you know from list_agents / describe_agent.
    Prefer short, natural answers.
  - For the actual work this system is designed for, call invoke_config with
    a clear task string. Rephrase the user's message if the underlying system
    expects a specific input format.
  - If you're unsure what the user wants, call user_input to ask.
  - Don't dump full internal structure at the user unless they ask for it.

Token-awareness: list_agents is cheap (names + one-line roles). describe_agent
is more expensive — use it only when you need specifics about one agent.`,
		name, description)
}
