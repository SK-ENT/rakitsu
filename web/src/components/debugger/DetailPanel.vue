<script setup lang="ts">
import { computed, ref } from 'vue';
import type { DebugTreeNode, DebugPausedPayload, AgentConfig, ToolConfig, SkillConfig, OrchestratorConfig, GroupConfig } from '../../types';
type NodeData = AgentConfig | ToolConfig | SkillConfig | OrchestratorConfig | GroupConfig;
import { NODE_TYPE_TO_EVENT } from '../../types/checkpoints';
import StreamingText from './StreamingText.vue';
import ParamEditor from './ParamEditor.vue';
import NodeEditor from '../builder/NodeEditor.vue';
import TokenizerPanel, { type TokenizerSegment } from '../builder/TokenizerPanel.vue';

const props = defineProps<{
  node: DebugTreeNode;
  renderKey?: number;
  isConnected?: boolean;
  isPreview?: boolean;
  isPaused?: boolean;
  isHistory?: boolean;
  allConfigTools?: string[];
  configProviderNames?: string[];
  configProviderMap?: Record<string, Record<string, string>>;
  configDefaults?: Record<string, unknown>;
  configAgentMap?: Record<string, Record<string, unknown>>;
  configOrchestratorMap?: Record<string, Record<string, unknown>>;
  replayResult?: Record<string, unknown> | null;
  selfUrl?: string;
}>();

const emit = defineEmits<{
  close: [];
  'apply-params': [agent: string, overrides: Record<string, unknown>];
  'reset-params': [agent: string];
  'replay-from': [iteration: number, agentName: string];
  'rerun-from-step': [stepName: string];
  'run-to-node': [condition: { checkpoint?: string; agent_name?: string; tool_name?: string }];
  'debug-from-here': [condition: { checkpoint?: string; agent_name?: string; tool_name?: string }];
}>();

const isThoughtStreaming = computed(() =>
  props.node.type === 'thought' && props.node.status === 'running' && !!props.node.streamingText,
);

// True while the reasoning_content stream is active on a running thought.
// Reasoning models spend the majority of wall-clock time in this state;
// without surfacing it the debugger appears frozen.
const isThoughtReasoning = computed(() =>
  props.node.type === 'thought' && props.node.status === 'running' && !!props.node.streamingReasoning,
);

// For pipeline nodes: aggregate child step outputs
const pipelineOutput = computed(() => {
  void props.renderKey;
  if (props.node.type !== 'pipeline') return '';
  const parts: string[] = [];
  for (const child of props.node.children) {
    if (child.type === 'step' && child.endEvent?.payload) {
      const output = (child.endEvent.payload as { output?: string }).output;
      if (output) parts.push(`## ${child.label}\n${output}`);
    }
  }
  return parts.join('\n\n');
});

// For agent nodes: find active thought's streaming text in children
const agentStreamingText = computed(() => {
  void props.renderKey; // force recompute on renderKey change
  if (props.node.type !== 'agent') return '';
  for (const child of [...props.node.children].reverse()) {
    if (child.type === 'iteration') {
      const thought = [...child.children].reverse().find(c => c.type === 'thought');
      if (thought?.streamingText) return thought.streamingText;
    }
    if (child.type === 'thought' && child.streamingText) return child.streamingText;
  }
  return '';
});

// Sibling of agentStreamingText for the reasoning_content lane. Walks the
// same path so the most recently active thought wins.
const agentStreamingReasoning = computed(() => {
  void props.renderKey;
  if (props.node.type !== 'agent') return '';
  for (const child of [...props.node.children].reverse()) {
    if (child.type === 'iteration') {
      const thought = [...child.children].reverse().find(c => c.type === 'thought');
      if (thought?.streamingReasoning) return thought.streamingReasoning;
    }
    if (child.type === 'thought' && child.streamingReasoning) return child.streamingReasoning;
  }
  return '';
});

const p = computed(() => props.node.startEvent?.payload ?? {});
const endP = computed(() => props.node.endEvent?.payload ?? {});

// Run-to-here condition based on node type
// Derive breakpoint condition from node type using centralized map
const runToCondition = computed<{ checkpoint?: string; agent_name?: string; tool_name?: string } | null>(() => {
  const n = props.node;
  const event = NODE_TYPE_TO_EVENT[n.type];
  if (!event) return null;
  const agentName = n.type === 'step' ? ((n.details?.stepName as string) ?? n.label) : n.agentName;
  return { checkpoint: event, agent_name: agentName };
});

