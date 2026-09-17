package server

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/agent"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// stubLLM is a no-op LLM used only to exercise the swap path. The /model
// command never invokes Generate during a swap — it only builds a new client
// — so an empty Generate is sufficient.
type stubLLM struct{ name, model string }

func (s *stubLLM) Generate(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return &llm.GenerateResult{Response: "ok", FinishReason: "stop"}, nil
}
func (s *stubLLM) GetName() string  { return s.name }
func (s *stubLLM) GetModel() string { return s.model }

// TestHandleSlashCommand_NotASlashCommand verifies the (reply, handled) shape:
// non-slash input returns ("", false) so the WS dispatcher falls through to
// the normal Submit path.
func TestHandleSlashCommand_NotASlashCommand(t *testing.T) {
	s := &ChatSession{} // zero value is fine — no fields needed for the fall-through case
	reply, handled := s.HandleSlashCommand("hello there")
	if handled {
		t.Errorf("plain text classified as slash command: reply=%q handled=true", reply)
	}
	if reply != "" {
		t.Errorf("plain text returned non-empty reply %q", reply)
	}
}

// TestHandleSlashCommand_ModelListAndSwap drives /model end-to-end against a
// real *agent.Agent backed by a stub LLM. Confirms:
//   - /model lists the runner agent
//   - /model AGENT MODEL swaps the LLM and reports it
//   - The agent's Model() reflects the swap on the next read
func TestHandleSlashCommand_ModelListAndSwap(t *testing.T) {
	// Build a single-agent session: runner == an *agent.Agent named "Coder".
	def := &config.AgentDefinition{
		Name:         "Coder",
		Role:         "worker",
		Provider:     "litellm",
		Model:        "nemotron-30b",
		SystemPrompt: "test",
	}
	ag := agent.NewAgent(def, &stubLLM{name: "openai-compatible", model: "nemotron-30b"}, nil, nil, nil)

	sess := &ChatSession{
		runner:         ag,
		innerRunner:    ag,
		knownProviders: []string{"litellm", "litellm-gemini-flash"},
		buildLLM: func(providerName, model string, mc *config.ModelConfig) (llm.LLMProvider, error) {
			return &stubLLM{name: "openai-compatible", model: model}, nil
		},
	}

	// /model — should list Coder
	reply, handled := sess.HandleSlashCommand("/model")
	if !handled {
		t.Fatalf("/model not handled")
	}
	if !strings.Contains(reply, "Coder") || !strings.Contains(reply, "nemotron-30b") {
		t.Errorf("/model listing missing expected fields. got:\n%s", reply)
	}

	// /model Coder gemini-3.1-flash-lite — should swap
	reply, handled = sess.HandleSlashCommand("/model Coder gemini-3.1-flash-lite")
	if !handled {
		t.Fatalf("/model swap not handled")
	}
	if !strings.Contains(reply, "swapped Coder") || !strings.Contains(reply, "gemini-3.1-flash-lite") {
		t.Errorf("/model swap reply missing expected fields. got:\n%s", reply)
	}
	if got := ag.Model(); got != "gemini-3.1-flash-lite" {
		t.Errorf("agent.Model() after swap = %q; want %q", got, "gemini-3.1-flash-lite")
	}
}

// TestHandleSlashCommand_UnknownCommand verifies graceful handling of an
// unknown /verb — handled=true (don't fall through to Submit) with a helpful
// reply. Matches TUI behavior.
func TestHandleSlashCommand_UnknownCommand(t *testing.T) {
	sess := &ChatSession{}
	reply, handled := sess.HandleSlashCommand("/banana split")
	if !handled {
		t.Errorf("unknown slash command not handled (would forward to agent)")
	}
	if !strings.Contains(reply, "Unknown command") || !strings.Contains(reply, "/banana") {
		t.Errorf("unknown-command reply missing expected fields. got: %q", reply)
	}
}
