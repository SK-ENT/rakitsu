<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, onActivated, onDeactivated, nextTick, defineAsyncComponent } from 'vue';
import { useEventStream, useSessionHistory, useDebugTree } from '../../composables';
import { recordedReplayCursor } from '../../composables/recordedReplayCursor';
import { useDebugControl } from '../../composables/useDebugControl';
import { useAgentRun } from '../../composables/useAgentRun';
import type { DebugTreeNode, AgentEvent, SessionMeta } from '../../types';
import DetailPanel from './DetailPanel.vue';
import yaml from 'js-yaml';
import BreakpointBar from './BreakpointBar.vue';
import TokenReport from './TokenReport.vue';
import SessionPanel, { type HubSession } from './SessionPanel.vue';
import StyleSelector from './StyleSelector.vue';
import SessionPicker from './SessionPicker.vue';
import type { RuntimeSessionMeta } from '../../composables/useSessionList';
import ChatPanel from '../chat/ChatPanel.vue';
import ChatHistoryPanel from '../chat/ChatHistoryPanel.vue';
import { reconstructChatBlocksFromEvents } from '../../composables/reconstructChat';
import { useWorkspace } from '../../composables/useWorkspace';
import { getVisualizationStyle } from './visualizations';

// Shared workspace — tag configs with source='debug' when the debugger owns
// the run so the Chat tab knows to stay on this config.
const workspace = useWorkspace();

// Collapsible chat pane — bound to the chat session for the current
// workspace config if one exists. If not, the pane shows a Start button
// that posts /api/chat/start; the same session then appears in the Chat
// tab sidebar, so typing in either surface stays in sync.
const chatPaneOpen = ref(false);
const chatPaneStarting = ref(false);
const chatPaneError = ref<string | null>(null);

// Session pin. The pinned id is what
// every /api/debug/* call threads through as session_id. It's decoupled from
// workspace.currentConfigId — picking a session doesn't mutate the Builder
// tab's current config (click-is-intent). URL hash #debug/session/<id>
// restores the pin on reload.
const pickedSessionId = ref<string | null>(null);

function readSessionIdFromHash(): string | null {
  if (typeof window === 'undefined') return null;
  const m = /^#debug\/session\/([^/?&]+)/.exec(window.location.hash || '');
  return m && m[1] ? decodeURIComponent(m[1]) : null;
}

function writeSessionIdToHash(id: string | null) {
  if (typeof window === 'undefined') return;
  const want = id ? `#debug/session/${encodeURIComponent(id)}` : '';
  if ((window.location.hash || '') === want) return;
  // Use replaceState so the picker doesn't pollute browser back-history with
  // every selection.
  history.replaceState(null, '', window.location.pathname + window.location.search + want);
}

function onHashChange() {
  const id = readSessionIdFromHash();
  if (id !== pickedSessionId.value) pickedSessionId.value = id;
}

function onPickSession(s: RuntimeSessionMeta | null) {
  pickedSessionId.value = s?.id ?? null;
  writeSessionIdToHash(pickedSessionId.value);
  if (s) {
    // Chat sessions have an interactive surface; auto-open the chat pane
    // so the operator sees the transcript + input field without an extra
    // click. One-shot sessions don't benefit — they have no chat UI.
    if (s.mode === 'chat') {
      chatPaneOpen.value = true;
      // Refresh the chat session list so debuggerChatSession can resolve
      // the pinned id to a ChatSessionMeta (name, config_id, generating).
      void workspace.refreshChatSessions();
    }
  }
}
// Chat session bound to the chat pane in the Debug tab.
// Priority (Phase 2.5): the session pinned in the SessionPicker wins — that's
// the explicit user intent. Only fall back to the workspace.currentConfigId
// match when no pin is set, preserving pre-Phase-2 behavior for users who
// never touch the picker.
const debuggerChatSession = computed(() => {
  const pinned = pickedSessionId.value;
  if (pinned) {
    const hit = workspace.knownChatSessions.value.find((s) => s.id === pinned);
    if (hit) return hit;
  }
  const cfg = workspace.currentConfigId.value;
  if (!cfg) return null;
  return workspace.knownChatSessions.value.find((s) => s.config_id === cfg) ?? null;
});
async function toggleChatPane() {
  chatPaneOpen.value = !chatPaneOpen.value;
  if (chatPaneOpen.value) {
    await workspace.refreshChatSessions();
  }
}
async function startDebuggerChat() {
  const cfgId = workspace.currentConfigId.value;
  if (!cfgId) return;
  chatPaneStarting.value = true;
  chatPaneError.value = null;
  try {
    const res = await fetch('/api/chat/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        config_id: cfgId,
        workdir: workspace.workdir.value,
        env_vars: workspace.envVarsMap(),
      }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(body.error || 'failed to start chat');
    }
    await workspace.refreshChatSessions();
  } catch (e: unknown) {
    chatPaneError.value = (e as Error).message;
  } finally {
    chatPaneStarting.value = false;
  }
}
import { NODE_TYPE_TO_EVENT } from '../../types/checkpoints';
import './visualizations'; // trigger registration

import { builderReplayEvent } from '../../composables/builderRunFeed';

const props = defineProps<{
  layoutRevision?: number;
  followTimeline?: boolean;
  pendingRun?: { yaml: string; query: string; timeout: number; workdir: string; debug: boolean; envVars?: Record<string, string> } | null;
  pendingDemo?: { configId: string; query: string; breakpoints: { event_type: string; agent_name: string }[]; envVars: Record<string, string> } | null;
  pendingHistorySession?: string | null;
  focusAgent?: string | null;
  focusTreeRoots?: DebugTreeNode[] | null;
}>();

const emit = defineEmits<{
  'run-consumed': [];
  'demo-consumed': [];
  'history-consumed': [];
  'inspect-in-builder': [data: { yaml: string; projectId?: string }];
  'focus-consumed': [];
  'open-builder': [];
}>();

const isDemoRun = ref(false);
const showDemoHint = ref(false);
const showDemoCta = ref(false);

type DataMode = 'live' | 'history';

const viewMode = ref<string>(props.layoutRevision ? 'block-diagram' : 'tree');
watch(() => props.layoutRevision, () => {
  viewMode.value = 'block-diagram';
  if (dataMode.value === 'live' && !isConnected.value) connectLive();
});
const dataMode = ref<DataMode>('live');
const debugUrl = ref(typeof window !== 'undefined' ? window.location.host : 'localhost:8080');
const isWebRun = ref(false);
const hubSessions = ref<HubSession[]>([]);
const hubLoading = ref(false);
const selectedHubSession = ref<HubSession | null>(null);
const isHubMode = ref(false);
const sessionPanelOpen = ref(true);
const showTokenReport = ref(false);
let hubPollTimer: ReturnType<typeof setInterval> | null = null;

// Live mode
const { events: liveEvents, isConnected, error: liveError, connect, disconnect, clearEvents } = useEventStream();

// History mode
const {
  sessions, loading, error: historyError,
  selectedSession, sessionEvents,
  fetchSessions, loadSession, rerunSession, deleteSessions,
} = useSessionHistory();

// Debug control (breakpoints, pause/resume)
const debugUrlRef = ref(typeof window !== 'undefined' ? window.location.host : 'localhost:8080');
const hubSessionIdRef = computed(() => selectedHubSession.value?.id ?? null);
const {
  debugState, breakpoints: debugBreakpoints, isPaused,
  pausedAgent, pausedCheckpoint, pausedContext,
  debugMode, requestPause, setMode,
  fetchState: fetchDebugState, setBreakpoint, clearBreakpoint, clearAllBreakpoints, resetDebugState,
  resume: debugResume, stepIn, stepOut, runUntil, setParams, clearParams, replay, rerunFromStep,
  handleEvent: handleDebugEvent,
  pendingUserInput, answerUserInput,
} = useDebugControl(debugUrlRef, hubSessionIdRef, pickedSessionId);

// Agent runner (web-launched runs)
const selfUrl = ref(typeof window !== 'undefined' ? window.location.host : 'localhost:8080');
const {
  runStatus, runError: webRunError, elapsedMs: webElapsedMs, sessionId: runSessionId, startRun,
} = useAgentRun(selfUrl);

// Direct ref — bypasses computed caching that breaks reactivity on array mutations
const debugEvents = ref<AgentEvent[]>([]);

// Debug tree
const {
  treeRoots, activeNodeIds, activeAgents,
  selectedNode, tokenTotals, visibleUpTo, renderKey, findNode, reset,
} = useDebugTree(debugEvents);

