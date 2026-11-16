<script setup lang="ts">
import { ref, watch, nextTick } from 'vue';

const props = defineProps<{
  text: string;
  isStreaming: boolean;
  // 'reasoning' renders dimmed/italic to distinguish chain-of-thought
  // from the committed answer when both stream side by side. Default
  // 'answer' preserves the original appearance.
  variant?: 'answer' | 'reasoning';
  // Optional label rendered above the text block (e.g. "Reasoning").
  // When omitted the block renders without a header.
  label?: string;
}>();

const container = ref<HTMLElement | null>(null);
const userScrolledUp = ref(false);

function onScroll() {
  if (!container.value) return;
  const { scrollTop, scrollHeight, clientHeight } = container.value;
  // User is "at bottom" if within 20px of the end
  userScrolledUp.value = scrollHeight - scrollTop - clientHeight > 20;
}

watch(() => props.text, async () => {
  await nextTick();
  // Auto-scroll to bottom unless user scrolled up
  if (container.value && !userScrolledUp.value) {
    container.value.scrollTop = container.value.scrollHeight;
  }
});
</script>

<template>
  <div class="streaming-block" :class="`variant-${props.variant ?? 'answer'}`">
    <div v-if="props.label" class="streaming-label">{{ props.label }}</div>
    <div ref="container" class="streaming-text" :class="{ active: isStreaming }" @scroll="onScroll">
      <span class="text-content">{{ text }}</span>
      <span v-if="isStreaming" class="cursor">|</span>
    </div>
  </div>
</template>

<style scoped>
.streaming-text {
  background: var(--surface-0);
  color: var(--text-primary);
  border: 1px solid var(--border-default);
  border-radius: 4px;
  padding: 8px;
  font-size: 11px;
  font-family: var(--font-mono);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 400px;
  overflow-y: auto;
  margin: 4px 0;
  line-height: 1.5;
}
.streaming-text.active {
  border-color: var(--border-focus);
  background: var(--surface-2);
}

.cursor {
  animation: blink 0.8s step-end infinite;
  color: var(--accent-agent);
  font-weight: bold;
}

@keyframes blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0; }
}

.streaming-block {
  display: flex;
  flex-direction: column;
}

.streaming-label {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-secondary);
  margin: 4px 0 2px 0;
  font-weight: 600;
}

.variant-reasoning .streaming-text {
  background: var(--surface-1);
  color: var(--text-secondary);
  font-style: italic;
}
.variant-reasoning .streaming-text.active {
  border-color: var(--accent-skill);
  background: var(--surface-2);
}
.variant-reasoning .streaming-label {
  color: var(--accent-skill);
}
.variant-reasoning .cursor {
  color: var(--accent-skill);
}
</style>
