// Package telemetry provides event bus and structured event types for
// real-time observability of agent execution.
package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// EventType defines all possible event types
type EventType string

const (
	EventAgentStart        EventType = "AGENT_START"
	EventAgentEnd          EventType = "AGENT_END"
	EventThoughtStart      EventType = "THOUGHT_START"
	EventThoughtEnd        EventType = "THOUGHT_END"
	EventToolCallStart     EventType = "TOOL_CALL_START"
	EventToolCallEnd       EventType = "TOOL_CALL_END"
	EventAgentHandoff      EventType = "AGENT_HANDOFF"
	EventAgentMessage      EventType = "AGENT_MESSAGE"
	EventTokenUsage        EventType = "TOKEN_USAGE"
	EventError             EventType = "ERROR"
	EventExecutionComplete EventType = "EXECUTION_COMPLETE"
	EventReflectionStart   EventType = "REFLECTION_START"
	EventReflectionEnd     EventType = "REFLECTION_END"
	EventGroundCheckStart  EventType = "GROUND_CHECK_START"
	EventGroundCheckEnd    EventType = "GROUND_CHECK_END"
	EventPipelineStart     EventType = "PIPELINE_START"
	EventPipelineEnd       EventType = "PIPELINE_END"
	EventPipelineStepStart EventType = "PIPELINE_STEP_START"
	EventPipelineStepEnd   EventType = "PIPELINE_STEP_END"
	EventDebugPaused       EventType = "DEBUG_PAUSED"
	EventDebugResumed      EventType = "DEBUG_RESUMED"
	EventTokenChunk        EventType = "TOKEN_CHUNK"
	EventReplayStart       EventType = "REPLAY_START"
	EventReplayEnd         EventType = "REPLAY_END"
	EventRetryAttempt      EventType = "RETRY_ATTEMPT"
	EventContextCompressed EventType = "CONTEXT_COMPRESSED"
	EventRetrievalInjected EventType = "RETRIEVAL_INJECTED"
	EventFormatSelected    EventType = "FORMAT_SELECTED"
	EventSalvagedOutput    EventType = "SALVAGED_OUTPUT"
	// EventWorkerRedelegationBlocked is emitted by an orchestrator's
	// DelegationTool when the supervisor attempted to delegate to a worker
	// that already returned salvaged content in this Run and the per-worker
	// post-salvage cap has been reached. The tool returns a [DELEGATION
	// BLOCKED] message string to the supervisor instead of calling the
	// worker; this event records the decision for the trace output.
	EventWorkerRedelegationBlocked EventType = "WORKER_REDELEGATION_BLOCKED"

	// EventModelChanged is emitted when an agent's LLM provider+model is swapped
	// at runtime (e.g. via the `/model` slash command). Lets session JSONLs
	// retain a clear audit trail of mid-session model swaps.
	EventModelChanged EventType = "MODEL_CHANGED"
	// EventReasoningChunk is emitted for each streaming reasoning_content delta
	// (peer to EventTokenChunk). For reasoning models the answer-stream gate
	// (provider.go: `if surface != ""`) suppresses internal chain-of-thought
	// from the visible TOKEN_CHUNK stream so it doesn't pollute the answer.
	// REASONING_CHUNK gives operators a separate, dimmed view of that activity
	// so a long reasoning phase doesn't look identical to a stalled connection.
	EventReasoningChunk EventType = "REASONING_CHUNK"
	// EventUserInputPending is emitted when an agent calls the user_input tool
	// and blocks waiting for a human answer. Frontend debuggers can surface
	// this as a wait state and prompt the user for a response.
	EventUserInputPending EventType = "USER_INPUT_PENDING"
	// EventUserInputAnswered is emitted after the user_input tool receives
	// its answer (from the chat UI or the debugger injection endpoint).
	EventUserInputAnswered EventType = "USER_INPUT_ANSWERED"
	// EventChatTurnStart is emitted at the beginning of each chat turn.
	// Lets the debug tree group a whole turn's events under one container node
	// instead of every turn showing as a separate top-level agent call.
	EventChatTurnStart EventType = "CHAT_TURN_START"
	// EventChatTurnEnd is emitted when the runner returns from a chat turn —
	// whether with a final answer, cancellation, or an error.
	EventChatTurnEnd EventType = "CHAT_TURN_END"
	// EventSessionEnd is the final event written to a session JSONL stream by
	// SessionStore.EndSession. Consumers reading the JSONL alone (without the
	// sessions.json index) can determine final status from this last line —
	// the line-1 SessionMeta stays Status:"running" forever per design (JSONL
	// is append-only).
	//
	// JSONL-only: this event is written directly to the session file, not
	// published to the EventBus / SSE stream. Live SSE consumers will never
	// observe it. SessionStore.GetSessionEvents filters it out so the count of
	// returned events matches SessionEndPayload.TotalEvents.
	EventSessionEnd EventType = "SESSION_END"
	// EventRollback is emitted when an agent performs a runtime self-correction:
	// it detected a dead-end iteration (per config.RollbackConfig triggers) and
	// rewound its conversation history to before that iteration so the failed
	// attempt's tool errors do not pollute the LLM context. The retry continues
	// from the checkpoint. Hidden from the chat surface; surfaced in the debug
	// tree as the "Agent IDE" payoff — operators see exactly where and why the
	// agent rewound.
	EventRollback EventType = "ROLLBACK"
	// EventMemoryWrite is emitted when an agent writes to the native memory /
	// knowledge-graph store (memory_add upsert or memory_link edge). Gives
	// session JSONLs an audit trail of what the agent chose to remember.
	EventMemoryWrite EventType = "MEMORY_WRITE"
	// EventMemoryRecall is emitted when an agent reads from the native memory
	// store (memory_query / memory_get / memory_list), recording the query and
	// which node IDs were surfaced back into the LLM context.
	EventMemoryRecall EventType = "MEMORY_RECALL"
	// EventDirectChatStart is emitted when the user sends a message to one
	// agent directly, outside the main conversation (directed agent chat).
	// Deliberately NOT reusing AGENT_START: the subagent-block renderer
	// draws every AGENT_START as a new spawn in the main transcript, which
	// a side chat is not.
	EventDirectChatStart EventType = "DIRECT_CHAT_START"
	// EventDirectChatEnd is emitted when a directed agent chat turn settles,
	// whether it produced a reply, errored, or was rejected (busy, still
	// running, unknown agent).
	EventDirectChatEnd EventType = "DIRECT_CHAT_END"
	// EventSessionMsgSent is emitted by the serve endpoint whenever any
	// sender (send_message tool, CLI, web UI) attempts a cross-session
	// message delivery, with the resulting status. One event per attempt,
	// including rejected ones — the delivery decision is the audit trail.
	EventSessionMsgSent EventType = "SESSION_MSG_SENT"
	// EventSessionMsgReceived is emitted by the RECEIVING session when it
	// accepts a cross-session message into its turn queue (web chat) or
	// consumes it from the hub command poll (interactive TUI).
	EventSessionMsgReceived EventType = "SESSION_MSG_RECEIVED"
	// EventMediaAttached is emitted once per file loaded via --attach or
	// returned by a tool to a vision agent (Via tells which). The
	// audit trail for "what non-text content actually entered this run" —
	// session replay / debug tree can show which turn had which
	// attachments. Later phases (attach_media builtin tool, chat drag-drop)
	// will emit the same event from their own ingress points.
	EventMediaAttached EventType = "MEDIA_ATTACHED"
)