// Current session ID — live run or selected history session
const currentSessionId = computed(() => {
  if (dataMode.value === 'live') return runSessionId.value || selectedHubSession.value?.id || null;
  return selectedSession.value?.id || null;
});

// History-mode chat replay: when the user loads a past chat session in
// History mode, we can't attach a live WebSocket (the session is gone), but
// the jsonl event log has CHAT_TURN_START/END events that let us rebuild
// the visible conversation. See composables/reconstructChat.ts.
const isHistoryChatSession = computed(() => {
  if (dataMode.value !== 'history') return false;
  const q = selectedSession.value?.query ?? '';
  return q === '(interactive session)';
});
const historyChatBlocks = computed(() => {
  if (!isHistoryChatSession.value) return [];
  return reconstructChatBlocksFromEvents(debugEvents.value ?? []);
});

// Auto-open the chat pane when the user lands on a history chat session —
// the reason they clicked it is usually to see the conversation. Don't close
// the pane when the flag flips off: the user may have just navigated away.
watch(isHistoryChatSession, (on) => {
  if (on) chatPaneOpen.value = true;
});

// Breakpointed agent names — for highlighting in tree
const breakpointedAgents = computed(() => {
  const set = new Set<string>();
  for (const bp of debugBreakpoints.value) {
    if (bp.agent_name && bp.agent_name !== '*') set.add(bp.agent_name);
  }
  return set;
});

// Parsed session config — single source for all debug config data
// Priority: previewConfigYaml (builder/rerun preview) → selected history session → any session with config
const parsedSessionConfig = computed(() => {
  const rawYaml = previewConfigYaml.value
    || selectedSession.value?.config_yaml
    || sessions.value.find(s => s.config_yaml)?.config_yaml;
  if (!rawYaml) return null;
  try {
    return yaml.load(rawYaml) as Record<string, unknown>;
  } catch { return null; }
});

const allConfigTools = computed<string[]>(() => {
  const tools = (parsedSessionConfig.value?.tools || []) as { name?: string }[];
  return tools.map(t => t.name).filter(Boolean) as string[];
});

const configProviderMap = computed<Record<string, Record<string, string>>>(() => {
  const settings = parsedSessionConfig.value?.settings as Record<string, unknown>;
  return (settings?.providers || {}) as Record<string, Record<string, string>>;
});

const configProviderNames = computed(() => Object.keys(configProviderMap.value));

const configDefaults = computed(() => {
  const settings = parsedSessionConfig.value?.settings as Record<string, unknown>;
  return (settings?.defaults || {}) as Record<string, unknown>;
});

// Full agent configs from session YAML — used as NodeEditor node data
const configAgentMap = computed<Record<string, Record<string, unknown>>>(() => {
  const agents = (parsedSessionConfig.value?.agents || []) as Record<string, unknown>[];
  const map: Record<string, Record<string, unknown>> = {};
  for (const a of agents) {
    if (a.name) map[a.name as string] = a;
  }
  return map;
});

// Orchestrator configs from session YAML — keyed by name
const configOrchestratorMap = computed<Record<string, Record<string, unknown>>>(() => {
  const cfg = parsedSessionConfig.value;
  if (!cfg) return {};
  const map: Record<string, Record<string, unknown>> = {};
  const orch = cfg.orchestrator as Record<string, unknown> | undefined;
  if (orch?.name) map[orch.name as string] = orch;
  const orchs = (cfg.orchestrators || []) as Record<string, unknown>[];
  for (const o of orchs) {
    if (o.name) map[o.name as string] = o;
  }
  return map;
});

// --- Playback controls (history mode) ---
const NODE_EVENTS = new Set(['AGENT_START', 'AGENT_END', 'TOOL_CALL_START', 'TOOL_CALL_END', 'PIPELINE_STEP_START', 'PIPELINE_STEP_END', 'THOUGHT_START', 'THOUGHT_END']);

const playbackIdx = computed(() => {
  const v = visibleUpTo.value;
  return v >= 0 ? v : (debugEvents.value ?? []).length - 1;
});

// Run is complete when we have events and the last one is PIPELINE_END, EXECUTION_COMPLETE, or ERROR
const isRunComplete = computed(() => {
  const evts = debugEvents.value ?? [];
  if (evts.length === 0) return false;
  const last = evts[evts.length - 1]!;
  return last.event_type === 'PIPELINE_END' || last.event_type === 'EXECUTION_COMPLETE' || last.event_type === 'ERROR';
});

// Show playback controls in history mode OR when a live run has completed
const showPlayback = computed(() => {
  return (debugEvents.value ?? []).length > 0 && (dataMode.value === 'history' || isRunComplete.value);
});

const isPlaying = ref(false);
let playTimer: ReturnType<typeof setInterval> | null = null;
const loadedReplayId = ref<string | null>(null);

function pausePlayback() {
  if (playTimer !== null) clearInterval(playTimer);
  playTimer = null;
  isPlaying.value = false;
}

function setPlaybackPosition(index: number) {
  visibleUpTo.value = index;
  const id = loadedReplayId.value;
  const event = debugEvents.value[index];
  if (dataMode.value === 'history' && id && event) recordedReplayCursor.set(id, event.id);
}

watch([dataMode, loadedReplayId, () => recordedReplayCursor.get(loadedReplayId.value ?? '')], () => {
  const id = loadedReplayId.value;
  if (dataMode.value !== 'history' || !id || !sessionEvents.value.length) return;
  const eventId = recordedReplayCursor.get(id);
  const index = sessionEvents.value.findIndex(event => event.id === eventId);
  if (index < 0) {
    recordedReplayCursor.set(id, sessionEvents.value[sessionEvents.value.length - 1]!.id);
  } else if (index !== playbackIdx.value) {
    pausePlayback();
    visibleUpTo.value = index;
  }
});

function playbackStepEvent(dir: number) {
  const max = (debugEvents.value ?? []).length - 1;
  setPlaybackPosition(Math.max(0, Math.min(max, playbackIdx.value + dir)));
}

function playbackStepNode(dir: number) {
  const evts = debugEvents.value ?? [];
  const max = evts.length - 1;
  let idx = playbackIdx.value;
  if (dir > 0) {
    for (let i = idx + 1; i <= max; i++) {
      if (NODE_EVENTS.has(evts[i]?.event_type ?? '')) { idx = i; break; }
      if (i === max) idx = max;
    }
  } else {
    for (let i = idx - 1; i >= 0; i--) {
      if (NODE_EVENTS.has(evts[i]?.event_type ?? '')) { idx = i; break; }
      if (i === 0) idx = 0;
    }
  }
  setPlaybackPosition(idx);
}

function togglePlay() {
  if (isPlaying.value) {
    pausePlayback();
  } else {
    isPlaying.value = true;
    playTimer = setInterval(() => {
      const max = (debugEvents.value ?? []).length - 1;
      const next = playbackIdx.value + 1;
      if (next > max) {
        pausePlayback();
        return;
      }
      setPlaybackPosition(next);
    }, 150);
  }
}

// Dynamic visualization style
const activeStyleComponent = computed(() => {
  const style = getVisualizationStyle(viewMode.value);
  if (!style) return null;
  if (typeof style.component === 'function') {
    return defineAsyncComponent(style.component as () => Promise<{ default: any }>);
  }
  return style.component;
});

// Attached session = persistent debug connection (SSE + debug controls)
const attachedSession = ref<HubSession | null>(null);

// Keep debugUrlRef in sync with the input
watch(debugUrl, (v) => { debugUrlRef.value = v; });

// Sync live events to debugEvents + forward to debug controller
// Must create a NEW array ref so useDebugTree's watcher detects the change.
// (liveEvents and debugEvents are different refs — mutating one's array via
// the other's proxy doesn't trigger watchers on the second ref.)
//
// An explicit session wins. Otherwise follow the latest web run to
// arrive, without switching back if an older run sends a late event.
function liveSessionScope(events: AgentEvent[]) {
  if (pickedSessionId.value) return pickedSessionId.value;
  const seen = new Set<string>();
  let latest = '';
  for (const event of events) {
    const id = event.session_id;
    if (id?.startsWith('web-') && !seen.has(id)) {
      seen.add(id);
      latest = id;
    }
  }
  return latest;
}

function filterByPin(events: AgentEvent[]): AgentEvent[] {
  const scope = liveSessionScope(events);
  return scope ? events.filter(event => event.session_id === scope) : events;
}

// Re-materializes debugEvents from the live stream through filterByPin, as a
// NEW array reference (required for the tree/graph watchers to detect the
// change — see the comment above filterByPin). Every live-mode call site
// that rebuilds debugEvents from liveEvents should go through this, not a
// bare `[...liveEvents.value]` copy — otherwise the "a pinned session shows
// only its own events" contract silently stops holding for that call site.
function materializeLiveEvents() {
  debugEvents.value = [...filterByPin(liveEvents.value)];
}

