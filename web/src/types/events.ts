// Event types matching the Go backend telemetry (internal/telemetry/events.go)

export type EventType =
  | 'AGENT_START'
  | 'AGENT_END'
  | 'THOUGHT_START'
  | 'THOUGHT_END'
  | 'TOOL_CALL_START'
  | 'TOOL_CALL_END'
  | 'AGENT_HANDOFF'
  | 'AGENT_MESSAGE'
  | 'TOKEN_USAGE'
  | 'ERROR'
  | 'EXECUTION_COMPLETE'
  | 'REFLECTION_START'
  | 'REFLECTION_END'
  | 'GROUND_CHECK_START'
  | 'GROUND_CHECK_END'
  | 'PIPELINE_START'
  | 'PIPELINE_END'
  | 'PIPELINE_STEP_START'
  | 'PIPELINE_STEP_END'
  | 'DEBUG_PAUSED'
  | 'DEBUG_RESUMED'
  | 'TOKEN_CHUNK'
  | 'REASONING_CHUNK'
  | 'REPLAY_START'
  | 'REPLAY_END'
  | 'RETRY_ATTEMPT'
  | 'CONTEXT_COMPRESSED'
  | 'RETRIEVAL_INJECTED'
  | 'FORMAT_SELECTED'
  | 'SALVAGED_OUTPUT'
  | 'WORKER_REDELEGATION_BLOCKED'
  | 'USER_INPUT_PENDING'
  | 'USER_INPUT_ANSWERED'
  | 'CHAT_TURN_START'
  | 'CHAT_TURN_END'
  /**
   * Runtime self-correction: the agent detected a dead-end iteration and
   * rewound its conversation history to before it (see RollbackPayload).
   * Hidden from the chat surface; surfaced in the debug tree.
   */
  | 'ROLLBACK'
  /**
   * Native memory / knowledge-graph store write: the agent persisted or
   * linked a knowledge node (memory_add / memory_link).
   */
  | 'MEMORY_WRITE'
  /**
   * Native memory store read whose results were surfaced back into the LLM
   * context (memory_query / memory_get / memory_list).
   */
  | 'MEMORY_RECALL'
  /**
   * Cross-session messaging: a delivery attempt through
   * POST /api/sessions/{id}/message, with its resulting status (delivered,
   * busy, not_found, rate_limited, rejected). Emitted by the serve endpoint
   * for every sender (send_message tool, CLI, web UI).
   */
  | 'SESSION_MSG_SENT'
  /**
   * Cross-session messaging: the receiving session accepted an injected
   * message into its turn queue. Sender attribution is unverified.
   */
  | 'SESSION_MSG_RECEIVED'
  | 'WAKE_TICK'
  | 'WAKE_ESCALATE'
  | 'WAKE_TASK_START'
  | 'WAKE_TASK_END'
  /**
   * Final lifecycle marker written as the last line of a session JSONL by
   * SessionStore.EndSession. JSONL-only — never published to the EventBus,
   * so live SSE consumers never observe it. Backend SessionStore.GetSessionEvents
   * filters it out, so frontend history-mode consumers don't see it either.
   * This member exists only for tooling that parses the raw JSONL directly
   * (debug exports, support-ticket attachments, dogfood verifiers).
   */
  | 'SESSION_END';

/**
 * Subset of EventType that can appear on the live SSE stream. Use this when
 * narrowing types for SSE consumers (`useEventStream`, hub `/events`,
 * runtime debug attach) — SESSION_END is excluded because it's written
 * directly to the JSONL file, not published to the bus.
 */
export type LiveEventType = Exclude<EventType, 'SESSION_END'>;

export interface TokenUsage {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
}

export interface AgentEvent {
  id: string;
  timestamp: string;
  agent_name: string;
  event_type: EventType;
  iteration?: number;
  parent_id?: string;
  // Correlation id shared by every event of one directed agent chat turn,
  // including its DIRECT_CHAT_START/END pair. Absent means the main run.
  origin?: string;
  replay_id?: string;
  payload: Record<string, unknown>;
  token_usage?: TokenUsage;
  duration_ms?: number;
  session_id?: string;
}

