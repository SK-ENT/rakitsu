<script setup lang="ts">
import { ref, computed } from 'vue';
import type { AgentEvent, AgentEndPayload, StructuredThought, ToolCallStartPayload, ToolCallEndPayload, ReflectionEndPayload, GroundCheckEndPayload, PipelineStartPayload, PipelineStepStartPayload, PipelineStepEndPayload, AgentHandoffPayload, AgentMessagePayload, RetryAttemptPayload, ContextCompressedPayload, RetrievalInjectedPayload, SalvagedOutputPayload, WorkerRedelegationBlockedPayload, RollbackPayload, WakeTickPayload, WakeEscalatePayload } from '../../types';

const props = defineProps<{
  event: AgentEvent;
  depth?: number;
}>();

const isExpanded = ref(true);

const eventIcon = computed(() => {
  switch (props.event.event_type) {
    case 'AGENT_START': return '🤖';
    case 'AGENT_END': return '🏁';
    case 'THOUGHT_START': return '💭';
    case 'THOUGHT_END': return '🧠';
    case 'TOOL_CALL_START': return '🔧';
    case 'TOOL_CALL_END': return '⚡';
    case 'TOKEN_USAGE': return '📊';
    case 'AGENT_HANDOFF': return '🤝';
    case 'AGENT_MESSAGE': return '💬';
    case 'ERROR': return '❌';
    case 'EXECUTION_COMPLETE': return '✅';
    case 'REFLECTION_START': return '🪞';
    case 'REFLECTION_END': return '🪞';
    case 'GROUND_CHECK_START': return '🔍';
    case 'GROUND_CHECK_END': return '🔍';
    case 'PIPELINE_START': return '🚀';
    case 'PIPELINE_END': return '🏁';
    case 'PIPELINE_STEP_START': return '▶️';
    case 'PIPELINE_STEP_END': return '✅';
    case 'DEBUG_PAUSED': return '⏸️';
    case 'DEBUG_RESUMED': return '▶️';
    case 'TOKEN_CHUNK': return '📝';
    case 'REASONING_CHUNK': return '💭';
    case 'REPLAY_START': return '⏪';
    case 'REPLAY_END': return '⏹️';
    case 'RETRY_ATTEMPT': return '⚠️';
    case 'CONTEXT_COMPRESSED': return '🗜';
    case 'RETRIEVAL_INJECTED': return '🔎';
    case 'SALVAGED_OUTPUT': return '⚠️';
    case 'WORKER_REDELEGATION_BLOCKED': return '⊘';
    case 'ROLLBACK': return '↩️';
    case 'MEMORY_WRITE': return '🧠';
    case 'MEMORY_RECALL': return '🔎';
    case 'SESSION_MSG_SENT': return '↪';
    case 'SESSION_MSG_RECEIVED': return '↩';
    case 'WAKE_TICK': return '⏱';
    case 'WAKE_ESCALATE': return '⏰';
    case 'WAKE_TASK_START': return '▶';
    case 'WAKE_TASK_END': return '■';
    default: return '📌';
  }
});

const eventClass = computed(() => {
  return `event-item event-${props.event.event_type}`;
});

const formattedTime = computed(() => {
  const date = new Date(props.event.timestamp);
  return date.toLocaleTimeString();
});

const formattedPayload = computed(() => {
  return JSON.stringify(props.event.payload, null, 2);
});

const thoughtData = computed((): StructuredThought | null => {
  if (props.event.event_type === 'THOUGHT_END') {
    return props.event.payload as unknown as StructuredThought;
  }
  return null;
});

const toolCallStart = computed((): ToolCallStartPayload | null => {
  if (props.event.event_type === 'TOOL_CALL_START') {
    return props.event.payload as unknown as ToolCallStartPayload;
  }
  return null;
});

const toolCallEnd = computed((): ToolCallEndPayload | null => {
  if (props.event.event_type === 'TOOL_CALL_END') {
    return props.event.payload as unknown as ToolCallEndPayload;
  }
  return null;
});

const reflectionData = computed((): ReflectionEndPayload | null => {
  if (props.event.event_type === 'REFLECTION_END') {
    return props.event.payload as unknown as ReflectionEndPayload;
  }
  return null;
});

const groundCheckData = computed((): GroundCheckEndPayload | null => {
  if (props.event.event_type === 'GROUND_CHECK_END') {
    return props.event.payload as unknown as GroundCheckEndPayload;
  }
  return null;
});

const handoffData = computed((): AgentHandoffPayload | null => {
  if (props.event.event_type === 'AGENT_HANDOFF') {
    return props.event.payload as unknown as AgentHandoffPayload;
  }
  return null;
});

const messageData = computed((): AgentMessagePayload | null => {
  if (props.event.event_type === 'AGENT_MESSAGE') {
    return props.event.payload as unknown as AgentMessagePayload;
  }
  return null;
});

