<script setup lang="ts">
import { type ComputedRef, inject, computed, ref } from 'vue';
import { Handle, Position } from '@vue-flow/core';
import BuilderSideHandles from './BuilderSideHandles.vue';
import type { OrchestratorConfig } from '../../types';
import type { Ref } from 'vue';

const props = defineProps<{
  data: OrchestratorConfig & {
    _runStatus?: 'running' | 'paused' | 'success' | 'error' | null;
    _tokenCount?: number;
    _hasBreakpoint?: boolean;
  };
  selected?: boolean;
}>();

const canvasMode = inject<Ref<string>>('canvasMode');
const showResults = inject<Ref<boolean>>('showResults', ref(false));
const isExecution = computed(() => canvasMode?.value !== 'design');
const hasResult = computed(() => !!(showResults?.value && props.data._runStatus));
// Match OrchestratorConfig.RunsWithoutChatProvider in the runtime.
const runsWithoutChatLLM = computed(() => {
  const { strategy, pipeline } = props.data;
  if (strategy !== 'Pipeline' || !pipeline?.steps.length || pipeline.synthesis) return false;
  return pipeline.steps.every(step =>
    (step.type === 'retrieval' && !!step.retrieval) ||
    (step.type === 'decision' && !!step.decision &&
      !step.decision.routes?.length && !step.decision.otherwise?.length)
  );
});
const toggleBreakpoint = inject<(agentName: string) => void>('toggleBreakpoint');

function fmtTokens(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}
const workflowFocus = inject<ComputedRef<{ agent: string; tool: string } | null>>('builderWorkflowFocus');
</script>

<template>
  <div
    class="orchestrator-node"
    :data-workflow-active="workflowFocus?.agent === data.name || undefined"
    :class="[{ selected }, data._runStatus && `status-${data._runStatus}`, isExecution && !data._runStatus && 'status-pending']"
  >
    <!-- Breakpoint dot — always visible for orchestrator nodes -->
    <div
      class="breakpoint-dot"
      :class="data._hasBreakpoint ? 'breakpoint-dot--active' : 'breakpoint-dot--inactive'"
      :title="data._hasBreakpoint ? 'Breakpoint set — click to remove' : 'Click to set breakpoint (auto-attaches debugger)'"
      @click.stop="toggleBreakpoint?.(data.name || '')"
    />
    <!-- Input handle -->
    <Handle type="target" :position="Position.Top" class="handle-input" />
    <BuilderSideHandles />

    <div class="node-header">
      <span class="node-title">{{ data.name || 'Orchestrator' }}</span>
      <span v-if="isExecution || hasResult" class="led" :class="data._runStatus ?? 'pending'" />
    </div>

    <!-- Execution overlay -->
    <div v-if="isExecution" class="exec-overlay">
      <div class="exec-status-row">
        <span class="exec-status-label">{{ data._runStatus ?? 'pending' }}</span>
        <span v-if="data._tokenCount" class="exec-tokens">{{ data._tokenCount >= 1000 ? (data._tokenCount/1000).toFixed(1)+'k' : data._tokenCount }} tkns</span>
      </div>
    </div>

    <div v-if="!isExecution" class="node-content">
      <div v-if="runsWithoutChatLLM" class="node-field">
        <label>EXECUTION</label>
        <span class="badge">No chat LLM</span>
      </div>
      <div v-else class="node-field">
        <label>PROVIDER</label>
        <span class="badge provider">{{ data.provider || 'openai' }}</span>
      </div>
      <div v-if="!runsWithoutChatLLM" class="node-field">
        <label>MODEL</label>
        <span class="field-value">{{ data.model }}</span>
      </div>
      <div class="node-field">
        <label>STRATEGY</label>
        <span class="badge" :class="data.strategy">{{ data.strategy }}</span>
      </div>
      <div class="node-field agents-list">
        <label>AGENTS</label>
        <span class="field-value">{{ data.agents?.join(', ') || 'none' }}</span>
      </div>
    </div>

    <!-- Results badge — shown in design mode after run completes -->
    <div v-if="hasResult && !isExecution" class="result-badge">
      <span class="result-status" :class="data._runStatus ?? ''">{{ data._runStatus }}</span>
      <span v-if="data._tokenCount" class="result-tokens">{{ fmtTokens(data._tokenCount) }} tkns</span>
    </div>

    <!-- Output handles -->
    <div class="output-handles">
      <div class="handle-group">
        <Handle type="source" :position="Position.Bottom" id="agents" class="handle-agents" />
        <span class="handle-label">AGENTS</span>
      </div>
      <div class="handle-group">
        <Handle type="source" :position="Position.Bottom" id="orchestrators" class="handle-orchestrators" />
        <span class="handle-label">NESTED</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.orchestrator-node {
  background: var(--node-bg);
  border: 1px solid var(--node-border);
  border-left: 4px solid var(--accent-orch);
  border-radius: var(--node-radius);
  color: var(--text-primary);
  min-width: 200px;
  font-size: 12px;
  position: relative;
  transition: box-shadow 0.2s ease;
}

