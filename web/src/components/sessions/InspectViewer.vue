<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import yaml from 'js-yaml';
import ConversationTab from './inspect/ConversationTab.vue';
import EventsTab from './inspect/EventsTab.vue';
import TreeTab from './inspect/TreeTab.vue';
import ConfigTab from './inspect/ConfigTab.vue';
import { useSessionHistory } from '../../composables/useSessionHistory';
import { useWorkspace } from '../../composables/useWorkspace';
import type { ChatSessionMeta } from '../../types';

// InspectViewer — right pane of SessionsView. Owns the sub-tab nav and the
// action toolbar, which wires four live actions:
//   - Resume: POST /api/chat/start with resume_id, switch to Chat with the
//     new live session selected. Continues the conversation as a live chat.
//   - Clone:  load the session's config YAML into the Builder canvas as a
//     fresh draft. Same flow as the legacy "Inspect" button.
//   - Re-run: POST /api/sessions/{id}/rerun, switch to Debugger to watch
//     the fresh execution. Same flow as the legacy SessionPanel Re-run.
//   - Fork:   POST /api/chat/{id}/fork — branch a new live chat from this
//     past conversation (path mode), then switch to Chat. The backend loads
//     the tree + config from disk; env_vars de-redacts API keys.
//   - Debug:  pure navigation — switch to the Debugger in History mode and
//     replay this session's event log through the tree/graph visualizations.

const props = defineProps<{
  sessionId: string;
}>();

const emit = defineEmits<{
  resume: [meta: ChatSessionMeta];
  clone: [data: { yaml: string; projectId?: string }];
  rerun: [newSessionId: string];
  fork: [meta: ChatSessionMeta];
  debug: [sessionId: string];
}>();

type SubTab = 'conversation' | 'events' | 'tree' | 'config';
const activeTab = ref<SubTab>('conversation');

const tabs: { key: SubTab; label: string }[] = [
  { key: 'conversation', label: 'Conversation' },
  { key: 'events', label: 'Events' },
  { key: 'tree', label: 'Tree' },
  { key: 'config', label: 'Config' },
];

const history = useSessionHistory();
const workspace = useWorkspace();
const busy = ref<'resume' | 'clone' | 'rerun' | 'fork' | null>(null);
const actionError = ref<string | null>(null);

// Override panel. Resume on a redacted YAML 401s because the
// persisted api_key is the literal "[REDACTED]" mask. The backend now
// reinjects from request env_vars by <UPPER_PROVIDER_NAME>_API_KEY
// convention; this panel is how the user supplies those env_vars without
// restarting `rakitsu serve` with new host env. Rows are persisted to
// localStorage so they survive page reloads, and pushed into the
// workspace composable so Resume / Clone / Re-run all pick them up via
// the existing envVarsMap() plumbing — also benefits Chat → + New.
const ENV_OVERRIDES_KEY = 'rakitsu.envOverrides.v1';
// These rows are conventionally *_API_KEY values (see comment above) — never
// persist any value to localStorage, only the name (a name-based filter let
// secrets under names like AUTH or DB_PWD through). The in-memory workspace
// push below keeps the real value; only the browser-storage copy blanks it.
const overrideRows = ref<{ key: string; value: string }[]>([]);
const showOverrides = ref(false);

function syncOverridesToWorkspace() {
  const rows = overrideRows.value.filter((r) => r.key.trim());
  workspace.setEnvVars(rows.map((r) => ({ key: r.key.trim(), value: r.value })));
  try {
    const forStorage = rows.map((r) => ({ key: r.key, value: '' }));
    localStorage.setItem(ENV_OVERRIDES_KEY, JSON.stringify(forStorage));
  } catch {
    /* quota or disabled; non-fatal */
  }
}

function loadOverridesFromStorage() {
  try {
    const raw = localStorage.getItem(ENV_OVERRIDES_KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) {
        overrideRows.value = parsed
          .filter((r) => r && typeof r.key === 'string' && typeof r.value === 'string')
          .map((r) => ({ key: r.key, value: r.value }));
      }
    }
  } catch {
    /* ignore */
  }
  // Merge in the workspace's real values: a persisted row's value may have
  // been blanked before storage (all values are, see above) — prefer the real workspace value over a blank persisted one so
  // reloading the page doesn't discard a secret that's still live in memory.
  // Also seeds any workspace env_vars that aren't already in localStorage
  // rows (e.g. Builder welcome screen wrote them via setEnvVars before the
  // user ever opened the Sessions tab).
  const workspaceValues = new Map(workspace.envVars.value.map((v) => [v.key, v.value]));
  const known = new Set(overrideRows.value.map((r) => r.key));
  overrideRows.value = overrideRows.value.map((row) => ({
    ...row,
    value: row.value || workspaceValues.get(row.key) || '',
  }));
  for (const v of workspace.envVars.value) {
    if (v.key && !known.has(v.key)) {
      overrideRows.value.push({ key: v.key, value: v.value });
      known.add(v.key);
    }
  }
  if (overrideRows.value.length > 0) showOverrides.value = true;
  syncOverridesToWorkspace();
}

