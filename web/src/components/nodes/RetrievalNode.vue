<script setup lang="ts">
import { Handle, Position } from '@vue-flow/core';
import type { PipelineStep } from '../../types';

defineProps<{ data: PipelineStep; selected?: boolean }>();
</script>

<template>
  <div class="retrieval-node" :class="{ selected }">
    <Handle type="target" :position="Position.Top" :connectable="false" />
    <div class="retrieval-heading"><span>Retrieval</span><span>local</span></div>
    <strong>{{ data.name }}</strong>
    <div class="retrieval-detail">{{ data.retrieval?.directory || 'No directory' }}</div>
    <div class="retrieval-detail">Up to {{ data.retrieval?.top_k || 3 }} passages</div>
    <Handle type="source" id="results" :position="Position.Bottom" :connectable="false" />
  </div>
</template>

<style scoped>
.retrieval-node { min-width: 172px; max-width: 288px; padding: 10px 12px; border: 1px solid #54b8a4; border-radius: 8px; background: var(--bg-secondary, #232237); color: var(--text-primary, #f4f2ff); }
.selected { outline: 2px solid #54b8a4; }
.retrieval-heading { display: flex; justify-content: space-between; color: #9de0d0; font-size: 11px; margin-bottom: 4px; }
.retrieval-detail { margin-top: 4px; font-size: 11px; overflow-wrap: anywhere; color: var(--text-secondary, #cfbfff); }
</style>