watch(() => liveEvents.value.length, (newLen, oldLen) => {
  // Auto-switch to live mode when a new run starts (e.g. from Builder) while viewing history
  if (dataMode.value === 'history' && newLen === 1 && (oldLen ?? 0) === 0) {
    dataMode.value = 'live';
  }
  const scope = liveSessionScope(liveEvents.value);
  if (dataMode.value === 'live' && newLen > (oldLen ?? 0)) {
    const previousScope = debugEvents.value[0]?.session_id;
    if (scope && previousScope !== scope) {
      pausePlayback();
      reset();
    }
    materializeLiveEvents();
  }
  for (let i = oldLen ?? 0; i < newLen; i++) {
    const ev = liveEvents.value[i];
    if (ev) {
      // Only forward debug-control events (pause/resume/user_input) and
      // PIPELINE_END matching the pin — cross-session DEBUG_PAUSED or
      // PIPELINE_END shouldn't move this pane's UI.
      const matchesPin = !scope || ev.session_id === scope;
      if (matchesPin || !ev.session_id) {
        handleDebugEvent(ev);
      }
      // PIPELINE_END fires once when entire pipeline completes
      // Don't clear isWebRun while paused — user needs controls to resume
      if (matchesPin && ev.event_type === 'PIPELINE_END' && !isPaused.value) {
        isWebRun.value = false;
        debugPhase.value = debugPhase.value === 'running' ? 'stopped' : 'idle';
      }
    }
  }
});

// Re-materialise debugEvents whenever the pin changes so switching pins
// instantly reflects the target session's history (not a drip-feed as new
// events arrive).
watch(pickedSessionId, () => {
  if (dataMode.value !== 'live') return;
  reset();
  debugEvents.value = filterByPin(liveEvents.value);
});

// Sync session events when loaded
watch(sessionEvents, (events) => {
  loadedReplayId.value = selectedSession.value?.id ?? null;
  if (dataMode.value === 'history') {
    debugEvents.value = [...events];
  }
});

// Swap debugEvents source when toggling data mode (preserve the history side —
// the live side always re-derives fresh from liveEvents through the pin
// filter instead of restoring a snapshot, since a snapshot saved under a
// different pin, or before more events arrived, would show stale/wrong-pin
// content; see materializeLiveEvents).
const savedHistoryEvents = ref<AgentEvent[]>([]);

watch(dataMode, (mode, oldMode) => {
  pausePlayback();
  // Save current history-mode events before switching away from them
  if (oldMode !== 'live') {
    savedHistoryEvents.value = [...debugEvents.value];
  }

  // Reset tree state, then assign new events
  // The reset() clears treeRoots + internal stacks; the length watcher
  // will reprocess once we assign the new events array.
  reset();

  if (mode === 'live') {
    materializeLiveEvents();
    if (isHubMode.value) fetchHubSessions();
  } else {
    debugEvents.value = [...savedHistoryEvents.value];
    fetchSessions();
  }
});

// Show demo hint when first breakpoint is hit
watch(isPaused, (paused) => {
  if (paused && isDemoRun.value && !localStorage.getItem('rakitsu-demo-hint-seen')) {
    showDemoHint.value = true;
  }
});

function dismissDemoHint() {
  showDemoHint.value = false;
  localStorage.setItem('rakitsu-demo-hint-seen', '1');
}

function dismissDemoCta() {
  showDemoCta.value = false;
  isDemoRun.value = false;
}

function openBuilderFromDemo() {
  showDemoCta.value = false;
  isDemoRun.value = false;
  emit('open-builder');
}

// Watch web run status — detect completion
watch(runStatus, async (status) => {
  if (isWebRun.value && (status === 'completed' || status === 'error' || status === 'cancelled')) {
    const wasDemo = isDemoRun.value;
    isWebRun.value = false;
    isDebugRun.value = false;
    debugPhase.value = 'idle';
    resetDebugState();

    if (wasDemo && status === 'completed') {
      showDemoCta.value = true;
      setTimeout(() => { if (showDemoCta.value) dismissDemoCta(); }, 10000);
    }

    // If SSE missed events (fast run or race), fetch from session store
    if (liveEvents.value.length === 0 && runSessionId.value) {
      try {
        const base = `http://${selfUrl.value}`;
        const res = await fetch(`${base}/api/sessions/${runSessionId.value}/events`);
        if (res.ok) {
          const events = await res.json();
          if (Array.isArray(events) && events.length > 0) {
            debugEvents.value = [...filterByPin(events)];
          }
        }
      } catch { /* ignore */ }
    }
  }
});

// Pause hub polling when tab is deactivated, resume when it comes back.
onActivated(() => {
  startHubPolling();
  if (dataMode.value === 'live' && liveEvents.value.length > 0) {
    // Shared SSE has events (builder run — active or completed) — sync tree
    materializeLiveEvents();
    fetchDebugState();
  } else if (dataMode.value === 'live' && !isConnected.value && !isWebRun.value) {
    connectLive();
  }
});

onDeactivated(() => {
  pausePlayback();
  stopHubPolling();
});

// Focus on a specific agent when drilled down from builder
watch(() => props.focusAgent, async (agentName) => {
  if (!agentName) return;

  if (props.focusTreeRoots && props.focusTreeRoots.length > 0) {
    // Switch to history mode first — its watcher calls reset() + clears treeRoots.
    // We must wait for that to flush before injecting our tree roots.
    dataMode.value = 'history';
    await nextTick();

    // Now inject the builder's tree roots after the dataMode watcher has settled
    treeRoots.value = props.focusTreeRoots;
    renderKey.value++;
  } else if (liveEvents.value.length > 0) {
    materializeLiveEvents();
  }

  await nextTick();
  const roots = props.focusTreeRoots ?? treeRoots.value;
  const flat = roots.flatMap(function flatten(n: DebugTreeNode): DebugTreeNode[] {
    return [n, ...n.children.flatMap(flatten)];
  });
  for (let i = flat.length - 1; i >= 0; i--) {
    if (flat[i]!.agentName === agentName) {
      selectedNode.value = flat[i]!;
      break;
    }
  }
  emit('focus-consumed');
});

function connectLive() {
  const alreadyHasEvents = isConnected.value && liveEvents.value.length > 0;
  reset();
  if (!alreadyHasEvents) clearEvents();
  materializeLiveEvents();
  const url = debugUrl.value.startsWith('http') ? debugUrl.value : `http://${debugUrl.value}`;
  connect(url);
  fetchDebugState();
}

function disconnectLive() {
  disconnect();
  reset();
  clearEvents();
  debugEvents.value = [];
  isWebRun.value = false;
  isDebugRun.value = false;
  debugPhase.value = 'idle';
}

const isDebugRun = ref(false);

// Debug phase: idle → preview (set breakpoints) → running → stopped → preview (restart)
type DebugPhase = 'idle' | 'preview' | 'running' | 'stopped';
const debugPhase = ref<DebugPhase>('idle');
const previewSessionId = ref('');
const previewConfigYaml = ref('');
const previewQuery = ref('');
const builderRunParams = ref<{ configId: string; query: string; timeout: number; workdir: string; envVars: Record<string, string> } | null>(null);

function enterPreviewMode(sessionId: string, configYaml: string, query: string) {
  previewSessionId.value = sessionId;
  previewConfigYaml.value = configYaml;
  previewQuery.value = query;
  debugPhase.value = 'preview';
  dataMode.value = 'live';
  isDebugRun.value = true;
  reset();
  clearEvents();
  debugEvents.value = [];

  // Parse config and create synthetic PIPELINE_START event for tree preview
  try {
    const cfg = yaml.load(configYaml) as Record<string, unknown>;
    const orch = cfg?.orchestrator as Record<string, unknown>;
    const pipeline = orch?.pipeline as Record<string, unknown>;
    const rawSteps = (pipeline?.steps || []) as Record<string, unknown>[];

    // Recursively map pipeline steps with agent names for preRenderStep
    const mapSteps = (steps: Record<string, unknown>[]): Record<string, unknown>[] =>
      steps.map(s => {
        const subSteps = s.steps ? mapSteps(s.steps as Record<string, unknown>[]) : undefined;
        // Collect agents: direct agent + agents from sub-steps
        const agents: string[] = [];
        if (s.agent) agents.push(s.agent as string);
        if (subSteps) {
          for (const sub of subSteps) {
            const subAgents = (sub as { agents?: string[] }).agents || [];
            agents.push(...subAgents);
          }
        }
        return {
          name: s.name || 'step',
          type: s.type || 'sequential',
          agents,
          steps: subSteps,
        };
      });

    let steps: Record<string, unknown>[];
    if (rawSteps.length > 0) {
      steps = mapSteps(rawSteps);
    } else {
      // Non-pipeline: create one step per agent
      const orchAgents = (orch?.agents || []) as string[];
      steps = orchAgents.map(name => ({
        name, type: 'sequential', agents: [name],
      }));
    }

    const syntheticEvent: AgentEvent = {
      id: 'preview-pipeline',
      timestamp: new Date().toISOString(),
      agent_name: (orch?.name as string) || 'Pipeline',
      event_type: 'PIPELINE_START',
      payload: {
        query: query,
        step_count: steps.length,
        steps,
      },
    };
    // Use nextTick to ensure reset() watchers have fully processed before setting new events
    nextTick(() => {
      debugEvents.value = [syntheticEvent];
    });
  } catch (e) { console.warn('[Preview] config parse failed:', e); }
}