// Derive the env var keys the loaded session expects, by walking the
// persisted YAML for providers (and the legacy flat api_keys map) whose
// values were redacted to the literal "[REDACTED]" mask. Backend de-redact
// uses <UPPER(provider_name)>_API_KEY with hyphens/dots normalized.
function deriveExpectedKeysFromYaml(yamlStr: string | undefined): string[] {
  if (!yamlStr) return [];
  let parsed: any;
  try {
    parsed = yaml.load(yamlStr);
  } catch {
    return [];
  }
  const norm = (n: string) => n.toUpperCase().replace(/[-.]/g, '_') + '_API_KEY';
  const out = new Set<string>();
  const providers = parsed?.settings?.providers;
  if (providers && typeof providers === 'object') {
    for (const [name, def] of Object.entries<any>(providers)) {
      if (def?.api_key === '[REDACTED]') out.add(norm(name));
    }
  }
  const flat = parsed?.settings?.api_keys;
  if (flat && typeof flat === 'object') {
    for (const [name, val] of Object.entries<any>(flat)) {
      if (val === '[REDACTED]') out.add(norm(name));
    }
  }
  return Array.from(out);
}

// Add a row for every key the current session expects but the user hasn't
// provided yet. Forward-additive: existing rows (including ones the user
// already typed values into) are left alone, and rows from earlier sessions
// stick around because they may still be useful for the next Resume.
function seedRowsForSession() {
  const meta = sessionMeta.value;
  const expected = deriveExpectedKeysFromYaml(meta?.config_yaml);
  if (expected.length === 0) return;
  const known = new Set(overrideRows.value.map((r) => r.key));
  let added = 0;
  for (const k of expected) {
    if (!known.has(k)) {
      overrideRows.value.push({ key: k, value: '' });
      known.add(k);
      added++;
    }
  }
  if (added > 0) {
    showOverrides.value = true;
    syncOverridesToWorkspace();
  }
}

function addOverrideRow() {
  overrideRows.value.push({ key: '', value: '' });
}

function removeOverrideRow(idx: number) {
  overrideRows.value.splice(idx, 1);
  syncOverridesToWorkspace();
}

function onOverrideEdit() {
  syncOverridesToWorkspace();
}

// Resume requires a persisted .chat.json. CLI runs and
// chat sessions that never finalized a turn don't have one. We probe
// /api/chat/{id}/tree on session select — a 200 means there's something
// to resume into, a 404 means there isn't. The button stays disabled
// with a clear tooltip in the 404 case instead of letting the user click
// and hit a raw server error.
const resumeAvailable = ref<boolean | null>(null);

async function probeResume(id: string) {
  resumeAvailable.value = null;
  try {
    const res = await fetch(`/api/chat/${encodeURIComponent(id)}/tree`, { method: 'GET' });
    resumeAvailable.value = res.ok;
  } catch {
    resumeAvailable.value = false;
  }
}

onMounted(() => {
  loadOverridesFromStorage();
  probeResume(props.sessionId);
  // useSessionHistory() returns fresh refs per call, so InspectViewer must
  // populate its OWN history.selectedSession — ConversationTab's load lives
  // on a separate instance and won't propagate here. The sessionMeta watch
  // below fires once this resolves and runs seedRowsForSession().
  history.loadSession(props.sessionId);
});
watch(() => props.sessionId, (id) => {
  if (id) {
    probeResume(id);
    history.loadSession(id);
  }
});

const sessionMeta = computed(() => history.selectedSession.value);

// sessionMeta is loaded asynchronously by useSessionHistory; re-seed once
// the config_yaml actually arrives (on first open AND on session switch).
watch(
  () => sessionMeta.value?.id + '|' + (sessionMeta.value?.config_yaml ?? ''),
  () => seedRowsForSession(),
);
const cloneAvailable = computed(() => !!sessionMeta.value?.config_yaml);

