<script setup lang="ts">
import { ref, watch } from 'vue';
import type { CanvasMode } from './VisualBuilder.vue';

const RUNBAR_STORAGE_KEY = 'rakitsu-runbar-state';
// Env-var values are never persisted to localStorage — only the names are
// remembered, so values must be re-typed. A name-based filter (the old
// /key|token|secret|.../ check) let secrets under names like AUTH or DB_PWD
// through.

const props = defineProps<{
  canvasMode: CanvasMode;
  selfUrl: string;
  initialWorkdir?: string;
  totalTokenCount?: number;
  isPaused?: boolean;
  pausedAt?: string;
  showResults?: boolean;
  resetTrigger?: number; // incremented by parent on clear/new project
  hasBreakpoints?: boolean; // true when breakpoints are set — auto-enables debug
  execView?: 'tree' | 'graph'; // current exec layout view
}>();

const emit = defineEmits<{
  run: [query: string, timeout: number, workdir: string, envVars: Record<string, string>];
  debug: [query: string, timeout: number, workdir: string, envVars: Record<string, string>];
  stop: [];
  'attach-debugger': [];
  'clear-results': [];
  'expand-all': [];
  'collapse-all': [];
  'set-exec-view': [view: 'tree' | 'graph'];
  pause: [];
  resume: [];
  step: [];
  'update:query': [value: string];
  'open-tokenizer': [];
}>();

// --- Restore persisted state ---
function loadRunBarState(): { query: string; timeout: number; workdir: string; envVars: { key: string; value: string }[] } | null {
  try {
    const raw = localStorage.getItem(RUNBAR_STORAGE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch { return null; }
}

const saved = loadRunBarState();
const query = ref(saved?.query ?? '');
const timeout = ref(saved?.timeout ?? 300);
const workdir = ref(saved?.workdir ?? '');
const envVars = ref<{ key: string; value: string }[]>(saved?.envVars ?? []);
const showAdvanced = ref(false);
const browsingDir = ref('');
const browseDirs = ref<string[]>([]);

// Sync initial workdir from VisualBuilder (set from /api/workdir)
watch(() => props.initialWorkdir, val => {
  if (val && !workdir.value) workdir.value = val;
}, { immediate: true });

// Emit query changes for tokenizer
watch(query, val => emit('update:query', val), { immediate: true });

// Auto-persist RunBar state (debounced)
let saveTimer: ReturnType<typeof setTimeout> | null = null;
function persistRunBarState() {
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    try {
      localStorage.setItem(RUNBAR_STORAGE_KEY, JSON.stringify({
        query: query.value,
        timeout: timeout.value,
        workdir: workdir.value,
        envVars: envVars.value
          .filter(e => e.key.trim())
          .map(e => ({ key: e.key, value: '' })),
      }));
    } catch { /* ignore */ }
  }, 1000);
}
watch([query, timeout, workdir, envVars], persistRunBarState, { deep: true });

// Clear on new project (parent increments resetTrigger)
watch(() => props.resetTrigger, () => {
  query.value = '';
  timeout.value = 300;
  workdir.value = '';
  envVars.value = [];
  showAdvanced.value = false;
  try { localStorage.removeItem(RUNBAR_STORAGE_KEY); } catch { /* ignore */ }
});

function buildEnvMap(): Record<string, string> {
  const map: Record<string, string> = {};
  for (const e of envVars.value) {
    if (e.key.trim()) map[e.key.trim()] = e.value;
  }
  return map;
}

function handleRun() {
  if (!query.value.trim()) return;
  // Auto-enable debug mode when breakpoints are set
  if (props.hasBreakpoints) {
    emit('debug', query.value.trim(), timeout.value, workdir.value.trim(), buildEnvMap());
  } else {
    emit('run', query.value.trim(), timeout.value, workdir.value.trim(), buildEnvMap());
  }
}


async function browseWorkdir() {
  const startDir = workdir.value || '';
  try {
    const res = await fetch(`http://${props.selfUrl}/api/browse?path=${encodeURIComponent(startDir)}`);
    const data = await res.json();
    browsingDir.value = data.path || '/';
    browseDirs.value = data.dirs || [];
  } catch { /* ignore */ }
}

