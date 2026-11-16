<script setup lang="ts">
import { computed } from 'vue';
import { Handle, Position } from '@vue-flow/core';
import type { PipelineStep } from '../../types';
import { decisionRouteLabel } from '../../utils/decisionRoutes';

const props = defineProps<{ data: PipelineStep; selected?: boolean }>();
const inputLabel = computed(() => {
  const input = props.data.decision?.input;
  if (!input || input.from === 'query') return 'Run query';
  const source = input.from === 'retrieval' ? 'Evidence' : 'Step output';
  return input.step ? `${source} · ${input.step}` : `${source} · No step selected`;
});
const questionCount = computed(() => Object.keys(props.data.decision?.questions ?? {}).length);
</script>

<template>
  <div class="decision-node" :class="{ selected }">
    <Handle type="target" :position="Position.Top" :connectable="false" />
    <div class="decision-heading">
      <span>Decision</span>
      <span>{{ data.decision?.backend || 'Unconfigured' }}</span>
    </div>
    <strong>{{ data.name }}</strong>
    <div class="decision-input">{{ inputLabel }}</div>
    <div class="decision-input">{{ questionCount }} {{ questionCount === 1 ? 'question' : 'questions' }}</div>
    <div v-for="(route, index) in data.decision?.routes" :key="index" class="route">
      <Handle type="source" :id="`route-${index}`" :position="Position.Right" :connectable="false" />
      {{ decisionRouteLabel(route.when) }} → {{ route.steps.map(step => step.agent).join(' → ') }}
    </div>
    <div v-if="data.decision?.otherwise?.length" class="route">
      <Handle type="source" id="otherwise" :position="Position.Right" :connectable="false" />
      Otherwise → {{ data.decision.otherwise.map(step => step.agent).join(' → ') }}
    </div>
  </div>
</template>

<style scoped>
.decision-node {
  min-width: 172px;
  max-width: 288px;
  padding: 10px 12px;
  border: 1px solid #9787e3;
  border-radius: 8px;
  background: var(--bg-secondary, #232237);
  color: var(--text-primary, #f4f2ff);
}
.selected { outline: 2px solid #9787e3; }
.decision-heading {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 4px;
  color: var(--text-secondary, #cfbfff);
  font-size: 11px;
}
.decision-input { margin-top: 4px; font-size: 11px; color: var(--text-secondary); overflow-wrap: anywhere; }
.route { position: relative; margin-top: 4px; padding-right: 5px; font-size: 11px; overflow-wrap: anywhere; }
</style>
