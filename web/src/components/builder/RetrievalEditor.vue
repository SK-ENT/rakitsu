<script setup lang="ts">
import { ref } from 'vue';
import type { PipelineStep } from '../../types';

const props = defineProps<{ step: PipelineStep }>();
const emit = defineEmits<{ update: [step: PipelineStep]; close: [] }>();
const name = ref(props.step.name);
const directory = ref(props.step.retrieval?.directory ?? '');
const limit = ref(props.step.retrieval?.top_k || 3);
const error = ref('');

function apply() {
  const path = directory.value.trim();
  if (!name.value.trim()) {
    error.value = 'Enter a step name.';
    return;
  }
  if (path !== '.' && (path.includes('\\') || path.split('/').some(part => !part || part === '.' || part === '..'))) {
    error.value = 'Enter a relative corpus directory, such as docs, or use . for the corpus root.';
    return;
  }
  if (!Number.isInteger(limit.value) || limit.value < 1 || limit.value > 8) {
    error.value = 'Choose between 1 and 8 passages.';
    return;
  }
  error.value = '';
  emit('update', {
    ...props.step,
    name: name.value.trim(),
    retrieval: { ...props.step.retrieval, directory: path, top_k: limit.value },
  });
}
</script>

<template>
  <aside class="retrieval-editor">
    <div class="retrieval-editor-header"><strong>Retrieval step</strong><button type="button" aria-label="Close editor" @click="emit('close')">×</button></div>
    <p>Search local Markdown and text files. Results can feed a Decision node.</p>
    <form novalidate @submit.prevent="apply">
      <label for="retrieval-name">Step name</label>
      <input id="retrieval-name" v-model="name" />
      <label for="retrieval-directory">Corpus directory</label>
      <input id="retrieval-directory" v-model="directory" spellcheck="false" aria-describedby="retrieval-directory-help" />
      <p id="retrieval-directory-help">Relative to the configured corpus root. Use a single dot for the root itself.</p>
      <label for="retrieval-limit">Maximum passages</label>
      <input id="retrieval-limit" v-model.number="limit" type="number" min="1" max="8" />
      <p v-if="error" role="alert">{{ error }}</p>
      <button type="submit">Apply Retrieval</button>
    </form>
  </aside>
</template>

<style scoped>
.retrieval-editor { width: min(380px, 42vw); flex: 0 0 auto; padding: 16px; overflow: auto; background: var(--bg-secondary, #232237); border-left: 1px solid var(--border-color, #56506c); }
.retrieval-editor-header { display: flex; justify-content: space-between; align-items: center; }
p, label { font-size: 12px; }
label { display: block; margin-top: 12px; }
form button { margin-top: 12px; }
input { box-sizing: border-box; width: 100%; margin-top: 6px; background: var(--bg-primary, #171623); color: var(--text-primary, #f4f2ff); border: 1px solid var(--border-color, #56506c); border-radius: 5px; padding: 8px; font: inherit; }
</style>
