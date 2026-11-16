<script setup lang="ts">
import { computed, ref } from 'vue';
import { useEventStream } from '../../composables';
import { builderReplayEvent, builderRunEvents } from '../../composables/builderRunFeed';
import EventItem from '../inspector/EventItem.vue';

const props = defineProps<{ replay?: boolean; followRun?: boolean }>();
const recording = computed(() => props.replay ? builderReplayEvent.value : null);
const { events: workspaceEvents, isConnected } = useEventStream();
const selectedSession = ref<string | null>(null);
const errorsOnly = ref(false);
const events = computed(() => {
  if (recording.value) return recording.value.events.slice(0, recording.value.position + 1);
  const ownEvents = builderRunEvents.value;
  const seen = new Set(workspaceEvents.value.map(event => event.id));
  return [...workspaceEvents.value, ...ownEvents.filter(event => !seen.has(event.id))]
    .sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
});

// Use each session’s earliest event, even when SSE delivers events out of order.
const sessions = computed(() => [...new Set(events.value.flatMap(event =>
  event.session_id ? [event.session_id] : [],
))].reverse());

const effectiveSession = computed(() => selectedSession.value
  ?? (props.followRun ? sessions.value.find(id => id.startsWith('web-')) ?? '' : ''));
const visibleEvents = computed(() => events.value.filter(event =>
  (recording.value || !effectiveSession.value || event.session_id === effectiveSession.value) &&
  (!errorsOnly.value || event.event_type === 'ERROR' ||
    (event.event_type === 'TOOL_CALL_END' && Boolean(event.payload.error))),
));
</script>

<template>
  <section class="run-log" aria-label="Run log events">
    <header class="log-controls">
      <label>
        Run
        <span v-if="recording">Recorded run</span>
        <select v-else v-model="selectedSession">
          <option :value="null">{{ followRun ? 'Latest web run' : 'Default view' }}</option>
          <option value="">All received events</option>
          <option v-if="selectedSession && !sessions.includes(selectedSession)" :value="selectedSession">
            {{ selectedSession }} (no events)
          </option>
          <option v-for="session in sessions" :key="session" :value="session">{{ session }}</option>
        </select>
      </label>
      <label><input v-model="errorsOnly" type="checkbox" /> Errors only</label>
      <span role="status">{{ visibleEvents.length }} events</span>
    </header>
    <div class="log-events">
      <EventItem v-for="event in visibleEvents" :key="event.id" :event="event" />
      <p v-if="!visibleEvents.length" class="log-empty">
        {{ events.length ? 'No events match this selection.' : isConnected
          ? 'Waiting for run events.'
          : 'Run an agent or connect to a live run to see its events here.' }}
      </p>
    </div>
  </section>
</template>

<style scoped>
.run-log {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--surface-1);
  color: var(--text-primary);
}

.log-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border-default);
  font-size: 12px;
}

.log-controls label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
}

.log-controls select {
  min-width: 0;
  max-width: 18ch;
  color: var(--text-primary);
  background: var(--input-bg);
  border: 1px solid var(--input-border);
}

.log-events {
  min-height: 0;
  overflow: auto;
  padding: 8px;
}

.log-empty {
  margin: 8px;
  color: var(--text-secondary);
}
</style>