async function doResume() {
  if (busy.value || resumeAvailable.value === false) return;
  busy.value = 'resume';
  actionError.value = null;
  try {
    // Pass workspace env_vars + workdir like Chat → + New does, so the
    // resumed session gets the real API keys instead of the redacted
    // placeholders that may be baked into the persisted config YAML.
    const res = await fetch('/api/chat/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        resume_id: props.sessionId,
        workdir: workspace.workdir.value,
        env_vars: workspace.envVarsMap(),
      }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText }));
      const raw = body.error || `HTTP ${res.status}`;
      // Friendlier surface for the common "no .chat.json" case.
      if (/no such file or directory|load chat tree/.test(raw)) {
        throw new Error(
          'This session has no persisted chat history (.chat.json). ' +
          'CLI runs (rakitsu run …) and chat sessions that never sent a turn ' +
          'don’t produce one. Try a chat session started via Chat → + New.',
        );
      }
      throw new Error(raw);
    }
    const meta = (await res.json()) as ChatSessionMeta;
    emit('resume', meta);
  } catch (e) {
    actionError.value = `Resume failed: ${(e as Error).message}`;
  } finally {
    busy.value = null;
  }
}

async function doFork() {
  if (busy.value || resumeAvailable.value === false) return;
  busy.value = 'fork';
  actionError.value = null;
  try {
    // Default mode = path: ChatGPT-style "branch a fresh thread from here".
    // env_vars de-redacts API keys baked into the persisted YAML — the fork
    // starts a live runner, same as Resume. The backend reads the workdir
    // from the session's persisted metadata.
    const res = await fetch(`/api/chat/${encodeURIComponent(props.sessionId)}/fork`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        env_vars: workspace.envVarsMap(),
      }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(body.error || `HTTP ${res.status}`);
    }
    const meta = (await res.json()) as ChatSessionMeta;
    emit('fork', meta);
  } catch (e) {
    actionError.value = `Fork failed: ${(e as Error).message}`;
  } finally {
    busy.value = null;
  }
}

function doClone() {
  if (busy.value) return;
  const yaml = sessionMeta.value?.config_yaml;
  if (!yaml) {
    actionError.value = 'Clone: no inline config YAML on this session.';
    return;
  }
  emit('clone', { yaml, projectId: sessionMeta.value?.project_id });
}

async function doRerun() {
  if (busy.value) return;
  busy.value = 'rerun';
  actionError.value = null;
  try {
    const newId = await history.rerunSession(props.sessionId, false);
    if (!newId) {
      actionError.value = history.error.value || 'Re-run failed';
      return;
    }
    emit('rerun', newId);
  } finally {
    busy.value = null;
  }
}
</script>

<template>
  <div class="inspect-viewer">
    <nav class="sub-tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        class="sub-tab"
        :class="{ active: activeTab === t.key }"
        @click="activeTab = t.key"
      >
        {{ t.label }}
      </button>
    </nav>

    <div class="tab-body">
      <ConversationTab
        v-if="activeTab === 'conversation'"
        :session-id="props.sessionId"
      />
      <EventsTab
        v-else-if="activeTab === 'events'"
        :session-id="props.sessionId"
      />
      <TreeTab
        v-else-if="activeTab === 'tree'"
        :session-id="props.sessionId"
      />
      <ConfigTab
        v-else-if="activeTab === 'config'"
        :session-id="props.sessionId"
      />
    </div>

    <footer class="action-toolbar">
      <details class="overrides" :open="showOverrides" @toggle="showOverrides = ($event.target as HTMLDetailsElement).open">
        <summary>
          <span class="overrides-label">Override env vars</span>
          <span class="overrides-count" v-if="overrideRows.length">({{ overrideRows.length }})</span>
          <span class="overrides-hint">
            Used as request env_vars on Resume / Clone / Re-run. Persisted to localStorage.
            Convention: <code>&lt;UPPER_PROVIDER_NAME&gt;_API_KEY</code> (e.g. <code>LITELLM_API_KEY</code>).
          </span>
        </summary>
        <div class="overrides-body">
          <div v-for="(row, idx) in overrideRows" :key="idx" class="override-row">
            <input
              v-model="row.key"
              class="override-input override-key"
              placeholder="KEY"
              autocomplete="off"
              spellcheck="false"
              @blur="onOverrideEdit"
            />
            <span class="override-eq">=</span>
            <input
              v-model="row.value"
              class="override-input override-value"
              placeholder="value"
              autocomplete="off"
              spellcheck="false"
              @blur="onOverrideEdit"
            />
            <button class="override-remove" title="Remove this row" @click="removeOverrideRow(idx)">×</button>
          </div>
          <button class="override-add" @click="addOverrideRow">+ Add row</button>
        </div>
      </details>

      <div class="actions">
        <button
          class="action-btn"
          :disabled="!!busy"
          title="Replay this session's event log in the Debugger's tree/graph visualization"
          @click="emit('debug', props.sessionId)"
        >Debug</button>
        <button
          class="action-btn"
          :disabled="!!busy || resumeAvailable === false"
          :title="resumeAvailable === false
            ? 'No persisted chat history (.chat.json) for this session — only chat sessions started via Chat → + New can be resumed.'
            : 'Continue this conversation as a new live chat (POST /api/chat/start with resume_id)'"
          @click="doResume"
        >{{ busy === 'resume' ? 'Resuming…' : 'Resume' }}</button>
        <button
          class="action-btn"
          :disabled="!!busy || !cloneAvailable"
          :title="cloneAvailable
            ? 'Load this session’s config YAML into the Builder canvas as a fresh draft'
            : 'No inline config YAML on this session'"
          @click="doClone"
        >Clone</button>
        <button
          class="action-btn"
          :disabled="!!busy"
          title="Re-execute the original config from scratch (new session, no conversation seed)"
          @click="doRerun"
        >{{ busy === 'rerun' ? 'Starting…' : 'Re-run' }}</button>
        <button
          class="action-btn"
          :disabled="!!busy || resumeAvailable === false"
          :title="resumeAvailable === false
            ? 'No persisted chat history (.chat.json) for this session — nothing to fork from.'
            : 'Branch a new live chat from this conversation (POST /api/chat/{id}/fork, path mode)'"
          @click="doFork"
        >{{ busy === 'fork' ? 'Forking…' : 'Fork' }}</button>
      </div>
      <div v-if="actionError" class="error">{{ actionError }}</div>
    </footer>
  </div>
