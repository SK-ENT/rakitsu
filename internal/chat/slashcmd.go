package chat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// SlashCommandContext is the dependency bundle a slash command handler needs
// in order to inspect and mutate agent state. Shared between the TUI Model
// and the server-side ChatSession so /model behaves identically in both
// surfaces.
//
// All fields are read at command-dispatch time, so a long-lived context can
// outlive individual turns. Nil-safe: a handler will report
// "model swap not supported in this build" when BuildLLM is nil.
type SlashCommandContext struct {
	// Runner is the chat's top-level runner. Typically *Agent (single-agent
	// mode or the ChatHost overlay) or *Orchestrator (no overlay).
	Runner agent.Runner

	// InnerRunner is the REAL root behind a ChatHost overlay — the actual
	// orchestrator whose sub-agents the user wants to address by name. Leave
	// nil (or equal to Runner) when there's no overlay; the lookup is dedup'd.
	InnerRunner agent.Runner

	// SingleAgent is the chat's primary agent (ChatHost in overlay mode, or
	// the user's single agent in single-agent mode). Nil in pure-orchestrator
	// mode without overlay.
	SingleAgent *agent.Agent

	// BuildLLM is the factory closure that instantiates a new LLM client for
	// /model swap. Captures cfg + ctx from the chat session's host. Nil
	// disables /model swap with a clear error message.
	BuildLLM func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error)

	// KnownProviders lists the YAML provider keys (e.g. "litellm",
	// "litellm-gemini-flash") used for `/model AGENT MODEL@PROVIDER` validation
	// and for clear error messages. Empty disables provider-name validation.
	KnownProviders []string
}

// HandleModelCommand is the surface-agnostic implementation of `/model`. Both
// the TUI Model.handleModelCommand and the server ChatSession's slash
// dispatcher call this and decide how to surface the returned reply text
// (TUI: append a system block; server: emit a `system` WS message).
//
//	/model                          → list all swappable agents and current models
//	/model AGENT                    → show that agent's current model
//	/model AGENT MODEL              → swap that agent's model (keeps current provider)
//	/model AGENT MODEL@PROVIDER     → swap both; PROVIDER must be in KnownProviders
func HandleModelCommand(c SlashCommandContext, args string) string {
	if c.BuildLLM == nil {
		return "model swap not supported in this build (BuildLLM not wired)"
	}

	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	switch len(parts) {
	case 0:
		return formatAllModels(c)
	case 1:
		ag, ok := lookupSwappableAgent(c, parts[0])
		if !ok {
			return unknownAgentMsg(c, parts[0])
		}
		return formatAgentStatus(ag)
	case 2:
		ag, ok := lookupSwappableAgent(c, parts[0])
		if !ok {
			return unknownAgentMsg(c, parts[0])
		}
		return swapAgentModel(c, ag, parts[1])
	default:
		return "usage: /model [<agent> [<model>[@<provider>]]]"
	}
}

// lookupSwappableAgent walks every reachable runner and returns the
// *agent.Agent matching name (case-sensitive). Returns (nil, false) when the
// name is unknown OR resolves to a *Orchestrator (not swappable in this
// release).
//
// Walk order (first match wins):
//   - c.SingleAgent  — the chat's primary agent (ChatHost in overlay mode,
//     or the user's single agent in single-agent mode)
//   - c.Runner       — if it's an *Orchestrator, look up its sub-agents
//   - c.InnerRunner  — the REAL root behind ChatHost in overlay mode; this is
//     where Coder/Tester/Auditor live in scenario-14-shaped
//     configs. Without this branch, /model in overlay mode
//     would only ever see ChatHost itself.
func lookupSwappableAgent(c SlashCommandContext, name string) (*agent.Agent, bool) {
	if c.SingleAgent != nil && c.SingleAgent.GetName() == name {
		return c.SingleAgent, true
	}
	if a, ok := orchSubAgent(c.Runner, name); ok {
		return a, true
	}
	if a, ok := orchSubAgent(c.InnerRunner, name); ok {
		return a, true
	}
	return nil, false
}

// orchSubAgent type-asserts r to *Orchestrator and looks up a sub-agent by
// name. Returns (nil, false) for non-orchestrator runners, absent names, or
// sub-runners that aren't leaf *Agent (e.g. nested orchestrators).
func orchSubAgent(r agent.Runner, name string) (*agent.Agent, bool) {
	orch, ok := r.(*agent.Orchestrator)
	if !ok {
		return nil, false
	}
	sub, ok := orch.GetAgent(name)
	if !ok {
		return nil, false
	}
	a, ok := sub.(*agent.Agent)
	return a, ok
}