async function browseInto(dir: string) {
  const newPath = browsingDir.value.endsWith('/')
    ? browsingDir.value + dir
    : browsingDir.value + '/' + dir;
  try {
    const res = await fetch(`http://${props.selfUrl}/api/browse?path=${encodeURIComponent(newPath)}`);
    const data = await res.json();
    browsingDir.value = data.path || newPath;
    browseDirs.value = data.dirs || [];
  } catch { /* ignore */ }
}

async function browseParent() {
  const parts = browsingDir.value.split('/').filter(Boolean);
  parts.pop();
  const parent = '/' + parts.join('/');
  try {
    const res = await fetch(`http://${props.selfUrl}/api/browse?path=${encodeURIComponent(parent)}`);
    const data = await res.json();
    browsingDir.value = data.path || parent;
    browseDirs.value = data.dirs || [];
  } catch { /* ignore */ }
}

function selectBrowseDir() {
  workdir.value = browsingDir.value;
  browsingDir.value = '';
}
</script>

<template>
  <!-- Results banner: shown in design mode after run completes -->
  <div v-if="canvasMode === 'design' && showResults" class="run-bar run-bar--results">
    <span class="rbar-results-badge">RUN COMPLETE</span>
    <span class="rbar-status-text">CLICK NODES TO INSPECT RESULTS</span>
    <span v-if="totalTokenCount && totalTokenCount > 0" class="rbar-tokens">
      {{ totalTokenCount >= 1000 ? (totalTokenCount / 1000).toFixed(1) + 'k' : totalTokenCount }} tokens
    </span>
    <div class="rbar-view-toggle">
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'tree' }"
        @click="emit('set-exec-view', 'tree')"
        title="Call tree layout"
      >Tree</button>
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'graph' }"
        @click="emit('set-exec-view', 'graph')"
        title="Auto graph layout"
      >Graph</button>
    </div>
    <button class="rbar-btn rbar-btn--tree" @click="emit('expand-all')" title="Expand all call tree nodes">Expand All</button>
    <button class="rbar-btn rbar-btn--tree" @click="emit('collapse-all')" title="Collapse all call tree nodes">Collapse All</button>
    <button class="rbar-btn rbar-btn--clear" @click="emit('clear-results')">Clear Results</button>
  </div>

  <!-- Design mode: persistent run form -->
  <div v-if="canvasMode === 'design'" class="run-bar run-bar--design">
    <div class="run-bar-main">
      <div class="run-bar-prompt">
        <textarea
          v-model="query"
          class="rbar-query"
          placeholder="Enter prompt... (Cmd+Enter to run)"
          rows="2"
          @keydown.meta.enter="handleRun"
          @keydown.ctrl.enter="handleRun"
        />
      </div>
      <div class="run-bar-controls">
        <div class="run-bar-inline">
          <label class="rbar-label">Timeout</label>
          <input v-model.number="timeout" type="number" class="rbar-timeout" min="10" max="3600" />
          <span class="rbar-unit">s</span>
        </div>
        <button
          class="rbar-btn rbar-btn--tokenizer"
          @click="emit('open-tokenizer')"
          title="Prompt tokenizer — see token breakdown"
        >Tk</button>
        <button
          class="rbar-btn"
          :class="hasBreakpoints ? 'rbar-btn--debug' : 'rbar-btn--run'"
          :disabled="!query.trim()"
          @click="handleRun"
          :title="hasBreakpoints ? 'Run with debugger (breakpoints active) — Cmd+Enter' : 'Run (Cmd+Enter)'"
        >
          {{ hasBreakpoints ? 'Debug' : 'Run' }}
        </button>
        <button
          class="rbar-btn rbar-btn--adv"
          :class="{ active: showAdvanced }"
          @click="showAdvanced = !showAdvanced"
          title="Workspace & environment variables"
        >
          ⚙ {{ showAdvanced ? '▲' : '▼' }}
        </button>
      </div>
    </div>

    <!-- Advanced section: workspace + env vars -->
    <div v-if="showAdvanced" class="run-bar-adv">
      <div class="adv-field">
        <label class="adv-label">Workspace</label>
        <div class="adv-workdir-row">
          <input
            v-model="workdir"
            type="text"
            class="adv-workdir"
            placeholder="/path/to/project"
          />
          <button class="rbar-btn rbar-btn--sm" @click="browseWorkdir" title="Browse folders">...</button>
        </div>
        <div v-if="!workdir.trim() && !browsingDir" class="adv-hint">
          No workspace — tools run in server's current directory
        </div>
        <!-- Directory browser -->
        <div v-if="browsingDir" class="adv-dir-browser">
          <div class="dir-browser-header">
            <span class="dir-path">{{ browsingDir }}</span>
            <button class="rbar-btn rbar-btn--sm" @click="browseParent">..</button>
            <button class="rbar-btn rbar-btn--sm rbar-btn--select" @click="selectBrowseDir">Select</button>
            <button class="rbar-btn rbar-btn--sm" @click="browsingDir = ''">Cancel</button>
          </div>
          <div class="dir-list">
            <div v-for="d in browseDirs" :key="d" class="dir-item" @click="browseInto(d)">
              {{ d }}/
            </div>
            <div v-if="browseDirs.length === 0" class="dir-empty">No subdirectories</div>
          </div>
        </div>
      </div>

      <div class="adv-field">
        <label class="adv-label">Environment Variables</label>
        <div v-for="(env, i) in envVars" :key="i" class="adv-env-row">
          <input v-model="env.key" type="text" class="adv-env-key" placeholder="VAR_NAME" />
          <input v-model="env.value" type="password" class="adv-env-val" placeholder="value" />
          <button class="adv-env-del" @click="envVars.splice(i, 1)">×</button>
        </div>
        <button class="rbar-btn rbar-btn--sm" @click="envVars.push({ key: '', value: '' })">+ Add Variable</button>
      </div>
    </div>
  </div>

  <!-- Monitor mode: stop + attach + live stats -->
  <div v-else-if="canvasMode === 'monitor'" class="run-bar run-bar--monitor">
    <button class="rbar-btn rbar-btn--stop" @click="emit('stop')">⏹ Stop</button>
    <button class="rbar-btn rbar-btn--attach" @click="emit('attach-debugger')">🐛 Attach Debugger</button>
    <span class="rbar-status-text">RUNNING</span>
    <span v-if="totalTokenCount && totalTokenCount > 0" class="rbar-tokens">
      {{ totalTokenCount >= 1000 ? (totalTokenCount / 1000).toFixed(1) + 'k' : totalTokenCount }} tokens
    </span>
    <div class="rbar-view-toggle">
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'tree' }"
        @click="emit('set-exec-view', 'tree')"
        title="Call tree layout"
      >Tree</button>
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'graph' }"
        @click="emit('set-exec-view', 'graph')"
        title="Auto graph layout"
      >Graph</button>
    </div>
  </div>

  <!-- Debug mode: controls -->
  <div v-else-if="canvasMode === 'debug'" class="run-bar run-bar--debug">
    <button class="rbar-btn rbar-btn--stop" @click="emit('stop')">⏹ Stop</button>
    <template v-if="isPaused">
      <button class="rbar-btn rbar-btn--resume" @click="emit('resume')">▶ Resume</button>
      <button class="rbar-btn rbar-btn--step" @click="emit('step')">⏭ Step</button>
      <span class="rbar-status-text rbar-status-paused">
        ⏸ Paused<template v-if="pausedAt"> — {{ pausedAt }}</template>
      </span>
    </template>
    <template v-else>
      <button class="rbar-btn rbar-btn--pause" @click="emit('pause')">⏸ Pause</button>
      <span class="rbar-status-text">Debug mode</span>
    </template>
    <div class="rbar-view-toggle">
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'tree' }"
        @click="emit('set-exec-view', 'tree')"
        title="Call tree layout"
      >Tree</button>
      <button
        class="rbar-btn rbar-btn--toggle"
        :class="{ active: execView === 'graph' }"
        @click="emit('set-exec-view', 'graph')"
        title="Auto graph layout"
      >Graph</button>
    </div>
  </div>

  <!-- Replay mode: controls -->
  <div v-else-if="canvasMode === 'replay'" class="run-bar run-bar--replay">
    <span class="rbar-status-text">Replay mode</span>
    <!-- TODO Phase 4: ReplayBar timeline slider -->
  </div>
