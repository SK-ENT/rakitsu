<script setup lang="ts">
import { type ComputedRef, inject, computed, ref } from 'vue';
import { Handle, Position } from '@vue-flow/core';
import BuilderSideHandles from './BuilderSideHandles.vue';
import type { AgentConfig } from '../../types';
import type { Ref } from 'vue';

const toggleBreakpoint = inject<(agentName: string) => void>('toggleBreakpoint');
const toggleExecExpand = inject<(agentName: string) => void>('toggleExecExpand');
const execExpandedAgents = inject<Ref<Set<string>>>('execExpandedAgents');

type RunStatus = 'running' | 'paused' | 'success' | 'error' | null;

const props = defineProps<{
  data: AgentConfig & {
    _runStatus?: RunStatus;
    _hasBreakpoint?: boolean;
    _tokenCount?: number;
    _iterationCount?: number;
    _lastThought?: string;
  };
  selected?: boolean;
}>();

const canvasMode = inject<Ref<string>>('canvasMode');
const showResults = inject<Ref<boolean>>('showResults', ref(false));
const isExecution = computed(() => canvasMode?.value !== 'design');
const hasResult = computed(() => !!(showResults?.value && props.data._runStatus));
const isExecExpanded = computed(() => execExpandedAgents?.value?.has(props.data.name) ?? false);

function fmtTokens(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}
const workflowFocus = inject<ComputedRef<{ agent: string; tool: string } | null>>('builderWorkflowFocus');
</script>

<template>
  <div
    class="agent-node"
    :data-workflow-active="workflowFocus?.agent === data.name || undefined"
    :class="[{ selected }, data._runStatus && `status-${data._runStatus}`, isExecution && !data._runStatus && 'status-pending']"
  >
    <!-- Breakpoint dot — always visible for agent nodes -->
    <div
      class="breakpoint-dot"
      :class="data._hasBreakpoint ? 'breakpoint-dot--active' : 'breakpoint-dot--inactive'"
      :title="data._hasBreakpoint ? 'Breakpoint set — click to remove' : 'Click to set breakpoint (auto-attaches debugger)'"
      @click.stop="toggleBreakpoint?.(data.name)"
    />
    <!-- Input handle -->
    <Handle type="target" :position="Position.Top" class="handle-input" />
    <!-- Auxiliary side pins for multi-edge spread -->
    <BuilderSideHandles />

    <div class="node-header">
      <span class="node-title">{{ data.name }}</span>
      <!-- Status LED — shown in execution mode or results mode -->
      <span v-if="isExecution || hasResult" class="led" :class="data._runStatus ?? 'pending'" />
    </div>

    <!-- Execution overlay (monitor/debug/replay modes) -->
    <div v-if="isExecution" class="exec-overlay">
      <div class="exec-status-row">
        <span class="exec-status-label">{{ data._runStatus ?? 'pending' }}</span>
        <span v-if="data._iterationCount" class="exec-iter">iter {{ data._iterationCount }}</span>
        <span v-if="data._tokenCount" class="exec-tokens">{{ fmtTokens(data._tokenCount) }} tkns</span>
        <button
          v-if="data._runStatus"
          class="exec-tree-toggle"
          :title="isExecExpanded ? 'Collapse execution tree' : 'Expand execution tree'"
          @click.stop="toggleExecExpand?.(data.name)"
        >{{ isExecExpanded ? '&#x25BE;' : '&#x25B8;' }}</button>
      </div>
      <div v-if="data._lastThought" class="exec-thought">{{ data._lastThought }}</div>
    </div>

    <!-- Design-mode content -->
    <div v-if="!isExecution" class="node-content">
      <div class="node-model" :title="data.model">{{ data.model || 'Default model' }}</div>
      <span class="badge" :class="data.role">{{ data.role }}</span>
      <span v-if="data.tools?.length" class="node-tool-count">
        {{ data.tools.length }} {{ data.tools.length === 1 ? 'tool' : 'tools' }}
      </span>
    </div>

    <!-- Results badge — shown in design mode after run completes -->
    <div v-if="hasResult && !isExecution" class="result-badge">
      <span class="result-status" :class="data._runStatus ?? ''">{{ data._runStatus }}</span>
      <span v-if="data._tokenCount" class="result-tokens">{{ fmtTokens(data._tokenCount) }} tkns</span>
    </div>

    <!-- Output handles -->
    <div class="output-handles">
      <div class="handle-group">
        <Handle type="source" :position="Position.Bottom" id="tools" class="handle-tools" />
        <span class="handle-label">TOOLS</span>
      </div>
      <div class="handle-group">
        <Handle type="source" :position="Position.Bottom" id="agents" class="handle-agents" />
        <span class="handle-label">AGENTS</span>
      </div>
      <div class="handle-group">
        <Handle type="source" :position="Position.Bottom" id="orchestrators" class="handle-orchestrators" />
        <span class="handle-label">SUB-ORCH</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-node {
  background: var(--node-bg);
  border: 1px solid var(--node-border);
  border-left: 4px solid var(--accent-agent);
  border-radius: var(--node-radius);
  color: var(--text-primary);
  min-width: 200px;
  font-size: 12px;
  position: relative;
  transition: box-shadow 0.2s ease;
}

