<script setup lang="ts">
import { computed, ref, watch, nextTick, onMounted, onUnmounted } from 'vue';
import EventItem from './EventItem.vue';
import { useSessionHistory } from '../../composables';
import { createEventStream } from '../../composables/useEventStream';
import type { EventType } from '../../types';
import ComboBox from '../builder/ComboBox.vue';

const mode = ref<'history' | 'live'>('history');

// Live mode
const { events, isConnected, error: liveError, sessionInfo, connect, disconnect, clearEvents } = createEventStream();
const debugUrl = ref('localhost:9100');

// History mode
const {
  sessions, loading, error: historyError,
  selectedSession, sessionEvents,
  fetchSessions, loadSession, clearSelection,
} = useSessionHistory();

const filterType = ref<EventType | ''>('');

const EVENT_TYPES: EventType[] = [
  'AGENT_START', 'AGENT_END', 'THOUGHT_START', 'THOUGHT_END',
  'TOOL_CALL_START', 'TOOL_CALL_END', 'AGENT_HANDOFF', 'AGENT_MESSAGE',
  'TOKEN_USAGE', 'ERROR', 'EXECUTION_COMPLETE',
  'REFLECTION_START', 'REFLECTION_END', 'GROUND_CHECK_START', 'GROUND_CHECK_END',
  'PIPELINE_START', 'PIPELINE_END', 'PIPELINE_STEP_START', 'PIPELINE_STEP_END',
  'DEBUG_PAUSED', 'DEBUG_RESUMED', 'TOKEN_CHUNK', 'REASONING_CHUNK', 'REPLAY_START', 'REPLAY_END',
  'RETRY_ATTEMPT', 'CONTEXT_COMPRESSED', 'RETRIEVAL_INJECTED',
];

const activeEvents = computed(() => {
  if (mode.value === 'history') return sessionEvents.value;
  return events.value;
});

const filteredEvents = computed(() => {
  const source = activeEvents.value;
  if (!filterType.value) return source;
  return source.filter(e => e.event_type === filterType.value);
});

const totalTokens = computed(() => {
  let input = 0, output = 0;
  for (const event of activeEvents.value) {
    if (event.token_usage) {
      input += event.token_usage.input_tokens;
      output += event.token_usage.output_tokens;
    }
  }
  return { input, output, total: input + output };
});

const totalCost = computed(() => {
  let cost = 0;
  for (const event of activeEvents.value) {
    if (event.event_type === 'AGENT_END' && event.payload?.total_cost) {
      cost += event.payload.total_cost as number;
    }
  }
  return cost;
});

const eventCount = computed(() => activeEvents.value.length);

// Auto-scroll
const autoScroll = ref(true);
const eventsContainer = ref<HTMLElement | null>(null);

function onContainerScroll() {
  if (!eventsContainer.value) return;
  const el = eventsContainer.value;
  const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 30;
  autoScroll.value = atBottom;
}

watch(filteredEvents, () => {
  if (autoScroll.value) {
    nextTick(() => {
      if (eventsContainer.value) {
        eventsContainer.value.scrollTop = eventsContainer.value.scrollHeight;
      }
    });
  }
}, { deep: true });

const currentError = computed(() => {
  if (mode.value === 'live') return liveError.value;
  return historyError.value;
});

function handleConnect() {
  const url = debugUrl.value.startsWith('http')
    ? debugUrl.value
    : `http://${debugUrl.value}`;
  connect(url);
}

