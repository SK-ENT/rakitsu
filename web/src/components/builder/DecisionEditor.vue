<script setup lang="ts">
import { ref } from 'vue';
import { dump, load } from 'js-yaml';
import type { PipelineStep } from '../../types';

const props = defineProps<{ step: PipelineStep; agentNames: string[] }>();
const emit = defineEmits<{ update: [step: PipelineStep]; close: [] }>();
const draft = ref(dump(props.step, { noRefs: true, lineWidth: -1 }));
const error = ref('');

function apply() {
  try {
    const step = load(draft.value) as PipelineStep | null;
    if (!step || typeof step.name !== 'string' || !step.name.trim() ||
        step.type !== 'decision' || !step.decision ||
        !['jev', 'laya', 'openai', 'gemini'].includes(step.decision.backend) ||
        !step.decision.questions || Object.keys(step.decision.questions).length === 0) {
      throw new Error('Enter a named Decision step with a backend and at least one question.');
    }
    for (const route of step.decision.routes ?? []) {
      if (!route.when || !Array.isArray(route.steps) || !route.steps.length) {
        throw new Error('Each route needs a condition and at least one agent step.');
      }
    }
    if (step.decision.routes?.length && !step.decision.otherwise?.length) {
      throw new Error('Add an otherwise branch for inputs that match no route.');
    }
    const branches = [...(step.decision.routes ?? []).map(route => route.steps), step.decision.otherwise ?? []];
    for (const branch of branches) {
      for (const child of branch) {
        if (!child?.agent || !props.agentNames.includes(child.agent)) {
          throw new Error(`Unknown branch agent: ${child?.agent || '(empty)'}`);
        }
      }
    }
    error.value = '';
    emit('update', step);
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Invalid Decision YAML.';
  }
}
</script>

<template>
  <aside class="decision-editor">
    <div class="decision-editor-header">
      <strong>Decision step</strong>
      <button type="button" aria-label="Close editor" @click="emit('close')">×</button>
    </div>
    <p>Set the backend, questions, and branch steps. Rakitsu runs the first matching route.</p>
    <label for="decision-yaml">Step YAML</label>
    <textarea id="decision-yaml" v-model="draft" spellcheck="false" rows="18" />
    <p v-if="agentNames.length">Available agents · {{ agentNames.join(', ') }}</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <button type="button" @click="apply">Apply Decision</button>
  </aside>
</template>

<style scoped>
.decision-editor {
  width: min(380px, 42vw);
  flex: 0 0 auto;
  padding: 16px;
  overflow: auto;
  background: var(--bg-secondary, #232237);
  border-left: 1px solid var(--border-color, #56506c);
}
.decision-editor-header { display: flex; justify-content: space-between; align-items: center; }
p, label { font-size: 12px; }
textarea {
  box-sizing: border-box;
  width: 100%;
  margin-top: 6px;
  background: var(--bg-primary, #171623);
  color: var(--text-primary, #f4f2ff);
  border: 1px solid var(--border-color, #56506c);
  border-radius: 5px;
  padding: 8px;
  font: 12px/1.5 monospace;
}
</style>