const runToDescription = computed(() => {
  const c = runToCondition.value;
  if (!c) return '';
  const parts: string[] = [];
  if (c.agent_name) parts.push(`agent: ${c.agent_name}`);
  if (c.tool_name) parts.push(`tool: ${c.tool_name}`);
  if (c.checkpoint) parts.push(c.checkpoint);
  return `Pause at ${parts.join(', ')}`;
});

const pauseCtx = computed(() =>
  (props.node.details?.pauseContext as DebugPausedPayload) ?? null,
);

const budgetPct = computed(() => {
  const ctx = pauseCtx.value;
  if (!ctx?.budget_ratio) return 0;
  return Math.round(ctx.budget_ratio * 100);
});

const budgetColor = computed(() => {
  const r = pauseCtx.value?.budget_ratio ?? 0;
  if (r >= 0.85) return '#f44336';
  if (r >= 0.70) return '#ff9800';
  return '#4caf50';
});

// Agent node data for NodeEditor — from event payload or session config
const agentNodeData = computed(() => {
  if (props.node.type !== 'agent') return null;
  const name = props.node.agentName;
  // Prefer session config (full data), fallback to event payload
  const fromConfig = props.configAgentMap?.[name];
  if (fromConfig) return JSON.parse(JSON.stringify(fromConfig));
  // Build from event payload
  return {
    name,
    role: (p.value.role as string) || 'worker',
    provider: (p.value.provider as string) || '',
    model: (p.value.model as string) || '',
    system_prompt: (p.value.system_prompt as string) || '',
    tools: (p.value.tools as string[]) || [],
  };
});

// Orchestrator node data for NodeEditor — from session config
const orchestratorNodeData = computed(() => {
  if (props.node.type !== 'pipeline') return null;
  const name = props.node.label || props.node.agentName;
  const fromConfig = props.configOrchestratorMap?.[name];
  if (fromConfig) return JSON.parse(JSON.stringify(fromConfig));
  // Build minimal from event payload
  return {
    name,
    strategy: (p.value.strategy as string) || 'ReAct',
    agents: (p.value.agents as string[]) || [],
  };
});

// Show NodeEditor when agent or orchestrator node is paused/in preview
const showNodeEditor = computed(() =>
  (props.node.type === 'agent' && !!agentNodeData.value) ||
  (props.node.type === 'pipeline' && !!orchestratorNodeData.value)
);

const nodeEditorType = computed(() =>
  props.node.type === 'pipeline' ? 'orchestrator' : 'agent'
);

const nodeEditorData = computed(() =>
  props.node.type === 'pipeline' ? orchestratorNodeData.value : agentNodeData.value
);

function handleDebugNodeUpdate(_nodeId: string, data: NodeData) {
  const agentData = data as AgentConfig;
  const ovr: Record<string, unknown> = {};
  if (agentData.model) ovr.model = agentData.model;
  if (agentData.model_config) {
    if (agentData.model_config.temperature != null) ovr.temperature = agentData.model_config.temperature;
    if (agentData.model_config.max_tokens != null) ovr.max_tokens = agentData.model_config.max_tokens;
    if (agentData.model_config.top_p != null) ovr.top_p = agentData.model_config.top_p;
  }
  if (agentData.system_prompt) ovr.system_prompt = agentData.system_prompt;
  if (agentData.tools) ovr.tools = agentData.tools;
  if (agentData.settings) {
    if (agentData.settings.max_iterations) ovr.max_iterations = agentData.settings.max_iterations;
    if (agentData.settings.reflection) ovr.reflection = agentData.settings.reflection;
    if (agentData.settings.ground_check) ovr.ground_check = agentData.settings.ground_check;
  }
  emit('apply-params', agentData.name, ovr);
}

const expandedTools = ref<Set<number>>(new Set());

function toggleTool(i: number) {
  if (expandedTools.value.has(i)) expandedTools.value.delete(i);
  else expandedTools.value.add(i);
}

function fmtTokens(n?: number): string {
  if (!n) return '0';
  return n >= 1000 ? `${(n / 1000).toFixed(1)}K` : String(n);
}

