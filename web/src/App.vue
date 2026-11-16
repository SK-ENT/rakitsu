<script setup lang="ts">
import { nextTick, ref, onMounted, watch } from 'vue';
import { RunInspector } from './components/inspector';
import { VisualBuilder } from './components/builder';
import { DebugView } from './components/debugger';
import ChatView from './components/chat/ChatView.vue';
import SessionsView from './components/sessions/SessionsView.vue';
import WorkspaceDock from './components/workspace/WorkspaceDock.vue';
import RunLogPanel from './components/workspace/RunLogPanel.vue';
import RunTimelinePanel from './components/workspace/RunTimelinePanel.vue';
import type { WorkspacePreset, WorkspaceView } from './components/workspace/workspaceViews';
import WelcomeScreen from './components/WelcomeScreen.vue';
import LicenseAcceptance from './components/LicenseAcceptance.vue';
import type { DebugTreeNode } from './types';
import { useWorkspace } from './composables/useWorkspace';
import rakitsuMark from './assets/rakitsu-mark.svg';

// Typed ref for the VisualBuilder component so we can pull the current
// canvas YAML when the user switches to the Chat tab — see autoDetectChatConfig.
type BuilderExposed = {
  getCurrentCanvasYaml: () => string | null;
};

// Shared workspace state — config / workdir / env vars visible to all tabs.
const workspace = useWorkspace();

const currentView = ref<WorkspaceView>('builder');
const dock = ref<InstanceType<typeof WorkspaceDock> | null>(null);
const layoutRevision = ref(0);
const layoutPreset = ref<WorkspacePreset | null>(null);
function resetLayout() {
  layoutPreset.value = null;
  dock.value?.reset();
}
function chooseLayout(event: Event) {
  const select = event.target as HTMLSelectElement;
  const layout = select.value as WorkspacePreset;
  layoutPreset.value = layout;
  dock.value?.applyPreset(layout);
  layoutRevision.value++;
  select.value = '';
}
const serverVersion = ref('');
const showLicense = ref(false);
const showWelcome = ref(false);

interface PendingDemo {
  configId: string;
  query: string;
  breakpoints: { event_type: string; agent_name: string }[];
  envVars: Record<string, string>;
}
const pendingDemo = ref<PendingDemo | null>(null);

onMounted(async () => {
  workspace.initFromServer();
  try {
    const res = await fetch('/api/status');
    const data = await res.json();
    if (data.version) serverVersion.value = data.version;
    if (data.license_required && !localStorage.getItem('rakitsu-license-accepted')) {
      showLicense.value = true;
    } else if (!data.has_user_configs && !localStorage.getItem('rakitsu-welcome-dismissed')) {
      showWelcome.value = true;
    }
  } catch { /* ignore */ }
});

async function handleRunDemo(provider: string, model: string, envVars: Record<string, string>) {
  try {
    const res = await fetch(`/api/demo?provider=${encodeURIComponent(provider)}&model=${encodeURIComponent(model)}`);
    const data = await res.json();
    showWelcome.value = false;
    localStorage.setItem('rakitsu-welcome-dismissed', '1');
    currentView.value = 'debugger';
    await nextTick();
    pendingDemo.value = {
      configId: data.config_id,
      query: data.query,
      breakpoints: data.breakpoints,
      envVars,
    };
  } catch { /* ignore */ }
}

function handleDismissWelcome(dontShowAgain: boolean) {
  showWelcome.value = false;
  if (dontShowAgain) {
    localStorage.setItem('rakitsu-welcome-dismissed', '1');
  }
}

function handleLicenseAccepted() {
  showLicense.value = false;
  localStorage.setItem('rakitsu-license-accepted', '1');
  // After license, check if welcome should show
  if (!localStorage.getItem('rakitsu-welcome-dismissed')) {
    // Re-check has_user_configs
    fetch('/api/status').then(r => r.json()).then(data => {
      if (!data.has_user_configs) showWelcome.value = true;
    }).catch(() => {});
  }
}

function handleDemoConsumed() {
  pendingDemo.value = null;
}

// Pending run from builder — passed to DebugView on next mount
const pendingRun = ref<{ yaml: string; query: string; timeout: number; workdir: string; debug: boolean; envVars: Record<string, string> } | null>(null);

