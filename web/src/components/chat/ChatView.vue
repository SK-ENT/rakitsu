<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useWorkspace } from '../../composables/useWorkspace';
import { useChatSession } from '../../composables/useChatSession';
import type { ChatSessionMeta } from '../../types';
import ChatPanel from './ChatPanel.vue';
import BranchTreeView from './BranchTreeView.vue';
import MonitorStatusCard from '../monitor/MonitorStatusCard.vue';

// ChatView is the primary chat surface. It owns:
//   - A left sidebar listing every active chat session (multi-session).
//   - A "+ New" action that spins up a session bound to the workspace's
//     currently-selected config.
//   - A ChatPanel on the right bound to the selected session id.
//
// State lives in the module-scoped useChatSession registry, so switching
// sessions here — or switching tabs away and back — doesn't lose history.

const workspace = useWorkspace();

const starting = ref(false);
const startError = ref<string | null>(null);

// Monitor status display (for autostarted wake sessions)
const monitors = ref<string[]>([]);

const sessions = computed<ChatSessionMeta[]>(() => workspace.knownChatSessions.value);
const activeId = computed<string | null>(() => workspace.activeChatSessionId.value);

const activeSession = computed<ChatSessionMeta | null>(() => {
  const id = activeId.value;
  if (!id) return null;
  return sessions.value.find((s) => s.id === id) ?? null;
});

const configId = computed(() => workspace.currentConfigId.value);
const config = computed(() => workspace.currentConfig.value);
const canStart = computed(() => !!configId.value && !starting.value);

async function newSession() {
  if (!configId.value) return;
  starting.value = true;
  startError.value = null;
  try {
    const res = await fetch('/api/chat/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        config_id: configId.value,
        workdir: workspace.workdir.value,
        env_vars: workspace.envVarsMap(),
      }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(body.error || 'failed to start chat');
    }
    const meta = (await res.json()) as ChatSessionMeta;
    await workspace.refreshChatSessions();
    workspace.setActiveChatSession(meta.id);
  } catch (e: unknown) {
    startError.value = (e as Error).message;
  } finally {
    starting.value = false;
  }
}

async function stopSession(id: string) {
  await fetch(`/api/chat/${encodeURIComponent(id)}/stop`, { method: 'POST' }).catch(() => {});
  await workspace.refreshChatSessions();
  if (workspace.activeChatSessionId.value === id) {
    const next = workspace.knownChatSessions.value[0];
    workspace.setActiveChatSession(next ? next.id : null);
  }
}

function selectSession(id: string) {
  workspace.setActiveChatSession(id);
}

// Cross-session messaging: minimal send affordance on each session row.
// POSTs to /api/sessions/{id}/message; the target renders the message as an
// injected turn (settings.session_msg.enabled must be on in its config).
const sendTarget = ref<string | null>(null);
const sendText = ref('');
const sendBusy = ref(false);
const sendError = ref<string | null>(null);

function toggleSend(id: string) {
  sendError.value = null;
  sendText.value = '';
  sendTarget.value = sendTarget.value === id ? null : id;
}