.agent-node.selected {
  box-shadow: 0 0 0 1px var(--accent-agent), 0 0 0 3px rgba(91, 141, 239, 0.3);
}

.node-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--border-subtle);
}

.node-title {
  font-weight: 600;
  font-size: 15px;
  font-family: var(--font-sans);
  color: var(--text-primary);
}

/* LED status indicator */
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

.node-content {
  padding: 6px 10px;
}

.node-model {
  font: 12px var(--font-mono);
  color: var(--text-secondary);
  margin-bottom: 5px;
}

.node-tool-count {
  margin-left: 6px;
  color: var(--text-secondary);
}

.badge {
  padding: 1px 5px;
  border-radius: 2px;
  font-size: 10px;
  font-family: var(--font-mono);
  text-transform: uppercase;
}

.badge.supervisor { background: rgba(245, 158, 11, 0.15); color: var(--status-paused); }
.badge.worker { background: rgba(34, 197, 94, 0.15); color: var(--status-success); }
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

.handle-tools {
  background: var(--accent-tool) !important;
  width: var(--handle-width) !important;
  height: var(--handle-height) !important;
  border-radius: var(--handle-radius) !important;
  border: none !important;
  position: relative !important;
  left: auto !important;
  right: auto !important;
  transform: none !important;
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

/* --- Status borders --- */
.agent-node.status-running { box-shadow: 0 0 0 1px var(--status-running); }
.agent-node.status-paused  { box-shadow: 0 0 0 1px var(--status-paused); }
.agent-node.status-success { box-shadow: 0 0 0 1px var(--status-success); }
.agent-node.status-error   { box-shadow: 0 0 0 1px var(--status-error); }
.agent-node.status-pending { opacity: 0.45; }

/* --- Execution overlay --- */
.exec-overlay { padding: 6px 10px; }
.exec-status-row { display: flex; align-items: center; gap: 6px; font-size: 11px; }
.exec-status-label { font-size: 10px; font-family: var(--font-mono); color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.04em; }
.exec-iter { font-size: 10px; font-family: var(--font-mono); color: var(--text-muted); }
.exec-tokens { margin-left: auto; font-size: 10px; font-family: var(--font-mono); background: var(--surface-4); padding: 1px 5px; border-radius: 2px; color: var(--text-secondary); }
.exec-thought { margin-top: 4px; font-size: 10px; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-style: italic; }
.exec-tree-toggle {
  background: none;
  border: 1px solid var(--border-subtle);
  border-radius: 3px;
  color: var(--text-secondary);
  font-size: 10px;
  line-height: 1;
  padding: 1px 4px;
  cursor: pointer;
  margin-left: 4px;
  transition: background 0.15s, color 0.15s;
}
.exec-tree-toggle:hover { background: var(--surface-4); color: var(--accent-agent); }

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