async function handleBuilderRun(yaml: string, query: string, timeout: number, workdir: string, debug: boolean, envVars: Record<string, string>) {
  // Builder handles runs in-place — no tab switch.
  // Only pass to debugger if user explicitly navigates there.
  pendingRun.value = { yaml, query, timeout, workdir, debug, envVars };
}

// Ref to the VisualBuilder component so autoDetectChatConfig can pull the
// current canvas YAML on tab switch without touching the Builder via events.
const builderRef = ref<BuilderExposed | null>(null);

// Switching to Chat auto-detects a config from the most relevant source:
//   1) Builder — if canvas has agents, upload current YAML as the config
//      (overwrites prior builder upload via project_id-based dedupe).
//   2) otherwise leave whatever the workspace already had (debug/session).
// When the user explicitly clicks from History, that flow sets workspace
// itself; we only auto-pull Builder when nothing more specific is active.
async function autoDetectChatConfig() {
  const src = workspace.currentSource.value;
  // If a non-builder source is active, respect it — the user got here via
  // debugger/history and probably wants to continue with that config.
  if (src === 'debug' || src === 'session') return;

  const yaml = builderRef.value?.getCurrentCanvasYaml?.() ?? null;
  if (!yaml) return; // empty canvas; leave workspace alone

  // Avoid re-uploading if the Builder YAML is unchanged from our last snapshot.
  if (!workspace.builderDrifted(yaml) && src === 'builder' && workspace.currentConfigId.value) {
    return;
  }
  await workspace.setInlineConfig(yaml, 'builder');
}

watch(
  () => currentView.value,
  (view) => {
    if (view === 'chat') autoDetectChatConfig();
  },
);

function handleRunConsumed() {
  pendingRun.value = null;
}

// Inspect in Builder — load session config into canvas
interface InspectData { yaml: string; projectId?: string }
const pendingInspect = ref<InspectData | null>(null);

function handleInspectInBuilder(data: InspectData) {
  pendingInspect.value = data;
  currentView.value = 'builder';
}

// Drill-down from builder → advanced debugger focused on specific agent
const drillDownAgent = ref<string | null>(null);
const drillDownTreeRoots = ref<DebugTreeNode[] | null>(null);

async function handleDrillDown(agentName: string, treeRoots?: DebugTreeNode[]) {
  currentView.value = 'debugger';
  await nextTick();
  drillDownTreeRoots.value = treeRoots ?? null;
  drillDownAgent.value = agentName;
}

function handleFocusConsumed() {
  drillDownAgent.value = null;
  drillDownTreeRoots.value = null;
}

// Sessions tab Inspect viewer fires these when the user
// picks a per-session action. Each one boils down to "navigate + state".
// Fork reuses handleSessionsResume — it also yields a new live chat session
// whose only follow-up is "open it in the Chat tab".
async function handleSessionsResume(meta: { id: string }) {
  await workspace.refreshChatSessions();
  workspace.setActiveChatSession(meta.id);
  currentView.value = 'chat';
}
function handleSessionsClone(data: { yaml: string; projectId?: string }) {
  // Reuses the existing pendingInspect → VisualBuilder load path.
  pendingInspect.value = data;
  currentView.value = 'builder';
}
async function handleSessionsRerun(_newSessionId: string) {
  // Re-run already starts the session on the server. Switch to Debugger
  // so the user can attach / watch the live run.
  void _newSessionId;
  currentView.value = 'debugger';
}
// Debug action: replay a past session in the Debugger's History mode.
// DebugView consumes the pending id via its pendingHistorySession watcher.
const pendingHistorySession = ref<string | null>(null);
function handleSessionsDebug(sessionId: string) {
  pendingHistorySession.value = sessionId;
  currentView.value = 'debugger';
}
</script>