const pipelineStartData = computed((): PipelineStartPayload | null => {
  if (props.event.event_type === 'PIPELINE_START') {
    return props.event.payload as unknown as PipelineStartPayload;
  }
  return null;
});

const pipelineStepStartData = computed((): PipelineStepStartPayload | null => {
  if (props.event.event_type === 'PIPELINE_STEP_START') {
    return props.event.payload as unknown as PipelineStepStartPayload;
  }
  return null;
});

const pipelineStepEndData = computed((): PipelineStepEndPayload | null => {
  if (props.event.event_type === 'PIPELINE_STEP_END') {
    return props.event.payload as unknown as PipelineStepEndPayload;
  }
  return null;
});

const retryData = computed((): RetryAttemptPayload | null => {
  if (props.event.event_type === 'RETRY_ATTEMPT') {
    return props.event.payload as unknown as RetryAttemptPayload;
  }
  return null;
});

const contextCompressedData = computed((): ContextCompressedPayload | null => {
  if (props.event.event_type === 'CONTEXT_COMPRESSED') {
    return props.event.payload as unknown as ContextCompressedPayload;
  }
  return null;
});

const retrievalInjectedData = computed((): RetrievalInjectedPayload | null => {
  if (props.event.event_type === 'RETRIEVAL_INJECTED') {
    return props.event.payload as unknown as RetrievalInjectedPayload;
  }
  return null;
});

const agentEndData = computed((): AgentEndPayload | null => {
  if (props.event.event_type === 'AGENT_END') {
    return props.event.payload as unknown as AgentEndPayload;
  }
  return null;
});

const salvagedData = computed((): SalvagedOutputPayload | null => {
  if (props.event.event_type === 'SALVAGED_OUTPUT') {
    return props.event.payload as unknown as SalvagedOutputPayload;
  }
  return null;
});

const redelegationBlockedData = computed((): WorkerRedelegationBlockedPayload | null => {
  if (props.event.event_type === 'WORKER_REDELEGATION_BLOCKED') {
    return props.event.payload as unknown as WorkerRedelegationBlockedPayload;
  }
  return null;
});

const rollbackData = computed((): RollbackPayload | null => {
  if (props.event.event_type === 'ROLLBACK') {
    return props.event.payload as unknown as RollbackPayload;
  }
  return null;
});

const wakeTickData = computed((): WakeTickPayload | null => {
  if (props.event.event_type === 'WAKE_TICK') {
    return props.event.payload as unknown as WakeTickPayload;
  }
  return null;
});

const wakeEscalateData = computed((): WakeEscalatePayload | null => {
  if (props.event.event_type === 'WAKE_ESCALATE') {
    return props.event.payload as unknown as WakeEscalatePayload;
  }
  return null;
});

function toggleExpand() {
  isExpanded.value = !isExpanded.value;
}
</script>