// --- Payload Types ---

export interface ToolCallSignature {
  name: string;
  arguments: Record<string, unknown>;
  reason?: string;
}

export interface StructuredThought {
  reasoning: string;
  plan?: string[];
  intended_tool_calls?: ToolCallSignature[];
  confidence?: number;
  alternatives?: string[];
}

export interface ThoughtStartPayload {
  iteration: number;
  history_length: number;
  available_tools: string[];
}

export interface ThoughtEndPayload extends StructuredThought {
  model: string;
  finish_reason: string;
}

export interface ToolCallStartPayload {
  tool_call_id: string;
  tool_name: string;
  arguments: Record<string, unknown>;
  sandbox_type?: string;
}

export interface ToolCallEndPayload {
  tool_call_id: string;
  tool_name: string;
  output: string;
  output_truncated: boolean;
  error?: string;
  exit_code?: number;
}

export interface AgentStartPayload {
  role: string;
  model: string;
  system_prompt?: string;
  tools: string[];
  skills?: string[];
  parent_agent?: string; // spawning agent's instance name (spawn_agent children only)
}

export interface AgentEndPayload {
  status: string;
  final_answer?: string;
  iterations: number;
  total_tokens: number;
  total_cost?: number;
  max_tokens?: number;
  max_cost?: number;
}

export interface AgentHandoffPayload {
  from_agent: string;
  to_agent: string;
  task: string;             // raw step task (original query)
  full_context: string;     // enriched task with prior step results injected
  context_size: number;
  prior_step_count: number; // number of prior step results included
}

export interface AgentMessagePayload {
  from_agent: string;
  to_agent: string;
  message: string;
  type: string;
}

export interface ErrorPayload {
  error_type: string;
  message: string;
  stack_trace?: string;
  recoverable: boolean;
}

export interface ReflectionStartPayload {
  iteration: number;
  mode: string;
}

export interface ReflectionEndPayload {
  iteration: number;
  mode: string;
  reflection: string;
}

export interface GroundCheckStartPayload {
  iteration: number;
}

export interface GroundCheckEndPayload {
  iteration: number;
  is_valid: boolean;
  confidence: number;
  issues?: string[];
  action: string;
  assessment?: string;
}

// --- Pipeline Event Payloads ---

export interface PipelineStepInfo {
  name: string;
  type: string; // "sequential", "parallel", "loop"
  agents?: string[];
  steps?: PipelineStepInfo[]; // nested sub-steps
}

export interface PipelineStartPayload {
  step_count: number;
  query: string;
  steps?: PipelineStepInfo[];
}

export interface PipelineEndPayload {
  status: string;
  steps_run: number;
}

export interface PipelineStepStartPayload {
  step_name: string;
  step_type: string;
  agents?: string[];
}

export interface PipelineStepEndPayload {
  step_name: string;
  step_type: string;
  status: string;
  output?: string;
  iteration?: number;
}

// --- Debug Tree Types (Debugger View) ---

export type DebugNodeType =
  | 'pipeline' | 'step' | 'agent' | 'iteration'
  | 'thought' | 'tool_call' | 'reflection' | 'ground_check'
  | 'handoff' | 'message' | 'error'
  | 'chat_turn';

/**
 * SalvagedOutputPayload accompanies SALVAGED_OUTPUT events. Emitted by the
 * agent when the format adapter promoted reasoning-only text as the visible
 * content (Result.Salvaged) — the model did NOT commit to an answer.
 * Recovery=true means the agent injected a recovery directive and continued
 * to the next iteration; Recovery=false means the loop exhausted
 * max_iterations and the accompanying AGENT_END carries status="salvaged_no_progress".
 * Backend: internal/telemetry/events.go::SalvagedOutputPayload.
 */
export interface SalvagedOutputPayload {
  agent: string;
  iteration: number;
  recovery: boolean;
  content_preview: string;
  reasoning_chars: number;
}