async function startDebugFromPreview() {
  const bps = debugBreakpoints.value.map(bp => ({
    event_type: bp.event_type,
    agent_name: bp.agent_name,
  }));

  reset();
  clearEvents();
  materializeLiveEvents();
  const base = `http://${selfUrl.value}`;
  debugUrl.value = selfUrl.value;
  debugUrlRef.value = selfUrl.value;
  connect(base);
  isWebRun.value = true;
  debugPhase.value = 'running';

  try {
    let ok = false;
    if (builderRunParams.value) {
      // Builder-initiated debug run — use /api/run with config ID + breakpoints
      const p = builderRunParams.value;
      const res = await fetch(`${base}/api/run`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          config_id: p.configId,
          query: p.query,
          timeout: p.timeout,
          debug: true,
          workdir: p.workdir,
          env_vars: p.envVars,
          breakpoints: bps,
        }),
      });
      ok = res.ok;
    } else {
      // Session rerun — use /api/sessions/{id}/rerun with breakpoints
      const res = await fetch(`${base}/api/sessions/${previewSessionId.value}/rerun?debug=true`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ breakpoints: bps }),
      });
      ok = res.ok;
    }
    if (!ok) {
      debugPhase.value = 'preview';
      isWebRun.value = false;
    }
  } catch {
    debugPhase.value = 'preview';
    isWebRun.value = false;
  }
}

function handleWebRunStarted(debug = false, joinExisting = false) {
  isWebRun.value = true;
  isDebugRun.value = debug;
  dataMode.value = 'live';
  if (!joinExisting) {
    // Fresh run — clear everything
    reset();
    clearEvents();
  }
  materializeLiveEvents();
  const url = `http://${selfUrl.value}`;
  debugUrl.value = selfUrl.value;
  debugUrlRef.value = selfUrl.value;
  connect(url);
  if (joinExisting && liveEvents.value.length > 0) {
    // Rebuild tree from existing events
    materializeLiveEvents();
  }
  if (debug) {
    fetchDebugState();
  }
}

function handleStopWebRun() {
  fetch(`http://${selfUrl.value}/api/run/stop`, { method: 'POST' }).catch(() => {});
  isWebRun.value = false;
  isDebugRun.value = false;
  debugPhase.value = 'idle';
  resetDebugState();
}

function selectSession(id: string) {
  pausePlayback();
  loadedReplayId.value = null;
  reset();
  loadSession(id);
}

async function handleInspectInBuilder(s: SessionMeta) {
  const projectId = s.project_id;
  if (s.config_yaml) {
    emit('inspect-in-builder', { yaml: s.config_yaml, projectId });
    return;
  }
  try {
    const res = await fetch(`/api/sessions/${s.id}/config`);
    if (res.ok) {
      const data = await res.json() as { yaml?: string };
      if (data.yaml) {
        emit('inspect-in-builder', { yaml: data.yaml, projectId });
      }
    }
  } catch {
    // silently fail
  }
}

// Re-run conflict dialog state
const showRerunDialog = ref(false);
const pendingRerunId = ref('');

async function handleRerun(id: string) {
  if (isWebRun.value || runStatus.value === 'running' || debugPhase.value === 'running') {
    pendingRerunId.value = id;
    showRerunDialog.value = true;
    return;
  }
  enterDebugPreview(id);
}

async function enterDebugPreview(id: string) {
  showRerunDialog.value = false;
  // Fetch session config for preview
  const session = sessions.value.find(s => s.id === id);
  const configYaml = session?.config_yaml || '';
  const query = session?.query || '';
  if (!configYaml) {
    // Fallback: fetch from server
    try {
      const res = await fetch(`/api/sessions/${id}/config`);
      if (res.ok) {
        const data = await res.json() as { yaml?: string };
        if (data.yaml) {
          enterPreviewMode(id, data.yaml, query);
          return;
        }
      }
    } catch { /* */ }
    // No config — run immediately without preview
    executeRerunDirect(id, true);
    return;
  }
  enterPreviewMode(id, configYaml, query);
}

async function executeRerunDirect(id: string, debug: boolean) {
  showRerunDialog.value = false;
  dataMode.value = 'live';
  reset();
  clearEvents();
  materializeLiveEvents();
  const url = `http://${selfUrl.value}`;
  debugUrl.value = selfUrl.value;
  debugUrlRef.value = selfUrl.value;
  connect(url);
  isWebRun.value = true;
  isDebugRun.value = debug;
  debugPhase.value = debug ? 'running' : 'idle';
  const newSessionId = await rerunSession(id, debug);
  if (!newSessionId) {
    isWebRun.value = false;
    isDebugRun.value = false;
    debugPhase.value = 'idle';
  }
}

async function handleDeleteSessions(ids: string[]) {
  await deleteSessions(ids);
}

function handleNodeSelect(node: DebugTreeNode) {
  selectedNode.value = node;
}

function handleGraphNodeClick(nodeId: string) {
  const node = findNode(nodeId);
  if (node) selectedNode.value = node;
}

function handleRunToNode(condition: { checkpoint?: string; agent_name?: string; tool_name?: string }) {
  runUntil(condition);
}

// Tree node context actions — convert DebugTreeNode to checkpoint condition
function treeNodeToCondition(node: DebugTreeNode): { checkpoint: string; agent_name: string } {
  const event = NODE_TYPE_TO_EVENT[node.type] || 'AGENT_START';
  const agentName = node.type === 'step' ? ((node.details?.stepName as string) ?? node.label) : node.agentName;
  return { checkpoint: event, agent_name: agentName };
}

function handleTreeBreakpoint(node: DebugTreeNode) {
  const cond = treeNodeToCondition(node);
  setBreakpoint(cond.checkpoint, cond.agent_name);
}

function handleTreeRunToNode(node: DebugTreeNode) {
  const cond = treeNodeToCondition(node);
  runUntil(cond);
}

function handleTreeDebugFromHere(node: DebugTreeNode) {
  const cond = treeNodeToCondition(node);
  handleDebugFromHere(cond);
}

async function handleDebugFromHere(condition: { checkpoint?: string; agent_name?: string; tool_name?: string }) {
  // Re-run the current history session in debug mode with a breakpoint at the target
  const sessionId = selectedSession.value?.id;
  if (!sessionId) return;

  // Check if there's an active run
  if (isWebRun.value || runStatus.value === 'running') {
    const choice = confirm(
      'An agent run is currently active.\n\nOK = Stop current run and debug from here\nCancel = Keep current run'
    );
    if (!choice) return;
  }

  // Connect SSE before starting run
  dataMode.value = 'live';
  isWebRun.value = true;
  isDebugRun.value = true;
  reset();
  clearEvents();
  materializeLiveEvents();
  const url = `http://${selfUrl.value}`;
  debugUrl.value = selfUrl.value;
  debugUrlRef.value = selfUrl.value;
  connect(url);
  fetchDebugState();

  // Now start the rerun (server auto-stops stale runs)
  const newSessionId = await rerunSession(sessionId, true);
  if (!newSessionId) {
    isWebRun.value = false;
    isDebugRun.value = false;
    return;
  }

  // Set breakpoint at the target node so it pauses there
  const checkpoint = condition.checkpoint ?? 'THOUGHT_START';
  const agentName = condition.agent_name ?? '*';
  await setBreakpoint(checkpoint, agentName);
}

function closeDetail() {
  selectedNode.value = null;
  replayResult.value = null;
}

const replayResult = ref<Record<string, unknown> | null>(null);

