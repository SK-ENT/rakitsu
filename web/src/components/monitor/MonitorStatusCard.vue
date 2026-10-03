<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useWakeStatus } from '../../composables/useWakeStatus'

const props = defineProps<{ sessionId: string }>()
const { status, error, busy, stop, resume } = useWakeStatus(toRef(props, 'sessionId'))

const fmt = (t?: string) => (t ? new Date(t).toLocaleTimeString() : 'never')
const canStop = computed(() => !!status.value && ['ok', 'stale', 'starting'].includes(status.value.state))
</script>

<template>
  <div class="monitor-card" :data-state="status?.state ?? 'unknown'">
    <div class="head">
      <span class="badge">{{ status?.state ?? 'unknown' }}</span>
      <strong class="label">{{ status?.label || sessionId }}</strong>
    </div>
    <dl v-if="status">
      <dt>Last tick</dt><dd>{{ fmt(status.last_tick) }}</dd>
      <dt>Turns this hour</dt><dd>{{ status.turns_last_hour }} / {{ status.max_turns_per_hour }}</dd>
      <dt>Running tasks</dt><dd>{{ status.running_tasks }}</dd>
      <dt>Next tick</dt><dd>{{ status.next_tick_in_seconds }}s</dd>
    </dl>
    <p v-if="error" class="err">{{ error }}</p>
    <div class="actions">
      <button :disabled="busy || !canStop" @click="stop">Stop</button>
      <button :disabled="busy || canStop" @click="resume">Resume</button>
    </div>
  </div>
</template>

<style scoped>
.monitor-card { border: 1px solid #8884; border-radius: 8px; padding: 12px; min-width: 240px; }
.head { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.badge { padding: 2px 8px; border-radius: 10px; font-size: 12px; color: #fff; background: #888; }
[data-state='ok'] .badge { background: #2e9e5b; }
[data-state='stale'] .badge { background: #d98b1f; }
[data-state='starting'] .badge { background: #3b82c4; }
[data-state='failed'] .badge { background: #c0392b; }
[data-state='stopped'] .badge { background: #666; }
dl { display: grid; grid-template-columns: auto 1fr; gap: 2px 12px; margin: 0 0 8px; font-size: 13px; }
dt { opacity: 0.7; }
dd { margin: 0; }
.err { color: #c0392b; font-size: 12px; margin: 0 0 8px; }
.actions { display: flex; gap: 8px; }
</style>