// SessionEndStatus is the final-state value carried in SessionEndPayload.
// Mirrors store.SessionStatus minus the in-flight "running" state, which is
// invariant: an EndSession call cannot terminate to "running".
type SessionEndStatus string

const (
	SessionEndSuccess SessionEndStatus = "success"
	SessionEndError   SessionEndStatus = "error"
	SessionEndTimeout SessionEndStatus = "timeout"
	SessionEndStale   SessionEndStatus = "stale"
)

// AgentEvent is the base event structure
type AgentEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	AgentName string    `json:"agent_name"`
	EventType EventType `json:"event_type"`
	Iteration int       `json:"iteration,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
	// Origin correlates every event produced by one directed agent chat turn
	// (Roster.Send), including its own DIRECT_CHAT_START/END pair. Empty means
	// the main run. AgentName cannot answer this — the same agent can be
	// delegated to by the main run and addressed in a side thread at the same
	// time — so consumers that must tell the two apart filter on this.
	Origin     string          `json:"origin,omitempty"`
	ReplayID   string          `json:"replay_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	TokenUsage *TokenUsage     `json:"token_usage,omitempty"`
	Duration   int64           `json:"duration_ms,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ============================================================
// STRUCTURED PAYLOAD TYPES
// These enable the frontend to render rich, structured visualizations
// ============================================================

// StructuredThought captures the LLM's reasoning process
type StructuredThought struct {
	Reasoning         string              `json:"reasoning"`
	Plan              []string            `json:"plan,omitempty"`
	IntendedToolCalls []ToolCallSignature `json:"intended_tool_calls,omitempty"`
	Confidence        float64             `json:"confidence,omitempty"`
	Alternatives      []string            `json:"alternatives,omitempty"`
}

// ToolCallSignature represents a planned tool call
type ToolCallSignature struct {
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Reason    string                 `json:"reason,omitempty"`
}

// ThoughtStartPayload is emitted before LLM call
type ThoughtStartPayload struct {
	Iteration      int      `json:"iteration"`
	HistoryLength  int      `json:"history_length"`
	AvailableTools []string `json:"available_tools"`
}

// ThoughtEndPayload is emitted after LLM response
type ThoughtEndPayload struct {
	StructuredThought
	Iteration    int    `json:"iteration"`
	Model        string `json:"model"`
	FinishReason string `json:"finish_reason"`
}

// ToolCallStartPayload is emitted before tool execution
type ToolCallStartPayload struct {
	ToolCallID  string                 `json:"tool_call_id"`
	ToolName    string                 `json:"tool_name"`
	Arguments   map[string]interface{} `json:"arguments"`
	SandboxType string                 `json:"sandbox_type,omitempty"`
}

// ToolCallEndPayload is emitted after tool execution
type ToolCallEndPayload struct {
	ToolCallID      string `json:"tool_call_id"`
	ToolName        string `json:"tool_name"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"output_truncated"`
	Error           string `json:"error,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
}