<template>
  <div class="app">
    <nav class="app-nav">
      <div class="nav-brand">
        <img :src="rakitsuMark" class="brand-mark" alt="Rakitsu" />
        <span class="brand-text">Rakitsu</span>
        <span v-if="serverVersion" class="brand-version">{{ serverVersion }}</span>
      </div>

      <div class="nav-status">
        <select class="reset-layout" aria-label="Workspace layout" @change="chooseLayout">
          <option value="" disabled selected>Layouts</option>
          <option value="debug">Debug</option>
          <option value="replay">Replay</option>
        </select>
        <button class="reset-layout" type="button" @click="resetLayout">Reset layout</button>
        <span class="status-item">
          <span class="status-led"></span>
          Ready
        </span>
      </div>
    </nav>
    
    <LicenseAcceptance
      v-if="showLicense"
      @accepted="handleLicenseAccepted"
    />

    <WelcomeScreen
      v-if="showWelcome"
      @run-demo="handleRunDemo"
      @dismiss="handleDismissWelcome"
    />

    <main class="app-main">
      <WorkspaceDock ref="dock" v-model="currentView">
        <template #builder="{ visible }">
          <KeepAlive>
            <VisualBuilder
              v-if="visible"
              ref="builderRef"
              class="app-tab"
              :pending-inspect="pendingInspect"
              :keyboard-active="currentView === 'builder'"
              @run-started="handleBuilderRun"
              @inspect-consumed="pendingInspect = null"
              @drill-down="handleDrillDown"
            />
          </KeepAlive>
        </template>
        <template #inspector="{ visible }">
          <KeepAlive>
            <RunInspector v-if="visible" class="app-tab" />
          </KeepAlive>
        </template>
        <template #debugger="{ visible }">
          <KeepAlive>
            <DebugView
              v-if="visible"
              class="app-tab"
              :layout-revision="layoutRevision"
              :follow-timeline="layoutPreset !== null"
              :pending-run="pendingRun"
              :pending-demo="pendingDemo"
              :pending-history-session="pendingHistorySession"
              :focus-agent="drillDownAgent"
              :focus-tree-roots="drillDownTreeRoots"
              @run-consumed="handleRunConsumed"
              @demo-consumed="handleDemoConsumed"
              @history-consumed="pendingHistorySession = null"
              @inspect-in-builder="handleInspectInBuilder"
              @focus-consumed="handleFocusConsumed"
              @open-builder="currentView = 'builder'"
            />
          </KeepAlive>
        </template>
        <template #sessions="{ visible }">
          <KeepAlive>
            <SessionsView
              v-if="visible"
              class="app-tab"
              @resume="handleSessionsResume"
              @clone="handleSessionsClone"
              @rerun="handleSessionsRerun"
              @fork="handleSessionsResume"
              @debug="handleSessionsDebug"
            />
          </KeepAlive>
        </template>
        <template #chat="{ visible }">
          <KeepAlive>
            <ChatView v-if="visible" class="app-tab" />
          </KeepAlive>
        </template>
        <template #run-log="{ visible }">
          <KeepAlive>
            <RunLogPanel v-if="visible" class="app-tab" :replay="layoutPreset === 'replay'" :follow-run="layoutPreset === 'debug'" />
          </KeepAlive>
        </template>
        <template #run-timeline="{ visible }">
          <KeepAlive>
            <RunTimelinePanel v-if="visible" class="app-tab" />
          </KeepAlive>
        </template>
      </WorkspaceDock>
    </main>
  </div>
</template>

<style>
@import './styles/theme.css';

/* Global styles */
* {
  box-sizing: border-box;
}

body {
  margin: 0;
  font-family: var(--font-sans);
  background: var(--surface-0);
  color: var(--text-primary);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

#app {
  width: 100vw;
  height: 100vh;
}
</style>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--surface-0);
}

.app-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
  height: 44px;
  background: var(--surface-1);
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}

.nav-brand {
  display: flex;
  align-items: center;
  gap: 8px;
}

.brand-mark {
  width: 16px;
  height: 16px;
}

.brand-text {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: -0.3px;
  color: var(--text-primary);
}

.brand-version {
  font-size: 10px;
  font-weight: 400;
  font-family: var(--font-mono);
  color: var(--text-muted);
  margin-left: 6px;
  padding: 1px 6px;
  background: var(--surface-3);
  border-radius: 2px;
}

.nav-status {
  display: flex;
  align-items: center;
  gap: 16px;
}

.reset-layout {
  padding: 5px 10px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--node-radius);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}

select.reset-layout option { background: var(--surface-2); }
.reset-layout:hover { background: var(--surface-3); }
.reset-layout:focus-visible { outline: 2px solid var(--accent-agent); }

.status-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.status-led {
  width: var(--led-size);
  height: var(--led-size);
  background: var(--status-running);
  border-radius: 50%;
  box-shadow: 0 0 4px rgba(34, 197, 94, 0.5);
}

.app-main {
  flex: 1;
  overflow: hidden;
  position: relative;
  display: flex;
  flex-direction: column;
}

/* Each visible view fills its pane. */
.app-tab {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
</style>
