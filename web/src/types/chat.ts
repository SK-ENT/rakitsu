// WebSocket protocol types for the chat transport.
// Mirrors internal/server/chat_session.go (serverMsg) and chat_ws.go (clientMsg).

import type { AgentEvent } from './events';

// Client → server. Branch ops (edit_turn / regenerate / switch_branch /
// set_active_path / request_tree) each carry the target turn id;
// switch_branch additionally carries the cycle direction.
export type ClientMsg =
  | { type: 'turn'; text: string }
  | { type: 'interrupt' }
  | { type: 'user_input_response'; request_id: string; text: string }
  | { type: 'edit_turn'; turn_id: string; text: string }
  | { type: 'regenerate'; turn_id: string }
  | { type: 'switch_branch'; turn_id: string; dir: -1 | 1 }
  | { type: 'set_active_path'; turn_id: string }
  | { type: 'request_tree' };

// Server → client (all fields optional — discriminate by `type`).
export interface ServerMsg {
  type:
    | 'attached'
    | 'turn_started'
    | 'token'
    | 'turn_done'
    | 'tool_start'
    | 'tool_end'
    | 'reasoning'
    | 'user_input_request'
    | 'event'
    | 'error'
    | 'closed'
    | 'transcript_resync'
    | 'tree_snapshot'
    // Runtime-spawned subagent lifecycle (spawn_agent). Only children carry
    // these — the root agent's own AGENT_START is filtered server-side.
    | 'agent_start'
    | 'agent_end'
    // Slash-command reply (e.g. /model ...) — handled entirely server-side
    // and sent back as a transient system message. Rendered as a system
    // block client-side; no turn lifecycle attached.
    | 'system';
  text?: string;
  final?: string;
  error?: string;
  gen_token?: number;
  request_id?: string;
  question?: string;
  default?: string;
  message?: string;
  tool_call_id?: string;
  tool_name?: string;
  tool_args?: string;
  output?: string;
  duration?: number;
  agent_name?: string;
  depth?: number;
  // Subagent lifecycle fields — parent_agent + model on agent_start;
  // status + tokens on agent_end.
  parent_agent?: string;
  model?: string;
  status?: string;
  tokens?: number;
  event?: AgentEvent;
  meta?: ChatSessionMeta;
  transcript?: TranscriptEntry[];
  tree?: TreeSnapshot;
  turn_id?: string;
}

// RetryContext mirrors turntree.RetryContext. Populated on Nodes
// forked by an agent-driven self-correction (rollback, salvage,
// auto-regenerate). Nil on user-driven branches.
export interface RetryContext {
  source: string;
  prior_turn_id?: string;
  reason: string;
}

// TreeNodeSummary mirrors internal/server/chat_branch.go treeNodeSummary —
// full Node metadata MINUS Entries plus entry_count, so a fresh
// tree_snapshot can re-render the Viewer without dragging entry bodies
// across the wire. Entry bodies are fetched per-node via
// GET /api/chat/{id}/turn/{turn_id} when a user clicks a node.
export interface TreeNodeSummary {
  id: string;
  parent_id?: string;
  user_text?: string;
  children?: string[];
  status: 'generating' | 'complete' | 'interrupted' | 'failed';
  created: string;
  entry_count: number;
  retry_ctx?: RetryContext;
}

// TreeSnapshot mirrors internal/server/chat_branch.go treeSnapshot.
// active_path is the precomputed root→leaf id chain so the UI can
// highlight the current branch without re-walking the tree.
export interface TreeSnapshot {
  nodes: TreeNodeSummary[];
  roots: string[];
  active_path: string[];
}

// TranscriptEntry mirrors internal/server/chat_session.go transcriptEntry.
// Sent on `attached` (initial replay) and on `transcript_resync` (after a
// branch op flips the active path).
export interface TranscriptEntry {
  kind: 'user' | 'assistant' | 'system' | 'user_input_request' | 'tool' | 'reasoning' | 'subagent' | 'wake';
  text?: string;
  interrupted?: boolean;
  error?: string; // assistant turn error text (kind === 'assistant', interrupted === true)
  request_id?: string;
  question?: string;
  default?: string;
  agent_name?: string;
  timestamp: string;
  // Tool-block fields (kind === 'tool').
  tool_call_id?: string;
  tool_name?: string;
  tool_args?: string;
  output?: string;
  tool_err?: string;
  duration?: number;
  // Subagent-block fields (kind === 'subagent') — a finished runtime-spawned
  // child, recorded for reconnect replay. agent_name above is the child's
  // instance name.
  status?: string;
  tokens?: number;
  // Nesting indent (tool + reasoning entries).
  depth?: number;
  // Branch metadata. Decorated wire-only by snapshotTranscript.
  // turn_id appears on every entry; branch_index/branch_count are set
  // only on "user" entries so the inline UX can render `‹n/m›` chips
  // for turns that have siblings.
  turn_id?: string;
  branch_index?: number;
  branch_count?: number;
}

export interface ChatSessionMeta {
  id: string;
  config_id: string;
  name: string;
  agent_name: string;
  model: string;
  interactive: boolean;
  created: string;
  generating: boolean;
}

// UI-side message record for rendering.
export type ChatBlockKind = 'user' | 'assistant' | 'system' | 'tool' | 'reasoning' | 'subagent' | 'wake';

export interface ChatBlock {
  kind: ChatBlockKind;
  text: string;
  interrupted?: boolean;
  // Subagent-block fields (kind === 'subagent'). agentName carries the
  // child's instance name; subModel is shown only while running.
  subModel?: string;
  subStatus?: string;
  subTokens?: number;
  subDone?: boolean;
  // When set, this block is a pending user_input request with the given id.
  pendingInputId?: string;
  pendingQuestion?: string;
  pendingDefault?: string;
  // Tool-block fields (kind === 'tool').
  toolCallId?: string;
  toolName?: string;
  toolArgs?: string;
  toolDone?: boolean;
  toolErr?: string;
  output?: string;
  duration?: number;
  // Emitting agent + nesting indent (tool + reasoning blocks).
  agentName?: string;
  depth?: number;
  // Collapsed hides a tool/reasoning block's body, leaving a one-line summary.
  collapsed?: boolean;
  // Branch metadata mirrored from TranscriptEntry. Lets templates
  // render `‹n/m›` chips on user blocks and the edit/regenerate/
  // switch affordances without a separate tree lookup.
  turnId?: string;
  branchIndex?: number;
  branchCount?: number;
}