.orchestrator-node.selected {
  box-shadow: 0 0 0 1px var(--accent-orch), 0 0 0 3px rgba(244, 114, 182, 0.3);
}

.orchestrator-node.status-running { box-shadow: 0 0 0 1px var(--status-running); }
.orchestrator-node.status-paused  { box-shadow: 0 0 0 1px var(--status-paused); }
.orchestrator-node.status-success { box-shadow: 0 0 0 1px var(--status-success); }
.orchestrator-node.status-error   { box-shadow: 0 0 0 1px var(--status-error); }
.orchestrator-node.status-pending { opacity: 0.45; }

.node-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--border-subtle);
}

.node-title { font-weight: 600; font-size: 13px; color: var(--text-primary); }

.led {
  width: var(--led-size);
  height: var(--led-size);
  border-radius: 50%;
  flex-shrink: 0;
}
.led.running { background: var(--status-running); box-shadow: 0 0 6px rgba(34, 197, 94, 0.6); }
.led.paused  { background: var(--status-paused); box-shadow: 0 0 6px rgba(245, 158, 11, 0.6); }
.led.success { background: var(--status-success); }
.led.error   { background: var(--status-error); box-shadow: 0 0 6px rgba(239, 68, 68, 0.6); }
.led.pending { background: var(--status-pending); }

.exec-overlay { padding: 6px 10px; }
.exec-status-row { display: flex; align-items: center; gap: 6px; font-size: 11px; }
.exec-status-label { font-size: 10px; font-family: var(--font-mono); color: var(--text-secondary); text-transform: uppercase; }
.exec-tokens { margin-left: auto; font-size: 10px; font-family: var(--font-mono); background: var(--surface-4); padding: 1px 5px; border-radius: 2px; color: var(--text-secondary); }

.node-content { padding: 6px 10px; }

.node-field {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 3px;
}

.node-field label {
  font-size: 9px;
  font-weight: 500;
  color: var(--text-muted);
  font-family: var(--font-mono);
  letter-spacing: 0.05em;
  min-width: 56px;
}

.field-value {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-secondary);
}

.badge {
  padding: 1px 5px;
  border-radius: 2px;
  font-size: 10px;
  font-family: var(--font-mono);
  text-transform: uppercase;
}
.badge.ReAct { background: rgba(91, 141, 239, 0.15); color: var(--accent-agent); }
.badge.PlanAndExecute { background: rgba(192, 132, 252, 0.15); color: var(--accent-skill); }
.badge.Hierarchical { background: rgba(245, 158, 11, 0.15); color: var(--status-paused); }
.badge.provider { background: rgba(91, 141, 239, 0.15); color: var(--accent-agent); }

.agents-list {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.output-handles {
  display: flex;
  justify-content: space-around;
  padding: 6px 10px;
  border-top: 1px solid var(--border-subtle);
}

.handle-group {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
}

.handle-label {
  font-size: 8px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  letter-spacing: 0.05em;
}

.handle-input {
  background: var(--text-secondary) !important;
  width: var(--handle-width) !important;
  height: var(--handle-height) !important;
  border-radius: var(--handle-radius) !important;
  border: none !important;
}

.handle-agents {
  background: var(--accent-agent) !important;
  width: var(--handle-width) !important;
  height: var(--handle-height) !important;
  border-radius: var(--handle-radius) !important;
  border: none !important;
  position: relative !important;
  left: auto !important;
  right: auto !important;
  transform: none !important;
}

.handle-orchestrators {
  background: var(--accent-orch) !important;
  width: var(--handle-width) !important;
  height: var(--handle-height) !important;
  border-radius: var(--handle-radius) !important;
  border: none !important;
  position: relative !important;
  left: auto !important;
  right: auto !important;
  transform: none !important;
}

/* --- Breakpoint dot --- */
.breakpoint-dot {
  position: absolute;
  top: -4px;
  right: -4px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  border: 1px solid var(--border-default);
  z-index: 10;
  cursor: pointer;
  transition: transform 0.15s, box-shadow 0.15s;
}
.breakpoint-dot:hover { transform: scale(1.3); }
.breakpoint-dot--active { background: var(--status-error); box-shadow: 0 0 4px rgba(239, 68, 68, 0.8); border-color: var(--status-error); }
.breakpoint-dot--inactive { background: var(--status-pending); }
.breakpoint-dot--inactive:hover { background: var(--status-error); box-shadow: 0 0 4px rgba(239, 68, 68, 0.5); }

/* --- Results badge (design mode after run) --- */
.result-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-top: 1px solid var(--border-subtle);
  background: rgba(34, 197, 94, 0.05);
}
.result-status {
  font-size: 9px;
  font-family: var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  font-weight: 600;
}
.result-status.success { color: var(--status-running); }
.result-status.error { color: var(--status-error); }
.result-tokens {
  margin-left: auto;
  font-size: 9px;
  font-family: var(--font-mono);
  color: var(--text-muted);
}
</style>