// availableAgentNames returns sorted, deduplicated names of agents the user
// could pass to /model. Walks the same sources as lookupSwappableAgent.
func availableAgentNames(c SlashCommandContext) []string {
	seen := make(map[string]struct{})
	add := func(n string) {
		if _, dup := seen[n]; !dup {
			seen[n] = struct{}{}
		}
	}
	if c.SingleAgent != nil {
		add(c.SingleAgent.GetName())
	}
	if orch, ok := c.Runner.(*agent.Orchestrator); ok {
		for _, n := range orch.AgentNames() {
			add(n)
		}
	}
	if orch, ok := c.InnerRunner.(*agent.Orchestrator); ok {
		for _, n := range orch.AgentNames() {
			add(n)
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// formatAllModels renders the system reply for `/model` with no args.
func formatAllModels(c SlashCommandContext) string {
	names := availableAgentNames(c)
	if len(names) == 0 {
		return "no swappable agents in this session"
	}
	var b strings.Builder
	b.WriteString("Current models (use `/model <agent> <model>` to swap):\n")
	for _, n := range names {
		ag, ok := lookupSwappableAgent(c, n)
		if !ok {
			continue
		}
		b.WriteString("  ")
		b.WriteString(formatAgentStatus(ag))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatAgentStatus renders one agent's current model + provider as a single
// line, used by both `/model` (no args) and `/model AGENT`.
func formatAgentStatus(ag *agent.Agent) string {
	provider := ag.ProviderName()
	if provider == "" {
		provider = "(default)"
	}
	model := ag.Model()
	if model == "" {
		model = ag.LLMProvider().GetModel()
		if model == "" {
			model = "(unknown)"
		}
	}
	return fmt.Sprintf("%-12s : %s (%s)", ag.GetName(), model, provider)
}

// unknownAgentMsg renders the "no agent named X" error with a list of valid names.
func unknownAgentMsg(c SlashCommandContext, name string) string {
	names := availableAgentNames(c)
	if len(names) == 0 {
		return fmt.Sprintf("no agent named %q (no swappable agents in this session)", name)
	}
	return fmt.Sprintf("no agent named %q. available: %s", name, strings.Join(names, ", "))
}

// swapAgentModel parses the model spec (model[@provider]), validates the
// provider against KnownProviders if set, builds a new LLM client via
// BuildLLM, and calls Agent.SetLLMProvider. On failure, returns a reply
// string describing the error and leaves the agent untouched.
func swapAgentModel(c SlashCommandContext, ag *agent.Agent, spec string) string {
	newModel, newProvider := ParseModelSpec(spec)
	if newModel == "" {
		return "model name cannot be empty"
	}

	// Determine which provider to use for the new LLM client:
	//   - explicit @provider wins
	//   - otherwise keep the agent's existing provider (so `/model Coder gemini-3.1-flash-lite`
	//     stays on whatever provider Coder was already using)
	if newProvider == "" {
		newProvider = ag.ProviderName()
	}
	if newProvider != "" && len(c.KnownProviders) > 0 {
		found := false
		for _, p := range c.KnownProviders {
			if p == newProvider {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf(
				"unknown provider %q. configured providers: %s",
				newProvider, strings.Join(c.KnownProviders, ", "),
			)
		}
	}

	llmProv, err := c.BuildLLM(newProvider, newModel, nil)
	if err != nil {
		return fmt.Sprintf("model swap failed: %v", err)
	}
	oldModel := ag.Model()
	ag.SetLLMProvider(llmProv, newProvider, newModel)
	providerLabel := newProvider
	if providerLabel == "" {
		providerLabel = "(default)"
	}
	return fmt.Sprintf(
		"swapped %s model: %s → %s [%s] (session only — restart reverts to YAML)",
		ag.GetName(), oldModel, newModel, providerLabel,
	)
}

// ParseModelSpec splits "model[@provider]" into (model, provider). Whitespace
// around the @ is tolerated. Returns ("","") for empty input. Exported so
// both the TUI and server-side handlers can share the same parser.
func ParseModelSpec(spec string) (model, provider string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ""
	}
	if i := strings.LastIndex(spec, "@"); i >= 0 {
		return strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
	}
	return spec, ""
}
