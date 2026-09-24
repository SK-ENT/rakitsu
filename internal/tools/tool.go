// Package tools provides the Tool interface and implementations.
// Tools are capabilities that agents can use to interact with the world.
package tools

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Tool is the interface for all tools that agents can use.
// Tools accept structured arguments (map[string]interface{}) instead of raw strings
// for safety and ease of implementation. The execution engine is responsible for
// parsing the LLM's JSON output once before calling the tool.
type Tool interface {
	// GetName returns the tool's name
	GetName() string

	// GetDescription returns the tool's description for the LLM
	GetDescription() string

	// GetParametersSchema returns the JSON Schema for the tool's parameters
	// This is used to validate arguments and to generate the tool definition for the LLM
	GetParametersSchema() map[string]interface{}

	// Execute runs the tool with the given arguments.
	// The context allows for cancellation and timeout.
	// Arguments are already parsed from JSON into a structured map.
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

// ContentTool is an optional interface for a Tool whose result can carry
// non-text content (e.g. an image) next to its text. The agent loop calls
// ExecuteContent instead of Execute when a tool implements it, and passes
// the blocks to the model only when the agent has `vision: true`.
// Execute stays the text-only path for every other caller.
type ContentTool interface {
	ExecuteContent(ctx context.Context, args map[string]interface{}) (string, []llm.ContentBlock, error)
}

// TurnResetter is an optional interface a Tool may implement to clear
// per-user-turn state. The agent loop calls ResetTurn at the start of
// each Run/RunWithHistory — for the ChatHost agent that is exactly one
// user turn. Tools that hold no per-turn state need not implement it.
type TurnResetter interface {
	ResetTurn()
}

// turnQueryContextKey is an unexported type so this package's context key
// can't collide with a key from another package (standard Go context-key
// pattern).
type turnQueryContextKey struct{}

// turnQueryValue is what's actually stored under turnQueryContextKey.
// HasAttachments matters because Query alone is incomplete when the turn
// also carried attachments (e.g. --attach images): a tool that falls back
// to Query as if it were the whole turn would silently evaluate only the
// text and miss the attachment content entirely.
type turnQueryValue struct {
	Query          string
	HasAttachments bool
}

// WithTurnQuery returns a copy of ctx carrying the current turn's query
// text (and whether the turn also had attachments), so a tool's Execute
// can read it back via TurnQueryFromContext instead of requiring the
// calling LLM to retype content it already has as a tool-call argument
// (e.g. the jev tool's `state` argument — see internal/tools/jev/tool.go).
// context.Context values are immutable and scoped to this call chain, so —
// unlike a field stored on the Tool instance itself — this is safe when
// the same Tool is shared across concurrent agent runs (e.g. rakitsu serve
// handling overlapping sessions, or an orchestrator running parallel
// sub-agents): each run's own ctx carries its own query, with no shared
// mutable state to race on.
func WithTurnQuery(ctx context.Context, query string, hasAttachments bool) context.Context {
	return context.WithValue(ctx, turnQueryContextKey{}, turnQueryValue{Query: query, HasAttachments: hasAttachments})
}

// TurnQueryFromContext returns the current turn's query text set by
// WithTurnQuery, whether the turn also had attachments, and whether a
// value was set at all.
func TurnQueryFromContext(ctx context.Context) (query string, hasAttachments bool, ok bool) {
	v, ok := ctx.Value(turnQueryContextKey{}).(turnQueryValue)
	if !ok {
		return "", false, false
	}
	return v.Query, v.HasAttachments, true
}

// ToolExecutor manages tool registration and execution
type ToolExecutor interface {
	// RegisterTool registers a tool
	RegisterTool(tool Tool)

	// GetTool returns a tool by name
	GetTool(name string) Tool

	// GetAllTools returns all registered tools
	GetAllTools() []Tool

	// ExecuteToolCall executes a tool call from the LLM
	ExecuteToolCall(ctx context.Context, call ToolCall) (string, error)

	// GetToolDefinitions returns tool definitions in the format expected by the LLM
	GetToolDefinitions() []ToolDefinition
}

// ToolCall represents a tool call request
type ToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// ToolDefinition represents a tool definition for the LLM
type ToolDefinition struct {
	Type     string             `json:"type"` // always "function"
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition defines a function for the LLM
type FunctionDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolRegistry is a basic implementation of ToolExecutor. It is safe for
// concurrent use: rakitsu's own callers only ever mutate a registry during
// a serialized build phase before handing it to any reader, but this is a
// public exported type — an external caller has no way to know that
// invariant, so the map itself is mutex-protected rather than relying on it.
type ToolRegistry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	closers []io.Closer
}

// NewToolRegistry creates a new tool registry
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// AddCloser registers a closer to be called when CloseAll is invoked.
func (r *ToolRegistry) AddCloser(c io.Closer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closers = append(r.closers, c)
}

// CloseAll closes all registered closers (e.g., MCP subprocess connections).
func (r *ToolRegistry) CloseAll() {
	r.mu.Lock()
	closers := r.closers
	r.mu.Unlock()
	for _, c := range closers {
		if c == nil {
			continue
		}
		c.Close() //nolint:errcheck
	}
}

// RegisterTool registers a tool
func (r *ToolRegistry) RegisterTool(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	if _, exists := r.tools[tool.GetName()]; exists {
		log.Printf("tools: %q overwrites an already-registered tool of the same name", tool.GetName())
	}
	r.tools[tool.GetName()] = tool
}

// RemoveTool unregisters a tool by name. Removing a name that was never
// registered is a no-op. Used to build a restricted registry — e.g. a
// directed agent chat rebuilds an agent without spawn_agent.
func (r *ToolRegistry) RemoveTool(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// GetTool returns a tool by name
func (r *ToolRegistry) GetTool(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// GetAllTools returns all registered tools
func (r *ToolRegistry) GetAllTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ExecuteToolCall executes a tool call
func (r *ToolRegistry) ExecuteToolCall(ctx context.Context, call ToolCall) (string, error) {
	r.mu.RLock()
	tool, ok := r.tools[call.Name]
	var available []string
	if !ok {
		available = make([]string, 0, len(r.tools))
		for name := range r.tools {
			available = append(available, name)
		}
	}
	r.mu.RUnlock()
	if !ok {
		return "", &ToolNotFoundError{Name: call.Name, Available: available}
	}
	return tool.Execute(ctx, call.Arguments)
}

// GetToolDefinitions returns tool definitions for the LLM
func (r *ToolRegistry) GetToolDefinitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, ToolDefinition{
			Type: "function",
			Function: FunctionDefinition{
				Name:        tool.GetName(),
				Description: tool.GetDescription(),
				Parameters:  tool.GetParametersSchema(),
			},
		})
	}
	return definitions
}

// ToolNotFoundError is returned when a tool is not found
type ToolNotFoundError struct {
	Name      string
	Available []string
}

func (e *ToolNotFoundError) Error() string {
	if len(e.Available) > 0 {
		return fmt.Sprintf("tool not found: %s. Available tools: %s", e.Name, strings.Join(e.Available, ", "))
	}
	return "tool not found: " + e.Name
}

// ToolExecutionError is returned when a tool execution fails
type ToolExecutionError struct {
	Name    string
	Message string
	Cause   error
}

func (e *ToolExecutionError) Error() string {
	if e.Cause != nil {
		return "tool execution error [" + e.Name + "]: " + e.Message + ": " + e.Cause.Error()
	}
	return "tool execution error [" + e.Name + "]: " + e.Message
}

func (e *ToolExecutionError) Unwrap() error {
	return e.Cause
}