const inputPct = computed(() => {
  if (!props.node.tokens || !props.node.tokens.total_tokens) return 0;
  return Math.round((props.node.tokens.input_tokens / props.node.tokens.total_tokens) * 100);
});

// Common model context window sizes (fallback lookup)
const MODEL_CONTEXT: Record<string, number> = {
  'gpt-4o': 128000, 'gpt-4o-mini': 128000, 'gpt-4-turbo': 128000, 'gpt-4': 8192,
  'gpt-3.5-turbo': 16385, 'claude-sonnet-4-6': 200000, 'claude-haiku-4-5': 200000,
  'claude-opus-4-6': 200000, 'gemini-2.0-flash': 1048576, 'gemini-1.5-pro': 2097152,
};

const contextWindowSize = computed(() => {
  void props.renderKey;
  const model = (props.node.startEvent?.payload?.model as string) || '';
  // Try exact match, then prefix match
  if (MODEL_CONTEXT[model]) return MODEL_CONTEXT[model];
  for (const [k, v] of Object.entries(MODEL_CONTEXT)) {
    if (model.includes(k)) return v;
  }
  return 0;
});

const contextPct = computed(() => {
  if (!contextWindowSize.value || !props.node.tokens?.input_tokens) return 0;
  return Math.round((props.node.tokens.input_tokens / contextWindowSize.value) * 100);
});