</template>

<style scoped>
/* === Shared === */
.run-bar {
  display: flex;
  flex-direction: column;
  background: var(--surface-1);
  border-top: 1px solid var(--border-subtle);
  padding: 8px 16px;
  flex-shrink: 0;
}

/* === Design mode === */
.run-bar--design { gap: 6px; }

.run-bar-main { display: flex; gap: 10px; align-items: flex-start; }
.run-bar-prompt { flex: 1; min-width: 0; }

.rbar-query {
  width: 100%;
  resize: none;
  background: var(--surface-0);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  color: var(--text-primary);
  font-size: 13px;
  padding: 7px 10px;
  line-height: 1.45;
  box-sizing: border-box;
  font-family: inherit;
}
.rbar-query:focus { outline: none; border-color: var(--border-focus); }
.rbar-query::placeholder { color: var(--text-muted); }

.run-bar-controls { display: flex; flex-direction: column; gap: 6px; align-items: flex-end; flex-shrink: 0; }
.run-bar-inline { display: flex; align-items: center; gap: 6px; }

.rbar-label { font-size: 10px; color: var(--text-secondary); font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.04em; }

.rbar-timeout {
  width: 64px;
  background: var(--surface-0);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  color: var(--text-primary);
  font-size: 13px;
  font-family: var(--font-mono);
  padding: 4px 6px;
  text-align: right;
}
.rbar-timeout:focus { outline: none; border-color: var(--border-focus); }
.rbar-unit { font-size: 12px; color: var(--text-muted); font-family: var(--font-mono); }