/**
 * WorkerRedelegationBlockedPayload accompanies WORKER_REDELEGATION_BLOCKED
 * events. Emitted by an orchestrator's DelegationTool when the supervisor
 * attempted to call a worker that already returned salvaged content earlier
 * in this turn and the per-worker post-salvage cap has been reached. The
 * supervisor receives a [DELEGATION BLOCKED] tool output instead of invoking
 * the worker.
 * Backend: internal/telemetry/events.go::WorkerRedelegationBlockedPayload.
 */
export interface WorkerRedelegationBlockedPayload {
  from_agent: string;
  blocked_worker: string;
  reason: string;
  post_salvage_count: number;
}

/**
 * RollbackPayload accompanies ROLLBACK events. Emitted when the agent performs
 * a runtime self-correction: it discarded a dead-end iteration's messages from
 * its conversation history (the failed attempt's tool errors) and is retrying
 * from the checkpoint. Capped per Run by max_rollbacks.
 * Backend: internal/telemetry/events.go::RollbackPayload.
 */
export interface RollbackPayload {
  agent: string;
  iteration: number;
  trigger: string;         // which trigger fired, e.g. "tool_error"
  reason: string;          // human-readable explanation
  rollback_num: number;    // 1-indexed: which rollback this is in the Run
  max_rollbacks: number;   // configured per-Run cap
  messages_pruned: number; // llm.Message entries removed from history
}

/**
 * MemoryWritePayload accompanies MEMORY_WRITE events: one upsert, link, or
 * retire write to the native memory / knowledge-graph store.
 * Backend: internal/telemetry/events.go::MemoryWritePayload.
 */
export interface MemoryWritePayload {
  op: 'add' | 'link' | 'retire';
  scope: string;
  node_id: string;
  node_type?: string;
  title?: string;
  created?: boolean; // add only: false = updated an existing node
  superseded?: string; // add only: id of the entry this add invalidated
}

/**
 * MemoryRecallPayload accompanies MEMORY_RECALL events: one read from the
 * native memory store whose results entered the LLM context.
 * Backend: internal/telemetry/events.go::MemoryRecallPayload.
 */
export interface MemoryRecallPayload {
  op: 'query' | 'get' | 'list';
  scope: string;
  query?: string;
  result_count: number;
  node_ids?: string[];
}

/**
 * SessionMsgSentPayload accompanies SESSION_MSG_SENT events: one cross-session
 * message delivery attempt. Text is truncated server-side.
 * Backend: internal/telemetry/events.go::SessionMsgSentPayload.
 */
export interface SessionMsgSentPayload {
  to_session_id: string;
  from_session_id?: string;
  from_name?: string;
  text: string;
  status: 'delivered' | 'busy' | 'not_found' | 'rate_limited' | 'rejected';
  error?: string;
}

/**
 * SessionMsgReceivedPayload accompanies SESSION_MSG_RECEIVED events: a
 * cross-session message accepted by the receiving session. From fields are
 * sender-claimed (unverified); text is truncated server-side.
 * Backend: internal/telemetry/events.go::SessionMsgReceivedPayload.
 */
export interface SessionMsgReceivedPayload {
  from_session_id?: string;
  from_name?: string;
  text: string;
  /** Steerable one-shot handling: 'injected' = drained into the running
   *  ReAct loop; 'dropped_overflow' = evicted mid-run by a newer steer when
   *  the queue was full; 'dropped_at_exit' = run ended first. Absent for
   *  ordinary chat deliveries. Backend: SessionMsgReceivedPayload.Status. */
  status?: 'injected' | 'dropped_overflow' | 'dropped_at_exit';
}

export interface WakeCheckResult {
  name: string
  status: 'ok' | 'alarm' | 'unknown'
  detail?: string
  label?: string
}

export interface WakeTickPayload {
  tick: number
  interval_seconds: number
  results?: WakeCheckResult[]
  outcome: 'quiet' | 'changed' | 'alarm' | 'unknown'
  result?: 'killed' | 'degraded'
  note?: string
  summary_chars: number
  summary_cap: number
}