function fmtDuration(ms?: number): string {
  if (!ms) return '-';
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`;
}

// --- Tokenizer ---
const showTokenizer = ref(false);
const tokenizerSegments = computed<TokenizerSegment[]>(() => {
  const segs: TokenizerSegment[] = [];
  const payload = props.node.startEvent?.payload as Record<string, unknown> | undefined;
  const endPayload = props.node.endEvent?.payload as Record<string, unknown> | undefined;
  const model = (payload?.model as string) || 'gpt-4';

  // Agent nodes: system_prompt + final_answer
  if (payload?.system_prompt) {
    segs.push({ label: 'System Prompt', text: payload.system_prompt as string, model });
  }
  if (endPayload?.final_answer) {
    segs.push({ label: 'Response', text: endPayload.final_answer as string, model });
  }
  // Thought nodes: reasoning text
  if (endPayload?.reasoning) {
    segs.push({ label: 'Reasoning', text: endPayload.reasoning as string, model });
  }
  // Tool nodes: output
  if (endPayload?.output) {
    segs.push({ label: 'Output', text: String(endPayload.output), model });
  }
  // Pipeline: query
  if (payload?.query) {
    segs.push({ label: 'Query', text: payload.query as string, model });
  }
  // Handoff: task/context
  if (payload?.task) {
    segs.push({ label: 'Task', text: payload.task as string, model });
  }
  if (payload?.full_context) {
    segs.push({ label: 'Context', text: payload.full_context as string, model });
  }
  // If nothing found, try streaming text or label
  if (segs.length === 0 && props.node.streamingText) {
    segs.push({ label: props.node.label || 'Text', text: props.node.streamingText, model });
  }
  return segs;
});

</script>

<template>
  <div class="detail-panel">
    <div class="panel-header">
      <span class="panel-title">{{ node.type }}: {{ node.label }}</span>
      <button class="close-btn" @click="emit('close')">x</button>
    </div>

    <!-- Token Tracker -->
    <section v-if="node.tokens && node.tokens.total_tokens > 0" class="section">
      <h4>
        Token Usage
        <button
          v-if="selfUrl && tokenizerSegments.length > 0"
          class="tokenize-btn"
          @click="showTokenizer = true"
          title="View color-coded token breakdown"
        >Tokenize</button>
      </h4>
      <div class="token-grid">
        <div class="token-cell">
          <span class="token-num">{{ fmtTokens(node.tokens.input_tokens) }}</span>
          <span class="token-label">Input</span>
        </div>
        <div class="token-cell">
          <span class="token-num">{{ fmtTokens(node.tokens.output_tokens) }}</span>
          <span class="token-label">Output</span>
        </div>
        <div class="token-cell">
          <span class="token-num total">{{ fmtTokens(node.tokens.total_tokens) }}</span>
          <span class="token-label">Total</span>
        </div>
      </div>
      <div class="token-bar-row">
        <div class="token-bar full">
          <div class="token-fill input" :style="{ width: inputPct + '%' }" title="Input tokens"></div>
          <div class="token-fill output" :style="{ width: (100 - inputPct) + '%' }" title="Output tokens"></div>
        </div>
      </div>
      <!-- Context window usage (estimated from model info if available) -->
      <div v-if="contextWindowSize > 0" class="context-usage">
        <div class="context-label">
          <span>Context Window</span>
          <span class="context-pct">{{ contextPct }}%</span>
        </div>
        <div class="token-bar full">
          <div class="token-fill context" :style="{ width: Math.min(contextPct, 100) + '%' }" :class="{ warning: contextPct > 75, danger: contextPct > 90 }"></div>
        </div>
        <span class="context-detail">{{ fmtTokens(node.tokens.input_tokens) }} / {{ fmtTokens(contextWindowSize) }}</span>
      </div>
    </section>

    <!-- Input -->
    <section class="section">
      <h4>Input</h4>
      <div class="kv-list">
        <template v-if="node.type === 'agent'">
          <div class="kv"><span class="k">Role</span><span class="v">{{ p.role }}</span></div>
          <div class="kv"><span class="k">Provider</span><span class="v">{{ p.provider || '(default)' }}</span></div>
          <div class="kv"><span class="k">Model</span><span class="v">{{ p.model || '(default)' }}</span></div>
          <div class="kv" v-if="p.tools"><span class="k">Tools</span><span class="v">{{ (p.tools as string[]).join(', ') }}</span></div>
          <!-- Reasoning stream (chain-of-thought) — rendered above the
               committed answer so operators can see "model is thinking"
               during the long reasoning phase of reasoning models. -->
          <StreamingText
            v-if="agentStreamingReasoning"
            :text="agentStreamingReasoning"
            :is-streaming="node.status === 'running' && !agentStreamingText"
            variant="reasoning"
            label="Reasoning"
          />
          <!-- Committed answer stream -->
          <StreamingText
            v-if="agentStreamingText"
            :text="agentStreamingText"
            :is-streaming="node.status === 'running'"
            :label="agentStreamingReasoning ? 'Answer' : undefined"
          />
        </template>
        <template v-else-if="node.type === 'tool_call'">
          <div class="kv"><span class="k">Tool</span><span class="v">{{ p.tool_name }}</span></div>
          <div class="kv"><span class="k">Arguments</span></div>
          <pre class="payload-pre">{{ JSON.stringify(p.arguments, null, 2) }}</pre>
        </template>
        <template v-else-if="node.type === 'thought'">
          <div class="kv"><span class="k">Iteration</span><span class="v">{{ p.iteration }}</span></div>
          <div class="kv"><span class="k">Tools</span><span class="v">{{ (p.available_tools as string[] || []).join(', ') }}</span></div>
          <!-- Reasoning lane (chain-of-thought) — rendered above the answer
               so the long reasoning phase is visible while the model thinks. -->
          <StreamingText
            v-if="isThoughtReasoning || node.streamingReasoning"
            :text="node.streamingReasoning || ''"
            :is-streaming="isThoughtReasoning && !isThoughtStreaming"
            variant="reasoning"
            label="Reasoning"
          />
          <!-- Committed answer stream -->
          <StreamingText
            v-if="isThoughtStreaming"
            :text="node.streamingText || ''"
            :is-streaming="true"
            :label="node.streamingReasoning ? 'Answer' : undefined"
          />
        </template>
        <template v-else-if="node.type === 'pipeline'">
          <div class="kv"><span class="k">Query</span><span class="v">{{ p.query }}</span></div>
          <div class="kv"><span class="k">Steps</span><span class="v">{{ p.step_count }}</span></div>
          <!-- Show aggregated step outputs for pipeline -->
          <StreamingText
            v-if="pipelineOutput"
            :text="pipelineOutput"
            :is-streaming="node.status === 'running'"
          />
        </template>
        <template v-else-if="node.type === 'step'">
          <div class="kv"><span class="k">Type</span><span class="v">{{ p.step_type }}</span></div>
          <div class="kv" v-if="p.agents"><span class="k">Agents</span><span class="v">{{ (p.agents as string[]).join(', ') }}</span></div>
          <div class="kv" v-if="p.agents"><span class="k">Agents</span><span class="v">{{ (p.agents as string[]).join(', ') }}</span></div>
        </template>
        <template v-else>
          <pre class="payload-pre">{{ JSON.stringify(p, null, 2) }}</pre>
        </template>
      </div>
    </section>

    <!-- Output -->
    <section v-if="node.endEvent" class="section">
      <h4>Output</h4>
      <div class="kv-list">
        <div class="kv"><span class="k">Status</span><span class="v" :class="node.status">{{ node.status }}</span></div>
        <div class="kv" v-if="node.durationMs"><span class="k">Duration</span><span class="v">{{ fmtDuration(node.durationMs) }}</span></div>
        <template v-if="node.type === 'agent'">
          <div class="kv"><span class="k">Iterations</span><span class="v">{{ endP.iterations }}</span></div>
          <div class="kv" v-if="endP.final_answer"><span class="k">Answer</span></div>
          <pre v-if="endP.final_answer" class="payload-pre answer">{{ endP.final_answer }}</pre>
        </template>
        <template v-else-if="node.type === 'tool_call'">
          <div class="kv" v-if="endP.error"><span class="k">Error</span><span class="v error">{{ endP.error }}</span></div>
          <div class="kv" v-if="endP.exit_code !== undefined"><span class="k">Exit Code</span><span class="v">{{ endP.exit_code }}</span></div>
          <pre v-if="endP.output" class="payload-pre">{{ endP.output }}</pre>
        </template>
        <template v-else-if="node.type === 'thought'">
          <div class="kv" v-if="endP.reasoning"><span class="k">Reasoning</span></div>
          <pre v-if="endP.reasoning" class="payload-pre">{{ endP.reasoning }}</pre>
          <div class="kv" v-if="endP.plan"><span class="k">Plan</span><span class="v">{{ (endP.plan as string[])?.join(' -> ') }}</span></div>
        </template>
        <template v-else-if="node.type === 'pipeline'">
          <div class="kv"><span class="k">Steps Run</span><span class="v">{{ endP.steps_run }}</span></div>
        </template>
        <template v-else-if="node.type === 'step'">
          <pre v-if="endP.output" class="payload-pre">{{ endP.output }}</pre>
        </template>
        <template v-else-if="node.type === 'reflection'">
          <pre class="payload-pre">{{ endP.reflection }}</pre>
        </template>
        <template v-else-if="node.type === 'ground_check'">
          <div class="kv"><span class="k">Valid</span><span class="v">{{ endP.is_valid ? 'Yes' : 'No' }}</span></div>
          <div class="kv"><span class="k">Confidence</span>
            <div class="confidence-bar"><div class="confidence-fill" :style="{ width: ((endP.confidence as number) || 0) * 100 + '%' }"></div></div>
          </div>
          <pre v-if="endP.assessment" class="payload-pre">{{ endP.assessment }}</pre>
        </template>
        <template v-else>
          <pre class="payload-pre">{{ JSON.stringify(endP, null, 2) }}</pre>
        </template>
      </div>
    </section>

    <!-- Replay (iteration nodes, history mode only) -->
    <section v-if="isHistory && node.type === 'iteration' && node.iteration !== undefined" class="section">
      <button
        class="action-btn"
        @click="emit('replay-from', node.iteration!, node.agentName)"
      >Replay from Iteration {{ node.iteration }}</button>
    </section>

    <!-- Run to here (debug mode, running — like "run to cursor") -->
    <section v-if="isConnected && !isPreview && runToCondition" class="section">
      <button
        class="action-btn run-to-btn"
        @click="emit('run-to-node', runToCondition)"
        :title="runToDescription"
      >Run to here</button>
      <span class="action-hint">{{ runToDescription }}</span>
    </section>

    <!-- Debug from here (history mode only — re-launch with breakpoint at this node) -->
    <section v-if="isHistory && !isPreview && runToCondition" class="section">
      <button
        class="action-btn debug-from-btn"
        @click="emit('debug-from-here', runToCondition)"
        :title="'Re-run session in Debug mode, pause at: ' + runToDescription"
      >Debug from here</button>
      <span class="action-hint">Re-run with breakpoint at {{ runToDescription }}</span>
    </section>

    <!-- Re-run from step (pipeline step nodes, live mode) -->
    <section v-if="node.type === 'step' && isConnected" class="section">
      <button
        class="action-btn rerun-btn"
        @click="emit('rerun-from-step', node.label)"
      >Re-run from here</button>
    </section>

    <!-- Replay Result -->
    <section v-if="replayResult" class="section">
      <h4>Replay Result</h4>
      <div class="kv-list">
        <div class="kv"><span class="k">Agent</span><span class="v">{{ replayResult.agent_name }}</span></div>
        <div class="kv"><span class="k">Iteration</span><span class="v">{{ replayResult.iteration }}</span></div>
        <div class="kv"><span class="k">Query</span><span class="v">{{ replayResult.query }}</span></div>
        <div class="kv"><span class="k">Events</span><span class="v">{{ replayResult.events }}</span></div>
      </div>
      <template v-if="Array.isArray(replayResult.history)">
        <h4 style="margin-top: 8px;">Reconstructed History</h4>
        <div v-for="(msg, i) in (replayResult.history as Record<string, unknown>[])" :key="i" class="replay-msg">
          <span class="replay-role">{{ msg.role }}</span>
          <pre class="payload-pre">{{ typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content, null, 2) }}</pre>
        </div>
      </template>
    </section>

    <!-- Pause Context (shown when node is paused) -->
    <section v-if="pauseCtx" class="section">
      <h4>Pause Context</h4>
      <div class="checkpoint-badge" :class="pauseCtx.checkpoint">{{ pauseCtx.checkpoint }}</div>

      <!-- Token Budget Bar -->
      <div v-if="pauseCtx.context_window" class="budget-section">
        <div class="kv"><span class="k">Budget</span><span class="v" :style="{ color: budgetColor }">{{ budgetPct }}%</span></div>
        <div class="budget-bar">
          <div class="budget-fill" :style="{ width: budgetPct + '%', background: budgetColor }"></div>
        </div>
        <div class="budget-detail">
          {{ fmtTokens((pauseCtx.total_tokens_in ?? 0) + (pauseCtx.total_tokens_out ?? 0)) }} / {{ fmtTokens(pauseCtx.context_window) }} tokens
        </div>
      </div>

      <!-- Token Counts -->
      <div class="kv-list" style="margin-top: 6px;">
        <div class="kv"><span class="k">Tokens In</span><span class="v">{{ fmtTokens(pauseCtx.total_tokens_in) }}</span></div>
        <div class="kv"><span class="k">Tokens Out</span><span class="v">{{ fmtTokens(pauseCtx.total_tokens_out) }}</span></div>
        <div v-if="pauseCtx.predicted_next_tokens" class="kv">
          <span class="k">Predicted</span><span class="v">{{ fmtTokens(pauseCtx.predicted_next_tokens) }}</span>
        </div>
      </div>

      <!-- Progress -->
      <div class="kv-list" style="margin-top: 6px;">
        <div class="kv"><span class="k">Iteration</span><span class="v">{{ pauseCtx.iteration }} / {{ pauseCtx.max_iterations || '∞' }}</span></div>
        <div class="kv"><span class="k">History</span><span class="v">{{ pauseCtx.history_length }} messages</span></div>
        <div v-if="pauseCtx.max_tokens" class="kv"><span class="k">Max Tokens</span><span class="v">{{ fmtTokens(pauseCtx.max_tokens) }}</span></div>
      </div>

      <!-- Last Thought -->
      <div v-if="pauseCtx.last_thought" style="margin-top: 8px;">
        <h4>Last Thought</h4>
        <pre class="payload-pre answer">{{ pauseCtx.last_thought }}</pre>
      </div>

      <!-- Pending Tool Calls -->
      <div v-if="pauseCtx.pending_tools?.length" style="margin-top: 8px;">
        <h4>Pending Tool Calls</h4>
        <div v-for="(tool, i) in pauseCtx.pending_tools" :key="i" class="pending-tool">
          <div class="pending-tool-header" @click="toggleTool(i)">
            <span class="pending-tool-arrow">{{ expandedTools.has(i) ? 'v' : '>' }}</span>
            <span class="pending-tool-name">{{ tool.name }}</span>
          </div>
          <pre v-if="expandedTools.has(i)" class="payload-pre">{{ JSON.stringify(tool.arguments, null, 2) }}</pre>
        </div>
      </div>
    </section>

    <!-- Full NodeEditor (paused or preview + agent/orchestrator node) — same as Visual Builder -->
    <section v-if="showNodeEditor && ((isConnected && isPaused) || isPreview)" class="section experiment-section">
      <div class="experiment-hint">{{ isPreview ? 'Preview — review and edit settings before starting' : 'Paused — edit settings below then resume' }}</div>
      <NodeEditor
        :node-id="node.id"
        :node-type="nodeEditorType"
        :node-data="nodeEditorData"
        :provider-names="configProviderNames || []"
        :provider-map="(configProviderMap as any) || {}"
        :tool-names="allConfigTools || []"
        :default-settings="(configDefaults as any) || {}"
        @update="handleDebugNodeUpdate"
        @close="emit('close')"
      />
    </section>

    <!-- Simple Parameter editor (agent nodes, live mode, not paused) -->
    <section v-else-if="node.type === 'agent' && isConnected" class="section">
      <ParamEditor
        :agent-name="node.agentName"
        :is-connected="!!isConnected"
        :current-model="(p.model as string) || ''"
        :current-provider="(p.provider as string) || ''"
        :current-temperature="(p.temperature as number) || undefined"
        :current-max-tokens="(p.max_tokens as number) || undefined"
        @apply="(agent, ovr) => emit('apply-params', agent, ovr)"
        @reset="(agent) => emit('reset-params', agent)"
      />
    </section>

    <!-- Hint for non-agent/non-pipeline nodes when paused -->
    <section v-if="!showNodeEditor && isConnected && isPaused && node.agentName" class="section">
      <div class="experiment-hint">
        Select the <strong>{{ node.agentName }}</strong> agent node to override its parameters while paused
      </div>
    </section>

    <!-- Tokenizer panel (teleports to body) -->
    <TokenizerPanel
      v-if="selfUrl"
      :self-url="selfUrl"
      :visible="showTokenizer"
      :initial-segments="tokenizerSegments"
      @close="showTokenizer = false"
    />
  </div>
</template>

<style scoped>
.detail-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow-y: auto;
  background: var(--surface-2);
  border-left: 1px solid var(--border-subtle);
  color: var(--text-primary);
}
.panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-subtle);
  font-weight: 600;
  font-size: 13px;
  color: var(--text-primary);
}
.close-btn {
  background: none;
  border: none;
  cursor: pointer;
  font-size: 16px;
  color: var(--text-secondary);
  padding: 2px 6px;
  border-radius: var(--node-radius);
}
.close-btn:hover { background: var(--surface-4); }

.section {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-subtle);
}
.experiment-section {
  border-left: 3px solid var(--status-paused);
  background: rgba(245,158,11,0.08);
}
.experiment-hint {
  font-size: 11px;
  color: var(--status-paused);
  margin-bottom: 6px;
  font-weight: 500;
}
.section h4 {
  margin: 0 0 8px;
  font-size: 11px;
  text-transform: uppercase;
  color: var(--text-muted);
  letter-spacing: 0.5px;
  font-family: var(--font-mono);
  display: flex;
  align-items: center;
  gap: 8px;
}

.tokenize-btn {
  font-size: 10px;
  font-family: var(--font-mono);
  text-transform: none;
  padding: 1px 6px;
  border: 1px solid var(--border-default);
  background: var(--surface-3);
  color: var(--accent-skill);
  border-radius: 3px;
  cursor: pointer;
  letter-spacing: 0;
}
.tokenize-btn:hover {
  background: var(--surface-4);
}

.token-grid {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
.token-cell {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 6px;
  background: var(--surface-3);
  border-radius: var(--node-radius);
}
.token-num { font-size: 14px; font-weight: 700; color: var(--text-primary); font-family: var(--font-mono); }
.token-num.total { color: var(--accent-agent); }
.token-label { font-size: 10px; color: var(--text-muted); margin-top: 2px; font-family: var(--font-mono); }
.token-bar-row { margin-bottom: 6px; }
.token-bar {
  height: 8px;
  background: var(--surface-1);
  border-radius: 4px;
  overflow: hidden;
}
.token-bar.full { display: flex; }
.token-fill { height: 100%; }
.token-fill.input { background: var(--accent-agent); }
.token-fill.output { background: var(--accent-tool); }
.token-fill.context { background: var(--status-running); transition: width 0.3s; }
.token-fill.context.warning { background: var(--status-paused); }
.token-fill.context.danger { background: var(--status-error); }
.context-usage { margin-top: 8px; }
.context-label {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--text-secondary);
  margin-bottom: 3px;
}
.context-pct { font-weight: 700; color: var(--text-primary); font-family: var(--font-mono); }
.context-detail { font-size: 10px; color: var(--text-muted); margin-top: 2px; display: block; font-family: var(--font-mono); }

.kv-list { font-size: 12px; }
.kv {
  display: flex;
  gap: 8px;
  margin-bottom: 4px;
}
.k { color: var(--text-muted); min-width: 70px; flex-shrink: 0; font-family: var(--font-mono); font-size: 11px; }
.v { color: var(--text-secondary); word-break: break-all; font-family: var(--font-mono); font-size: 12px; }
.v.success { color: var(--status-running); }
.v.error { color: var(--status-error); }

.payload-pre {
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--node-radius);
  padding: 8px;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-primary);
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 200px;
  overflow-y: auto;
  margin: 4px 0;
}
.payload-pre.answer { max-height: 400px; }

.confidence-bar {
  flex: 1;
  height: 8px;
  background: var(--surface-1);
  border-radius: 4px;
  overflow: hidden;
}
.confidence-fill {
  height: 100%;
  background: var(--status-running);
  border-radius: 4px;
}

.exp-badge {
  font-size: 9px;
  background: var(--status-paused);
  color: white;
  padding: 1px 5px;
  border-radius: 2px;
  font-weight: 400;
  margin-left: 6px;
  vertical-align: middle;
  font-family: var(--font-mono);
}

.action-btn {
  padding: 6px 12px;
  background: var(--accent-agent);
  color: white;
  border: none;
  border-radius: var(--node-radius);
  font-size: 12px;
  cursor: pointer;
  font-family: var(--font-sans);
}
.action-btn:hover { background: #4a7cdf; }
.rerun-btn { background: var(--accent-tool); }
.rerun-btn:hover { background: #25b78a; }
.run-to-btn { background: var(--accent-agent); }
.run-to-btn:hover { background: #4a7cdf; }
.debug-from-btn { background: var(--status-paused); }
.debug-from-btn:hover { background: #d97706; }
.action-hint {
  font-size: 10px;
  color: var(--text-muted);
  margin-top: 4px;
  display: block;
}

.checkpoint-badge {
  display: inline-block;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 2px 8px;
  border-radius: 2px;
  margin-bottom: 8px;
  background: rgba(91,141,239,0.15);
  color: var(--accent-agent);
  font-family: var(--font-mono);
}
.checkpoint-badge.pre_tool { background: rgba(245,158,11,0.15); color: var(--status-paused); }
.checkpoint-badge.post_thought { background: rgba(34,197,94,0.15); color: var(--status-running); }

.budget-section { margin-top: 6px; }
.budget-bar {
  height: 10px;
  background: var(--surface-1);
  border-radius: 5px;
  overflow: hidden;
  margin: 4px 0;
}
.budget-fill {
  height: 100%;
  border-radius: 5px;
  transition: width 0.3s;
}
.budget-detail {
  font-size: 10px;
  color: var(--text-muted);
  font-family: var(--font-mono);
}

.pending-tool { margin-bottom: 4px; }
.pending-tool-header {
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  font-size: 12px;
  padding: 2px 0;
  color: var(--text-primary);
}
.pending-tool-header:hover { color: var(--accent-agent); }
.pending-tool-arrow {
  font-family: var(--font-mono);
  font-size: 10px;
  width: 12px;
  color: var(--text-muted);
}
.pending-tool-name { font-weight: 500; }

.replay-msg {
  margin-bottom: 8px;
}
.replay-role {
  display: inline-block;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  padding: 1px 6px;
  border-radius: 2px;
  margin-bottom: 2px;
  background: rgba(91,141,239,0.15);
  color: var(--accent-agent);
  font-family: var(--font-mono);
}
</style>