async function handleReplayFrom(iteration: number, agentName: string) {
  if (selectedSession.value) {
    const idx = debugEvents.value.findIndex(
      (e) => e.event_type === 'THOUGHT_START' && e.agent_name === agentName && e.iteration === iteration,
    );
    if (idx >= 0) {
      replayResult.value = await replay(selectedSession.value.id, idx);
    }
  }
}

function fmtElapsed(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`;
}

async function startBuilderRun(yaml: string, query: string, timeout: number, workdir = '', debug = false, envVars: Record<string, string> = {}) {
  const base = `http://${selfUrl.value}`;
  try {
    const inlineRes = await fetch(`${base}/api/configs/inline`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ yaml }),
    });
    const inlineData = await inlineRes.json();
    if (!inlineRes.ok || inlineData.error) {
      console.error('[rakitsu] config upload failed:', inlineData.error);
      return;
    }
    // Connect SSE then start run
    handleWebRunStarted(debug);
    await startRun(inlineData.id, query, timeout, workdir, debug, envVars);
    // Tag workspace so the Chat tab shows this run's config instead of
    // pulling from the Builder canvas.
    workspace.setConfig(inlineData.id, 'debug');
    // If run errored immediately, still keep SSE connected for error events
  } catch (e) {
    console.error('[rakitsu] startBuilderRun failed:', e);
  }
}

async function fetchHubSessions(silent = false) {
  if (!silent) hubLoading.value = true;
  try {
    const base = debugUrl.value.startsWith('http') ? debugUrl.value : `http://${debugUrl.value}`;
    const res = await fetch(`${base}/api/hub/sessions`);
    const data = await res.json();
    const incoming: HubSession[] = Array.isArray(data) ? data : [];
    // Only update ref when data actually changed — prevents Vue re-renders that
    // kill native <select> dropdowns and cause flickering in the session panel.
    if (JSON.stringify(incoming) !== JSON.stringify(hubSessions.value)) {
      hubSessions.value = incoming;
    }
    if (incoming.length > 0) {
      isHubMode.value = true;
    }
  } catch {
    if (hubSessions.value.length > 0) {
      hubSessions.value = [];
    }
  } finally {
    if (!silent) hubLoading.value = false;
  }
}

function startHubPolling() {
  if (hubPollTimer) return;
  hubPollTimer = setInterval(() => {
    if (!selectedHubSession.value) {
      fetchHubSessions(true);
    }
  }, 3000);
}

function stopHubPolling() {
  if (hubPollTimer) {
    clearInterval(hubPollTimer);
    hubPollTimer = null;
  }
}

// Preview a hub session (read-only, fetch past events — no SSE)
async function selectHubSession(session: HubSession) {
  selectedHubSession.value = session;

  // If this is the attached session, restore its live view
  if (attachedSession.value?.id === session.id) {
    materializeLiveEvents();
    const len = liveEvents.value.length;
    if (len > 0) {
      reset();
      materializeLiveEvents();
    }
    return;
  }

  // Otherwise preview: fetch past events (snapshot, no SSE)
  reset();
  const url = debugUrl.value.startsWith('http') ? debugUrl.value : `http://${debugUrl.value}`;
  try {
    const res = await fetch(`${url}/api/hub/sessions/events?id=${encodeURIComponent(session.id)}`);
    if (res.ok) {
      const pastEvents = (await res.json()) as AgentEvent[];
      debugEvents.value = Array.isArray(pastEvents) ? pastEvents : [];
    }
  } catch {
    debugEvents.value = [];
  }
}

async function attachDebugger(session: HubSession) {
  if (attachedSession.value && attachedSession.value.id !== session.id) {
    const ok = confirm(`Detach from "${attachedSession.value.name}" and attach to "${session.name}"?`);
    if (!ok) return;
    await detachDebugger(attachedSession.value);
  }

  const base = debugUrl.value.startsWith('http') ? debugUrl.value : `http://${debugUrl.value}`;
  await fetch(`${base}/api/hub/debug`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_id: session.id, action: 'debug_enable', data: {} }),
  });
  session.debug = true;
  attachedSession.value = session;

  reset();
  clearEvents();
  materializeLiveEvents();
  connect(base, session.id);

  try {
    const res = await fetch(`${base}/api/hub/sessions/events?id=${encodeURIComponent(session.id)}`);
    if (res.ok) {
      const pastEvents = (await res.json()) as AgentEvent[];
      if (Array.isArray(pastEvents) && pastEvents.length > 0) {
        const liveOnly = liveEvents.value;
        const pastIds = new Set(pastEvents.map(e => e.id));
        const newLive = liveOnly.filter(e => !pastIds.has(e.id));
        liveEvents.value = [...pastEvents, ...newLive];
      }
    }
  } catch {
    // live-only is fine
  }

  fetchDebugState();
  selectedHubSession.value = session;
}

async function detachDebugger(session: HubSession) {
  const base = debugUrl.value.startsWith('http') ? debugUrl.value : `http://${debugUrl.value}`;
  await fetch(`${base}/api/hub/debug`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session_id: session.id, action: 'debug_disable', data: {} }),
  });
  session.debug = false;
  if (attachedSession.value?.id === session.id) {
    attachedSession.value = null;
    disconnect();
  }
}

// Handle pending run from builder (watcher instead of onMounted for v-show compat)
watch(() => props.pendingRun, async (run) => {
  if (run) {
    const { yaml: yamlStr, query, timeout, workdir, debug, envVars } = run;
    emit('run-consumed');
    if (debug) {
      // Upload config first, then enter preview mode
      try {
        const base = `http://${selfUrl.value}`;
        const inlineRes = await fetch(`${base}/api/configs/inline`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ yaml: yamlStr }),
        });
        const inlineData = await inlineRes.json();
        if (inlineRes.ok && !inlineData.error) {
          // Store builder run params for startDebugFromPreview
          builderRunParams.value = { configId: inlineData.id, query, timeout, workdir, envVars: envVars ?? {} };
          enterPreviewMode('builder', yamlStr, query);
          return;
        }
      } catch { /* fall through to direct run */ }
    }
    await startBuilderRun(yamlStr, query, timeout, workdir, debug, envVars ?? {});
  }
});

// Sessions tab → Debug action: replay a persisted session's event log in
// History mode. The dataMode watcher resets the tree and fetches the session
// list; after it settles, selectSession loads the events, which flow through
// the sessionEvents watcher into debugEvents and rebuild the tree.
// immediate: true covers the first-mount case (prop set before mount).
watch(() => props.pendingHistorySession, async (id) => {
  if (!id) return;
  emit('history-consumed');
  dataMode.value = 'history';
  await nextTick();
  selectSession(id);
}, { immediate: true });

watch(() => props.followTimeline ? builderReplayEvent.value?.sessionId : undefined, async id => {
  if (!id || loadedReplayId.value === id && dataMode.value === 'history') return;
  dataMode.value = 'history';
  await nextTick();
  if (props.followTimeline && builderReplayEvent.value?.sessionId === id) selectSession(id);
}, { immediate: true });

// Handle demo run from welcome screen
watch(() => props.pendingDemo, async (demo) => {
  if (!demo) return;
  emit('demo-consumed');
  isDemoRun.value = true;
  handleWebRunStarted(true);
  await startRun(demo.configId, demo.query, 60, '', true, demo.envVars ?? {}, demo.breakpoints);
});

onMounted(async () => {
  // Phase 2: restore pinned session from URL hash on mount; react to hash
  // changes (back/forward navigation, external link) for the rest of the
  // lifecycle.
  const fromHash = readSessionIdFromHash();
  if (fromHash) pickedSessionId.value = fromHash;
  if (typeof window !== 'undefined') {
    window.addEventListener('hashchange', onHashChange);
  }

  fetchSessions();

  try {
    const res = await fetch(`http://${selfUrl.value}/api/run`);
    const data = await res.json();
    if (data.status === 'running') {
      // If shared SSE already has events (builder started this run), join without clearing
      const hasSharedEvents = isConnected.value && liveEvents.value.length > 0;
      handleWebRunStarted(data.debug === true, hasSharedEvents);
      return;
    }
  } catch {
    // no active run
  }

  if (dataMode.value === 'live' && !isConnected.value) {
    debugUrl.value = selfUrl.value;
    if (props.followTimeline) {
      connectLive();
      return;
    }
    await fetchHubSessions();
    if (hubSessions.value.length > 0) {
      isHubMode.value = true;
    }
    startHubPolling();
  }
});

onUnmounted(() => {
  pausePlayback();
  stopHubPolling();
  if (typeof window !== 'undefined') {
    window.removeEventListener('hashchange', onHashChange);
  }
});
</script>