function handleSwitchMode(m: 'history' | 'live') {
  mode.value = m;
  if (m === 'history') {
    fetchSessions();
    disconnect();
  } else {
    clearSelection();
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

onMounted(() => {
  fetchSessions();
});
onUnmounted(disconnect);
</script>

<template>
  <div class="run-inspector">
    <div class="inspector-header">
      <h2>Run Inspector</h2>
      <div class="mode-toggle">
        <button
          :class="['mode-btn', { active: mode === 'history' }]"
          @click="handleSwitchMode('history')"
        >History</button>
        <button
          :class="['mode-btn', { active: mode === 'live' }]"
          @click="handleSwitchMode('live')"
        >Live</button>
      </div>
      <div
        v-if="mode === 'live'"
        class="connection-status"
        :class="{ connected: isConnected, disconnected: !isConnected }"
      >
        {{ isConnected ? 'Connected' : 'Disconnected' }}
      </div>
    </div>

    <!-- Live mode toolbar -->
    <div v-if="mode === 'live'" class="inspector-toolbar">
      <div class="toolbar-left">
        <div class="url-group">
          <input
            v-model="debugUrl"
            type="text"
            class="url-input"
            placeholder="localhost:9100"
            :disabled="isConnected"
            @keyup.enter="handleConnect"
          />
          <button v-if="!isConnected" @click="handleConnect" class="btn btn-primary">Connect</button>
          <button v-else @click="disconnect" class="btn btn-secondary">Disconnect</button>
        </div>
        <button @click="clearEvents" class="btn btn-outline" :disabled="events.length === 0">Clear</button>
        <ComboBox
          :model-value="filterType"
          :options="[{value:'',label:'All Events'}, ...EVENT_TYPES.map(t => ({value:t,label:t}))]"
          placeholder="Filter events"
          :allow-custom="false"
          @update:model-value="filterType = $event as any"
        />
      </div>
      <div class="stats">
        <span class="stat">
          <span class="stat-label">Events:</span>
          <span class="stat-value">{{ eventCount }}</span>
        </span>
        <span class="stat">
          <span class="stat-label">Tokens:</span>
          <span class="stat-value">{{ totalTokens.total }}</span>
        </span>
        <span v-if="totalCost > 0" class="stat">
          <span class="stat-label">Cost:</span>
          <span class="stat-value">${{ totalCost.toFixed(4) }}</span>
        </span>
      </div>
      <label class="auto-scroll-toggle">
        <input type="checkbox" v-model="autoScroll" />
        Auto-scroll
      </label>
    </div>

    <!-- Session info banner (live) -->
    <div v-if="mode === 'live' && sessionInfo && isConnected" class="session-banner">
      <div class="session-row">
        <span class="session-label">Session:</span>
        <span class="session-value">{{ sessionInfo.name }}</span>
        <span class="session-label">Agents:</span>
        <span class="session-value">{{ sessionInfo.agents.join(', ') }}</span>
      </div>
      <div class="session-row">
        <span class="session-label">Query:</span>
        <span class="session-value session-query">{{ sessionInfo.query }}</span>
      </div>
    </div>

    <!-- Session info banner (history) -->
    <div v-if="mode === 'history' && selectedSession" class="session-banner">
      <div class="session-row">
        <span class="session-label">Session:</span>
        <span class="session-value">{{ selectedSession.name }}</span>
        <span :class="['status-badge', 'status-' + selectedSession.status]">{{ selectedSession.status }}</span>
        <span class="session-label">Duration:</span>
        <span class="session-value">{{ formatDuration(selectedSession.duration_ms) }}</span>
        <span class="session-label">Tokens:</span>
        <span class="session-value">{{ selectedSession.total_tokens }}</span>
      </div>
      <div class="session-row">
        <span class="session-label">Query:</span>
        <span class="session-value session-query">{{ selectedSession.query }}</span>
      </div>
      <div class="session-toolbar">
        <button @click="clearSelection(); fetchSessions()" class="btn btn-outline btn-sm">Back to list</button>
        <ComboBox
          :model-value="filterType"
          :options="[{value:'',label:'All Events'}, ...EVENT_TYPES.map(t => ({value:t,label:t}))]"
          placeholder="Filter events"
          :allow-custom="false"
          @update:model-value="filterType = $event as any"
        />
        <div class="stats">
          <span class="stat">
            <span class="stat-label">Events:</span>
            <span class="stat-value">{{ eventCount }}</span>
          </span>
        </div>
      </div>
    </div>

    <div v-if="currentError" class="error-banner">{{ currentError }}</div>

    <div class="events-container" ref="eventsContainer" @scroll="onContainerScroll">
      <!-- History: session list -->
      <div v-if="mode === 'history' && !selectedSession" class="session-list">
        <div v-if="loading" class="empty-state"><p>Loading sessions...</p></div>
        <div v-else-if="sessions.length === 0" class="empty-state">
          <p>No sessions recorded yet</p>
          <p class="empty-hint">Run <code>rakitsu run agent.yaml "query"</code> to create your first session</p>
        </div>
        <div
          v-else
          v-for="s in sessions"
          :key="s.id"
          class="session-card"
          @click="loadSession(s.id)"
        >
          <div class="session-card-header">
            <span class="session-card-name">{{ s.name }}</span>
            <span :class="['status-badge', 'status-' + s.status]">{{ s.status }}</span>
          </div>
          <div class="session-card-query">{{ s.query }}</div>
          <div class="session-card-meta">
            <span>{{ s.agents.join(', ') }}</span>
            <span>{{ s.total_events }} events</span>
            <span>{{ s.total_tokens }} tokens</span>
            <span>{{ formatDuration(s.duration_ms) }}</span>
            <span class="session-card-time">{{ formatTime(s.start_time) }}</span>
          </div>
        </div>
      </div>

      <!-- History: selected session events -->
      <div v-else-if="mode === 'history' && selectedSession" class="events-list">
        <div v-if="loading" class="empty-state"><p>Loading events...</p></div>
        <EventItem v-for="event in filteredEvents" :key="event.id" :event="event" />
      </div>

      <!-- Live: events stream -->
      <div v-else-if="mode === 'live'">
        <div v-if="eventCount === 0" class="empty-state">
          <p>No events yet</p>
          <p class="empty-hint">
            Run <code>rakitsu run --debug-port 9100 agent.yaml "query"</code><br/>
            then connect to see real-time execution traces
          </p>
        </div>
        <div v-else class="events-list">
          <EventItem v-for="event in filteredEvents" :key="event.id" :event="event" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.run-inspector {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--surface-0);
}

.inspector-header {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
}

.inspector-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}

