<script setup lang="ts">
import { type ComputedRef, inject, computed } from 'vue';
import { Handle, Position } from '@vue-flow/core';
import BuilderSideHandles from './BuilderSideHandles.vue';
import type { ToolConfig } from '../../types';
import type { Ref } from 'vue';

const props = defineProps<{
  data: ToolConfig & {
    _runStatus?: 'running' | 'paused' | 'success' | 'error' | null;
    _hasBreakpoint?: boolean;
  };
  selected?: boolean;
}>();

const canvasMode = inject<Ref<string>>('canvasMode');
const isExecution = computed(() => canvasMode?.value !== 'design');
const toggleToolBreakpoint = inject<(toolName: string) => void>('toggleToolBreakpoint');
const workflowFocus = inject<ComputedRef<{ agent: string; tool: string } | null>>('builderWorkflowFocus');
</script>

<template>
  <div
    class="tool-node"
    :data-workflow-active="workflowFocus?.tool === data.name || undefined"
    :class="[{ selected }, data._runStatus && `status-${data._runStatus}`, isExecution && !data._runStatus && 'status-pending']"
  >
    <!-- Breakpoint dot -->
    <div
      class="breakpoint-dot"
      :class="data._hasBreakpoint ? 'breakpoint-dot--active' : 'breakpoint-dot--inactive'"
      :title="data._hasBreakpoint ? 'Tool breakpoint set — click to remove' : 'Click to break before this tool is called'"
      @click.stop="toggleToolBreakpoint?.(data.name)"
    />
    <!-- Input handle only - tools are leaf nodes -->
    <Handle type="target" :position="Position.Top" class="handle-input" />
    <BuilderSideHandles />

    <div class="node-header">
      <span class="node-title">{{ data.name }}</span>
      <span v-if="isExecution" class="led" :class="data._runStatus ?? 'pending'" />
    </div>

    <!-- Execution overlay -->
    <div v-if="isExecution" class="exec-overlay">
      <div class="exec-status-row">
        <span class="exec-status-label">{{ data._runStatus ?? 'waiting' }}</span>
      </div>
    </div>

    <div v-if="!isExecution" class="node-content">
      <div class="node-field">
        <label>TYPE</label>
        <span class="badge" :class="data.type">{{ data.type }}</span>
      </div>
      <div class="node-field description">
        <label>DESC</label>
        <span class="field-value">{{ data.description?.slice(0, 50) }}{{ data.description && data.description.length > 50 ? '...' : '' }}</span>
      </div>
      <div v-if="data.sandbox" class="node-field">
        <label>SANDBOX</label>
        <span class="badge sandbox">{{ data.sandbox.type }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tool-node {
  background: var(--node-bg);
  border: 1px solid var(--node-border);
  border-left: 4px solid var(--accent-tool);
  border-radius: var(--node-radius);
  color: var(--text-primary);
  min-width: 170px;
  font-size: 12px;
  position: relative;
  transition: box-shadow 0.2s ease;
}

.tool-node.selected {
  box-shadow: 0 0 0 1px var(--accent-tool), 0 0 0 3px rgba(45, 212, 160, 0.3);
}

.tool-node.status-running { box-shadow: 0 0 0 1px var(--status-running); }
.tool-node.status-success { box-shadow: 0 0 0 1px var(--status-success); }
.tool-node.status-error   { box-shadow: 0 0 0 1px var(--status-error); }
.tool-node.status-pending { opacity: 0.4; }

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
.led.success { background: var(--status-success); }
.led.error   { background: var(--status-error); box-shadow: 0 0 6px rgba(239, 68, 68, 0.6); }
.led.pending { background: var(--status-pending); }

.exec-overlay { padding: 6px 10px; }
.exec-status-row { display: flex; align-items: center; gap: 6px; font-size: 11px; }
.exec-status-label { font-size: 10px; font-family: var(--font-mono); color: var(--text-secondary); text-transform: uppercase; }

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
  min-width: 48px;
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
.badge.cli     { background: rgba(91, 141, 239, 0.15); color: var(--accent-agent); }
.badge.fs      { background: rgba(192, 132, 252, 0.15); color: var(--accent-skill); }
.badge.sandbox { background: rgba(239, 68, 68, 0.15); color: var(--status-error); }

.description {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.handle-input {
  background: var(--text-secondary) !important;
  width: var(--handle-width) !important;
  height: var(--handle-height) !important;
  border-radius: var(--handle-radius) !important;
  border: none !important;
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
</style>