<template>
  <div class="debug-view">
    <!-- Toolbar -->
    <div class="toolbar">
      <div class="toolbar-left">
        <StyleSelector v-model="viewMode" />

        <div class="mode-toggle">
          <button
            class="toggle-btn"
            :class="{ active: dataMode === 'live' }"
            title="Monitor live agent executions via SSE"
            @click="dataMode = 'live'"
          >Live</button>
          <button
            class="toggle-btn"
            :class="{ active: dataMode === 'history' }"
            title="Browse and replay past sessions"
            @click="dataMode = 'history'"
          >History</button>
        </div>

        <button
          class="toggle-btn chat-toggle"
          :class="{ active: chatPaneOpen }"
          :title="chatPaneOpen ? 'Collapse chat pane' : 'Open chat pane for the current run'"
          @click="toggleChatPane"
        >
          {{ chatPaneOpen ? '✕ Chat' : '💬 Chat' }}
        </button>

        <SessionPicker v-model="pickedSessionId" @select="onPickSession" />
      </div>

      <div class="toolbar-center">
        <!-- Attached session indicator -->
        <div v-if="attachedSession" class="attached-badge" @click="selectHubSession(attachedSession)">
          Attached: {{ attachedSession.name }}
          <button class="detach-inline-btn" @click.stop="detachDebugger(attachedSession)">Detach</button>
        </div>

        <!-- Debug preview: set breakpoints then start -->
        <template v-if="debugPhase === 'preview'">
          <div class="run-status-badge preview">Preview — set breakpoints</div>
          <button class="toolbar-btn connect" @click="startDebugFromPreview">Start Debug</button>
          <button class="toolbar-btn" @click="debugPhase = 'idle'; isDebugRun = false; reset();">Cancel</button>
        </template>
        <!-- Debug stopped: restart or dismiss -->
        <template v-else-if="debugPhase === 'stopped'">
          <div class="run-status-badge stopped">Completed</div>
          <button class="toolbar-btn connect" @click="enterPreviewMode(previewSessionId, previewConfigYaml, previewQuery)">Restart</button>
          <button class="toolbar-btn" @click="debugPhase = 'idle'; isDebugRun = false;">Done</button>
        </template>
        <!-- Web run status + stop -->
        <template v-else-if="isWebRun || runStatus === 'running'">
          <div class="run-status-badge running">
            Running... {{ fmtElapsed(webElapsedMs) }}
          </div>
          <button class="toolbar-btn disconnect" @click="handleStopWebRun">Stop</button>
        </template>
        <!-- Connection controls (live, no attached, no web run) -->
        <template v-else-if="dataMode === 'live' && !attachedSession && !isWebRun">
          <template v-if="isConnected">
            <input v-model="debugUrl" type="text" class="url-input" disabled />
            <button class="toolbar-btn disconnect" @click="disconnectLive">Disconnect</button>
          </template>
          <template v-else>
            <input v-model="debugUrl" type="text" placeholder="host:port" class="url-input" @keyup.enter="connectLive()" />
            <button class="toolbar-btn connect" @click="connectLive()">Connect</button>
          </template>
        </template>
      </div>

      <div class="toolbar-right">
        <div v-if="currentSessionId" class="session-id-badge" :title="currentSessionId">
          {{ currentSessionId.slice(0, 12) }}
        </div>
        <div v-if="tokenTotals.total_tokens > 0" class="token-summary clickable" @click="showTokenReport = true" title="Click for token report">
          {{ tokenTotals.total_tokens.toLocaleString() }} tokens
        </div>
        <div v-if="isConnected && !isWebRun" class="live-badge">LIVE</div>
        <div v-if="isWebRun && isDebugRun" class="live-badge debug-run">DEBUG</div>
        <div v-else-if="isWebRun" class="live-badge web-run">RUN</div>
        <div v-if="webRunError && isWebRun" class="error-badge">{{ webRunError }}</div>
        <div v-else-if="liveError" class="error-badge">{{ liveError }}</div>
        <button
          class="toolbar-btn panel-toggle"
          :class="{ active: sessionPanelOpen }"
          @click="sessionPanelOpen = !sessionPanelOpen"
          title="Toggle session panel"
        >Sessions</button>
      </div>
    </div>

    <!-- Breakpoint bar -->
    <BreakpointBar
      v-if="debugBreakpoints.length > 0 || debugPhase === 'preview' || debugPhase === 'running' || debugPhase === 'stopped' || (dataMode === 'live' && (attachedSession || (isWebRun && isDebugRun) || isPaused || !!pendingUserInput))"
      :debug-state="debugState"
      :is-paused="isPaused"
      :paused-agent="pausedAgent"
      :paused-checkpoint="pausedCheckpoint"
      :paused-context="pausedContext"
      :breakpoints="debugBreakpoints"
      :is-connected="isConnected"
      :debug-mode="debugMode"
      :is-running="isConnected || isWebRun"
      :pending-user-input="pendingUserInput"
      @resume="(a) => a === 'step_in' ? stepIn() : a === 'step_out' ? stepOut() : debugResume(a)"
      @run-until="(c) => runUntil(c)"
      @set-breakpoint="setBreakpoint"
      @clear-breakpoint="clearBreakpoint"
      @clear-all="clearAllBreakpoints"
      @request-pause="requestPause"
      @set-mode="(m, t) => setMode(m, t)"
      @answer-user-input="(text) => answerUserInput(text)"
    />

    <!-- Main content -->
    <div class="content">
      <!-- Main area: always shows tree/graph -->
      <div class="main-area">
        <!-- Empty state with onboarding hints -->
        <div v-if="(debugEvents ?? []).length === 0 && treeRoots.length === 0" class="empty-state">
          <div class="empty-guide">
            <div class="empty-title">Agent Debugger</div>
            <template v-if="dataMode === 'live'">
              <div v-if="isWebRun" class="empty-hint">Waiting for events...</div>
              <template v-else>
                <div class="empty-hint">
                  {{ isHubMode ? 'Select a session from the panel to inspect.' : 'Open the Sessions panel to launch a run.' }}
                </div>
                <div class="empty-features">
                  <div class="feature-item">
                    <span class="feature-icon">T B L M</span>
                    <span>Switch visualization styles — Tree, Block, Timeline, Mind Map</span>
                  </div>
                  <div class="feature-item">
                    <span class="feature-icon">||</span>
                    <span>Set breakpoints to pause agents mid-execution</span>
                  </div>
                  <div class="feature-item">
                    <span class="feature-icon">~</span>
                    <span>Override system prompts, params, and tools while paused</span>
                  </div>
                  <div class="feature-item">
                    <span class="feature-icon">Q</span>
                    <span>Search and filter sessions with incremental matching</span>
                  </div>
                </div>
              </template>
            </template>
            <template v-else>
              <div class="empty-hint">Select a history session from the panel to replay its events.</div>
              <div class="empty-features">
                <div class="feature-item">
                  <span class="feature-icon">|--|</span>
                  <span>Use the timeline slider to scrub through past events</span>
                </div>
                <div class="feature-item">
                  <span class="feature-icon">re</span>
                  <span>Re-run or inspect sessions in the visual builder</span>
                </div>
              </div>
            </template>
          </div>
        </div>

        <!-- Active agents bar -->
        <div v-if="activeAgents.length > 1" class="active-agents-bar">
          <span class="active-label">Active:</span>
          <span
            v-for="a in activeAgents"
            :key="a.id"
            class="active-agent-chip"
            @click="handleNodeSelect(a)"
          >{{ a.label }}</span>
        </div>

        <!-- Tree/Graph + Detail -->
        <div
          v-show="(debugEvents ?? []).length > 0 || treeRoots.length > 0 || chatPaneOpen"
          class="split-layout"
          :class="{ 'has-detail': !!selectedNode, 'has-chat': chatPaneOpen }"
        >
          <div class="main-panel">
            <component
              :is="activeStyleComponent"
              :key="viewMode"
              :roots="treeRoots"
              :render-key="renderKey"
              :active-ids="activeNodeIds"
              :selected-id="selectedNode?.id"
              :visible-up-to="visibleUpTo"
              :total-events="(debugEvents ?? []).length"
              :events="debugEvents"
              :is-history="dataMode === 'history'"
              :is-debug-mode="isDebugRun"
              :is-connected="isConnected && dataMode === 'live'"
              :is-preview="debugPhase === 'preview'"
              :breakpointed-agents="breakpointedAgents"
              :visible="true"
              @select="handleNodeSelect"
              @node-click="handleGraphNodeClick"
              @update:visible-up-to="setPlaybackPosition($event)"
              @set-breakpoint="handleTreeBreakpoint"
              @run-to-node="handleTreeRunToNode"
              @debug-from-here="handleTreeDebugFromHere"
            />
          </div>
          <div v-if="selectedNode" class="detail-side">
            <DetailPanel
              :node="selectedNode"
              :render-key="renderKey"
              :is-connected="isConnected && dataMode === 'live'"
              :is-preview="debugPhase === 'preview'"
              :is-paused="isPaused"
              :is-history="dataMode === 'history'"
              :self-url="selfUrl"
              :all-config-tools="allConfigTools"
              :config-provider-names="configProviderNames"
              :config-provider-map="configProviderMap"
              :config-defaults="configDefaults"
              :config-agent-map="configAgentMap"
              :config-orchestrator-map="configOrchestratorMap"
              :replay-result="replayResult"
              @close="closeDetail"
              @apply-params="(agent: string, ovr: Record<string, unknown>) => setParams(agent, ovr as any)"
              @reset-params="(agent: string) => clearParams(agent)"
              @replay-from="handleReplayFrom"
              @rerun-from-step="(stepName: string) => rerunFromStep(stepName)"
              @run-to-node="handleRunToNode"
              @debug-from-here="handleDebugFromHere"
            />
          </div>
          <div v-if="chatPaneOpen" class="chat-side">
            <div class="chat-side-header">
              <span class="chat-side-title">Chat</span>
              <button class="chat-side-close" @click="chatPaneOpen = false">Close</button>
            </div>
            <!-- History mode: read-only replay reconstructed from events. -->
            <ChatHistoryPanel
              v-if="isHistoryChatSession"
              :blocks="historyChatBlocks"
              :title="selectedSession?.name || 'chat'"
            />
            <template v-else>
              <div v-if="chatPaneError" class="chat-side-error">{{ chatPaneError }}</div>
              <div v-else-if="!workspace.currentConfigId.value" class="chat-side-empty">
                Load a config in Builder or start a run here to enable chat.
              </div>
              <div v-else-if="!debuggerChatSession" class="chat-side-empty">
                <p>No chat session for <strong>{{ workspace.currentConfig.value?.name }}</strong> yet.</p>
                <button
                  class="chat-side-start"
                  :disabled="chatPaneStarting"
                  @click="startDebuggerChat"
                >
                  {{ chatPaneStarting ? 'Starting…' : 'Start chat' }}
                </button>
              </div>
              <ChatPanel
                v-else
                :key="debuggerChatSession.id"
                :session-id="debuggerChatSession.id"
                :forward-events="true"
              />
            </template>
          </div>
        </div>

        <!-- Playback controls (history mode or completed live run) -->
        <div v-if="showPlayback" class="playback-controls">
          <input
            type="range"
            :min="0"
            :max="(debugEvents ?? []).length - 1"
            :value="playbackIdx"
            @input="setPlaybackPosition(Number(($event.target as HTMLInputElement).value))"
            class="pb-slider"
          />
          <div class="playback-bar">
            <button class="pb-btn" title="Go to start" @click="setPlaybackPosition(0)">|&lt;</button>
            <button class="pb-btn" title="Step back (node)" @click="playbackStepNode(-1)">&lt;&lt;</button>
            <button class="pb-btn" title="Step back (event)" @click="playbackStepEvent(-1)">&lt;</button>
            <span class="pb-label">{{ playbackIdx + 1 }} / {{ (debugEvents ?? []).length }}</span>
            <button class="pb-btn" title="Step forward (event)" @click="playbackStepEvent(1)">&gt;</button>
            <button class="pb-btn" title="Step forward (node)" @click="playbackStepNode(1)">&gt;&gt;</button>
            <button class="pb-btn" title="Go to end" @click="setPlaybackPosition(debugEvents.length - 1)">&gt;|</button>
            <button class="pb-btn play-btn" :class="{ playing: isPlaying }" title="Play / Pause" @click="togglePlay">{{ isPlaying ? '||' : '>' }}</button>
          </div>
        </div>
      </div>

      <!-- Session Panel (right side, collapsible). History cards were
           suppressed when browsing moved to the Sessions top-tab, which
           left the toolbar's History toggle a dead end — restored so
           History mode is usable in place again. -->
      <SessionPanel
        :data-mode="dataMode"
        :hub-sessions="hubSessions"
        :hub-loading="hubLoading"
        :attached-session="attachedSession"
        :sessions="sessions"
        :history-loading="loading"
        :history-error="historyError"
        :selected-session-id="selectedSession?.id ?? null"
        :is-open="sessionPanelOpen"
        :is-hub-mode="isHubMode"
        :base-url="selfUrl"
        @select-hub-session="selectHubSession"
        @attach-debugger="attachDebugger"
        @detach-debugger="detachDebugger"
        @select-history-session="selectSession"
        @inspect-in-builder="handleInspectInBuilder"
        @rerun="handleRerun"
        @refresh-hub="fetchHubSessions"
        @run-started="(debug: boolean) => handleWebRunStarted(debug)"
        @delete-sessions="handleDeleteSessions"
        @toggle="sessionPanelOpen = !sessionPanelOpen"
      />
    </div>
  </div>

  <!-- Token Usage Report overlay -->
  <TokenReport
    v-if="showTokenReport"
    :tree-roots="treeRoots"
    :render-key="renderKey"
    @close="showTokenReport = false"
  />

  <!-- Re-run conflict dialog -->
  <div v-if="showRerunDialog" class="modal-overlay" @click.self="showRerunDialog = false">
    <div class="modal-card">
      <div class="modal-header">A session is currently active</div>
      <div class="modal-body">
        <button class="modal-btn primary" @click="enterDebugPreview(pendingRerunId)">
          Re-run with Debugger
        </button>
        <button class="modal-btn" @click="executeRerunDirect(pendingRerunId, false)">
          Re-run without Debugger
        </button>
        <button class="modal-btn cancel" @click="showRerunDialog = false">
          Cancel
        </button>
      </div>
    </div>
  </div>

  <!-- Demo hint: shown when first breakpoint hits during demo -->
  <div v-if="showDemoHint" class="demo-hint">
    <span class="demo-hint-text">
      The agent is paused at a breakpoint. Try changing the system prompt or temperature in the panel below, then click <strong>Resume</strong>.
    </span>
    <button class="demo-hint-dismiss" @click="dismissDemoHint">&times;</button>
  </div>

  <!-- Post-demo CTA -->
  <div v-if="showDemoCta" class="demo-cta-overlay" @click.self="dismissDemoCta">
    <div class="demo-cta-card">
      <p class="demo-cta-title">Demo complete</p>
      <p class="demo-cta-desc">Now build your own agent.</p>
      <button class="demo-cta-btn" @click="openBuilderFromDemo">Open Builder</button>
      <button class="demo-cta-skip" @click="dismissDemoCta">Dismiss</button>
    </div>
  </div>