.mode-toggle {
  display: flex;
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  overflow: hidden;
}

.mode-btn {
  padding: 5px 14px;
  font-size: 12px;
  font-weight: 500;
  border: none;
  background: var(--surface-0);
  cursor: pointer;
  color: var(--text-secondary);
  transition: all 0.15s;
}

.mode-btn.active {
  background: #667eea;
  color: white;
}

.mode-btn:not(.active):hover {
  background: var(--surface-2);
}

.connection-status {
  margin-left: auto;
  font-size: 12px;
  padding: 4px 12px;
  border-radius: 12px;
  font-weight: 500;
}

.connection-status.connected { background: #e8f5e9; color: #2e7d32; }
.connection-status.disconnected { background: #ffebee; color: #c62828; }

.session-banner {
  padding: 10px 20px;
  background: rgba(91, 141, 239, 0.08);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px;
}

.session-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 2px;
}
.session-row:last-child { margin-bottom: 0; }

.session-label { color: var(--text-secondary); font-weight: 500; }
.session-value { color: var(--accent-agent); font-weight: 600; margin-right: 12px; }
.session-query { font-style: italic; font-weight: 400; color: var(--text-primary); }

.session-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 8px;
}

.inspector-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 20px;
  border-bottom: 1px solid var(--border-subtle);
  flex-wrap: wrap;
}

.toolbar-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.url-group {
  display: flex;
  align-items: center;
  gap: 6px;
}

.url-input {
  padding: 6px 10px;
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  font-size: 13px;
  font-family: monospace;
  width: 180px;
}
.url-input:disabled { background: var(--surface-2); color: var(--text-muted); }

.filter-select {
  padding: 6px 10px;
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  font-size: 12px;
  background: var(--surface-2);
  cursor: pointer;
}

.btn {
  padding: 6px 14px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all 0.2s;
}
.btn:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-sm { padding: 4px 10px; font-size: 12px; }
.btn-primary { background: #667eea; color: white; }
.btn-primary:hover:not(:disabled) { background: #5a67d8; }
.btn-secondary { background: #e0e0e0; color: var(--text-primary); }
.btn-secondary:hover:not(:disabled) { background: #d0d0d0; }
.btn-outline { background: transparent; border: 1px solid var(--border-subtle); color: var(--text-secondary); }
.btn-outline:hover:not(:disabled) { background: var(--surface-2); }

.stats { display: flex; gap: 16px; }
.stat { display: flex; align-items: center; gap: 4px; font-size: 12px; }
.stat-label { color: var(--text-secondary); }
.stat-value { font-weight: 600; color: var(--text-primary); }

.auto-scroll-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--text-secondary);
  cursor: pointer;
  user-select: none;
}
.auto-scroll-toggle input { cursor: pointer; }

.status-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
  margin-right: 8px;
}
.status-success { background: #e8f5e9; color: #2e7d32; }
.status-error { background: #ffebee; color: #c62828; }
.status-timeout { background: #fff3e0; color: #e65100; }
.status-running { background: #e3f2fd; color: #1565c0; }

.error-banner {
  padding: 12px 20px;
  background: #ffebee;
  color: #c62828;
  font-size: 13px;
}

.events-container {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

/* Session list */
.session-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.session-card {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 14px 16px;
  cursor: pointer;
  transition: all 0.15s;
}
.session-card:hover {
  border-color: var(--accent-agent);
  box-shadow: 0 2px 8px rgba(91, 141, 239, 0.15);
}

.session-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}

.session-card-name {
  font-weight: 600;
  font-size: 14px;
  color: var(--text-primary);
}

.session-card-query {
  font-size: 13px;
  color: var(--text-secondary);
  margin-bottom: 8px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.session-card-meta {
  display: flex;
  gap: 12px;
  font-size: 11px;
  color: var(--text-muted);
}

.session-card-time {
  margin-left: auto;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--text-muted);
  padding: 40px;
}
.empty-state p { margin: 0; font-size: 14px; }
.empty-hint {
  margin-top: 8px !important;
  font-size: 12px !important;
  color: var(--text-muted);
  text-align: center;
  line-height: 1.6;
}
.empty-hint code {
  background: var(--surface-3);
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 11px;
  color: var(--text-secondary);
}

.events-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