/* === Buttons === */
.rbar-btn {
  padding: 6px 12px;
  border-radius: var(--node-radius);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all 0.15s;
  white-space: nowrap;
}
.rbar-btn:disabled { opacity: 0.4; cursor: not-allowed; }

.rbar-btn--tokenizer {
  background: var(--surface-3);
  color: var(--text-secondary);
  border: 1px solid var(--border-default);
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 600;
  padding: 5px 8px;
}
.rbar-btn--tokenizer:hover { background: var(--surface-4); color: var(--accent-skill); }

.rbar-btn--run { background: var(--status-running); color: #000; }
.rbar-btn--run:hover:not(:disabled) { background: #1aad4a; }

.rbar-btn--debug { background: var(--accent-agent); color: #fff; }
.rbar-btn--debug:hover:not(:disabled) { background: #4a7ad8; }


.rbar-btn--adv { background: var(--surface-3); color: var(--text-muted); border: 1px solid var(--border-default); padding: 5px 10px; font-size: 11px; }
.rbar-btn--adv:hover { border-color: var(--border-focus); color: var(--text-primary); }
.rbar-btn--adv.active { border-color: var(--accent-agent); color: var(--accent-agent); }

.rbar-btn--sm { padding: 4px 10px; font-size: 11px; background: var(--surface-3); color: var(--text-secondary); border: 1px solid var(--border-default); border-radius: var(--node-radius); }
.rbar-btn--sm:hover { border-color: var(--border-focus); color: var(--text-primary); }
.rbar-btn--select { color: var(--status-running); border-color: var(--status-running); }

.rbar-btn--stop { background: var(--status-error); color: #fff; }
.rbar-btn--stop:hover { background: #dc2626; }

.rbar-btn--attach { background: var(--accent-agent); color: #fff; }
.rbar-btn--attach:hover { background: #4a7ad8; }

.rbar-btn--pause { background: var(--status-paused); color: #000; }
.rbar-btn--pause:hover { background: #d97706; }

.rbar-btn--resume { background: var(--status-running); color: #000; }
.rbar-btn--resume:hover { background: #1aad4a; }

.rbar-btn--step { background: var(--accent-agent); color: #fff; }
.rbar-btn--step:hover { background: #4a7ad8; }

.rbar-status-paused { color: var(--status-paused); font-weight: 500; }

/* === Advanced section === */
.run-bar-adv { display: flex; flex-direction: column; gap: 10px; padding-top: 8px; border-top: 1px solid var(--border-subtle); margin-top: 4px; }
.adv-field { display: flex; flex-direction: column; gap: 4px; }
.adv-label { font-size: 10px; color: var(--text-secondary); font-weight: 500; font-family: var(--font-mono); text-transform: uppercase; letter-spacing: 0.04em; }
.adv-workdir-row { display: flex; gap: 6px; align-items: center; }

.adv-workdir { flex: 1; background: var(--surface-0); border: 1px solid var(--border-default); border-radius: var(--node-radius); color: var(--text-primary); font-size: 12px; font-family: var(--font-mono); padding: 5px 8px; }
.adv-workdir:focus { outline: none; border-color: var(--border-focus); }
.adv-hint { font-size: 11px; color: var(--text-muted); }

.adv-dir-browser { background: var(--surface-0); border: 1px solid var(--border-default); border-radius: var(--node-radius); margin-top: 4px; }
.dir-browser-header { display: flex; align-items: center; gap: 6px; padding: 6px 8px; border-bottom: 1px solid var(--border-subtle); }
.dir-path { flex: 1; font-size: 11px; font-family: var(--font-mono); color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dir-list { max-height: 120px; overflow-y: auto; padding: 4px 0; }
.dir-item { padding: 4px 12px; font-size: 12px; font-family: var(--font-mono); color: var(--text-secondary); cursor: pointer; }
.dir-item:hover { background: var(--surface-4); color: var(--text-primary); }
.dir-empty { padding: 8px 12px; font-size: 12px; color: var(--text-muted); }

.adv-env-row { display: flex; gap: 6px; align-items: center; }
.adv-env-key { width: 140px; background: var(--surface-0); border: 1px solid var(--border-default); border-radius: var(--node-radius); color: var(--text-primary); font-size: 12px; font-family: var(--font-mono); padding: 4px 6px; }
.adv-env-val { flex: 1; background: var(--surface-0); border: 1px solid var(--border-default); border-radius: var(--node-radius); color: var(--text-primary); font-size: 12px; padding: 4px 6px; }
.adv-env-key:focus, .adv-env-val:focus { outline: none; border-color: var(--border-focus); }
.adv-env-del { background: none; border: none; color: var(--text-muted); font-size: 16px; cursor: pointer; padding: 0 4px; line-height: 1; }
.adv-env-del:hover { color: var(--status-error); }

/* === Results banner === */
.run-bar--results { flex-direction: row; align-items: center; gap: 12px; min-height: 36px; background: var(--surface-2); border-bottom: 1px solid var(--border-subtle); padding: 6px 16px; }
.rbar-results-badge { font-size: 9px; font-family: var(--font-mono); background: rgba(34,197,94,0.15); color: var(--status-running); padding: 2px 6px; border-radius: 2px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; }
.rbar-btn--tree { background: var(--surface-3); color: var(--text-secondary); border: 1px solid var(--border-default); font-size: 11px; padding: 3px 8px; }
.rbar-btn--tree:hover { border-color: var(--border-focus); color: var(--text-primary); }
.rbar-view-toggle { display: flex; gap: 0; }
.rbar-btn--toggle { background: var(--surface-3); color: var(--text-muted); border: 1px solid var(--border-default); font-size: 11px; padding: 3px 10px; border-radius: 0; }
.rbar-btn--toggle:first-child { border-radius: var(--node-radius) 0 0 var(--node-radius); }
.rbar-btn--toggle:last-child { border-radius: 0 var(--node-radius) var(--node-radius) 0; border-left: none; }
.rbar-btn--toggle:hover { color: var(--text-primary); }
.rbar-btn--toggle.active { background: var(--accent-agent); color: #fff; border-color: var(--accent-agent); }
.rbar-btn--clear { background: var(--surface-3); color: var(--text-secondary); border: 1px solid var(--border-default); margin-left: auto; }
.rbar-btn--clear:hover { border-color: var(--border-focus); color: var(--text-primary); }

/* === Monitor/Debug/Replay modes === */
.run-bar--monitor, .run-bar--debug, .run-bar--replay { flex-direction: row; align-items: center; gap: 12px; min-height: 40px; }
.rbar-status-text { font-size: 12px; font-family: var(--font-mono); color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.04em; }
.rbar-tokens { font-size: 11px; font-family: var(--font-mono); color: var(--text-secondary); background: var(--surface-3); padding: 2px 8px; border-radius: var(--node-radius); border: 1px solid var(--border-default); }
</style>