</template>

<style scoped>
.demo-hint {
  position: absolute;
  top: 80px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 20;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 16px;
  background: var(--surface-3);
  border: 1px solid var(--accent-agent);
  border-left: 3px solid var(--accent-agent);
  border-radius: var(--node-radius);
  max-width: 520px;
  box-shadow: 0 4px 16px rgba(0,0,0,0.3);
}
.demo-hint-text {
  font-size: 12px;
  color: var(--text-secondary);
  line-height: 1.5;
}
.demo-hint-text strong {
  color: var(--text-primary);
}
.demo-hint-dismiss {
  background: none;
  border: none;
  color: var(--text-muted);
  font-size: 16px;
  cursor: pointer;
  padding: 0 4px;
  flex-shrink: 0;
}
.demo-hint-dismiss:hover {
  color: var(--text-primary);
}

.demo-cta-overlay {
  position: absolute;
  inset: 0;
  background: rgba(0,0,0,0.4);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 20;
}
.demo-cta-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 32px 40px;
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  box-shadow: 0 8px 32px rgba(0,0,0,0.4);
  text-align: center;
}
.demo-cta-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
}
.demo-cta-desc {
  margin: 0;
  font-size: 13px;
  color: var(--text-secondary);
}
.demo-cta-btn {
  padding: 8px 24px;
  font-size: 13px;
  font-weight: 600;
  color: #fff;
  background: var(--accent-agent);
  border: none;
  border-radius: var(--node-radius);
  cursor: pointer;
}
.demo-cta-btn:hover {
  opacity: 0.9;
}
.demo-cta-skip {
  background: none;
  border: none;
  color: var(--text-muted);
  font-size: 11px;
  cursor: pointer;
}
.demo-cta-skip:hover {
  color: var(--text-secondary);
}