</template>

<style scoped>
.inspect-viewer {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--surface-0);
  min-height: 0;
}

.sub-tabs {
  display: flex;
  gap: 2px;
  padding: 8px 12px 0;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
  flex-shrink: 0;
}

.sub-tab {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  background: transparent;
  border: 1px solid transparent;
  border-bottom: none;
  color: var(--text-secondary);
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  border-radius: 4px 4px 0 0;
  position: relative;
  top: 1px;
}
.sub-tab:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
.sub-tab.active {
  background: var(--surface-0);
  border-color: var(--border-subtle);
  color: var(--text-primary);
}

.tab-body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.action-toolbar {
  flex-shrink: 0;
  padding: 8px 16px;
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-1);
  font-size: 11px;
}

.actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

.action-btn {
  padding: 6px 14px;
  background: var(--surface-2);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.action-btn:hover:not(:disabled) {
  background: var(--surface-3);
  border-color: var(--accent-agent);
}
.action-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.error {
  margin-top: 6px;
  color: var(--status-error, #ef4444);
  font-family: var(--font-mono);
  font-size: 11px;
}

.overrides {
  margin-bottom: 8px;
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  background: var(--surface-0);
}
.overrides > summary {
  list-style: none;
  padding: 6px 10px;
  cursor: pointer;
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 11px;
  color: var(--text-secondary);
  user-select: none;
}
.overrides > summary::-webkit-details-marker { display: none; }
.overrides > summary::before {
  content: '▸';
  font-size: 10px;
  color: var(--text-muted);
  transition: transform 0.1s;
}
.overrides[open] > summary::before { transform: rotate(90deg); }
.overrides-label {
  font-weight: 600;
  color: var(--text-primary);
}
.overrides-count {
  font-family: var(--font-mono);
  color: var(--accent-agent);
}
.overrides-hint {
  font-size: 10.5px;
  color: var(--text-muted);
  margin-left: auto;
}
.overrides-hint code {
  font-family: var(--font-mono);
  font-size: 10px;
  padding: 0 3px;
  background: var(--surface-2);
  border-radius: 2px;
}
.overrides-body {
  padding: 8px 10px 10px;
  border-top: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.override-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.override-input {
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 4px 6px;
  border: 1px solid var(--border-subtle);
  border-radius: 3px;
  background: var(--surface-1);
  color: var(--text-primary);
}
.override-input:focus {
  outline: none;
  border-color: var(--accent-agent);
}
.override-key { width: 200px; }
.override-value { flex: 1; min-width: 0; }
.override-eq {
  color: var(--text-muted);
  font-family: var(--font-mono);
}
.override-remove {
  width: 22px;
  height: 22px;
  padding: 0;
  background: transparent;
  border: 1px solid transparent;
  color: var(--text-muted);
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  border-radius: 3px;
}
.override-remove:hover {
  color: var(--status-error, #ef4444);
  border-color: var(--border-subtle);
}
.override-add {
  align-self: flex-start;
  padding: 4px 10px;
  background: var(--surface-1);
  color: var(--text-secondary);
  border: 1px dashed var(--border-subtle);
  border-radius: 3px;
  font-family: var(--font-sans);
  font-size: 11px;
  cursor: pointer;
}
.override-add:hover {
  background: var(--surface-2);
  color: var(--text-primary);
  border-style: solid;
}
</style>