async function sendToSession() {
  const id = sendTarget.value;
  const text = sendText.value.trim();
  if (!id || !text || sendBusy.value) return;
  sendBusy.value = true;
  sendError.value = null;
  try {
    const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/message`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text, from_session_id: 'web-ui', from_name: 'Web UI' }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(body.error || body.status || res.statusText);
    sendTarget.value = null;
    sendText.value = '';
  } catch (e: unknown) {
    sendError.value = (e as Error).message;
  } finally {
    sendBusy.value = false;
  }
}

// Refresh the session list on mount and whenever the config changes, so
// the sidebar stays in sync with the server. No heavy polling — this is
// a singleton component.
onMounted(() => {
  workspace.refreshChatSessions();
});
watch(configId, () => workspace.refreshChatSessions());

// Tree Viewer overlay. Per-session toggle so switching sessions
// doesn't carry the viewer state across (one session might be a
// linear conversation, another might have branches worth inspecting).
const viewerOpen = ref<Record<string, boolean>>({});
const showViewer = computed<boolean>(() => {
  const id = activeId.value;
  return !!id && !!viewerOpen.value[id];
});
function toggleViewer() {
  const id = activeId.value;
  if (!id) return;
  viewerOpen.value[id] = !viewerOpen.value[id];
}
// Close the viewer if the active session changes away from the one we
// opened it on — handled implicitly because viewerOpen is keyed by id,
// but reset on stop to keep the map tidy.
watch(activeId, (nextId, prevId) => {
  if (prevId && !sessions.value.find((s) => s.id === prevId)) {
    delete viewerOpen.value[prevId];
  }
  // No-op for the new id; user toggles explicitly per session.
  void nextId;
});

// Fork the active session via the existing REST endpoint
// (POST /api/chat/{id}/fork). Path-mode default = ChatGPT-style fork
// (keeps just the active path, no subtree copy). The composable
// returns the new session meta; we refresh the workspace list and
// flip the active selection so the user lands in the new chat.
const forkBusy = ref(false);
const forkError = ref<string | null>(null);
async function forkCurrent() {
  const id = activeId.value;
  if (!id || forkBusy.value) return;
  forkBusy.value = true;
  forkError.value = null;
  try {
    const chat = useChatSession(id);
    const newMeta = await chat.fork({ mode: 'path' });
    await workspace.refreshChatSessions();
    workspace.setActiveChatSession(newMeta.id);
  } catch (e) {
    forkError.value = (e as Error).message;
  } finally {
    forkBusy.value = false;
  }
}

function shortPath(p: string | undefined): string {
  if (!p) return '';
  const home = '/Users/';
  let s = p;
  if (s.startsWith(home)) {
    const rest = s.slice(home.length);
    const slash = rest.indexOf('/');
    s = '~/' + (slash >= 0 ? rest.slice(slash + 1) : rest);
  }
  const parts = s.split('/').filter(Boolean);
  if (parts.length <= 3) return s;
  return '…/' + parts.slice(-2).join('/');
}
</script>

<template>
  <div class="chat-view">
    <aside class="sidebar">
      <div class="sidebar-header">
        <h3>Sessions</h3>
        <button
          class="btn-new"
          :disabled="!canStart"
          :title="configId ? 'Start a new chat for the current workspace config' : 'Load a config in Builder or Debugger first'"
          @click="newSession"
        >
          + New
        </button>
      </div>

      <div v-if="sessions.length === 0" class="empty">
        No active chat sessions.
        <div class="empty-hint">
          <template v-if="configId">
            Click <strong>+ New</strong> to chat with
            <strong>{{ config?.name ?? 'the current config' }}</strong>.
          </template>
          <template v-else>
            Load a config in Builder or Debugger first.
          </template>
        </div>
      </div>

      <ul v-else class="session-list">
        <li
          v-for="s in sessions"
          :key="s.id"
          class="session-row"
          :class="{ active: s.id === activeId, generating: s.generating }"
          @click="selectSession(s.id)"
        >
          <div class="session-main">
            <div class="session-name">{{ s.name || s.agent_name }}</div>
            <div class="session-meta">
              <span class="agent">{{ s.agent_name }}</span>
              <span v-if="s.generating" class="pulse">●</span>
            </div>
          </div>
          <button
            class="session-send"
            title="Send a message into this session"
            @click.stop="toggleSend(s.id)"
          >↪</button>
          <button
            class="session-stop"
            title="Stop session"
            @click.stop="stopSession(s.id)"
          >✕</button>
          <div v-if="sendTarget === s.id" class="session-send-box" @click.stop>
            <input
              v-model="sendText"
              placeholder="Message this session…"
              :disabled="sendBusy"
              @keyup.enter="sendToSession"
            />
            <button :disabled="sendBusy || !sendText.trim()" @click="sendToSession">
              {{ sendBusy ? '…' : 'Send' }}
            </button>
          </div>
        </li>
      </ul>

      <div v-if="monitors.length > 0" class="monitors-section">
        <div class="section-label">Monitors</div>
        <div class="monitors-grid">
          <MonitorStatusCard
            v-for="monitorId in monitors"
            :key="monitorId"
            :session-id="monitorId"
          />
        </div>
      </div>

      <div v-if="startError" class="error">{{ startError }}</div>
      <div v-if="sendError" class="error">{{ sendError }}</div>

      <div v-if="config" class="current-config">
        <div class="cfg-label">New chats use</div>
        <div class="cfg-name">
          <span v-if="workspace.currentSource.value" class="src-tag" :class="`src-${workspace.currentSource.value}`">
            {{ workspace.currentSource.value }}
          </span>
          {{ config.name }}
        </div>
        <div v-if="config.path" class="cfg-path" :title="config.path">
          {{ shortPath(config.path) }}
        </div>
      </div>
    </aside>

    <section class="main">
      <template v-if="activeSession">
        <!-- Toolbar moved to relative-flow strip above the chat panel,
             killing the overlap with ChatPanel's connection badge. Fork button
             added next to the Chat/Tree toggle. -->
        <div class="view-toolbar">
          <button
            v-if="!showViewer"
            type="button"
            class="view-toggle"
            :title="forkBusy ? 'Forking…' : 'Fork this conversation into a new session'"
            :disabled="forkBusy"
            @click="forkCurrent"
          >{{ forkBusy ? 'Forking…' : 'Fork' }}</button>
          <button
            type="button"
            class="view-toggle"
            :class="{ active: showViewer }"
            :title="showViewer ? 'Back to chat' : 'Open Branch Tree Viewer'"
            @click="toggleViewer"
          >{{ showViewer ? 'Chat' : 'Tree' }}</button>
        </div>
        <BranchTreeView
          v-if="showViewer"
          :key="`viewer-${activeSession.id}`"
          :session-id="activeSession.id"
          class="viewer-overlay"
          @close="toggleViewer"
        />
        <ChatPanel
          v-else
          :key="activeSession.id"
          :session-id="activeSession.id"
        />
      </template>
      <div v-else class="placeholder">
        <h3>No chat selected</h3>
        <p v-if="sessions.length > 0">
          Pick a session from the sidebar, or start a new one.
        </p>
        <p v-else-if="configId">
          Click <strong>+ New</strong> in the sidebar to chat with
          <strong>{{ config?.name }}</strong>.
        </p>
        <p v-else>
          Load a config in Builder or Debugger first — the Chat tab follows
          the workspace's current config.
        </p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.chat-view {
  display: grid;
  grid-template-columns: 260px 1fr;
  height: 100%;
  background: var(--surface-0);
}

.sidebar {
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border-subtle);
  background: var(--surface-1);
  overflow: hidden;
}

.sidebar-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-subtle);
}

.sidebar-header h3 {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-secondary);
}

.btn-new {
  font-size: 12px;
  padding: 5px 10px;
  background: var(--accent-agent);
  color: var(--surface-0);
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-weight: 600;
}
.btn-new:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.empty {
  padding: 16px;
  font-size: 12px;
  color: var(--text-muted);
}
.empty-hint {
  margin-top: 8px;
  font-size: 12px;
  line-height: 1.5;
}

.monitors-section {
  border-top: 1px solid var(--border-subtle);
  padding: 10px 16px;
}
.section-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-secondary);
  margin-bottom: 8px;
}
.monitors-grid {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.session-list {
  flex: 1;
  overflow-y: auto;
  list-style: none;
  margin: 0;
  padding: 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.session-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  padding: 8px 10px;
  border-radius: 4px;
  border: 1px solid transparent;
  cursor: pointer;
}

.session-send {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 14px;
  padding: 2px 6px;
  border-radius: 3px;
}
.session-send:hover {
  background: var(--surface-3);
}

.session-send-box {
  width: 100%;
  display: flex;
  gap: 4px;
}
.session-send-box input {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  padding: 4px 6px;
  background: var(--surface-1);
  color: var(--text-primary);
  border: 1px solid var(--surface-3);
  border-radius: 3px;
}
.session-send-box button {
  font-size: 12px;
  padding: 4px 8px;
  background: var(--surface-3);
  color: var(--text-primary);
  border: none;
  border-radius: 3px;
  cursor: pointer;
}
.session-send-box button:disabled {
  opacity: 0.5;
  cursor: default;
}
.session-row:hover {
  background: var(--surface-2);
}
.session-row.active {
  background: var(--surface-2);
  border-color: var(--accent-agent);
}

.session-main {
  flex: 1;
  min-width: 0;
}

.session-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-muted);
}

.agent {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse {
  color: var(--status-running);
  animation: pulse 1.2s ease-in-out infinite;
}
@keyframes pulse {
  0%, 100% { opacity: 0.4; }
  50% { opacity: 1; }
}

.session-stop {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 14px;
  padding: 2px 6px;
  border-radius: 3px;
}
.session-stop:hover {
  background: var(--surface-3);
  color: var(--status-error, #ef4444);
}

.error {
  margin: 8px 16px;
  padding: 8px;
  font-size: 12px;
  color: var(--status-error, #ef4444);
  background: rgba(239, 68, 68, 0.08);
  border-radius: 4px;
}

.current-config {
  border-top: 1px solid var(--border-subtle);
  padding: 10px 16px;
  font-size: 11px;
}
.cfg-label {
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-family: var(--font-mono);
}
.cfg-name {
  font-size: 13px;
  color: var(--text-primary);
  font-weight: 500;
  margin-top: 4px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.cfg-path {
  font-family: var(--font-mono);
  color: var(--text-muted);
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.src-tag {
  font-family: var(--font-mono);
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  padding: 1px 5px;
  border-radius: 2px;
  color: var(--surface-0);
}
.src-tag.src-builder { background: var(--accent-agent); }
.src-tag.src-debug   { background: var(--status-paused, #f59e0b); }
.src-tag.src-session { background: var(--text-muted); }

.main {
  overflow: hidden;
  min-width: 0;
  position: relative;
  display: flex;
  flex-direction: column;
}

/* Relative-flow strip above the chat panel. Previously
   position:absolute, which overlapped ChatPanel's connection badge. */
.view-toolbar {
  display: flex;
  justify-content: flex-end;
  gap: 6px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
  flex-shrink: 0;
}

.view-toggle {
  padding: 4px 10px;
  font-size: 12px;
  font-family: var(--font-sans);
  border: 1px solid var(--border-subtle, rgba(255, 255, 255, 0.18));
  border-radius: 4px;
  background: var(--surface-1, rgba(20, 22, 30, 0.85));
  color: var(--text-primary, rgba(255, 255, 255, 0.92));
  cursor: pointer;
}
.view-toggle:hover {
  background: rgba(255, 255, 255, 0.08);
}
.view-toggle.active {
  border-color: var(--accent, #4f8cff);
  color: var(--accent, #4f8cff);
}

.viewer-overlay {
  flex: 1;
  min-height: 0;
}

.placeholder {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 32px;
  color: var(--text-muted);
  text-align: center;
}
.placeholder h3 {
  margin: 0 0 8px;
  font-size: 16px;
  color: var(--text-primary);
}
.placeholder p {
  margin: 0;
  font-size: 13px;
  line-height: 1.5;
}
</style>