.modal-overlay {
  position: absolute; /* was fixed — escaped tab container causing ghost overlay */
  inset: 0;
  background: rgba(0,0,0,0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal-card {
  background: var(--surface-3);
  border-radius: var(--node-radius);
  border: 1px solid var(--node-border);
  box-shadow: 0 8px 32px rgba(0,0,0,0.4);
  min-width: 300px;
  overflow: hidden;
}
.modal-header {
  padding: 14px 16px;
  font-weight: 600;
  font-size: 14px;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-subtle);
}
.modal-body {
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.modal-btn {
  padding: 8px 16px;
  border: 1px solid var(--node-border);
  border-radius: var(--node-radius);
  font-size: 13px;
  cursor: pointer;
  background: var(--surface-2);
  color: var(--text-primary);
  text-align: left;
}
.modal-btn:hover { background: var(--surface-4); }
.modal-btn.primary {
  background: var(--accent-agent);
  color: white;
  border-color: var(--accent-agent);
  font-weight: 500;
}
.modal-btn.primary:hover { background: #4a7cdf; }
.modal-btn.cancel { color: var(--text-muted); }

.debug-view {
  container-type: inline-size;
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--surface-0);
  color: var(--text-primary);
}

@container (max-width: 900px) {
  .toolbar, .toolbar-left, .toolbar-center, .toolbar-right { flex-wrap: wrap; }
  .toolbar-center { min-width: 0; }
}

@container (max-width: 600px) {
  .content { position: relative; }
  .debug-view :deep(.session-panel:not(.collapsed)) {
    position: absolute;
    right: 0;
    z-index: 2;
    height: 100%;
    max-width: 100%;
    min-width: 0 !important;
  }
  .debug-view :deep(.session-panel .panel-content) { max-width: 100%; }
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
  gap: 12px;
  flex-shrink: 0;
}

.toolbar-left, .toolbar-center, .toolbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mode-toggle {
  display: flex;
  border: 1px solid var(--node-border);
  border-radius: var(--node-radius);
  overflow: hidden;
}

.toggle-btn {
  padding: 5px 12px;
  border: none;
  background: var(--surface-2);
  font-size: 12px;
  cursor: pointer;
  color: var(--text-secondary);
  font-family: var(--font-sans);
}
.toggle-btn:not(:last-child) {
  border-right: 1px solid var(--node-border);
}
.toggle-btn.active {
  background: var(--accent-agent);
  color: white;
}

.url-input {
  padding: 5px 10px;
  border: 1px solid var(--node-border);
  border-radius: var(--node-radius);
  font-size: 12px;
  font-family: var(--font-mono);
  width: 180px;
  outline: none;
  background: var(--surface-1);
  color: var(--text-primary);
}
.url-input:focus { border-color: var(--accent-agent); }

.toolbar-btn {
  padding: 5px 12px;
  border: 1px solid var(--node-border);
  border-radius: var(--node-radius);
  background: var(--surface-3);
  font-size: 12px;
  cursor: pointer;
  color: var(--text-primary);
  font-family: var(--font-sans);
}
.toolbar-btn.connect { color: var(--status-running); border-color: var(--status-running); }
.toolbar-btn.connect:hover { background: var(--status-running); color: white; }
.toolbar-btn.disconnect { color: var(--status-error); border-color: var(--status-error); }
.toolbar-btn.disconnect:hover { background: var(--status-error); color: white; }
.toolbar-btn.panel-toggle { color: var(--accent-agent); border-color: var(--accent-agent); }
.toolbar-btn.panel-toggle.active { background: var(--accent-agent); color: white; }

.run-status-badge {
  font-size: 12px;
  font-weight: 500;
  font-family: var(--font-mono);
  padding: 4px 10px;
  border-radius: var(--node-radius);
}
.run-status-badge.running { background: rgba(91,141,239,0.15); color: var(--accent-agent); }
.run-status-badge.preview { background: rgba(245,158,11,0.15); color: var(--status-paused); }
.run-status-badge.stopped { background: rgba(192,132,252,0.15); color: var(--accent-skill); }

.session-id-badge {
  font-size: 10px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  padding: 2px 6px;
  background: var(--surface-3);
  border-radius: 2px;
  cursor: default;
}
.token-summary {
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  padding: 3px 8px;
  background: var(--surface-3);
  border-radius: var(--node-radius);
}
.token-summary.clickable {
  cursor: pointer;
}
.token-summary.clickable:hover {
  background: var(--surface-4);
  color: var(--text-primary);
}

.live-badge {
  font-size: 10px;
  font-weight: 700;
  font-family: var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: white;
  background: var(--status-running);
  padding: 2px 8px;
  border-radius: 2px;
}
.live-badge.web-run {
  background: var(--accent-agent);
}
.live-badge.debug-run {
  background: var(--status-paused);
}

.error-badge {
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--status-error);
}

.attached-badge {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  background: rgba(192,132,252,0.15);
  color: var(--accent-skill);
  border-radius: var(--node-radius);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
}
.attached-badge:hover { background: rgba(192,132,252,0.25); }

.detach-inline-btn {
  padding: 2px 6px;
  border: 1px solid var(--accent-skill);
  border-radius: 2px;
  background: transparent;
  color: var(--accent-skill);
  font-size: 10px;
  cursor: pointer;
}
.detach-inline-btn:hover { background: var(--accent-skill); color: white; }

/* Content layout */
.content {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.main-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.playback-controls {
  padding: 6px 12px;
  background: var(--surface-2);
  border-top: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.pb-slider {
  width: 100%;
  accent-color: var(--accent-agent);
}
.playback-bar {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  margin-top: 4px;
}
.pb-btn {
  background: var(--surface-3);
  border: 1px solid var(--node-border);
  border-radius: 2px;
  padding: 2px 8px;
  font-size: 11px;
  font-family: var(--font-mono);
  cursor: pointer;
  color: var(--text-secondary);
  min-width: 28px;
}
.pb-btn:hover { background: var(--surface-4); border-color: var(--text-muted); }
.pb-btn:active { background: var(--surface-1); }
.play-btn { font-weight: bold; color: var(--accent-agent); }
.play-btn.playing { color: var(--status-error); }
.pb-label {
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  min-width: 70px;
  text-align: center;
}

.empty-state {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.empty-guide {
  text-align: center;
  max-width: 400px;
}
.empty-title {
  font-size: 18px;
  font-weight: 600;
  font-family: var(--font-sans);
  color: var(--accent-agent);
  margin-bottom: 8px;
}
.empty-hint {
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 16px;
}
.empty-features {
  display: flex;
  flex-direction: column;
  gap: 8px;
  text-align: left;
}
.feature-item {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  color: var(--text-secondary);
}
.feature-icon {
  width: 32px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--surface-3);
  border-radius: var(--node-radius);
  font-size: 9px;
  font-weight: 700;
  color: var(--accent-agent);
  flex-shrink: 0;
  font-family: var(--font-mono);
}

.split-layout {
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.main-panel {
  flex: 1;
  overflow: hidden;
}

.split-layout.has-chat .main-panel {
  flex: 5;
}
.chat-side {
  flex: 4;
  min-width: 320px;
  max-width: 520px;
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--border-subtle);
  background: var(--surface-0);
  overflow: hidden;
}
.chat-side-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
}
.chat-side-title {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-secondary);
}
.chat-side-close {
  font-size: 11px;
  padding: 3px 8px;
  background: var(--surface-3);
  color: var(--text-primary);
  border: none;
  border-radius: 3px;
  cursor: pointer;
}
.chat-side-empty {
  padding: 16px;
  font-size: 12px;
  color: var(--text-muted);
}
.chat-side-empty p {
  margin: 0 0 12px;
  line-height: 1.5;
}
.chat-side-start {
  font-size: 12px;
  padding: 6px 14px;
  background: var(--accent-agent);
  color: var(--surface-0);
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-weight: 500;
}
.chat-side-start:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.chat-side-error {
  padding: 12px 16px;
  font-size: 12px;
  color: var(--status-error, #ef4444);
  background: rgba(239, 68, 68, 0.08);
}

.split-layout.has-detail .main-panel {
  flex: 7;
}

.detail-side {
  flex: 3;
  min-width: 280px;
  max-width: 400px;
  overflow: hidden;
  border-left: 1px solid var(--border-subtle);
}


.active-agents-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  background: var(--surface-2);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 11px;
  flex-shrink: 0;
}
.active-label {
  color: var(--accent-agent);
  font-weight: 600;
  font-family: var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  font-size: 10px;
}
.active-agent-chip {
  padding: 2px 8px;
  background: var(--accent-agent);
  color: white;
  border-radius: 2px;
  cursor: pointer;
  font-size: 10px;
  font-weight: 500;
  font-family: var(--font-mono);
}
.active-agent-chip:hover {
  background: #4a7cdf;
}
</style>
