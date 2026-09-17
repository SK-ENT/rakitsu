// Package userinput provides a tool that blocks for user input during agent execution.
// It is registered only in chat mode, enabling agents to ask clarifying questions mid-run.
package userinput

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// InputRequest is sent from the tool to the TUI/chat UI when the agent asks a
// question. The RequestID lets downstream consumers (debuggers) correlate
// incoming answers with the right pending request — the same ID is carried
// on the USER_INPUT_PENDING / USER_INPUT_ANSWERED telemetry events.
type InputRequest struct {
	RequestID string
	Question  string
	Default   string
}

// Tool implements the tools.Tool interface for interactive user input.
// Execute blocks on ResponseCh until the TUI sends a response or ctx is cancelled.
type Tool struct {
	RequestCh  chan<- InputRequest // send question to TUI
	ResponseCh <-chan string       // receive answer from TUI
	agentName  string              // reported in telemetry (optional)
	bus        *telemetry.EventBus // optional: emit pending/answered events
}

// NewTool creates a user_input tool with the given channels.
// agentName and bus may be empty/nil — they are used solely for emitting
// USER_INPUT_PENDING / USER_INPUT_ANSWERED events so debugger UIs can surface
// the pause state. The caller (chat TUI / ChatSession) owns channel lifecycle.
func NewTool(requestCh chan<- InputRequest, responseCh <-chan string, agentName string, bus *telemetry.EventBus) *Tool {
	return &Tool{
		RequestCh:  requestCh,
		ResponseCh: responseCh,
		agentName:  agentName,
		bus:        bus,
	}
}

func (t *Tool) GetName() string {
	return "user_input"
}

func (t *Tool) GetDescription() string {
	return "Ask the user a question and wait for their response. Use this when you need clarification, confirmation, or additional information from the user to proceed."
}

func (t *Tool) GetParametersSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user",
			},
			"default": map[string]interface{}{
				"type":        "string",
				"description": "Optional default value suggested to the user",
			},
		},
		"required": []string{"question"},
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	question, _ := args["question"].(string)
	if question == "" {
		return "", fmt.Errorf("question is required")
	}
	defaultVal, _ := args["default"].(string)

	// Stable request id links the pending event, the channel payload, and
	// the answer — the debugger's injection endpoint needs this to target
	// the correct waiter.
	reqID := uuid.New().String()

	if t.bus != nil {
		t.bus.Emit(t.agentName, telemetry.EventUserInputPending, telemetry.UserInputPendingPayload{
			RequestID: reqID,
			AgentName: t.agentName,
			Question:  question,
			Default:   defaultVal,
		})
	}

	select {
	case t.RequestCh <- InputRequest{RequestID: reqID, Question: question, Default: defaultVal}:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	select {
	case resp := <-t.ResponseCh:
		if t.bus != nil {
			t.bus.Emit(t.agentName, telemetry.EventUserInputAnswered, telemetry.UserInputAnsweredPayload{
				RequestID: reqID,
				AgentName: t.agentName,
				Source:    "chat",
			})
		}
		return resp, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