export interface WakeEscalatePayload {
  tick: number
  reason: string
  deferred?: boolean
  suppressed?: 'hourly_cap' | 'single_flight' | 'degraded'
  result?: 'timeout' | 'done' | 'error'
  level: string
  summary_chars: number
  summary_cap: number
}

export interface WakeTaskPayload {
  task_id?: string
  config: string
  status: 'started' | 'refused' | 'done' | 'error' | 'timeout' | 'cancelled'
  reason?: string
  args?: Record<string, string>
  summary?: string
}

export interface DebugTreeNode {
  id: string;
  type: DebugNodeType;
  label: string;
  agentName: string;
  status: 'pending' | 'running' | 'success' | 'error' | 'idle' | 'paused' | 'salvaged';
  startEvent: AgentEvent;
  endEvent?: AgentEvent;
  children: DebugTreeNode[];
  tokens?: TokenUsage;
  durationMs?: number;
  iteration?: number;
  streamingText?: string;
  // Live chain-of-thought from REASONING_CHUNK events (separate from the
  // committed-answer streamingText so the UI can render the two streams
  // with distinct styling). Reasoning models like Nemotron / DeepSeek-R1
  // emit the majority of their wall-clock time as reasoning_content;
  // without surfacing it here the operator sees nothing during that phase.
  streamingReasoning?: string;
  details?: Record<string, unknown>;
}

// --- Debug Event Payloads ---

export interface DebugPendingToolInfo {
  name: string;
  arguments: Record<string, unknown>;
}

export interface DebugPausedPayload {
  agent_name: string;
  checkpoint: string;
  iteration: number;
  reason: string;
  // Inspection context
  last_thought?: string;
  pending_tools?: DebugPendingToolInfo[];
  history_length?: number;
  total_tokens_in?: number;
  total_tokens_out?: number;
  max_tokens?: number;
  max_iterations?: number;
  context_window?: number;
  predicted_next_tokens?: number;
  budget_ratio?: number;
}

export interface DebugResumedPayload {
  action: string;
}

export interface TokenChunkPayload {
  text: string;
  agent_name: string;
  iteration: number;
}

export interface ReasoningChunkPayload {
  text: string;
  agent_name: string;
  iteration: number;
}

export interface ChatTurnStartPayload {
  turn: number;
  text: string;
}

export interface ChatTurnEndPayload {
  turn: number;
  final?: string;
  interrupted?: boolean;
  error?: string;
}

export interface ReplayStartPayload {
  replay_id: string;
  original_session: string;
  from_iteration: number;
}

export interface ReplayEndPayload {
  replay_id: string;
  status: string;
}

export interface RetryAttemptPayload {
  attempt: number;
  max_attempts: number;
  error: string;
  iteration: number;
}

export interface ContextCompressedPayload {
  from_strategy: string;
  to_strategy: string;
  messages_before: number;
  messages_after: number;
  tokens_before: number;
  tokens_after: number;
  pressure: number;
  iteration: number;
}

export interface RetrievalInjectedPayload {
  agent: string;
  iteration: number;
  segments: number;
  backend: string; // "bm25" or "ollama"
}

// Emitted when an LLM provider resolves a ResponseFormat adapter for a
// request. Fires once per request from providers using the pluggable
// internal/llm/format/ system.
// Reason codes: "pattern_match" | "override" | "sniff_commit" | "fallback"
// Resolved codes: "registry" | "sniffing"
export interface FormatSelectedPayload {
  agent: string;
  model: string;
  adapter: string;
  reason: string;
  resolved: string;
}

// --- Hub Session Types (internal/server/sse.go) ---

export interface ActiveSession {
  id: string;
  name: string;
  query: string;
  config_path: string;
  agents: string[];
  start_time: string;
  status: string;
  debug: boolean;
}

// --- Session History Types (internal/store/store.go) ---

export interface SessionMeta {
  id: string;
  project_id?: string;
  name: string;
  query: string;
  config_path: string;
  agents: string[];
  start_time: string;
  end_time?: string;
  status: 'running' | 'success' | 'error' | 'timeout';
  total_tokens: number;
  total_events: number;
  duration_ms: number;
  config_yaml?: string;
}