// AgentHandoffPayload is emitted when supervisor delegates to worker
type AgentHandoffPayload struct {
	FromAgent      string `json:"from_agent"`
	ToAgent        string `json:"to_agent"`
	Task           string `json:"task"`         // raw step task (original query)
	FullContext    string `json:"full_context"` // enriched task with prior step results injected
	ContextSize    int    `json:"context_size"`
	PriorStepCount int    `json:"prior_step_count"` // number of prior step results included
}

// AgentMessagePayload is emitted for inter-agent communication
type AgentMessagePayload struct {
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Message   string `json:"message"`
	Type      string `json:"type"` // "task", "result", "query"
}

// AgentStartPayload is emitted when an agent begins processing
type AgentStartPayload struct {
	Role         string   `json:"role"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Tools        []string `json:"tools"`
	Skills       []string `json:"skills,omitempty"`
	ParentAgent  string   `json:"parent_agent,omitempty"` // spawning agent's instance name; set only for runtime-spawned children
}

// AgentEndPayload is emitted when an agent finishes
type AgentEndPayload struct {
	Status       string  `json:"status"` // "success", "error", "max_iterations", "budget_exceeded"
	FinalAnswer  string  `json:"final_answer,omitempty"`
	Iterations   int     `json:"iterations"`
	TotalTokens  int     `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost,omitempty"`    // accumulated cost in USD
	MaxTokens    int     `json:"max_tokens,omitempty"`    // configured token budget
	MaxCost      float64 `json:"max_cost,omitempty"`      // configured cost budget
	PricingKnown bool    `json:"pricing_known,omitempty"` // true iff settings.pricing (or a built-in default) had an entry for this model
}

