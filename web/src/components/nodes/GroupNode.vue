<script setup lang="ts">
import { inject, computed } from 'vue';
import { Handle, Position } from '@vue-flow/core';
import { NodeResizer } from '@vue-flow/node-resizer';
import '@vue-flow/node-resizer/dist/style.css';
import type { Ref } from 'vue';

interface GroupConfig {
  _id?: string;
  name: string;
  blockType: 'pipeline' | 'parallel' | 'team' | 'generic';
  viewMode?: 'compact' | 'mixed' | 'full';
  collapsed?: boolean;
  autoFit?: boolean;
  _width?: number;
  _height?: number;
  _childCount?: number;
  _runStatus?: 'running' | 'paused' | 'success' | 'error' | null;
}

const props = defineProps<{
  data: GroupConfig;
  selected?: boolean;
}>();

const canvasMode = inject<Ref<string>>('canvasMode');
const isExecution = computed(() => canvasMode?.value !== 'design');

const isCompact = () => props.data.viewMode === 'compact' || props.data.collapsed === true;
const isAutoFit = () => props.data.autoFit !== false;
const viewMode = () => props.data.viewMode || (props.data.collapsed ? 'compact' : 'mixed');

function setMode(mode: 'compact' | 'mixed' | 'full') {
  props.data.viewMode = mode;
  props.data.collapsed = mode === 'compact';
}

const blockTypeColors: Record<string, string> = {
  pipeline: 'var(--accent-group-pipeline)',
  parallel: 'var(--accent-group-parallel)',
  team: 'var(--accent-group-team)',
  loop: 'var(--accent-group-loop, #f59e0b)',
  generic: 'var(--accent-group-generic)',
};

const blockTypeLabels: Record<string, string> = {
  pipeline: 'PIP',
  parallel: 'PAR',
  team: 'TEAM',
  loop: 'LOOP',
  generic: 'GRP',
};
</script>

<template>
  <div
    class="group-node"
    :class="{ selected, collapsed: isCompact(), [data.blockType]: true }"
  >
    <NodeResizer
      v-if="!isCompact() && !isAutoFit()"
      :is-visible="!!selected"
      :min-width="250"
      :min-height="120"
      :color="blockTypeColors[data.blockType] || 'var(--accent-group-generic)'"
    />

    <Handle type="target" :position="Position.Top" class="handle" />

    <div class="group-header" :style="{ borderColor: blockTypeColors[data.blockType] || 'var(--accent-group-generic)' }">
      <span class="block-label">{{ blockTypeLabels[data.blockType] || 'GRP' }}</span>
      <span class="group-name">{{ data.name }}</span>
      <span v-if="isCompact() && (data._childCount ?? 0) > 0" class="child-count">{{ data._childCount }}</span>
      <!-- Execution status LED in header -->
      <span v-if="isExecution && data._runStatus" class="led" :class="data._runStatus" />
      <div v-if="!isExecution" class="view-mode-group" @click.stop>
        <button class="vm-btn" :class="{ active: viewMode() === 'compact' }" title="Compact" aria-label="Fold group" @click="setMode('compact')">−</button>
        <button class="vm-btn" :class="{ active: viewMode() === 'mixed' }" title="Mixed" aria-label="Unfold group" @click="setMode('mixed')">+</button>
        <button class="vm-btn" :class="{ active: viewMode() === 'full' }" title="Full" @click="setMode('full')">F</button>
      </div>
    </div>

    <div v-if="!isCompact()" class="group-body">
      <!-- Child nodes positioned by Vue Flow parentNode -->
    </div>

    <Handle type="source" :position="Position.Bottom" class="handle" />
  </div>
</template>

<style scoped>
.group-node {
  background: rgba(37, 37, 64, 0.5);
  border: 2px dashed var(--accent-group-generic);
  border-radius: var(--node-radius);
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 200px;
  min-height: 60px;
}
.group-node.selected {
  box-shadow: 0 0 0 1px var(--accent-agent), 0 0 0 3px rgba(91, 141, 239, 0.3);
}
.group-node.collapsed {
  border-style: solid;
  min-height: 40px;
  background: var(--surface-3);
}

.group-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  border-radius: 1px 1px 0 0;
  background: var(--surface-3);
  border-bottom: 1px solid;
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 600;
  cursor: grab;
}
.group-node.collapsed .group-header {
  border-radius: 1px;
}

.block-label {
  font-size: 9px;
  font-family: var(--font-mono);
  background: rgba(255, 255, 255, 0.08);
  padding: 1px 5px;
  border-radius: 2px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-secondary);
}
.group-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.child-count {
  font-size: 10px;
  font-family: var(--font-mono);
  background: rgba(255, 255, 255, 0.1);
  padding: 0 5px;
  border-radius: 2px;
  min-width: 16px;
  text-align: center;
  color: var(--text-secondary);
}

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

.view-mode-group {
  display: flex;
  gap: 1px;
  background: rgba(0, 0, 0, 0.3);
  border-radius: 2px;
  overflow: hidden;
}
.vm-btn {
  background: rgba(255, 255, 255, 0.06);
  border: none;
  color: var(--text-muted);
  font-size: 9px;
  font-weight: 700;
  font-family: var(--font-mono);
  padding: 1px 4px;
  cursor: pointer;
  line-height: 1.4;
}
.vm-btn:hover { background: rgba(255, 255, 255, 0.12); }
.vm-btn.active {
  background: rgba(255, 255, 255, 0.2);
  color: var(--text-primary);
}

.group-body {
  flex: 1;
  padding: 8px;
  position: relative;
  pointer-events: none;
}
.group-body * { pointer-events: auto; }

.handle {
  width: var(--handle-width);
  height: var(--handle-height);
  background: var(--accent-agent);
  border: none;
  border-radius: var(--handle-radius);
}

/* Block type borders */
.group-node.pipeline { border-color: var(--accent-group-pipeline); }
.group-node.parallel { border-color: var(--accent-group-parallel); }
.group-node.team { border-color: var(--accent-group-team); }
.group-node.loop { border-color: var(--accent-group-loop, #f59e0b); }
.group-node.generic { border-color: var(--accent-group-generic); }

/* Compact mode: hide child nodes and their edges */
:global(.vue-flow__node.compact-hidden) {
  transform: scale(0) !important;
  opacity: 0 !important;
  pointer-events: none !important;
  transition: transform 0.15s ease, opacity 0.15s ease;
}
:global(.vue-flow__edge.compact-hidden) {
  opacity: 0 !important;
  pointer-events: none !important;
  transition: opacity 0.15s ease;
}

/* Drop target highlight */
:global(.vue-flow__node.drop-target) .group-node {
  border-style: solid !important;
  border-width: 3px !important;
  background: rgba(91, 141, 239, 0.08) !important;
  box-shadow: 0 0 20px rgba(91, 141, 239, 0.2);
  transition: all 0.15s ease;
}
</style>