<template>
  <div :class="eventClass" :style="{ marginLeft: `${(depth || 0) * 16}px` }">
    <div class="event-header" @click="toggleExpand">
      <span class="expand-icon">{{ isExpanded ? '▼' : '▶' }}</span>
      <span class="event-icon">{{ eventIcon }}</span>
      <span class="event-type">{{ event.event_type }}</span>
      <span class="event-agent" v-if="event.agent_name">{{ event.agent_name }}</span>
      <span class="event-time">{{ formattedTime }}</span>
    </div>
    
    <div class="event-content" v-if="isExpanded">
      <!-- Structured Thought Display -->
      <div v-if="thoughtData" class="thought-content">
        <div class="thought-field" v-if="thoughtData.reasoning">
          <label>Reasoning:</label>
          <p>{{ thoughtData.reasoning }}</p>
        </div>
        <div class="thought-field" v-if="thoughtData.plan && thoughtData.plan.length > 0">
          <label>Plan:</label>
          <ul>
            <li v-for="(step, idx) in thoughtData.plan" :key="idx">{{ step }}</li>
          </ul>
        </div>
        <div class="thought-field" v-if="thoughtData.confidence">
          <label>Confidence:</label>
          <span>{{ (thoughtData.confidence * 100).toFixed(0) }}%</span>
        </div>
        <div class="thought-field" v-if="thoughtData.alternatives && thoughtData.alternatives.length > 0">
          <label>Alternatives:</label>
          <ul>
            <li v-for="(alt, idx) in thoughtData.alternatives" :key="idx">{{ alt }}</li>
          </ul>
        </div>
      </div>
      
      <!-- Tool Call Start Display -->
      <div v-else-if="toolCallStart" class="tool-call-content">
        <div class="tool-name">
          <label>Tool:</label>
          <span>{{ toolCallStart.tool_name }}</span>
        </div>
        <div class="tool-args">
          <label>Arguments:</label>
          <pre>{{ JSON.stringify(toolCallStart.arguments, null, 2) }}</pre>
        </div>
      </div>
      
      <!-- Tool Call End Display -->
      <div v-else-if="toolCallEnd" class="tool-result-content">
        <div class="tool-name">
          <label>Tool:</label>
          <span>{{ toolCallEnd.tool_name }}</span>
        </div>
        <div v-if="event.duration_ms" class="tool-duration">
          <label>Duration:</label>
          <span>{{ event.duration_ms }}ms</span>
        </div>
        <div class="tool-output" :class="{ error: !!toolCallEnd.error }">
          <label>Output:</label>
          <pre>{{ toolCallEnd.output }}</pre>
        </div>
        <div v-if="toolCallEnd.error" class="tool-error">
          <label>Error:</label>
          <pre>{{ toolCallEnd.error }}</pre>
        </div>
      </div>
      
      <!-- Reflection Display -->
      <div v-else-if="reflectionData" class="reflection-content">
        <div class="reflection-mode">
          <label>Mode:</label>
          <span class="mode-badge">{{ reflectionData.mode }}</span>
        </div>
        <div class="reflection-text">
          <label>Reflection:</label>
          <p>{{ reflectionData.reflection }}</p>
        </div>
      </div>

      <!-- Ground Check Display -->
      <div v-else-if="groundCheckData" class="ground-check-content">
        <div class="gc-status">
          <span class="gc-badge" :class="{ valid: groundCheckData.is_valid, invalid: !groundCheckData.is_valid }">
            {{ groundCheckData.is_valid ? 'VALID' : 'INVALID' }}
          </span>
          <span class="gc-action">Action: {{ groundCheckData.action }}</span>
        </div>
        <div class="gc-confidence">
          <label>Confidence:</label>
          <div class="confidence-bar">
            <div class="confidence-fill" :style="{ width: `${groundCheckData.confidence * 100}%` }" :class="{ low: groundCheckData.confidence < 0.5, medium: groundCheckData.confidence >= 0.5 && groundCheckData.confidence < 0.7, high: groundCheckData.confidence >= 0.7 }"></div>
          </div>
          <span class="confidence-value">{{ (groundCheckData.confidence * 100).toFixed(0) }}%</span>
        </div>
        <div class="gc-issues" v-if="groundCheckData.issues && groundCheckData.issues.length > 0">
          <label>Issues:</label>
          <ul>
            <li v-for="(issue, idx) in groundCheckData.issues" :key="idx">{{ issue }}</li>
          </ul>
        </div>
        <div class="gc-assessment" v-if="groundCheckData.assessment">
          <label>Assessment:</label>
          <p>{{ groundCheckData.assessment }}</p>
        </div>
      </div>

      <!-- Agent Handoff Display -->
      <div v-else-if="handoffData" class="handoff-content">
        <div class="handoff-info">
          <span class="handoff-icon">&#x2937;</span>
          <span class="handoff-label">Delegating to</span>
          <span class="handoff-agent">{{ handoffData.to_agent }}</span>
          <span v-if="handoffData.prior_step_count > 0" class="handoff-context-badge">
            +{{ handoffData.prior_step_count }} prior step{{ handoffData.prior_step_count > 1 ? 's' : '' }} injected
          </span>
        </div>
        <!-- Show full enriched context when prior steps were injected; raw task otherwise -->
        <div v-if="handoffData.prior_step_count > 0 && handoffData.full_context" class="handoff-task">
          <label>Context sent to agent:</label>
          <p>{{ handoffData.full_context }}</p>
        </div>
        <div v-else-if="handoffData.task" class="handoff-task">
          <label>Task:</label>
          <p>{{ handoffData.task }}</p>
        </div>
      </div>

      <!-- Agent Message Display -->
      <div v-else-if="messageData" class="message-content">
        <div class="message-info">
          <span class="handoff-icon">&#x2936;</span>
          <span class="handoff-label">Result from</span>
          <span class="handoff-agent">{{ messageData.from_agent }}</span>
        </div>
        <div v-if="messageData.message" class="message-text">
          <label>Response:</label>
          <pre>{{ messageData.message }}</pre>
        </div>
      </div>

      <!-- Pipeline Start Display -->
      <div v-else-if="pipelineStartData" class="pipeline-content">
        <div class="pipeline-info">
          <span class="pipeline-badge">Pipeline</span>
          <span>{{ pipelineStartData.step_count }} steps</span>
        </div>
      </div>

      <!-- Pipeline Step Start Display -->
      <div v-else-if="pipelineStepStartData" class="pipeline-step-content">
        <div class="pipeline-step-info">
          <span class="step-type-badge" :class="pipelineStepStartData.step_type">{{ pipelineStepStartData.step_type }}</span>
          <span class="step-name">{{ pipelineStepStartData.step_name }}</span>
          <span v-if="pipelineStepStartData.agents" class="step-agents">{{ pipelineStepStartData.agents.join(', ') }}</span>
        </div>
      </div>

      <!-- Pipeline Step End Display -->
      <div v-else-if="pipelineStepEndData" class="pipeline-step-content">
        <div class="pipeline-step-info">
          <span class="step-type-badge" :class="pipelineStepEndData.step_type">{{ pipelineStepEndData.step_type }}</span>
          <span class="step-name">{{ pipelineStepEndData.step_name }}</span>
          <span class="gc-badge" :class="{ valid: pipelineStepEndData.status === 'success', invalid: pipelineStepEndData.status === 'error' }">
            {{ pipelineStepEndData.status }}
          </span>
        </div>
        <div v-if="pipelineStepEndData.output" class="step-output">
          <label>Output:</label>
          <pre>{{ pipelineStepEndData.output }}</pre>
        </div>
      </div>

      <!-- Retry Attempt Display -->
      <div v-else-if="retryData" class="retry-content">
        <div class="retry-info">
          <span class="retry-badge">Retry {{ retryData.attempt }}/{{ retryData.max_attempts }}</span>
          <span class="retry-error">{{ retryData.error }}</span>
        </div>
      </div>

      <!-- Context Compressed Display -->
      <div v-else-if="contextCompressedData" class="context-compressed-content">
        <div class="context-compressed-info">
          <span class="context-compressed-badge">{{ contextCompressedData.from_strategy }} → {{ contextCompressedData.to_strategy }}</span>
          <span class="context-compressed-detail">iter {{ contextCompressedData.iteration }}, {{ contextCompressedData.messages_before }}→{{ contextCompressedData.messages_after }} msgs</span>
          <span v-if="contextCompressedData.pressure > 0" class="context-compressed-pressure">{{ (contextCompressedData.pressure * 100).toFixed(0) }}% ctx</span>
        </div>
      </div>

      <!-- Retrieval Injected Display -->
      <div v-else-if="retrievalInjectedData" class="retrieval-injected-content">
        <div class="retrieval-injected-info">
          <span class="retrieval-injected-badge">{{ retrievalInjectedData.backend }}</span>
          <span class="retrieval-injected-detail">{{ retrievalInjectedData.segments }} segments · iter {{ retrievalInjectedData.iteration }}</span>
        </div>
      </div>

      <!-- Salvaged Output Display (Track A) -->
      <div v-else-if="salvagedData" class="salvaged-content">
        <div class="salvaged-header">
          <span class="salvaged-badge" :class="{ recovered: salvagedData.recovery, terminal: !salvagedData.recovery }">
            {{ salvagedData.recovery ? 'recovery' : 'terminal' }}
          </span>
          <span class="salvaged-meta">iteration {{ salvagedData.iteration }}</span>
          <span class="salvaged-meta">{{ salvagedData.reasoning_chars.toLocaleString() }} reasoning chars</span>
        </div>
        <div v-if="salvagedData.content_preview" class="salvaged-preview">
          <label>Preview:</label>
          <pre>{{ salvagedData.content_preview }}</pre>
        </div>
        <div class="salvaged-explainer">
          <p v-if="salvagedData.recovery">
            Model emitted reasoning-only output. Agent injected a recovery directive and continued to the next iteration.
          </p>
          <p v-else>
            Max iterations exhausted while output was still salvaged. AGENT_END status will be "salvaged_no_progress".
          </p>
        </div>
      </div>

      <!-- Worker Redelegation Blocked Display (Track A) -->
      <div v-else-if="redelegationBlockedData" class="redelegation-blocked-content">
        <div class="redelegation-blocked-header">
          <span class="redelegation-blocked-badge">blocked</span>
          <span class="redelegation-blocked-worker">{{ redelegationBlockedData.blocked_worker }}</span>
        </div>
        <div class="redelegation-blocked-meta">
          <span>from: {{ redelegationBlockedData.from_agent }}</span>
          <span>post-salvage count: {{ redelegationBlockedData.post_salvage_count }}</span>
        </div>
        <div class="redelegation-blocked-reason">
          <label>Reason:</label>
          <span>{{ redelegationBlockedData.reason }}</span>
        </div>
      </div>

      <!-- Rollback (runtime self-correction) Display -->
      <div v-else-if="rollbackData" class="rollback-content">
        <div class="rollback-header">
          <span class="rollback-badge">rewound</span>
          <span class="rollback-meta">iteration {{ rollbackData.iteration }}</span>
          <span class="rollback-meta">{{ rollbackData.rollback_num }}/{{ rollbackData.max_rollbacks }}</span>
          <span class="rollback-meta">−{{ rollbackData.messages_pruned }} msgs</span>
        </div>
        <div class="rollback-reason">
          <label>Trigger:</label>
          <span>{{ rollbackData.trigger }} — {{ rollbackData.reason }}</span>
        </div>
        <p class="rollback-explainer">
          The agent discarded this dead-end attempt from its conversation history and retried
          from a clean checkpoint, so the failure does not pollute later context.
        </p>
      </div>

      <!-- Agent End Display -->
      <div v-else-if="agentEndData" class="agent-end-content">
        <div class="agent-end-status">
          <span class="gc-badge" :class="{ valid: agentEndData.status === 'success', invalid: agentEndData.status === 'error', salvaged: agentEndData.status === 'salvaged_no_progress' }">
            {{ agentEndData.status }}
          </span>
          <span class="agent-end-iters">{{ agentEndData.iterations }} iterations</span>
          <span class="agent-end-tokens">{{ agentEndData.total_tokens.toLocaleString() }} tokens</span>
          <span v-if="agentEndData.total_cost" class="agent-end-cost">${{ agentEndData.total_cost.toFixed(4) }}</span>
        </div>
        <div v-if="agentEndData.max_tokens || agentEndData.max_cost" class="agent-end-budget">
          <span class="agent-end-budget-label">Budget:</span>
          <span v-if="agentEndData.max_tokens">{{ agentEndData.total_tokens.toLocaleString() }}/{{ agentEndData.max_tokens.toLocaleString() }} tokens</span>
          <span v-if="agentEndData.max_cost">${{ agentEndData.total_cost?.toFixed(4) || '0.0000' }}/${{ agentEndData.max_cost.toFixed(2) }}</span>
        </div>
        <div v-if="agentEndData.final_answer" class="agent-end-answer">
          <label>Answer:</label>
          <pre>{{ agentEndData.final_answer }}</pre>
        </div>
      </div>

      <!-- Wake Tick Display -->
      <div v-else-if="wakeTickData" class="wake-tick-content">
        <div class="wake-tick-header">
          <span class="wake-tick-badge">Tick #{{ wakeTickData.tick }}</span>
          <span class="wake-outcome-badge" :class="wakeTickData.outcome">{{ wakeTickData.outcome }}</span>
          <span class="wake-summary-badge">summary {{ wakeTickData.summary_chars }}/{{ wakeTickData.summary_cap }}</span>
        </div>
        <div v-if="wakeTickData.interval_seconds" class="wake-tick-interval">
          <label>Interval:</label>
          <span>{{ wakeTickData.interval_seconds }}s</span>
        </div>
        <div v-if="wakeTickData.results && wakeTickData.results.length > 0" class="wake-results">
          <label>Results:</label>
          <ul>
            <li v-for="(result, idx) in wakeTickData.results" :key="idx" class="wake-result-item">
              <span class="result-name">{{ result.name }}</span>
              <span class="result-status" :class="result.status">{{ result.status }}</span>
              <span v-if="result.detail" class="result-detail">{{ result.detail }}</span>
              <span v-if="result.label" class="result-label">{{ result.label }}</span>
            </li>
          </ul>
        </div>
        <div v-if="wakeTickData.note" class="wake-note">
          <label>Note:</label>
          <p>{{ wakeTickData.note }}</p>
        </div>
        <div v-if="wakeTickData.result" class="wake-result-status">
          <label>Result:</label>
          <span :class="wakeTickData.result">{{ wakeTickData.result }}</span>
        </div>
      </div>

      <!-- Wake Escalate Display -->
      <div v-else-if="wakeEscalateData" class="wake-escalate-content">
        <div class="wake-escalate-header">
          <span class="wake-escalate-badge">Wake turn</span>
          <span class="escalate-level-badge">{{ wakeEscalateData.level }}</span>
          <span :class="wakeEscalateData.result ? 'wake-result-' + wakeEscalateData.result : ''">
            {{ wakeEscalateData.result ?? (wakeEscalateData.suppressed ? 'suppressed: ' + wakeEscalateData.suppressed : wakeEscalateData.deferred ? 'deferred' : 'queued') }}
          </span>
          <span class="wake-summary-badge">summary {{ wakeEscalateData.summary_chars }}/{{ wakeEscalateData.summary_cap }}</span>
        </div>
        <div class="wake-escalate-reason">
          <label>Reason:</label>
          <p>{{ wakeEscalateData.reason }}</p>
        </div>
      </div>

      <!-- Default Payload Display -->
      <div v-else class="payload-content">
        <pre>{{ formattedPayload }}</pre>
      </div>
      
      <!-- Token Usage -->
      <div v-if="event.token_usage" class="token-usage">
        <span class="token-badge">
          📊 {{ event.token_usage.input_tokens }} in / {{ event.token_usage.output_tokens }} out
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.event-item {
  border-left: 3px solid var(--border-subtle);
  margin-bottom: 4px;
  background: var(--surface-2);
  border-radius: 0 4px 4px 0;
}

