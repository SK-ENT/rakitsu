<script setup lang="ts">
import { computed, onActivated, onDeactivated, onUnmounted, ref, watch } from 'vue';
import { useSessionHistory } from '../../composables';
import { recordedReplayCursor } from '../../composables/recordedReplayCursor';
import { builderReplayEvent } from '../../composables/builderRunFeed';
import EventItem from '../inspector/EventItem.vue';

const { sessions, selectedSession, sessionEvents, loading, error, fetchSessions, loadSession, clearSelection } = useSessionHistory();
const runId = ref('');
const position = ref(0);
const playing = ref(false);
let timer: ReturnType<typeof setTimeout> | undefined;

const lastPosition = computed(() => sessionEvents.value.length - 1);
const current = computed(() => sessionEvents.value[position.value]);
const nearbyStart = computed(() => Math.max(0, Math.min(position.value - 2, lastPosition.value - 4)));
const nearby = computed(() => sessionEvents.value.slice(nearbyStart.value, nearbyStart.value + 5));

function elapsed(timestamp: string) {
  const start = sessionEvents.value[0]?.timestamp;
  const ms = start ? Date.parse(timestamp) - Date.parse(start) : NaN;
  return Number.isFinite(ms) ? `+${Math.max(0, ms)} ms` : '—';
}

function pause() {
  clearTimeout(timer);
  timer = undefined;
  playing.value = false;
}

function scheduleNext() {
  const next = sessionEvents.value[position.value + 1];
  if (!current.value || !next) {
    pause();
    return;
  }
  const gap = Date.parse(next.timestamp) - Date.parse(current.value.timestamp);
  const delay = Number.isFinite(gap) ? Math.min(2000, Math.max(150, gap)) : 150;
  timer = setTimeout(() => {
    setPosition(position.value + 1);
    scheduleNext();
  }, delay);
}

function togglePlayback() {
  if (playing.value) {
    pause();
    return;
  }
  if (lastPosition.value < 1) return;
  if (position.value === lastPosition.value) setPosition(0);
  playing.value = true;
  scheduleNext();
}

function setPosition(index: number) {
  position.value = Math.max(0, Math.min(index, lastPosition.value));
  if (selectedSession.value && current.value) {
    recordedReplayCursor.set(selectedSession.value.id, current.value.id);
  }
}

function seek(index: number) {
  pause();
  setPosition(index);
}

watch(() => selectedSession.value?.id, () => {
  pause();
  position.value = 0;
});

watch([sessionEvents, () => recordedReplayCursor.get(selectedSession.value?.id ?? '')], () => {
  const id = selectedSession.value?.id;
  if (!id || !sessionEvents.value.length) {
    pause();
    position.value = 0;
    return;
  }
  const index = sessionEvents.value.findIndex(event => event.id === recordedReplayCursor.get(id));
  if (index < 0) {
    setPosition(0);
  } else if (index !== position.value) {
    pause();
    position.value = index;
  }
});

async function selectRun() {
  pause();
  position.value = 0;
  clearSelection();
  if (runId.value) await loadSession(runId.value);
}

onActivated(() => {
  if (!sessions.value.length && !loading.value) void fetchSessions();
});
function publishBuilderReplay() {
  const session = selectedSession.value;
  builderReplayEvent.value = session && current.value ? {
    sessionId: session.id, projectId: session.project_id, event: current.value,
    events: sessionEvents.value, position: position.value,
  } : null;
}
watch([selectedSession, current], publishBuilderReplay);
onActivated(publishBuilderReplay);
function leaveTimeline() {
  pause();
  builderReplayEvent.value = null;
}
onDeactivated(leaveTimeline);
onUnmounted(leaveTimeline);
</script>

<template>
  <section class="run-timeline" aria-label="Recorded run timeline">
    <header class="timeline-controls">
      <label>
        Recorded run
        <select v-model="runId" :disabled="loading" @change="selectRun">
          <option value="">Choose a run</option>
          <option v-for="session in sessions" :key="session.id" :value="session.id">
            {{ session.name || 'Untitled run' }} · {{ session.id }}
          </option>
        </select>
      </label>
      <button type="button" :disabled="loading" @click="fetchSessions()">Refresh runs</button>
    </header>

    <p v-if="loading" role="status">Loading runs or events…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="selectedSession">
      <p class="run-identity"><strong>{{ selectedSession.name || 'Untitled run' }}</strong> <code>{{ selectedSession.id }}</code></p>
      <p v-if="!current">This run has no recorded events.</p>
      <template v-else>
        <div class="timeline-controls">
          <button type="button" :disabled="position === 0" @click="seek(position - 1)">Previous event</button>
          <button type="button" :disabled="lastPosition < 1" @click="togglePlayback">{{ playing ? 'Pause' : 'Play' }}</button>
          <button type="button" :disabled="position === lastPosition" @click="seek(position + 1)">Next event</button>
          <span>Event {{ position + 1 }} of {{ sessionEvents.length }}</span>
        </div>
        <input
          aria-label="Replay position"
          type="range"
          min="0"
          :max="lastPosition"
          :value="position"
          :disabled="lastPosition < 1"
          @input="seek(Number(($event.target as HTMLInputElement).value))"
        />
        <ol class="nearby-events" :start="nearbyStart + 1" aria-label="Nearby events">
          <li v-for="(event, index) in nearby" :key="event.id">
            <button
              type="button"
              :aria-current="nearbyStart + index === position ? 'step' : undefined"
              @click="seek(nearbyStart + index)"
            >
              <span>{{ event.event_type }} <small>{{ event.agent_name }}</small></span>
              <code>{{ elapsed(event.timestamp) }}</code>
            </button>
          </li>
        </ol>
        <EventItem :key="current.id" :event="current" />
      </template>
    </template>
    <p v-else-if="!loading && !error">Choose a recorded run to replay its events.</p>
  </section>
</template>

<style scoped>
.run-timeline {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  overflow: auto;
  background: var(--surface-1);
  color: var(--text-primary);
}

.timeline-controls {
  display: flex;
  align-items: center;
  gap: 6px 10px;
  flex-wrap: wrap;
}

.timeline-controls label {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  max-width: 100%;
}

.run-timeline :is(select, button) {
  padding: 5px 8px;
  max-width: 100%;
  font: inherit;
  color: inherit;
  background: var(--surface-2);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
}

.run-timeline p { margin: 0; }
.run-identity { overflow-wrap: anywhere; }
.run-timeline input { accent-color: var(--accent-agent); }
.nearby-events { margin: 0; padding-left: 24px; }
.nearby-events li + li { margin-top: 4px; }

.nearby-events button {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  flex-wrap: wrap;
  width: 100%;
  text-align: left;
}

.nearby-events button[aria-current] { border-color: var(--accent-agent); }
.nearby-events small { color: var(--text-secondary); }
</style>
