package main

import (
	"context"
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools/userinput"
)

// buildChatRunner is the shared chat-runner factory used by both the CLI
// interactive path (cmd/rakitsu/interactive.go) and the server ChatManager
// (internal/server/chat_manager.go via NewChatBuildFunc).
//
// It calls BuildRunner, then applies the ChatHost overlay when appropriate
// (non-conversational configs get wrapped so the LLM has discovery tools).
// The returned agentName/modelLabel are used for session metadata + UI.
func buildChatRunner(
	ctx context.Context,
	cfg *config.Config,
	eventBus *telemetry.EventBus,
	userInputReqCh chan userinput.InputRequest,
	userInputRespCh chan string,
	sessionID string,
	selfURL string,
) (runner agent.Runner, innerRunner agent.Runner, cleanup func(), agentName string, modelLabel string, err error) {
	br, err := BuildRunner(ctx, cfg, eventBus, BuildOptions{
		UserInputReqCh:  userInputReqCh,
		UserInputRespCh: userInputRespCh,
		SessionID:       sessionID,
		HubURL:          selfURL,
	})
	if err != nil {
		return nil, nil, nil, "", "", err
	}

	// Decide whether to wrap the root Runner in a ChatHost meta-agent.
	// Default: wrap non-conversational configs (those with an orchestrator).
	// Single-agent configs are already conversational — use them directly.
	// Override with `interactive_overlay: true/false` in YAML.
	overlay := br.RootOrch != nil
	if cfg.InteractiveOverlay != nil {
		overlay = *cfg.InteractiveOverlay
	}

	runner = br.Runner
	innerRunner = br.Runner // preserved across overlay wrap so /model can reach sub-agents
	agentName = runner.GetName()
	modelLabel = "interactive"
	if br.RootOrch != nil {
		modelLabel = "orchestrator: " + cfg.Orchestrator.Strategy
	}

	if overlay {
		host, hostErr := buildChatHostAgent(ctx, cfg, br.Runner, eventBus, userInputReqCh, userInputRespCh, br.SpawnToolFor, sessionID, selfURL)
		if hostErr != nil {
			// Overlay construction can fail when the config's default_provider
			// has no credentials at runtime (the common case: a canvas built
			// with openai default but only LITELLM_API_KEY is set in env).
			// Rather than failing the whole chat session, fall back to
			// direct-runner mode — the agent's own provider still works for
			// regular turns. The operator loses the list_agents /
			// describe_agent discovery tools; emit a warning event so the UI
			// can surface this degraded mode.
			eventBus.Emit("system", telemetry.EventError, telemetry.ErrorPayload{
				Message: fmt.Sprintf("chat-host overlay unavailable (%v) — falling back to direct runner. Configure a valid default_provider in Builder Settings to restore discovery tools.", hostErr),
			})
			// leave runner / agentName / modelLabel as computed above
		} else {
			runner = host
			agentName = host.GetName()
			modelLabel = "chat-host (" + cfg.Settings.Defaults.Model + ")"
		}
	}

	return runner, innerRunner, br.Cleanup, agentName, modelLabel, nil
}

// chatBuildFunc adapts buildChatRunner to the server.ChatBuildFunc signature.
var chatBuildFunc server.ChatBuildFunc = buildChatRunner