.event-item:hover {
  background: var(--surface-3);
}

.event-AGENT_START { border-left-color: #9c27b0; }
.event-AGENT_END { border-left-color: #673ab7; }
.event-SALVAGED_OUTPUT { border-left-color: #f59e0b; }
.event-WORKER_REDELEGATION_BLOCKED { border-left-color: #f59e0b; }
.event-ROLLBACK { border-left-color: #38bdf8; }
.event-MEMORY_WRITE { border-left-color: #a78bfa; }
.event-MEMORY_RECALL { border-left-color: #a78bfa; }
.event-SESSION_MSG_SENT { border-left-color: #38bdf8; }
.event-SESSION_MSG_RECEIVED { border-left-color: #38bdf8; }
.event-WAKE_TICK { border-left-color: #a78bfa; }
.event-WAKE_ESCALATE { border-left-color: #f59e0b; }
.event-WAKE_TASK_START { border-left-color: #34d399; }
.event-WAKE_TASK_END { border-left-color: #10b981; }
.event-THOUGHT_START { border-left-color: #ff9800; }
.event-THOUGHT_END { border-left-color: #ff5722; }
.event-TOOL_CALL_START { border-left-color: #00bcd4; }
.event-TOOL_CALL_END { border-left-color: #009688; }
.event-TOKEN_USAGE { border-left-color: #795548; }
.event-AGENT_HANDOFF { border-left-color: #e91e63; }
.event-AGENT_MESSAGE { border-left-color: #ff6f00; }
.event-ERROR { border-left-color: #f44336; background: rgba(244, 67, 54, 0.08); }
.event-EXECUTION_COMPLETE { border-left-color: #4caf50; }
.event-REFLECTION_START { border-left-color: #ab47bc; }
.event-REFLECTION_END { border-left-color: #8e24aa; background: rgba(156, 39, 176, 0.08); }
.event-GROUND_CHECK_START { border-left-color: #42a5f5; }
.event-GROUND_CHECK_END { border-left-color: #1e88e5; background: rgba(33, 150, 243, 0.08); }
.event-PIPELINE_START { border-left-color: #2196f3; background: rgba(33, 150, 243, 0.08); }
.event-PIPELINE_END { border-left-color: #1976d2; background: rgba(33, 150, 243, 0.08); }
.event-PIPELINE_STEP_START { border-left-color: #26a69a; }
.event-PIPELINE_STEP_END { border-left-color: #00897b; }
.event-DEBUG_PAUSED { border-left-color: #ff9800; background: rgba(255, 152, 0, 0.08); }
.event-DEBUG_RESUMED { border-left-color: #4caf50; background: rgba(76, 175, 80, 0.08); }
.event-TOKEN_CHUNK { border-left-color: #78909c; }
.event-REASONING_CHUNK { border-left-color: #b8a878; font-style: italic; }
.event-REPLAY_START { border-left-color: #7c4dff; background: rgba(124, 77, 255, 0.08); }
.event-REPLAY_END { border-left-color: #651fff; background: rgba(124, 77, 255, 0.08); }
.event-RETRY_ATTEMPT { border-left-color: #ff9800; background: rgba(255, 152, 0, 0.08); }
.event-CONTEXT_COMPRESSED { border-left-color: #f59e0b; background: rgba(245, 158, 11, 0.08); }

.event-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  cursor: pointer;
  user-select: none;
}

.expand-icon {
  font-size: 10px;
  color: var(--text-secondary);
  width: 12px;
}

.event-icon {
  font-size: 14px;
}

.event-type {
  font-weight: 600;
  font-size: 12px;
  color: var(--text-primary);
}

.event-agent {
  font-size: 11px;
  color: var(--text-secondary);
  background: var(--surface-3);
  padding: 2px 6px;
  border-radius: 4px;
}

.event-time {
  margin-left: auto;
  font-size: 10px;
  color: var(--text-muted);
}

.event-content {
  padding: 0 12px 12px 36px;
}

.thought-content,
.tool-call-content,
.tool-result-content,
.payload-content {
  font-size: 12px;
}

.thought-field,
.tool-name,
.tool-args,
.tool-duration,
.tool-output {
  margin-bottom: 8px;
}

.thought-field label,
.tool-name label,
.tool-args label,
.tool-duration label,
.tool-output label {
  display: block;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-size: 11px;
}

.thought-field p {
  margin: 0;
  color: var(--text-primary);
  line-height: 1.5;
}

pre {
  margin: 0;
  padding: 8px;
  background: #263238;
  color: #aed581;
  border-radius: 4px;
  overflow-x: auto;
  font-size: 11px;
  max-height: 200px;
  overflow-y: auto;
}

.tool-output.error pre {
  background: rgba(244, 67, 54, 0.1);
  color: #ff8a80;
}

.tool-error pre {
  background: rgba(244, 67, 54, 0.1);
  color: #ff8a80;
}

.token-usage {
  margin-top: 8px;
}

.token-badge {
  font-size: 10px;
  background: #e3f2fd;
  color: #1565c0;
  padding: 2px 8px;
  border-radius: 10px;
}

.reflection-content,
.ground-check-content {
  font-size: 12px;
}

.reflection-mode {
  margin-bottom: 8px;
}

.mode-badge {
  background: #e1bee7;
  color: #6a1b9a;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
}

.reflection-text label,
.gc-confidence label,
.gc-issues label,
.gc-assessment label {
  display: block;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-size: 11px;
}

.reflection-text p,
.gc-assessment p {
  margin: 0;
  color: var(--text-primary);
  line-height: 1.5;
}

.gc-status {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.gc-badge {
  padding: 2px 10px;
  border-radius: 4px;
  font-weight: 700;
  font-size: 11px;
}

.gc-badge.valid {
  background: #c8e6c9;
  color: #2e7d32;
}

.gc-badge.invalid {
  background: #ffcdd2;
  color: #c62828;
}

.gc-action {
  font-size: 11px;
  color: var(--text-secondary);
}

.gc-confidence {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.confidence-bar {
  flex: 1;
  height: 8px;
  background: var(--surface-3);
  border-radius: 4px;
  overflow: hidden;
  max-width: 200px;
}

.confidence-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.3s;
}

.confidence-fill.low { background: #ef5350; }
.confidence-fill.medium { background: #ff9800; }
.confidence-fill.high { background: #4caf50; }

.confidence-value {
  font-weight: 600;
  font-size: 12px;
  min-width: 36px;
}

.pipeline-content,
.pipeline-step-content {
  font-size: 12px;
}

.pipeline-info,
.pipeline-step-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pipeline-badge {
  background: #1976d2;
  color: white;
  padding: 2px 10px;
  border-radius: 4px;
  font-weight: 700;
  font-size: 11px;
}

.step-type-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  color: white;
}

.step-type-badge.sequential { background: #26a69a; }
.step-type-badge.parallel { background: #7b1fa2; }
.step-type-badge.loop { background: #ef6c00; }
.step-type-badge.loop_iteration { background: #ff9800; }

.step-name {
  font-weight: 600;
  color: var(--text-primary);
}

.step-agents {
  font-size: 11px;
  color: var(--text-secondary);
}

.step-output {
  margin-top: 8px;
}

.step-output label {
  display: block;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-size: 11px;
}

.gc-issues {
  margin-bottom: 8px;
}

.gc-issues ul {
  margin: 0;
  padding-left: 20px;
}

.gc-issues li {
  color: #c62828;
  font-size: 12px;
  margin-bottom: 2px;
}

.handoff-content,
.message-content {
  font-size: 12px;
}

.handoff-info,
.message-info {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.handoff-icon {
  font-size: 16px;
  color: #e91e63;
}

.handoff-label {
  color: var(--text-secondary);
  font-size: 11px;
}

.handoff-agent {
  font-weight: 700;
  color: #1565c0;
  background: #e3f2fd;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.handoff-context-badge {
  font-size: 10px;
  color: #6a1b9a;
  background: #f3e5f5;
  padding: 2px 6px;
  border-radius: 4px;
  font-weight: 600;
}

.handoff-task label,
.message-text label {
  display: block;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-size: 11px;
}

.handoff-task p {
  margin: 0;
  color: var(--text-primary);
  line-height: 1.5;
}

.retry-content {
  font-size: 12px;
}

.retry-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.retry-badge {
  background: #fff3e0;
  color: #e65100;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 700;
  font-size: 11px;
}

.retry-error {
  color: #e65100;
  font-size: 12px;
}

.context-compressed-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.context-compressed-badge {
  background: #fef3c7;
  color: #92400e;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 700;
  font-size: 11px;
}

.context-compressed-detail {
  color: #92400e;
  font-size: 12px;
}

.context-compressed-pressure {
  color: #b45309;
  font-size: 12px;
  font-weight: 600;
  background: #fde68a;
  border-radius: 3px;
  padding: 1px 5px;
}

.retrieval-injected-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.retrieval-injected-badge {
  background: #e0f2fe;
  color: #0369a1;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 700;
  font-size: 11px;
}

.retrieval-injected-detail {
  color: #0369a1;
  font-size: 12px;
}

.agent-end-content {
  font-size: 12px;
}

.agent-end-status {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.agent-end-iters,
.agent-end-tokens {
  font-size: 11px;
  color: #555;
  background: var(--surface-3);
  padding: 2px 8px;
  border-radius: 10px;
}

.agent-end-cost {
  font-size: 11px;
  color: #2e7d32;
  background: #c8e6c9;
  padding: 2px 8px;
  border-radius: 10px;
}

.agent-end-budget {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 11px;
  color: var(--text-secondary);
  padding: 4px 8px;
  background: #fff8e1;
  border-radius: 4px;
  border-left: 3px solid #f59e0b;
}

.agent-end-budget-label {
  font-weight: 600;
  color: #92400e;
}

.agent-end-answer label {
  display: block;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  font-size: 11px;
}

.agent-end-answer pre {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text-primary);
  font-size: 11px;
  max-height: 200px;
  overflow-y: auto;
}

/* Track A: SALVAGED_OUTPUT + WORKER_REDELEGATION_BLOCKED rendering */
.salvaged-content,
.redelegation-blocked-content {
  padding: 6px 8px;
  background: rgba(245, 158, 11, 0.06);
  border-left: 3px solid #f59e0b;
  border-radius: 4px;
  font-size: 11px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.salvaged-header,
.redelegation-blocked-header {
  display: flex;
  gap: 8px;
  align-items: center;
}
.salvaged-badge,
.redelegation-blocked-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 600;
  font-size: 10px;
  text-transform: uppercase;
  background: #f59e0b;
  color: white;
}
.salvaged-badge.recovered {
  background: #10b981;
}
.salvaged-meta,
.redelegation-blocked-meta {
  color: var(--text-secondary, #666);
  font-family: var(--font-mono, monospace);
}
.redelegation-blocked-meta {
  display: flex;
  gap: 12px;
}
.redelegation-blocked-worker {
  font-family: var(--font-mono, monospace);
  font-weight: 600;
}
.salvaged-preview pre {
  background: rgba(0, 0, 0, 0.04);
  padding: 4px 6px;
  border-radius: 3px;
  font-size: 10px;
  max-height: 100px;
  overflow-y: auto;
  white-space: pre-wrap;
}
.salvaged-explainer p {
  margin: 0;
  font-size: 10px;
  color: var(--text-secondary, #666);
  font-style: italic;
}
.redelegation-blocked-reason {
  font-size: 10px;
}
.redelegation-blocked-reason label {
  font-weight: 600;
  margin-right: 4px;
}
.gc-badge.salvaged {
  background: #f59e0b;
  color: white;
}

/* Phase 1: ROLLBACK (runtime self-correction) rendering */
.rollback-content {
  padding: 6px 8px;
  background: rgba(56, 189, 248, 0.07);
  border-left: 3px solid #38bdf8;
  border-radius: 4px;
  font-size: 11px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.rollback-header {
  display: flex;
  gap: 8px;
  align-items: center;
}
.rollback-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 600;
  font-size: 10px;
  text-transform: uppercase;
  background: #38bdf8;
  color: white;
}
.rollback-meta {
  color: var(--text-secondary, #666);
  font-family: var(--font-mono, monospace);
}
.rollback-reason {
  font-size: 10px;
}
.rollback-reason label {
  font-weight: 600;
  margin-right: 4px;
}
.rollback-explainer {
  margin: 0;
  font-size: 10px;
  color: var(--text-secondary, #666);
  font-style: italic;
}

.wake-tick-content,
.wake-escalate-content {
  font-size: 11px;
  color: var(--text-primary);
}

.wake-tick-header,
.wake-escalate-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.wake-tick-badge,
.wake-escalate-badge,
.wake-outcome-badge,
.escalate-level-badge,
.wake-summary-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 600;
  font-size: 11px;
}

.wake-tick-badge {
  background: #a78bfa;
  color: white;
}

.wake-escalate-badge {
  background: #f59e0b;
  color: white;
}

.wake-outcome-badge {
  background: #e0e7ff;
  color: #4f46e5;
}

.wake-outcome-badge.alarm {
  background: #fee2e2;
  color: #dc2626;
}

.wake-outcome-badge.changed {
  background: #dcfce7;
  color: #16a34a;
}

.escalate-level-badge {
  background: #f59e0b;
  color: white;
}

.wake-summary-badge {
  background: #f3e8ff;
  color: #7e22ce;
}

.wake-tick-interval,
.wake-escalate-reason {
  margin: 6px 0;
}

.wake-tick-interval label,
.wake-escalate-reason label {
  font-weight: 600;
  margin-right: 4px;
}

.wake-results {
  margin: 8px 0;
}

.wake-results label {
  font-weight: 600;
  display: block;
  margin-bottom: 4px;
}

.wake-result-item {
  list-style: none;
  padding: 4px 0;
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.result-name {
  font-family: var(--font-mono, monospace);
  font-weight: 600;
}

.result-status {
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 600;
  font-size: 10px;
  background: #e0e7ff;
  color: #4f46e5;
}

.result-status.alarm {
  background: #fee2e2;
  color: #dc2626;
}

.result-status.ok {
  background: #dcfce7;
  color: #16a34a;
}

.result-status.unknown {
  background: #f3e8ff;
  color: #7e22ce;
}

.result-detail,
.result-label {
  font-size: 10px;
  color: var(--text-secondary);
}

.wake-note {
  margin: 6px 0;
}

.wake-note label {
  font-weight: 600;
  display: block;
  margin-bottom: 2px;
}

.wake-note p {
  margin: 0;
  font-size: 10px;
}

.wake-result-status {
  margin: 6px 0;
}

.wake-result-status label {
  font-weight: 600;
  margin-right: 4px;
}

.wake-result-timeout {
  color: #dc2626;
  font-weight: 600;
}

.wake-result-done {
  color: #16a34a;
  font-weight: 600;
}

.wake-result-error {
  color: #dc2626;
  font-weight: 600;
}
</style>