// DirectChatStartPayload is emitted when a directed agent chat turn begins.
// Kind is the roster kind of the addressed agent: host | config | spawned.
type DirectChatStartPayload struct {
	Agent   string `json:"agent"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// DirectChatEndPayload is emitted when a directed agent chat turn settles.
// Status is "success", "error", or "rejected"; Error is empty on success.
// Kind is the roster kind of the addressed agent: host | config | spawned |
// unknown — "unknown" appears only on a rejection where no kind could be
// resolved at all (the roster is disabled, has no builder wired, or the name
// doesn't match any entry), never as a blank string.
type DirectChatEndPayload struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Reply  string `json:"reply,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// SessionMsgSentPayload records a cross-session message delivery attempt.
// Status is delivered | busy | not_found | rate_limited | rejected | mailboxed. Text is
// truncated (chat.TruncateSessionMsg) so full message bodies don't spill
// into the event stream — the receiving session's transcript holds the
// authoritative text.
type SessionMsgSentPayload struct {
	ToSessionID   string `json:"to_session_id"`
	FromSessionID string `json:"from_session_id,omitempty"`
	FromName      string `json:"from_name,omitempty"`
	Text          string `json:"text"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

// SessionMsgReceivedPayload records a cross-session message accepted by the
// receiving session. From fields are sender-claimed (unverified). Text is
// truncated like SessionMsgSentPayload.
type SessionMsgReceivedPayload struct {
	FromSessionID string `json:"from_session_id,omitempty"`
	FromName      string `json:"from_name,omitempty"`
	Text          string `json:"text"`
	// Status distinguishes steerable-one-shot handling (spec §7):
	// "injected" = drained into the ReAct loop mid-run,
	// "dropped_overflow" = the queue was full and this message evicted an
	// older one still waiting to be drained (mid-run),
	// "dropped_at_exit" = the run ended before draining it.
	// Empty = ordinary chat-session delivery (pre-existing shape).
	Status string `json:"status,omitempty"`
}

// MediaAttachedPayload records one file that entered a run as non-text
// content: via --attach, or returned by a tool (e.g. fs read_image) to a
// vision agent.
type MediaAttachedPayload struct {
	Modality  string `json:"modality"`      // "image" — the only value this phase produces
	Via       string `json:"via,omitempty"` // "cli_attach" | "tool:<tool name>"
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	MIMEType  string `json:"mime_type"`
}

// ModelChangedPayload is emitted when an agent's LLM provider+model is swapped
// at runtime. AgentName duplicates the outer AgentEvent.AgentName so the
// payload is self-contained for downstream consumers.
type ModelChangedPayload struct {
	AgentName   string `json:"agent_name"`
	OldModel    string `json:"old_model"`
	OldProvider string `json:"old_provider,omitempty"`
	NewModel    string `json:"new_model"`
	NewProvider string `json:"new_provider,omitempty"`
}

// ErrorPayload is emitted when an error occurs
type ErrorPayload struct {
	ErrorType   string `json:"error_type"`
	Message     string `json:"message"`
	StackTrace  string `json:"stack_trace,omitempty"`
	Recoverable bool   `json:"recoverable"`
}

// RetryAttemptPayload is emitted when an LLM call is retried after a transient error.
type RetryAttemptPayload struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	Error       string `json:"error"`
	Iteration   int    `json:"iteration"`
}

// SessionEndPayload is the body of the SESSION_END event written as the last
// line of a session JSONL stream. Mirrors the final fields written to
// sessions.json so consumers reading only the JSONL get an authoritative
// verdict without a second file lookup.
//
// TotalEvents counts the agent execution events emitted during the session
// (everything written via SessionStore.WriteEvent). The SESSION_END marker
// itself is excluded; SessionStore.GetSessionEvents also excludes it, so
// `len(GetSessionEvents(id)) == payload.TotalEvents`.
type SessionEndPayload struct {
	Status      SessionEndStatus `json:"status"`
	TotalEvents int              `json:"total_events"`
	TotalTokens int              `json:"total_tokens"`
	DurationMs  int64            `json:"duration_ms"`
}

// ReflectionStartPayload is emitted before agent self-reflection
type ReflectionStartPayload struct {
	Iteration int    `json:"iteration"`
	Mode      string `json:"mode"` // "after_tool" or "before_answer"
}

// ReflectionEndPayload is emitted after agent self-reflection
type ReflectionEndPayload struct {
	Iteration  int    `json:"iteration"`
	Mode       string `json:"mode"`
	Reflection string `json:"reflection"`
}

// GroundCheckStartPayload is emitted before ground-check validation
type GroundCheckStartPayload struct {
	Iteration int `json:"iteration"`
}

// GroundCheckEndPayload is emitted after ground-check validation
type GroundCheckEndPayload struct {
	Iteration  int      `json:"iteration"`
	IsValid    bool     `json:"is_valid"`
	Confidence float64  `json:"confidence"`
	Issues     []string `json:"issues,omitempty"`
	Action     string   `json:"action"` // "proceed", "retry", "fail"
	Assessment string   `json:"assessment,omitempty"`
}

// PipelineStepInfo describes a pipeline step for pre-rendering in the UI.
type PipelineStepInfo struct {
	Name   string             `json:"name"`
	Type   string             `json:"type"` // "sequential", "parallel", "loop"
	Agents []string           `json:"agents,omitempty"`
	Steps  []PipelineStepInfo `json:"steps,omitempty"` // nested sub-steps (for parallel)
}

// PipelineStartPayload is emitted when a pipeline begins
type PipelineStartPayload struct {
	StepCount int                `json:"step_count"`
	Query     string             `json:"query"`
	Steps     []PipelineStepInfo `json:"steps,omitempty"`
}

// PipelineEndPayload is emitted when a pipeline completes
type PipelineEndPayload struct {
	Status   string `json:"status"` // "success", "error"
	StepsRun int    `json:"steps_run"`
}

// PipelineStepStartPayload is emitted when a pipeline step begins
type PipelineStepStartPayload struct {
	StepName string   `json:"step_name"`
	StepType string   `json:"step_type"` // "sequential", "parallel", "loop"
	Agents   []string `json:"agents,omitempty"`
}

// PipelineStepEndPayload is emitted when a pipeline step completes
type PipelineStepEndPayload struct {
	StepName  string `json:"step_name"`
	StepType  string `json:"step_type"`
	Status    string `json:"status"` // "success", "error"
	Output    string `json:"output,omitempty"`
	Iteration int    `json:"iteration,omitempty"` // for loop steps
}

// DebugPendingToolInfo describes a tool call about to be executed (for debug inspection).
type DebugPendingToolInfo struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// DebugPausedPayload is emitted when execution pauses at a breakpoint
type DebugPausedPayload struct {
	AgentName  string `json:"agent_name"`
	Checkpoint string `json:"checkpoint"`
	Iteration  int    `json:"iteration"`
	Reason     string `json:"reason"` // "breakpoint" or "step"
	// Inspection context (populated when CheckpointContext is provided)
	LastThought         string                 `json:"last_thought,omitempty"`
	PendingTools        []DebugPendingToolInfo `json:"pending_tools,omitempty"`
	HistoryLength       int                    `json:"history_length,omitempty"`
	TotalTokensIn       int                    `json:"total_tokens_in,omitempty"`
	TotalTokensOut      int                    `json:"total_tokens_out,omitempty"`
	MaxTokens           int                    `json:"max_tokens,omitempty"`
	MaxIterations       int                    `json:"max_iterations,omitempty"`
	ContextWindow       int                    `json:"context_window,omitempty"`
	PredictedNextTokens int                    `json:"predicted_next_tokens,omitempty"`
	BudgetRatio         float64                `json:"budget_ratio,omitempty"`
}

// DebugResumedPayload is emitted when execution resumes after a pause
type DebugResumedPayload struct {
	Action string `json:"action"` // "resume", "step", "stop"
}

// TokenChunkPayload is emitted for each streaming token chunk
type TokenChunkPayload struct {
	Text      string `json:"text"`
	AgentName string `json:"agent_name"`
	Iteration int    `json:"iteration"`
}

// ReasoningChunkPayload is the streaming peer of TokenChunkPayload for the
// reasoning_content channel. Carries each incremental reasoning delta from
// reasoning models (Nemotron, DeepSeek-R1, QwQ, etc.) without conflating it
// with the visible answer stream. Frontends render it in a separate lane
// (dimmed, collapsible, or behind a "show thinking" toggle).
type ReasoningChunkPayload struct {
	Text      string `json:"text"`
	AgentName string `json:"agent_name"`
	Iteration int    `json:"iteration"`
}

// UserInputPendingPayload is emitted when a user_input tool call blocks
// waiting for a human response. Debugger UIs listen for this and render a
// prompt so the operator can unblock execution.
type UserInputPendingPayload struct {
	RequestID string `json:"request_id"`
	AgentName string `json:"agent_name"`
	Question  string `json:"question"`
	Default   string `json:"default,omitempty"`
}

// UserInputAnsweredPayload is emitted once a pending user_input request is
// satisfied. Source distinguishes regular chat answers from debugger
// injections — useful for audit and for clearing debugger UI state.
type UserInputAnsweredPayload struct {
	RequestID string `json:"request_id"`
	AgentName string `json:"agent_name"`
	Source    string `json:"source"` // "chat" | "debug"
}

// ChatTurnStartPayload is emitted at the start of each chat turn. Turn is
// 1-indexed; Text is the user prompt (so the tree can label the turn without
// reconstructing from transcript frames).
type ChatTurnStartPayload struct {
	Turn int    `json:"turn"`
	Text string `json:"text"`
}

// ChatTurnEndPayload records how the turn resolved. Interrupted is true when
// the turn ended via cancellation or runner error.
type ChatTurnEndPayload struct {
	Turn        int    `json:"turn"`
	Final       string `json:"final,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Err         string `json:"error,omitempty"`
}

// ReplayStartPayload is emitted when a replay run begins
type ReplayStartPayload struct {
	ReplayID        string `json:"replay_id"`
	OriginalSession string `json:"original_session"`
	FromIteration   int    `json:"from_iteration"`
}

// ReplayEndPayload is emitted when a replay run completes
type ReplayEndPayload struct {
	ReplayID string `json:"replay_id"`
	Status   string `json:"status"`
}

// ContextCompressedPayload is emitted when the auto-strategy escalates context compression.
type ContextCompressedPayload struct {
	FromStrategy   string  `json:"from_strategy"`
	ToStrategy     string  `json:"to_strategy"`
	MessagesBefore int     `json:"messages_before"`
	MessagesAfter  int     `json:"messages_after"`
	TokensBefore   int     `json:"tokens_before"`
	TokensAfter    int     `json:"tokens_after"`
	Pressure       float64 `json:"pressure"`
	Iteration      int     `json:"iteration"`
}

// RetrievalInjectedPayload is emitted when retrieved segments are injected into context.
type RetrievalInjectedPayload struct {
	Agent     string `json:"agent"`
	Iteration int    `json:"iteration"`
	Segments  int    `json:"segments"`
	Backend   string `json:"backend"` // "bm25" or "ollama"
}

// FormatSelectedPayload is emitted when an LLM provider resolves a ResponseFormat
// adapter for a request. Fires once per request from providers that use the
// pluggable format.ResponseFormat system (currently openai/openai-compatible).
//
// Reason codes:
//   - "pattern_match"  → model name matched a built-in registry entry
//   - "override"       → explicit response_format set in provider config
//   - "sniff_commit"   → Sniffing fallback inspected early deltas and committed
//   - "fallback"       → no signal found, committed to default (standard_openai)
//
// Operators (and the web UI debugger) can use this to understand why a given
// request was handled by a specific adapter — critical for diagnosing
// reasoning-model behavior when things go wrong.
type FormatSelectedPayload struct {
	Agent    string `json:"agent"`
	Model    string `json:"model"`
	Adapter  string `json:"adapter"`  // committed adapter name, e.g. "reasoning_content_field"
	Reason   string `json:"reason"`   // how we arrived at this adapter (see codes above)
	Resolved string `json:"resolved"` // resolver used: "registry" | "sniffing"
}

// SalvagedOutputPayload is emitted when the format adapter promoted reasoning
// text as content (with the [REASONING-ONLY OUTPUT] marker prefix) and the
// agent loop applied Phase 6.2 Mitigation A recovery instead of accepting
// the salvaged output as a successful final answer.
//
// Two situations emit this event:
//   - Recovery attempted: Recovery=true, the agent injected a directive and
//     will continue to the next iteration.
//   - Terminal: Recovery=false, the loop exhausted max_iterations while the
//     last result was still salvaged. The accompanying AGENT_END carries
//     status="salvaged_no_progress".
//
// Consumers use this event to count salvaged occurrences as a model-quality
// signal.
type SalvagedOutputPayload struct {
	Agent          string `json:"agent"`
	Iteration      int    `json:"iteration"`
	Recovery       bool   `json:"recovery"`        // true = retried with directive; false = terminal
	ContentPreview string `json:"content_preview"` // first ~200 chars of salvaged content
	ReasoningChars int    `json:"reasoning_chars"` // length of the underlying reasoning blob
}

// WorkerRedelegationBlockedPayload accompanies EventWorkerRedelegationBlocked.
// Emitted when an orchestrator declines to call delegate_to_<worker> because
// the worker already returned unproductively earlier in this supervisor turn
// and the post-unproductive cap (default 1) was reached. This originally
// covered salvaged-only returns, and was later broadened to also cover
// max_iter+empty and success+empty terminal shapes. The payload shape is
// unchanged so existing consumers keep working; PostSalvageCount carries the
// post-unproductive count (legacy field name retained — salvaged is the
// strict subset that motivated the original telemetry).
type WorkerRedelegationBlockedPayload struct {
	FromAgent        string `json:"from_agent"`         // supervisor name
	BlockedWorker    string `json:"blocked_worker"`     // worker name we refused to invoke
	Reason           string `json:"reason"`             // e.g. "post-unproductive cap reached"
	PostSalvageCount int    `json:"post_salvage_count"` // post-unproductive count (legacy JSON name)
}

// RollbackPayload accompanies EventRollback. It records a single runtime
// self-correction: the agent discarded a dead-end iteration's messages from
// its conversation history and is retrying from the checkpoint.
type RollbackPayload struct {
	Agent          string `json:"agent"`
	Iteration      int    `json:"iteration"`       // the dead-end iteration being rewound
	Trigger        string `json:"trigger"`         // which trigger fired, e.g. "tool_error"
	Reason         string `json:"reason"`          // human-readable explanation
	RollbackNum    int    `json:"rollback_num"`    // 1-indexed: which rollback this is in the Run
	MaxRollbacks   int    `json:"max_rollbacks"`   // configured per-Run cap
	MessagesPruned int    `json:"messages_pruned"` // llm.Message entries removed from history
}

// MemoryWritePayload accompanies EventMemoryWrite: one upsert, link, or
// retire write to the native memory / knowledge-graph store (internal/memory).
type MemoryWritePayload struct {
	Op         string `json:"op"` // "add", "link", or "retire"
	Scope      string `json:"scope"`
	NodeID     string `json:"node_id"`
	NodeType   string `json:"node_type,omitempty"`
	Title      string `json:"title,omitempty"`
	Created    bool   `json:"created,omitempty"`    // add only: false = updated an existing node
	Superseded string `json:"superseded,omitempty"` // add only: id of the entry this add invalidated
}

// MemoryRecallPayload accompanies EventMemoryRecall: one read from the native
// memory store whose results were surfaced back into the LLM context.
type MemoryRecallPayload struct {
	Op          string   `json:"op"` // "query", "get", "list", or "auto_recall"
	Scope       string   `json:"scope"`
	Query       string   `json:"query,omitempty"`
	ResultCount int      `json:"result_count"`
	NodeIDs     []string `json:"node_ids,omitempty"`
}

// ============================================================
// EVENT BUS
// ============================================================

// EventBus provides pub/sub for agent events
type EventBus struct {
	subscribers []chan AgentEvent
	mu          sync.RWMutex
	buffer      int
	dropped     atomic.Int64
	dropWarn    sync.Once

	// parent and origin are set only on a derived bus (see WithOrigin). A
	// derived bus owns no subscribers of its own: it stamps origin and hands
	// the event to its parent, so a tagged producer publishes to exactly the
	// same audience an untagged one does.
	parent *EventBus
	origin string
}

// WithOrigin returns a view of eb that stamps every event it publishes with
// origin. Subscribers are shared with eb, so tagging costs nothing in
// recording: the session JSONL still receives every event, now correlated.
//
// This exists because agent names do not identify a source. A directed agent
// chat rebuilds a config agent and runs it on the same bus as the main run,
// which may be delegating to an agent of that same name at that same moment —
// so a consumer that needs to route or filter by origin has to be told at the
// emitting end. ParentID is not that substrate: it carries the spawn-tree link
// and the tracer computes nesting depth from it.
//
// A nil bus derives to nil, so a caller holding no bus stays no-op.
func (eb *EventBus) WithOrigin(origin string) *EventBus {
	if eb == nil {
		return nil
	}
	return &EventBus{parent: eb, origin: origin}
}

// NewEventBus creates a new event bus
func NewEventBus(bufferSize int) *EventBus {
	return &EventBus{
		subscribers: make([]chan AgentEvent, 0),
		buffer:      bufferSize,
	}
}

// Subscribe returns a channel that receives all events
func (eb *EventBus) Subscribe() <-chan AgentEvent {
	if eb.parent != nil {
		return eb.parent.Subscribe()
	}
	eb.mu.Lock()
	defer eb.mu.Unlock()

	ch := make(chan AgentEvent, eb.buffer)
	eb.subscribers = append(eb.subscribers, ch)
	return ch
}

// Unsubscribe removes a subscriber
func (eb *EventBus) Unsubscribe(ch <-chan AgentEvent) {
	if eb.parent != nil {
		eb.parent.Unsubscribe(ch)
		return
	}
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for i, sub := range eb.subscribers {
		if sub == ch {
			eb.subscribers = append(eb.subscribers[:i], eb.subscribers[i+1:]...)
			close(sub)
			break
		}
	}
}

// Publish sends an event to all subscribers
func (eb *EventBus) Publish(event AgentEvent) {
	if eb.parent != nil {
		// Never overwrite an origin the caller set explicitly: a nested
		// derivation should keep the innermost tag.
		if event.Origin == "" {
			event.Origin = eb.origin
		}
		eb.parent.Publish(event)
		return
	}
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for _, sub := range eb.subscribers {
		select {
		case sub <- event:
		default:
			// Channel full — count the drop and warn once, so parallel fan-out
			// can't silently lose AGENT_START/END events (tree views depend on them).
			eb.dropped.Add(1)
			eb.dropWarn.Do(func() {
				fmt.Fprintln(os.Stderr, "warn: telemetry event bus dropped an event (slow subscriber); further drops counted silently")
			})
		}
	}
}

// DroppedEvents reports how many events were discarded because a subscriber
// channel was full.
func (eb *EventBus) DroppedEvents() int64 {
	if eb.parent != nil {
		return eb.parent.DroppedEvents()
	}
	return eb.dropped.Load()
}

// Emit is a helper to create and publish events
func (eb *EventBus) Emit(agentName string, eventType EventType, payload interface{}) {
	payloadJSON, _ := json.Marshal(payload)

	event := AgentEvent{
		ID:        generateID(),
		Timestamp: time.Now(),
		AgentName: agentName,
		EventType: eventType,
		Payload:   payloadJSON,
	}

	eb.Publish(event)
}

// EmitWithParent creates an event with a parent ID for nesting
func (eb *EventBus) EmitWithParent(agentName string, eventType EventType, parentID string, payload interface{}) {
	payloadJSON, _ := json.Marshal(payload)

	event := AgentEvent{
		ID:        generateID(),
		Timestamp: time.Now(),
		AgentName: agentName,
		EventType: eventType,
		ParentID:  parentID,
		Payload:   payloadJSON,
	}

	eb.Publish(event)
}

// EmitWithTokenUsage includes token information
func (eb *EventBus) EmitWithTokenUsage(agentName string, eventType EventType, payload interface{}, usage TokenUsage) {
	payloadJSON, _ := json.Marshal(payload)

	event := AgentEvent{
		ID:         generateID(),
		Timestamp:  time.Now(),
		AgentName:  agentName,
		EventType:  eventType,
		Payload:    payloadJSON,
		TokenUsage: &usage,
	}

	eb.Publish(event)
}

// EmitWithDuration includes duration information
func (eb *EventBus) EmitWithDuration(agentName string, eventType EventType, payload interface{}, durationMs int64) {
	payloadJSON, _ := json.Marshal(payload)

	event := AgentEvent{
		ID:        generateID(),
		Timestamp: time.Now(),
		AgentName: agentName,
		EventType: eventType,
		Payload:   payloadJSON,
		Duration:  durationMs,
	}

	eb.Publish(event)
}

// generateID generates a unique event ID
func generateID() string {
	return uuid.New().String()
}
