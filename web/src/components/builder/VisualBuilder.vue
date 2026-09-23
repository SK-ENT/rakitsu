<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onUnmounted, onActivated, provide, markRaw } from 'vue';
import { VueFlow, useVueFlow, type Node, type Edge } from '@vue-flow/core';
import { Background } from '@vue-flow/background';
import { Controls } from '@vue-flow/controls';
import { MiniMap } from '@vue-flow/minimap';
import { nodeTypes } from '../nodes';
import { execNodeTypes } from '../debugger/debug-nodes';
import { useYamlExport, useModularConfig, useEventStream, useDebugTree, useAgentRun, useRunSnapshots } from '../../composables';
import { useDebugControl } from '../../composables/useDebugControl';
import { useCanvasExecutionTree } from '../../composables/useCanvasExecutionTree';
import NodeEditor from './NodeEditor.vue';
import SettingsPanel from './SettingsPanel.vue';
import type { AgentConfig, ToolConfig, SkillConfig, OrchestratorConfig, GroupConfig, PipelineStep, Settings, Config } from '../../types';
import type { DebugTreeNode } from '../../types';
import { generateEntityId } from '../../utils/entityId';
import { useGroupAutoFit } from '../../composables/useGroupAutoFit';
import { useCanvasSearch } from '../../composables/useCanvasSearch';
import { useUndoRedo } from '../../composables/useUndoRedo';
import HierarchyPanel from './HierarchyPanel.vue';
import RunBar from './RunBar.vue';
import MonitorCard from './MonitorCard.vue';
import DebugCard from './DebugCard.vue';
import { useExecutionLayout } from '../../composables/useExecutionLayout';
import ElkEdge from '../edges/ElkEdge.vue';
import RakitsuExecEdge from '../edges/RakitsuExecEdge.vue';
import TokenizerPanel from './TokenizerPanel.vue';
import DetailPanel from '../debugger/DetailPanel.vue';
import WallpaperHost from '../WallpaperHost.vue';
import { useWallpaperPrefs } from '../../composables/useWallpaperPrefs';

// --- Canvas mode ---
export type CanvasMode = 'design' | 'monitor' | 'debug' | 'replay';
const canvasMode = ref<CanvasMode>('design');
provide('canvasMode', canvasMode);

// Post-run results overlay — visible in design mode after run completes
const showResults = ref(false);
provide('showResults', showResults);

// Saved manual positions — restored when leaving execution modes
const savedPositions = ref<Record<string, { x: number; y: number }>>({});

const { computeLayout } = useExecutionLayout();

/**
 * Re-run dagre layout on design nodes using actual rendered dimensions.
 * Waits for VueFlow to measure DOM, then repositions with transition.
 */
function relayoutDesign() {
  nextTick(() => {
    // Step 1: Re-arrange children within each group (they may have grown wider)
    const groupIds = nodes.value.filter(n => n.type === 'group').map(n => n.id);
    for (const gid of groupIds) {
      rearrangeSiblings(gid);
    }
    // Step 2: Auto-fit all groups bottom-up so group dimensions are correct
    autoFitAllGroups();
    // Step 3: Wait for DOM to update group sizes, then ELK layout top-level
    nextTick(async () => {
      const liveNodes = nodes.value.map(n => {
        const live = vfFindNode(n.id);
        return live ?? n;
      });
      const { positions } = await computeLayout(liveNodes, edges.value);
      if (positions.size === 0) return;
      nodes.value = nodes.value.map(n => {
        const pos = positions.get(n.id);
        return pos ? { ...n, position: pos, class: `${n.class ?? ''} node--transitioning`.trim() } : n;
      });
      nextTick(() => fitView({ padding: 0.1 }));
      // Remove transition class after animation
      setTimeout(() => {
        nodes.value = nodes.value.map(n => ({
          ...n,
          class: (String(n.class ?? '')).replace('node--transitioning', '').trim() || undefined,
        }));
      }, 400);
    });
  });
}

async function enterExecutionMode(mode: 'monitor' | 'debug' | 'replay') {
  // Clean up any frozen exec tree from previous run
  clearExecTree();
  nodes.value = nodes.value.filter(n => !n.id.startsWith('exec_'));
  edges.value = edges.value.filter(e => !e.id.startsWith('exec_edge_'));
  showResults.value = false;
  // Set canvas mode BEFORE async layout — SSE events may arrive during computeLayout
  // and rebuild() clears exec nodes when canvasMode is still 'design'.
  canvasMode.value = mode;
  // Save current design-node positions (exclude exec_ nodes)
  savedPositions.value = Object.fromEntries(
    nodes.value.filter(n => !n.id.startsWith('exec_')).map(n => [n.id, { ...n.position }])
  );
  // Compute structured ELK layout
  const { positions } = await computeLayout(nodes.value, edges.value);
  // Apply with transition class
  nodes.value = nodes.value.map(n => {
    const pos = positions.get(n.id);
    return pos ? { ...n, position: pos, class: `${n.class ?? ''} node--transitioning`.trim() } : n;
  });
  // Dim design edges during execution
  edges.value = edges.value.map(e => ({ ...e, class: `${e.class ?? ''} design-dim`.trim() }));
  // Collapse panels
  hierarchyPanelVisible.value = false;
  editorPanelVisible.value = false;
  nextTick(() => fitView({ padding: 0.1 }));
}

function returnToDesign() {
  pipelineDepth = 0;
  resetDebugState();
  debugCardNodeId.value = null;
  execDetailNodeId.value = null;
  showResults.value = false;
  // Restore manual positions
  if (Object.keys(savedPositions.value).length > 0) {
    nodes.value = nodes.value.map(n => {
      const saved = savedPositions.value[n.id];
      const cls = (String(n.class ?? '')).replace('node--transitioning', '').trim();
      return saved ? { ...n, position: saved, class: cls } : { ...n, class: cls };
    });
  }
  canvasMode.value = 'design';
  monitorCardNodeId.value = null;
  nodeTokenCounts.value = {};
  totalTokenCount.value = 0;
  // Clear execution overlay fields
  nodes.value = nodes.value.map(n => ({
    ...n,
    data: {
      ...n.data,
      _runStatus: null,
      _tokenCount: undefined,
      _iterationCount: undefined,
      _lastThought: undefined,
      _errorMessage: undefined,
    },
  }));
  // Remove exec tree nodes and edges
  clearExecTree();
  nodes.value = nodes.value.filter(n => !n.id.startsWith('exec_'));
  edges.value = edges.value.filter(e => !e.id.startsWith('exec_edge_'));
  // Stop all edge animations + remove dim/active classes
  edges.value = edges.value.map(e => ({
    ...e,
    animated: false,
    class: (String(e.class ?? '')).replace(/design-dim|exec-active/g, '').trim() || undefined,
  }));
  // Restore panels
  hierarchyPanelVisible.value = true;
  editorPanelVisible.value = true;
  nextTick(() => fitView({ padding: 0.1 }));
}

// Enter results mode — keep execution data visible, restore design capabilities
function enterResultsMode() {
  pipelineDepth = 0;
  // Snapshot the execution tree
  if (debugTreeRoots.value.length > 0) {
    captureSnapshot(debugTreeRoots.value, lastRunQuery.value, lastRunDebug.value);
  }
  resetDebugState();
  debugCardNodeId.value = null;
  // Stop all edge animations, clear dim/active classes, but KEEP exec tree
  edges.value = edges.value.map(e => ({
    ...e,
    animated: false,
    class: (String(e.class ?? '')).replace(/design-dim|exec-active/g, '').trim() || undefined,
  }));
  // Freeze composable so rebuild() won't clear exec nodes when canvasMode goes to 'design'
  freezeExecTree();
  // Freeze exec tree nodes visually (mark as completed, non-animated)
  nodes.value = nodes.value.map(n => {
    if (n.id.startsWith('exec_')) {
      return { ...n, class: `${n.class ?? ''} exec-frozen`.trim() };
    }
    return n;
  });
  canvasMode.value = 'design';
  showResults.value = true;
  monitorCardNodeId.value = null;
  // Restore panels so user can edit while viewing results
  hierarchyPanelVisible.value = true;
  editorPanelVisible.value = true;
  resetRunState();
  // Fit view to show design + call tree (don't dagre-relayout exec nodes)
  nextTick(() => setTimeout(() => {
    fitView({ padding: 0.1 });
  }, 200));
}

/** ELK layout ONLY exec nodes — design nodes stay in place, edges get orthogonal routing */
function relayoutWithExecNodes() {
  // Wait for VueFlow to render exec nodes so we get actual dimensions.
  // First nextTick lets Vue add the DOM elements; the 150ms delay lets
  // VueFlow measure them and populate node.dimensions.
  nextTick(() => setTimeout(async () => {
    // Separate exec nodes from design nodes
    const execOnly = nodes.value.filter(n => n.id.startsWith('exec_') && !n.parentNode);
    const execEdgesOnly = edges.value.filter(e => e.id.startsWith('exec_edge_'));

    if (execOnly.length === 0) {
      nextTick(() => fitView({ padding: 0.1 }));
      return;
    }

    // Get live rendered dimensions for exec nodes
    const liveExec = execOnly.map(n => {
      const live = vfFindNode(n.id);
      return live ?? n;
    });

    const { positions, edgeRoutes } = await computeLayout(liveExec, execEdgesOnly);
    if (positions.size === 0) {
      nextTick(() => fitView({ padding: 0.1 }));
      return;
    }

    // Offset ELK positions to sit right of design nodes
    const designBounds = nodes.value
      .filter(n => !n.id.startsWith('exec_') && !n.parentNode)
      .reduce((acc, n) => {
        const live = vfFindNode(n.id);
        const dim = (live as any)?.dimensions;
        const right = n.position.x + (dim?.width ?? 200);
        return { maxRight: Math.max(acc.maxRight, right), minTop: Math.min(acc.minTop, n.position.y) };
      }, { maxRight: 0, minTop: Infinity });

    const offsetX = designBounds.maxRight + 120;
    const offsetY = isFinite(designBounds.minTop) ? designBounds.minTop : 0;

    // Apply positions and routes in one batch to avoid intermediate states
    // where edges have routes but nodes haven't moved yet
    const newNodes = nodes.value.map(n => {
      const pos = positions.get(n.id);
      if (!pos) return n;
      return {
        ...n,
        position: { x: pos.x + offsetX, y: pos.y + offsetY },
        class: `${n.class ?? ''} node--transitioning`.trim(),
      };
    });

    const newEdges = edges.value.map(e => {
      const route = edgeRoutes.get(e.id);
      if (!route) return e;
      return {
        ...e,
        type: 'elk',
        data: {
          ...e.data,
          route: {
            startPoint: { x: route.startPoint.x + offsetX, y: route.startPoint.y + offsetY },
            bendPoints: route.bendPoints.map(bp => ({ x: bp.x + offsetX, y: bp.y + offsetY })),
            endPoint: { x: route.endPoint.x + offsetX, y: route.endPoint.y + offsetY },
          },
        },
      };
    });

    // Assign both at once so VueFlow renders nodes+edges in sync
    nodes.value = newNodes;
    edges.value = newEdges;

    nextTick(() => fitView({ padding: 0.1 }));
    setTimeout(() => {
      nodes.value = nodes.value.map(n => ({
        ...n,
        class: (String(n.class ?? '')).replace('node--transitioning', '').trim() || undefined,
      }));
    }, 400);
  }, 150));
}

/** Toggle a run snapshot on/off on the canvas (undoable) */
async function toggleRunSnapshot(snapId: string) {
  saveSnapshot(true);
  const isActive = selectedSnapshot.value?.id === snapId;
  if (isActive) {
    // Deselect — remove exec tree, go back to base design
    selectSnapshot(null);
    clearExecTree();
    nodes.value = nodes.value.filter(n => !n.id.startsWith('exec_'));
    edges.value = edges.value.filter(e => !e.id.startsWith('exec_edge_'));
    showResults.value = false;
    nextTick(() => relayoutDesign());
  } else {
    // Select — load this snapshot's exec tree
    selectSnapshot(snapId);
    const snap = runSnapshots.value.find(s => s.id === snapId);
    if (!snap) return;

    // Load tree data on demand if not yet loaded
    if (!snap.loaded && snap.sessionId) {
      const roots = await loadTreeForSnapshot(snap.sessionId);
      if (roots.length > 0) {
        snap.treeRoots = roots;
        snap.loaded = true;
        // Update summary with actual tree data
        const { buildTreeFromEvents: _ } = await import('../../composables/debugTreeBuilder');
        // summarizeTree is internal; recalc from tree
        let totalIter = 0, totalTools = 0, totalTokens = 0;
        function walk(n: any) {
          if (n.type === 'iteration') totalIter++;
          if (n.type === 'tool_call') totalTools++;
          if (n.tokens?.total_tokens) totalTokens += n.tokens.total_tokens;
          for (const c of n.children) walk(c);
        }
        roots.forEach(walk);
        snap.summary.totalIterations = totalIter;
        snap.summary.totalToolCalls = totalTools;
        if (totalTokens > 0) snap.summary.totalTokens = totalTokens;
      }
    }

    // Clear any existing exec nodes first
    nodes.value = nodes.value.filter(n => !n.id.startsWith('exec_'));
    edges.value = edges.value.filter(e => !e.id.startsWith('exec_edge_'));
    // Load snapshot tree and relayout
    loadExecFromSnapshot(snap.treeRoots);
    showResults.value = true;
    // Apply remembered view mode (tree or graph)
    if (execView.value === 'graph') {
      relayoutWithExecNodes();
    } else {
      nextTick(() => setTimeout(() => fitView({ padding: 0.1 }), 200));
    }
  }
}

/** Remove a single snapshot (undoable via Ctrl+Z) */
function handleRemoveSnapshot(id: string) {
  if (!confirm('Remove this run from history? (Ctrl+Z to undo)')) return;
  saveSnapshot(true);
  if (selectedSnapshot.value?.id === id) clearResults();
  removeSnapshot(id);
}

/** Clear all snapshots (undoable via Ctrl+Z) */
function confirmClearSnapshots() {
  if (!confirm('Clear all run history? (Ctrl+Z to undo)')) return;
  saveSnapshot(true);
  clearResults();
  clearSnapshots();
}

// Clear results overlay — wipe execution data from nodes and remove exec tree
function clearResults() {
  showResults.value = false;
  nodeTokenCounts.value = {};
  totalTokenCount.value = 0;
  // Remove frozen exec tree nodes/edges
  clearExecTree();
  nodes.value = nodes.value.filter(n => !n.id.startsWith('exec_'));
  edges.value = edges.value.filter(e => !e.id.startsWith('exec_edge_'));
  // Wipe execution data overlays from design nodes
  nodes.value = nodes.value.map(n => ({
    ...n,
    data: {
      ...n.data,
      _runStatus: null,
      _tokenCount: undefined,
      _iterationCount: undefined,
      _lastThought: undefined,
      _errorMessage: undefined,
    },
  }));
}

// Panel visibility — toggled by execution mode and user toggle
const hierarchyPanelVisible = ref(true);
const editorPanelVisible = ref(true);

const props = defineProps<{
  pendingInspect?: { yaml: string; projectId?: string } | null;
}>();

const emit = defineEmits<{
  'run-started': [yaml: string, query: string, timeout: number, workdir: string, debug: boolean, envVars: Record<string, string>];
  'inspect-consumed': [];
  'drill-down': [agentName: string, treeRoots?: DebugTreeNode[]];
}>();

const { enabled: wallpaperEnabled } = useWallpaperPrefs();

// Import Vue Flow styles
import '@vue-flow/core/dist/style.css';
import '@vue-flow/core/dist/theme-default.css';
import '@vue-flow/controls/dist/style.css';
import '@vue-flow/minimap/dist/style.css';

const { generateYaml, downloadYaml, parseYaml } = useYamlExport();
const { parseModularFiles, generateModularFiles, downloadModularZip } = useModularConfig();

// --- Live execution state (SSE stream → node highlights) ---
const selfUrl = ref(typeof window !== 'undefined' ? window.location.host : 'localhost:8080');
const baseUrlRef = computed(() => selfUrl.value);
const showTokenizer = ref(false);
const tokenizerQuery = ref('');
const tokenizerAgents = computed(() =>
  nodes.value
    .filter(n => n.type === 'agent')
    .map(n => n.data as AgentConfig)
);

// --- Self-contained run (no tab switch) ---
const { startRun, stopRun, runStatus: _runStatus, reset: resetRunState } = useAgentRun(baseUrlRef);
const { events: liveEvents, connect: connectSSE, clearEvents } = useEventStream();
const { treeRoots: debugTreeRoots, activeAgents, renderKey: debugRenderKey, reset: resetDebugTree } = useDebugTree(liveEvents);

// --- Project identity (declared early so snapshots can scope by it) ---
const LAST_PROJECT_KEY = 'rakitsu-last-project';
function loadLastProject(): { id: string; name: string } {
  try {
    const raw = localStorage.getItem(LAST_PROJECT_KEY);
    if (raw) return JSON.parse(raw);
  } catch { /* ignore */ }
  return { id: crypto.randomUUID(), name: 'My Agent System' };
}
const _lastProject = loadLastProject();
const projectName = ref(_lastProject.name);
const projectId = ref<string>(_lastProject.id);
const projectVersion = ref('1.0');
const projectDescription = ref('');
const projectInteractive = ref(false);

// Persist project identity for run history scoping across reloads
watch([projectId, projectName], ([id, name]) => {
  try { localStorage.setItem(LAST_PROJECT_KEY, JSON.stringify({ id, name })); } catch { /* ignore */ }
});

// --- Run snapshots (execution history scoped per project) ---
const { snapshots: runSnapshots, selectedSnapshot, capture: captureSnapshot, select: selectSnapshot, remove: removeSnapshot, clear: clearSnapshots, loadTreeForSnapshot } = useRunSnapshots(projectId);
const lastRunQuery = ref('');
const lastRunDebug = ref(false);

// Debug control (per-node breakpoints + pause/resume)
const {
  breakpoints,
  setBreakpoint,
  clearBreakpoint,
  isPaused,
  pausedAgent,
  pausedCheckpoint,
  pausedContext,
  handleEvent: handleDebugEvent,
  resume: debugResume,
  requestPause: debugPause,
  resetDebugState,
} = useDebugControl(baseUrlRef);

// Which node's DebugCard is open
const debugCardNodeId = ref<string | null>(null);

// Open DebugCard when paused — find the node by agent name, auto-switch to debug mode
watch(isPaused, (paused) => {
  if (paused && pausedAgent.value) {
    // Auto-switch from monitor to debug when breakpoint hits
    if (canvasMode.value === 'monitor') {
      canvasMode.value = 'debug';
    }
    const node = nodes.value.find(
      n => (n.type === 'agent' || n.type === 'orchestrator') &&
           (n.data as AgentConfig).name === pausedAgent.value
    );
    debugCardNodeId.value = node?.id ?? null;
  } else {
    debugCardNodeId.value = null;
  }
});
// Also reopen DebugCard when pausedCheckpoint changes while still paused (e.g. after stepping)
watch(pausedCheckpoint, () => {
  if (isPaused.value && pausedAgent.value && canvasMode.value === 'debug') {
    const node = nodes.value.find(
      n => (n.type === 'agent' || n.type === 'orchestrator') &&
           (n.data as AgentConfig).name === pausedAgent.value
    );
    debugCardNodeId.value = node?.id ?? null;
  }
});

// toggleBreakpoint — provided to all node components
async function toggleBreakpoint(agentName: string) {
  const existing = breakpoints.value.find(b => b.agent_name === agentName);
  if (existing) {
    await clearBreakpoint(existing.event_type, agentName);
  } else {
    // Default: break before each LLM call (most useful checkpoint)
    await setBreakpoint('pre_thought', agentName);
    // Auto-attach debug if currently monitoring a live run
    if (canvasMode.value === 'monitor') {
      canvasMode.value = 'debug';
    }
  }
}
provide('toggleBreakpoint', toggleBreakpoint);
provide('breakpoints', breakpoints);
provide('setBreakpoint', setBreakpoint);
provide('clearBreakpoint', clearBreakpoint);
provide('liveEvents', liveEvents);

// --- Debug detail panel (bottom drawer) ---

// Tool-node breakpoint: sets pre_tool on the parent agent(s) that reference this tool
async function toggleToolBreakpoint(toolName: string) {
  // Find agent(s) connected to this tool via edges
  const toolNode = nodes.value.find(n => n.type === 'tool' && (n.data as ToolConfig).name === toolName);
  if (!toolNode) return;
  // Edges where target is the tool node → source is the parent agent
  const parentEdges = edges.value.filter(e => e.target === toolNode.id);
  const parentAgentNames: string[] = [];
  for (const e of parentEdges) {
    const srcNode = nodes.value.find(n => n.id === e.source);
    if (srcNode && (srcNode.type === 'agent' || srcNode.type === 'orchestrator')) {
      const name = (srcNode.data as AgentConfig).name;
      if (name) parentAgentNames.push(name);
    }
  }
  // If no parent found, set wildcard breakpoint
  const agents = parentAgentNames.length > 0 ? parentAgentNames : ['*'];
  // Check if breakpoint already exists for this tool
  const existing = breakpoints.value.find(b => b.event_type === 'pre_tool' && agents.includes(b.agent_name));
  if (existing) {
    for (const name of agents) await clearBreakpoint('pre_tool', name);
  } else {
    for (const name of agents) await setBreakpoint('pre_tool', name);
    if (canvasMode.value === 'monitor') canvasMode.value = 'debug';
  }
}
provide('toggleToolBreakpoint', toggleToolBreakpoint);

// Map agentName → run status
const agentStatusMap = computed(() => {
  const map = new Map<string, 'running' | 'paused'>();
  for (const a of activeAgents.value) {
    map.set(a.agentName, a.status === 'paused' ? 'paused' : 'running');
  }
  return map;
});

// Execution overlay accumulators (reset on returnToDesign)
const nodeTokenCounts = ref<Record<string, number>>({});
const totalTokenCount = ref(0);

// Which node's MonitorCard is open (null = none)
const monitorCardNodeId = ref<string | null>(null);

// Exec detail panel: shows DetailPanel for a clicked exec node
const execDetailNodeId = ref<string | null>(null);

function findTreeNode(execNodeId: string): DebugTreeNode | null {
  const treeId = execNodeId.replace('exec_', '');
  const roots = selectedSnapshot.value?.treeRoots ?? debugTreeRoots.value;
  function search(node: DebugTreeNode): DebugTreeNode | null {
    if (node.id === treeId) return node;
    for (const child of node.children) {
      const found = search(child);
      if (found) return found;
    }
    return null;
  }
  for (const root of roots) {
    const found = search(root);
    if (found) return found;
  }
  return null;
}

const execDetailNode = computed<DebugTreeNode | null>(() => {
  if (!execDetailNodeId.value) return null;
  return findTreeNode(execDetailNodeId.value);
});

// Track active pipeline depth — individual EXECUTION_COMPLETE events from pipeline
// step agents must not exit monitor mode while the pipeline is still running.
let pipelineDepth = 0;

// Process SSE events → write execution overlay data to nodes/edges.
// Uses `liveEvents.value.length` as the watch key because liveEvents is mutated in-place.
watch(
  () => liveEvents.value.length,
  () => {
    const evt = liveEvents.value[liveEvents.value.length - 1];
    if (!evt) return;

    if (evt.event_type === 'PIPELINE_START') {
      pipelineDepth++;
    } else if (evt.event_type === 'PIPELINE_END') {
      pipelineDepth = Math.max(0, pipelineDepth - 1);
      if (pipelineDepth === 0) {
        enterResultsMode();
        return;
      }
    } else if (evt.event_type === 'ERROR') {
      pipelineDepth = 0;
      enterResultsMode();
      return;
    } else if (evt.event_type === 'EXECUTION_COMPLETE' && pipelineDepth === 0) {
      // Only exit monitor for a standalone (non-pipeline) agent completion
      enterResultsMode();
      return;
    }

    // Let debug controller process DEBUG_PAUSED / DEBUG_RESUMED
    handleDebugEvent(evt);

    if (canvasMode.value === 'design') return;

    const agentName = evt.agent_name;

    if (evt.event_type === 'AGENT_START') {
      // Animate outgoing edges from this agent's node + highlight execution path
      nextTick(() => {
        edges.value = edges.value.map(e => {
          const src = nodes.value.find(n => n.id === e.source);
          if (!src || (src.data as AgentConfig).name !== agentName) return e;
          return { ...e, animated: true, class: 'exec-active' };
        });
      });

    } else if (evt.event_type === 'AGENT_END') {
      const payload = evt.payload as { status?: string };
      const runStatus = payload.status === 'success' ? 'success' : 'error';
      // nextTick so this fires AFTER agentStatusMap clears _runStatus
      nextTick(() => {
        nodes.value = nodes.value.map(n => {
          if (n.type !== 'agent' && n.type !== 'orchestrator') return n;
          if ((n.data as AgentConfig).name !== agentName) return n;
          return { ...n, data: { ...n.data, _runStatus: runStatus } };
        });
        // Stop edge animation + remove execution highlight
        edges.value = edges.value.map(e => {
          const src = nodes.value.find(n => n.id === e.source);
          if (!src || (src.data as AgentConfig).name !== agentName) return e;
          return { ...e, animated: false, class: '' };
        });
      });

    } else if (evt.event_type === 'TOKEN_USAGE' && evt.token_usage) {
      const added = evt.token_usage.total_tokens;
      const prev = nodeTokenCounts.value[agentName] ?? 0;
      const next = prev + added;
      nodeTokenCounts.value = { ...nodeTokenCounts.value, [agentName]: next };
      totalTokenCount.value += added;
      nodes.value = nodes.value.map(n => {
        if (n.type !== 'agent') return n;
        if ((n.data as AgentConfig).name !== agentName) return n;
        return { ...n, data: { ...n.data, _tokenCount: next } };
      });

    } else if (evt.event_type === 'THOUGHT_END') {
      const reasoning = (evt.payload as { reasoning?: string }).reasoning ?? '';
      const preview = reasoning.length > 80 ? reasoning.slice(0, 80) + '…' : reasoning;
      nodes.value = nodes.value.map(n => {
        if (n.type !== 'agent') return n;
        if ((n.data as AgentConfig).name !== agentName) return n;
        return { ...n, data: { ...n.data, _lastThought: preview } };
      });
    }
  }
);

// Sync running/paused from debugTree — preserve terminal states (success/error) set above
watch(agentStatusMap, (statusMap) => {
  nodes.value = nodes.value.map((n) => {
    if (n.type !== 'agent') return n;
    const name = (n.data as AgentConfig).name;
    const live = statusMap.get(name);
    const cur = (n.data as AgentConfig & { _runStatus?: string })._runStatus;
    // Don't wipe terminal states written by AGENT_END watcher
    if (!live && (cur === 'success' || cur === 'error')) return n;
    return { ...n, data: { ...n.data, _runStatus: live ?? null } };
  });
});

watch(breakpoints, (bps) => {
  // Pre-compute which agents have pre_tool breakpoints
  const preToolAgents = new Set(bps.filter(b => b.event_type === 'pre_tool').map(b => b.agent_name));
  nodes.value = nodes.value.map((n) => {
    if (n.type === 'agent' || n.type === 'orchestrator') {
      const name = (n.data as AgentConfig).name;
      const hasBreakpoint = bps.some((b) => b.agent_name === name || b.agent_name === '*');
      return { ...n, data: { ...n.data, _hasBreakpoint: hasBreakpoint } };
    }
    if (n.type === 'tool') {
      // Tool has breakpoint if any parent agent has pre_tool breakpoint
      const parentEdges = edges.value.filter(e => e.target === n.id);
      const hasBreakpoint = preToolAgents.has('*') || parentEdges.some(e => {
        const src = nodes.value.find(s => s.id === e.source);
        return src && preToolAgents.has((src.data as AgentConfig).name);
      });
      return { ...n, data: { ...n.data, _hasBreakpoint: hasBreakpoint } };
    }
    return n;
  });
}, { deep: true });

// --- Inspect from history: load config into canvas (watcher at end of setup) ---

// --- Re-fit VueFlow when the tab becomes active (DOM reinserted by keep-alive) ---
onActivated(async () => {
  await nextTick();
  fitView();
});

// --- Node context menu ---
interface ContextMenu {
  x: number;
  y: number;
  agentName: string;
  nodeId: string;
  nodeType: string;
  parentNode?: string;
}
const contextMenu = ref<ContextMenu | null>(null);

// Clipboard for copy/paste node settings
const nodeClipboard = ref<{ type: string; data: Record<string, unknown> } | null>(null);

function handleNodeContextMenu(event: { node: Node; event: MouseEvent | TouchEvent }) {
  event.event.preventDefault();
  const clientX = event.event instanceof MouseEvent ? event.event.clientX : (event.event as TouchEvent).touches[0]?.clientX ?? 0;
  const clientY = event.event instanceof MouseEvent ? event.event.clientY : (event.event as TouchEvent).touches[0]?.clientY ?? 0;
  contextMenu.value = {
    x: clientX,
    y: clientY,
    agentName: (event.node.data as { name?: string }).name || event.node.id,
    nodeId: event.node.id,
    nodeType: event.node.type || '',
    parentNode: event.node.parentNode as string | undefined,
  };
}

function closeContextMenu() {
  contextMenu.value = null;
}

async function addBreakpointForNode(checkpoint: string) {
  if (!contextMenu.value) return;
  await setBreakpoint(checkpoint, contextMenu.value.agentName);
  contextMenu.value = null;
}

async function clearBreakpointsForNode() {
  if (!contextMenu.value) return;
  const name = contextMenu.value.agentName;
  const toRemove = breakpoints.value.filter((b) => b.agent_name === name);
  for (const b of toRemove) {
    await clearBreakpoint(b.event_type, name);
  }
  contextMenu.value = null;
}

const nodes = ref<Node[]>([]);
const edges = ref<Edge[]>([]);

// Late-bound VueFlow findNode (assigned after useVueFlow)
let vfFindNode: (id: string) => Node | undefined = () => undefined;

// --- Canvas execution tree (debug detail nodes) ---
const {
  execNodes, execEdges, expandedAgents: execExpandedAgents,
  toggleExpand: toggleExecExpand, expandAll: expandAllExec, collapseAll: collapseAllExec,
  clearExecTree, loadFromSnapshot: loadExecFromSnapshot,
  freeze: freezeExecTree, relayoutTree,
} = useCanvasExecutionTree(debugTreeRoots, nodes, canvasMode, debugRenderKey, (id) => vfFindNode(id));

// Exec view mode: 'tree' (call tree) or 'graph' (dagre auto-layout)
const execView = ref<'tree' | 'graph'>('tree');

function setExecView(view: 'tree' | 'graph') {
  execView.value = view;
  if (view === 'tree') {
    // relayoutTree rebuilds execNodes/execEdges → watchers sync to main nodes/edges.
    relayoutTree();
    nextTick(() => {
      // Force new array references so VueFlow picks up position changes.
      // VueFlow caches node positions internally; in-place element updates
      // (from the execNodes watcher) don't trigger a re-render.
      nodes.value = nodes.value.map(n => ({ ...n }));
      edges.value = edges.value.map(e =>
        e.type === 'elk' ? { ...e, type: 'smoothstep', data: { ...e.data, route: undefined } } : e
      );
      setTimeout(() => fitView({ padding: 0.1 }), 200);
    });
  } else {
    relayoutWithExecNodes();
  }
}
provide('toggleExecExpand', toggleExecExpand);
provide('execExpandedAgents', execExpandedAgents);

// Merged node types: design + exec
const mergedNodeTypes = { ...nodeTypes, ...execNodeTypes };
const edgeTypes = { elk: markRaw(ElkEdge), 'rakitsu-exec': markRaw(RakitsuExecEdge) };

// Sync exec nodes into canvas imperatively
watch(execNodes, (newExecNodes, oldExecNodes) => {
  // Build a NEW array to ensure VueFlow detects the change.
  // In-place .push() doesn't change the array reference — VueFlow misses it.
  const oldIds = new Set((oldExecNodes ?? []).map(n => n.id));
  const newIds = new Set(newExecNodes.map(n => n.id));
  const toRemove = [...oldIds].filter(id => !newIds.has(id));

  let updated = toRemove.length > 0
    ? nodes.value.filter(n => !toRemove.includes(n.id))
    : nodes.value.slice();

  for (const en of newExecNodes) {
    const idx = updated.findIndex(n => n.id === en.id);
    if (idx >= 0) {
      updated[idx] = { ...updated[idx]!, ...en };
    } else {
      updated.push(en);
    }
  }

  nodes.value = updated;
}, { deep: true });

// Sync exec edges — preserve ELK routes if graph view is active
watch(execEdges, (newExecEdges) => {
  if (execView.value === 'graph') {
    // In graph mode, keep existing elk-typed edges (they have route data).
    // Only add new edges that don't already exist with routes.
    const existing = new Map(
      edges.value.filter(e => e.id.startsWith('exec_edge_') && e.type === 'elk')
        .map(e => [e.id, e])
    );
    const merged = newExecEdges.map(e => existing.get(e.id) ?? e);
    edges.value = [
      ...edges.value.filter(e => !e.id.startsWith('exec_edge_')),
      ...merged,
    ];
  } else {
    edges.value = [
      ...edges.value.filter(e => !e.id.startsWith('exec_edge_')),
      ...newExecEdges,
    ];
  }
});

const isDirty = ref(false);

// Auto-fit groups
const { scheduleAutoFit, autoFitAllGroups } = useGroupAutoFit(nodes, (id) => vfFindNode(id));

// Undo/Redo
// useUndoRedo initialized after projectSettings (see below)

// Hierarchy panel
const showHierarchy = ref(false);

// Canvas search (Cmd+F)
const {
  searchOpen, searchQuery, searchField,
  matchedNodeIds, matchCount, currentMatchIndex,
  nextResult, prevResult, toggleSearch, closeSearch,
} = useCanvasSearch(nodes);

const { onConnect, addNodes, onNodeClick, onNodeDoubleClick, addEdges: addNewEdges, updateNodeData, fitView, getSelectedNodes, removeNodes, findNode, dimensions } = useVueFlow();
vfFindNode = findNode;

// Only render MiniMap/Background when VueFlow has measured real container dimensions (prevents NaN SVG errors)
const miniMapReady = computed(() => dimensions.value.width > 0 && dimensions.value.height > 0);

// Node editor state
const showEditor = ref(false);
const selectedNodeRef = ref<Node | null>(null);

// --- Scope-based selection (like Maya/Figma) ---
// selectionScope: null = canvas root, string = group node ID we're "inside"
const selectionScope = ref<string | null>(null);
// Compact mode: hide children via CSS class on Vue Flow node wrapper.
// Vue Flow applies node.class to the wrapper div — 'compact-hidden' sets display:none.
const compactGroupIds = new Set<string>();
const fullViewGroupId = ref<string | null>(null);

// Re-layout siblings inside a parent to close gaps (used after compact/expand)
function rearrangeSiblings(parentId: string) {
  const siblings = nodes.value
    .filter(c => c.parentNode === parentId && !((c.class as string) || '').includes('compact-hidden'))
    .sort((a, b) => a.position.x - b.position.x || a.position.y - b.position.y);
  if (siblings.length === 0) return;

  const PAD = 40;
  const HEADER = 50;
  let x = 20;
  for (const sib of siblings) {
    sib.position = { x, y: HEADER };
    // Read actual rendered width from VueFlow, fall back to style, then default
    const live = vfFindNode(sib.id);
    const renderedW = (live as any)?.dimensions?.width;
    const style = sib.style as Record<string, string> | undefined;
    const styleW = style?.width ? parseInt(style.width) : 0;
    const w = renderedW > 0 ? renderedW : (styleW > 0 ? styleW : 220);
    x += w + PAD;
  }
}

// Collect ALL descendants recursively
function collectAllDescendantIds(parentId: string): string[] {
  const ids: string[] = [];
  for (const n of nodes.value) {
    if (n.parentNode === parentId) {
      ids.push(n.id);
      ids.push(...collectAllDescendantIds(n.id));
    }
  }
  return ids;
}

// Detect viewMode changes from GroupNode (mutates data directly)
setInterval(() => {
  for (const n of nodes.value) {
    if (n.type !== 'group') continue;
    const data = n.data as GroupConfig;
    const mode = data.viewMode || 'mixed';
    const wasCompact = compactGroupIds.has(n.id);
    const isCompact = mode === 'compact';
    const isFull = mode === 'full';

    // Handle compact toggle (save snapshot for undo on mode change)
    if (isCompact && !wasCompact) {
      saveSnapshot(true);
      compactGroupIds.add(n.id);
      const descendantIds = new Set(collectAllDescendantIds(n.id));
      for (const c of nodes.value) {
        if (descendantIds.has(c.id)) c.class = 'compact-hidden';
      }
      (data as any)._childCount = nodes.value.filter(c => c.parentNode === n.id).length;
      // Hide edges that connect to/from hidden descendants
      edges.value = edges.value.map(e => {
        if (descendantIds.has(e.source) || descendantIds.has(e.target)) {
          return { ...e, class: `${e.class ?? ''} compact-hidden`.trim() };
        }
        return e;
      });
      // Shrink group to compact size + rearrange siblings + re-fit parent
      n.style = { width: '200px', height: '44px' };
      if (n.parentNode) {
        nextTick(() => { rearrangeSiblings(n.parentNode!); scheduleAutoFit(n.parentNode!); });
      }
      if (fullViewGroupId.value === n.id) fullViewGroupId.value = null;
      // Relayout whole canvas after compact shrink (groups changed size)
      nextTick(() => setTimeout(() => relayoutDesign(), 150));
    } else if (!isCompact && wasCompact) {
      saveSnapshot(true);
      compactGroupIds.delete(n.id);
      // Unhide ALL nodes not belonging to another compact group
      for (const c of nodes.value) {
        const inOtherCompact = c.parentNode && compactGroupIds.has(c.parentNode);
        if (!inOtherCompact) {
          c.class = ((c.class as string) || '').replace('compact-hidden', '').trim();
        }
      }
      // Restore edges (except those still hidden by other compact groups)
      edges.value = edges.value.map(e => {
        const srcInCompact = nodes.value.some(nd => nd.id === e.source && compactGroupIds.has(nd.parentNode ?? ''));
        const tgtInCompact = nodes.value.some(nd => nd.id === e.target && compactGroupIds.has(nd.parentNode ?? ''));
        if (!srcInCompact && !tgtInCompact) {
          return { ...e, class: ((e.class as string) ?? '').replace('compact-hidden', '').trim() || undefined };
        }
        return e;
      });
      // Sequence: auto-fit self → rearrange siblings → auto-fit parent → relayout canvas
      nextTick(() => {
        scheduleAutoFit(n.id);
        if (n.parentNode) {
          const pid = n.parentNode;
          nextTick(() => { rearrangeSiblings(pid); nextTick(() => scheduleAutoFit(pid)); });
        }
        // Relayout whole canvas after expand (groups changed size)
        setTimeout(() => relayoutDesign(), 200);
      });
    }

    // Handle full view toggle
    if (isFull && fullViewGroupId.value !== n.id) {
      saveSnapshot(true);
      fullViewGroupId.value = n.id;
      const memberIds = new Set([n.id, ...collectAllDescendantIds(n.id)]);
      // Keep ancestors visible
      let parent = n.parentNode;
      while (parent) { memberIds.add(parent); parent = nodes.value.find(p => p.id === parent)?.parentNode; }
      for (const c of nodes.value) {
        if (!memberIds.has(c.id)) c.class = 'compact-hidden';
      }
      // Hide edges that connect to/from hidden nodes
      edges.value = edges.value.map(e => {
        const srcVisible = memberIds.has(e.source);
        const tgtVisible = memberIds.has(e.target);
        if (!srcVisible || !tgtVisible) {
          return { ...e, class: `${e.class ?? ''} compact-hidden`.trim() };
        }
        return e;
      });
      nextTick(() => fitView({ padding: 0.2 }));
    } else if (!isFull && fullViewGroupId.value === n.id) {
      saveSnapshot(true);
      fullViewGroupId.value = null;
      // Unhide all (except nodes in other compact groups)
      for (const c of nodes.value) {
        if (!compactGroupIds.has(c.parentNode ?? '')) {
          c.class = ((c.class as string) || '').replace('compact-hidden', '').trim();
        }
      }
      // Restore hidden edges
      edges.value = edges.value.map(e => ({
        ...e,
        class: ((e.class as string) ?? '').replace('compact-hidden', '').trim() || undefined,
      }));
      // Relayout canvas after leaving full view
      nextTick(() => setTimeout(() => relayoutDesign(), 150));
    }
  }
}, 200);

// Breadcrumb: chain of parent groups from root to current scope
const scopeBreadcrumb = computed(() => {
  const crumbs: { id: string | null; name: string }[] = [{ id: null, name: 'Canvas' }];
  let current = selectionScope.value;
  const chain: { id: string; name: string }[] = [];
  while (current) {
    const node = nodes.value.find(n => n.id === current);
    if (!node) break;
    chain.unshift({ id: current, name: (node.data as { name?: string }).name || current });
    current = node.parentNode ?? null;
  }
  return [...crumbs, ...chain];
});

// Group nodes at current scope level (for "Enter" buttons)
const canvasGroupNodes = computed(() =>
  nodes.value.filter(n => n.type === 'group' && (n.parentNode ?? null) === selectionScope.value)
);

// Selected nodes filtered to current scope
const selectedNodes = computed(() =>
  getSelectedNodes.value.filter(n => (n.parentNode ?? null) === selectionScope.value)
);
const isMultiSelect = computed(() => selectedNodes.value.length > 1);
const selectedNode = computed(() => {
  if (selectedNodes.value.length === 1) return selectedNodes.value[0];
  return selectedNodeRef.value;
});

// Enter a group scope (double-click)
function enterGroupScope(groupId: string) {
  selectionScope.value = groupId;
  // Deselect all nodes outside this scope
  nodes.value = nodes.value.map(n => ({
    ...n,
    selected: false,
  }));
  selectedNodeRef.value = null;
}

// Exit to a parent scope (breadcrumb click or Escape)
function exitToScope(scopeId: string | null) {
  selectionScope.value = scopeId;
  nodes.value = nodes.value.map(n => ({
    ...n,
    selected: false,
  }));
  selectedNodeRef.value = null;
  showEditor.value = false;
}

// File input refs for import
const fileInput = ref<HTMLInputElement | null>(null);
const dirInput = ref<HTMLInputElement | null>(null);

// Dropdown menu state
const showImportMenu = ref(false);
const showExportMenu = ref(false);

// Settings state
const showSettings = ref(false);
const projectSettings = ref<Settings>(createDefaultSettings());

// Undo/Redo (after projectSettings is defined)
const { canUndo, canRedo, saveSnapshot, undo, redo, clearHistory: _clearHistory, registerAux } = useUndoRedo(nodes, edges, projectSettings as any);

// Register run snapshots as auxiliary state for undo/redo
registerAux('runSnapshots', {
  capture: () => JSON.stringify({
    selected: selectedSnapshot.value?.id ?? null,
  }),
  restore: (data: string) => {
    const parsed = JSON.parse(data);
    selectSnapshot(parsed.selected);
  },
});
registerAux('showResults', {
  capture: () => JSON.stringify(showResults.value),
  restore: (data: string) => { showResults.value = JSON.parse(data); },
});

// Track dirty state on any canvas or settings change
watch([nodes, edges, projectSettings, projectName], () => { isDirty.value = true; }, { deep: true });

function createDefaultSettings(): Settings {
  return {
    default_provider: '',
    providers: {},
    api_keys: {},
    base_urls: {},
    credentials_files: {},
    allowed_commands: [],
    defaults: { model: '', temperature: 0, max_tokens: 0 },
    execution: { max_iterations: 0, timeout_seconds: 0, retry_attempts: 0 },
    logging: { level: '', file: '' },
  };
}

function handleSettingsUpdate(settings: Settings) {
  saveSnapshot();
  projectSettings.value = settings;

  // Propagate all inherited field changes to agent/orchestrator nodes
  const newProviders = settings.providers || {};
  nodes.value = nodes.value.map(n => {
    if (n.type !== 'agent' && n.type !== 'orchestrator') return n;
    const data = n.data as Record<string, unknown> & { provider?: string; _providerId?: string; _inherited?: Record<string, boolean>; model?: string; model_config?: Record<string, unknown>; settings?: Record<string, unknown> };
    if (!data._inherited) return n;
    let changed = false;
    const updated = { ...n.data } as typeof data;

    // Model ← provider.default_model
    if (data._inherited.model !== false) {
      const provName = data.provider ?? '';
      const provDef = Object.entries(newProviders).find(([name, def]) => def._id === data._providerId || name === provName)?.[1];
      if (provDef?.default_model && data.model !== provDef.default_model) {
        updated.model = provDef.default_model;
        if (!updated._inherited) updated._inherited = {};
        updated._inherited.model = true;
        changed = true;
      }
    }

    // Temperature ← settings.defaults.temperature
    if (data._inherited.temperature !== false && settings.defaults?.temperature !== undefined) {
      const cur = (data.model_config as Record<string, unknown> | undefined)?.temperature;
      if (cur !== settings.defaults.temperature) {
        updated.model_config = { ...updated.model_config, temperature: settings.defaults.temperature };
        changed = true;
      }
    }

    // Max tokens ← settings.defaults.max_tokens
    if (data._inherited.max_tokens !== false && settings.defaults?.max_tokens !== undefined) {
      const cur = (data.model_config as Record<string, unknown> | undefined)?.max_tokens;
      if (cur !== settings.defaults.max_tokens) {
        updated.model_config = { ...updated.model_config, max_tokens: settings.defaults.max_tokens };
        changed = true;
      }
    }

    // Max iterations ← settings.execution.max_iterations
    if (data._inherited.max_iterations !== false && settings.execution?.max_iterations !== undefined) {
      const cur = (data.settings as Record<string, unknown> | undefined)?.max_iterations;
      if (cur !== settings.execution.max_iterations) {
        updated.settings = { ...updated.settings, max_iterations: settings.execution.max_iterations };
        changed = true;
      }
    }

    // Timeout ← settings.execution.timeout_seconds
    if (data._inherited.timeout !== false && settings.execution?.timeout_seconds !== undefined) {
      const cur = (data.settings as Record<string, unknown> | undefined)?.timeout;
      if (cur !== settings.execution.timeout_seconds) {
        updated.settings = { ...updated.settings, timeout: settings.execution.timeout_seconds };
        changed = true;
      }
    }

    return changed ? { ...n, data: updated } : n;
  });
}

// When a provider is renamed, update all agent/orchestrator nodes that reference it
function handleProviderRenamed(providerId: string, oldName: string, newName: string) {
  saveSnapshot();
  nodes.value = nodes.value.map(n => {
    if (n.type === 'agent' || n.type === 'orchestrator') {
      const data = n.data as { provider?: string; _providerId?: string };
      // Match by _providerId (stable) OR by old name (fallback for nodes without ID)
      if (data._providerId === providerId || data.provider === oldName) {
        return {
          ...n,
          data: { ...n.data, provider: newName, _providerId: providerId },
        };
      }
    }
    return n;
  });
}

// Apply a provider (and its default model) to ALL agent/orchestrator nodes
function handleApplyProviderToAll(providerName: string, defaultModel: string) {
  saveSnapshot();
  const provDef = projectSettings.value.providers?.[providerName];
  const providerId = provDef?._id;
  let count = 0;
  nodes.value = nodes.value.map(n => {
    if (n.type === 'agent' || n.type === 'orchestrator') {
      const data = n.data as Record<string, unknown>;
      const updates: Record<string, unknown> = { provider: providerName };
      if (providerId) updates._providerId = providerId;
      if (defaultModel) {
        updates.model = defaultModel;
        updates._inherited = { ...(data._inherited as Record<string, boolean> | undefined), model: true };
      }
      count++;
      return { ...n, data: { ...data, ...updates } };
    }
    return n;
  });
  if (count > 0) {
    isDirty.value = true;
    // Re-layout after VueFlow re-renders with new (potentially wider) content
    setTimeout(() => relayoutDesign(), 150);
  }
}

// Sync wires when chip buttons are toggled in NodeEditor
function handleWireToggle(sourceId: string, targetName: string, targetType: string, connect: boolean) {
  saveSnapshot();
  // Find target node by name and type
  const targetNode = nodes.value.find(n =>
    n.type === targetType && (n.data as { name?: string }).name === targetName
  );
  if (!targetNode) return;

  if (connect) {
    // Check if edge already exists
    const exists = edges.value.some(e => e.source === sourceId && e.target === targetNode.id);
    if (!exists) {
      addNewEdges([{
        id: `edge-${Date.now()}-${Math.random()}`,
        source: sourceId,
        target: targetNode.id,
        animated: true,
      }]);
    }
  } else {
    // Remove edge between source and target
    edges.value = edges.value.filter(e =>
      !(e.source === sourceId && e.target === targetNode.id)
    );
    // Clear _connectedGroupId when orchestrator disconnects from group
    const sourceNode = nodes.value.find(n => n.id === sourceId);
    if (sourceNode?.type === 'orchestrator' && targetNode.type === 'group') {
      const orchData = sourceNode.data as OrchestratorConfig;
      orchData._connectedGroupId = undefined;
      updateNodeData(sourceId, orchData, { replace: true });
    }
  }
}

// Reorder children inside a group — re-layouts canvas positions to match new order
function handleReorderChildren(groupId: string, orderedChildIds: string[]) {
  saveSnapshot();
  if (orderedChildIds.length === 0) return;

  const NODE_W = 220;
  const PAD_X = 40;
  const GROUP_PAD = 20;
  const GROUP_HEADER = 50;

  // Re-layout children left-to-right in the new order
  let x = GROUP_PAD;
  for (const childId of orderedChildIds) {
    const node = nodes.value.find(n => n.id === childId);
    if (!node) continue;
    node.position = { x, y: GROUP_HEADER };
    // Use node's current width if it's a group, else default
    const style = node.style as Record<string, string> | undefined;
    const w = style?.width ? parseInt(style.width) : NODE_W;
    x += w + PAD_X;
  }

  nextTick(() => scheduleAutoFit(groupId));
  // Sync pipeline steps to connected orchestrator
  const rootGroup = findRootGroup(groupId);
  if (rootGroup) syncGroupToOrchestrator(rootGroup);
}

function toggleSettings() {
  showSettings.value = !showSettings.value;
  // Close node editor when opening settings
  if (showSettings.value) {
    showEditor.value = false;
    selectedNodeRef.value = null;
  }
}

// Get group step defaults for an orchestrator's connected pipeline group
function getConnectedGroupDefaults(orchNode: typeof nodes.value[number]): Record<string, import('../../types').GroupStepConfig> {
  const orchData = orchNode.data as OrchestratorConfig;
  if (!orchData._connectedGroupId) return {};
  const groupNode = nodes.value.find(n =>
    n.type === 'group' && ((n.data as GroupConfig)._id === orchData._connectedGroupId || n.id === orchData._connectedGroupId)
  );
  return (groupNode?.data as GroupConfig)?.stepDefaults || {};
}

// Canvas entity names — passed to NodeEditor for connected selectors
const canvasToolNames = computed(() =>
  nodes.value.filter(n => n.type === 'tool').map(n => (n.data as ToolConfig).name).filter(Boolean)
);
const canvasAgentNames = computed(() =>
  nodes.value.filter(n => n.type === 'agent' || n.type === 'orchestrator').map(n => (n.data as AgentConfig).name).filter(Boolean)
);
// All names across all node types — for global uniqueness checks
const canvasAllNames = computed(() =>
  nodes.value
    .filter(n => ['agent', 'tool', 'skill', 'orchestrator', 'group'].includes(n.type || ''))
    .map(n => (n.data as { name?: string }).name)
    .filter(Boolean) as string[]
);

// Generate a unique name by appending _2, _3, etc.
function uniqueName(baseName: string): string {
  const existing = new Set(canvasAllNames.value);
  if (!existing.has(baseName)) return baseName;
  // Strip existing suffix (_2, _3, etc.)
  const stripped = baseName.replace(/_\d+$/, '');
  let i = 2;
  while (existing.has(`${stripped}_${i}`)) i++;
  return `${stripped}_${i}`;
}
const canvasSkillNames = computed(() =>
  nodes.value.filter(n => n.type === 'skill').map(n => (n.data as SkillConfig).name).filter(Boolean)
);

// Merge flat maps + named provider definitions for NodeEditor
const mergedBaseUrls = computed(() => {
  const map: Record<string, string> = { ...(projectSettings.value.base_urls ?? {}) };
  for (const [name, def] of Object.entries(projectSettings.value.providers ?? {})) {
    if (def.base_url && !map[name]) map[name] = def.base_url;
  }
  return map;
});
const mergedApiKeys = computed(() => {
  const map: Record<string, string> = { ...(projectSettings.value.api_keys ?? {}) };
  for (const [name, def] of Object.entries(projectSettings.value.providers ?? {})) {
    if (def.api_key && !map[name]) map[name] = def.api_key;
  }
  return map;
});

// Run dialog
const showRunDialog = ref(false);
const runQuery = ref('');
const runTimeout = ref(300);
const runWorkdir = ref('');
const runEnvVars = ref<{ key: string; value: string }[]>([]);
const browsingDir = ref('');
const browseDirs = ref<string[]>([]);

async function browseWorkdir() {
  const startDir = runWorkdir.value || '';
  try {
    const res = await fetch(`http://${selfUrl.value}/api/browse?path=${encodeURIComponent(startDir)}`);
    const data = await res.json();
    browsingDir.value = data.path || '/';
    browseDirs.value = data.dirs || [];
  } catch { /* ignore */ }
}

async function browseInto(dir: string) {
  const newPath = browsingDir.value.endsWith('/') ? browsingDir.value + dir : browsingDir.value + '/' + dir;
  try {
    const res = await fetch(`http://${selfUrl.value}/api/browse?path=${encodeURIComponent(newPath)}`);
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
    const res = await fetch(`http://${selfUrl.value}/api/browse?path=${encodeURIComponent(parent)}`);
    const data = await res.json();
    browsingDir.value = data.path || parent;
    browseDirs.value = data.dirs || [];
  } catch { /* ignore */ }
}

// Fetch server's default workdir
fetch(`http://${selfUrl.value}/api/workdir`).then(r => r.json()).then((d: { workdir?: string }) => {
  if (d.workdir && !runWorkdir.value) runWorkdir.value = d.workdir;
}).catch(() => {});

function closeRunDialog() {
  showRunDialog.value = false;
}

function handleRunSubmit(debug = false) {
  if (!runQuery.value.trim()) return;
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  if (agents.length === 0) return;
  const yaml = generateYaml(projectName.value, projectId.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators, projectVersion.value, projectDescription.value, projectInteractive.value);
  resetDebugTree();
  clearEvents();
  connectSSE(`http://${selfUrl.value}`);
  const envMap: Record<string, string> = {};
  for (const e of runEnvVars.value) { if (e.key.trim()) envMap[e.key.trim()] = e.value; }
  enterExecutionMode(debug ? 'debug' : 'monitor');
  emit('run-started', yaml, runQuery.value.trim(), runTimeout.value, runWorkdir.value.trim(), debug, envMap);
  showRunDialog.value = false;
}

// Expose a pull-based hook so App.vue can grab the current canvas YAML when
// the user switches to the Chat tab. No push here — avoids spamming inline
// config uploads on every canvas edit.
function getCurrentCanvasYaml(): string | null {
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  if (agents.length === 0) return null;
  return generateYaml(projectName.value, projectId.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators, projectVersion.value, projectDescription.value, projectInteractive.value);
}
defineExpose({ getCurrentCanvasYaml });

// RunBar handlers — submit run directly from builder (no tab switch)
async function handleRunBarRun(q: string, t: number, wd: string, envMap: Record<string, string>) {
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  if (agents.length === 0) return;
  const yaml = generateYaml(projectName.value, projectId.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators, projectVersion.value, projectDescription.value, projectInteractive.value);
  resetDebugTree();
  clearEvents();
  showResults.value = false;
  lastRunQuery.value = q;
  lastRunDebug.value = false;
  connectSSE(`http://${selfUrl.value}`);
  enterExecutionMode('monitor');
  // Upload inline config + start run directly
  try {
    const base = `http://${selfUrl.value}`;
    const inlineRes = await fetch(`${base}/api/configs/inline`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ yaml }),
    });
    const inlineData = await inlineRes.json();
    if (inlineRes.ok && !inlineData.error) {
      await startRun(inlineData.id, q, t, wd, false, envMap, [], projectId.value);
    } else {
      console.error('[rakitsu] inline config upload failed:', inlineData.error);
      returnToDesign();
    }
  } catch (e) {
    console.error('[rakitsu] run start failed:', e);
    returnToDesign();
  }
}

async function handleRunBarDebug(q: string, t: number, wd: string, envMap: Record<string, string>) {
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  if (agents.length === 0) return;
  const yaml = generateYaml(projectName.value, projectId.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators, projectVersion.value, projectDescription.value, projectInteractive.value);
  resetDebugTree();
  clearEvents();
  showResults.value = false;
  lastRunQuery.value = q;
  lastRunDebug.value = true;
  connectSSE(`http://${selfUrl.value}`);
  enterExecutionMode('debug');
  // Upload inline config + start debug run with breakpoints
  try {
    const base = `http://${selfUrl.value}`;
    const inlineRes = await fetch(`${base}/api/configs/inline`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ yaml }),
    });
    const inlineData = await inlineRes.json();
    if (inlineRes.ok && !inlineData.error) {
      const bps = breakpoints.value.map(b => ({ event_type: b.event_type, agent_name: b.agent_name }));
      await startRun(inlineData.id, q, t, wd, true, envMap, bps, projectId.value);
    } else {
      console.error('[rakitsu] inline config upload failed:', inlineData.error);
      returnToDesign();
    }
  } catch (e) {
    console.error('[rakitsu] debug run start failed:', e);
    returnToDesign();
  }
}

// Stop: cancel the server-side run, then return to design mode
async function handleStop() {
  await stopRun();
  returnToDesign();
}

// Attach debugger to a running monitor-mode run
async function handleAttachDebugger() {
  const base = `http://${selfUrl.value}`;
  try {
    const bps = breakpoints.value.map(b => ({ event_type: b.event_type, agent_name: b.agent_name }));
    const res = await fetch(`${base}/api/debug/attach`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ breakpoints: bps }),
    });
    if (res.ok) {
      canvasMode.value = 'debug';
    } else {
      const data = await res.json().catch(() => ({ error: 'attach failed' }));
      console.error('[rakitsu] attach debugger failed:', data.error);
    }
  } catch (e) {
    console.error('[rakitsu] attach debugger error:', e);
  }
}

// --- Debug detail panel handlers ---

// --- Pipeline sync helpers (shared by onConnect + reactive triggers) ---

// Recursively collect all agent names from a group and its nested sub-groups
function collectAgentNames(groupId: string): string[] {
  const names: string[] = [];
  for (const n of nodes.value.filter(c => c.parentNode === groupId)) {
    if (n.type === 'agent') {
      const name = (n.data as { name?: string }).name;
      if (name) names.push(name);
    } else if (n.type === 'group') {
      names.push(...collectAgentNames(n.id));
    }
  }
  return names;
}

// Recursively build pipeline steps from group hierarchy
function buildStepsFromGroup(groupId: string, previousSteps?: PipelineStep[]): PipelineStep[] {
  const children = nodes.value
    .filter(n => n.parentNode === groupId)
    .sort((a, b) => a.position.y - b.position.y || a.position.x - b.position.x);

  const steps: PipelineStep[] = [];
  for (const child of children) {
    if (child.type === 'group') {
      const sub = child.data as GroupConfig;
      const subGroupId = sub._id || child.id;
      const prevStep = previousSteps?.find(s => s._groupId === subGroupId);
      const subSteps = buildStepsFromGroup(child.id, prevStep?.steps);
      if (sub.blockType === 'loop') {
        steps.push({
          name: sub.name, type: 'loop', _groupId: subGroupId, steps: subSteps,
          max_iterations: sub.maxIterations || 5,
          condition_agent: sub.conditionAgent || '',
          condition_prompt: sub.conditionPrompt || '',
          timeout_sec: prevStep?.timeout_sec,
        });
      } else if (sub.blockType === 'parallel') {
        steps.push({ name: sub.name, type: 'parallel', _groupId: subGroupId, steps: subSteps, timeout_sec: prevStep?.timeout_sec });
      } else if (sub.blockType === 'pipeline') {
        steps.push({ name: sub.name, type: 'sequential', _groupId: subGroupId, steps: subSteps, timeout_sec: prevStep?.timeout_sec });
      } else {
        steps.push(...subSteps);
      }
    } else if (child.type === 'agent') {
      const agentData = child.data as AgentConfig;
      const agentId = agentData._id || child.id;
      const prevStep = previousSteps?.find(s => s._agentId === agentId);
      // Read step defaults from the parent group's stepDefaults (source of truth for group editor)
      const parentGroup = nodes.value.find(n => n.id === groupId);
      const groupStepDef = (parentGroup?.data as GroupConfig)?.stepDefaults?.[agentId];
      steps.push({
        name: agentData.name,
        agent: agentData.name,
        _agentId: agentId,
        task: prevStep?.task ?? groupStepDef?.task,
        timeout_sec: prevStep?.timeout_sec ?? groupStepDef?.timeoutSec,
        depends_on: prevStep?.depends_on ?? groupStepDef?.dependsOn,
      });
    }
  }
  return steps;
}

// Walk up to find the top-level group that contains a node
function findRootGroup(nodeId: string): string | null {
  const node = nodes.value.find(n => n.id === nodeId);
  if (!node) return null;
  if (!node.parentNode) return node.type === 'group' ? nodeId : null;
  return findRootGroup(node.parentNode);
}

// Sync a group's children to its connected orchestrator(s)
function syncGroupToOrchestrator(groupId: string) {
  const groupNode = nodes.value.find(n => n.id === groupId);
  if (!groupNode || groupNode.type !== 'group') return;
  const groupData = groupNode.data as GroupConfig;
  const gid = groupData._id || groupId;

  // Find orchestrator(s) connected to this group
  const orchNodes = nodes.value.filter(n =>
    n.type === 'orchestrator' &&
    (n.data as OrchestratorConfig)._connectedGroupId === gid
  );

  for (const orchNode of orchNodes) {
    const orchData = orchNode.data as OrchestratorConfig;
    const previousSteps = orchData.pipeline?.steps;

    if (groupData.blockType === 'pipeline') {
      orchData.strategy = 'Pipeline';
      const steps = buildStepsFromGroup(groupId, previousSteps);
      if (!orchData.pipeline) orchData.pipeline = { steps: [] };
      orchData.pipeline.steps = steps;
    } else if (groupData.blockType === 'parallel') {
      orchData.strategy = 'Pipeline';
      const subSteps = buildStepsFromGroup(groupId, previousSteps?.[0]?.steps);
      if (!orchData.pipeline) orchData.pipeline = { steps: [] };
      orchData.pipeline.steps = [{ name: groupData.name, type: 'parallel', _groupId: gid, steps: subSteps }];
    }

    // Update agents list
    orchData.agents = collectAgentNames(groupId);
    updateNodeData(orchNode.id, orchData, { replace: true });

  }
}

onConnect((params) => {
  saveSnapshot();
  // Validate connection types
  const sourceNode = nodes.value.find(n => n.id === params.source);
  const targetNode = nodes.value.find(n => n.id === params.target);
  
  if (!sourceNode || !targetNode) return;
  
  // Define valid connections:
  // - Orchestrator -> Agent (delegation)
  // - Orchestrator -> Orchestrator (nested orchestration)
  // - Agent -> Tool (tool usage)
  // - Agent -> Agent (collaboration)
  // - Agent -> Orchestrator (sub-workflow delegation)
  const validConnections: Record<string, string[]> = {
    orchestrator: ['agent', 'orchestrator', 'group'],
    agent: ['tool', 'skill', 'agent', 'orchestrator', 'group'],
    group: ['agent', 'tool', 'skill', 'orchestrator', 'group'],
    skill: [],  // Skills are leaf nodes (referenced by agents)
    tool: [],   // Tools are leaf nodes
  };
  
  const sourceType = sourceNode.type as string;
  const targetType = targetNode.type as string;
  
  if (!validConnections[sourceType]?.includes(targetType)) {
    console.warn(`Invalid connection: ${sourceType} -> ${targetType}`);
    return;
  }
  
  addNewEdges([
    {
      ...params,
      id: `edge-${Date.now()}`,
      animated: true,
    },
  ]);

  // Auto-populate references on wire connect
  const targetName = (targetNode.data as { name?: string }).name;
  if (!targetName) return;

  if (sourceType === 'orchestrator' && targetType === 'agent') {
    // Orchestrator → Agent: add to orchestrator.agents[]
    const data = sourceNode.data as OrchestratorConfig;
    if (!data.agents?.includes(targetName)) {
      data.agents = [...(data.agents || []), targetName];
      updateNodeData(sourceNode.id, { ...data }, { replace: true });
    }
  } else if (sourceType === 'agent' && targetType === 'tool') {
    // Agent → Tool: add to agent.tools[]
    const data = sourceNode.data as AgentConfig;
    if (!data.tools?.includes(targetName)) {
      data.tools = [...(data.tools || []), targetName];
      updateNodeData(sourceNode.id, { ...data }, { replace: true });
    }
  } else if (sourceType === 'agent' && targetType === 'skill') {
    // Agent → Skill: add to agent.skills[]
    const data = sourceNode.data as AgentConfig;
    if (!data.skills?.includes(targetName)) {
      data.skills = [...(data.skills || []), targetName];
      updateNodeData(sourceNode.id, { ...data }, { replace: true });
    }
  } else if (sourceType === 'orchestrator' && targetType === 'group') {
    // Orchestrator → Group: one pipeline group per orchestrator
    const orchData = { ...sourceNode.data } as OrchestratorConfig;
    const groupData = targetNode.data as GroupConfig;

    // Check if orchestrator already has a pipeline group connected
    const existingGroupEdge = edges.value.find(e =>
      e.source === sourceNode.id &&
      nodes.value.find(n => n.id === e.target && n.type === 'group' && n.id !== targetNode.id)
    );
    if (existingGroupEdge) {
      const existingGroup = nodes.value.find(n => n.id === existingGroupEdge.target);
      const existingName = (existingGroup?.data as GroupConfig | undefined)?.name || 'existing group';
      if (!confirm(`Orchestrator already connected to "${existingName}". Replace with "${groupData.name}"?`)) {
        // Remove the edge we just created
        edges.value = edges.value.filter(e =>
          !(e.source === sourceNode.id && e.target === targetNode.id)
        );
        return;
      }
      // Remove old group edge
      edges.value = edges.value.filter(e => e.id !== existingGroupEdge.id);
    }

    // Track connected group by stable ID (uses extracted helpers above)
    orchData._connectedGroupId = groupData._id || targetNode.id;

    // Set strategy based on group type
    const previousSteps = orchData.pipeline?.steps;
    if (groupData.blockType === 'pipeline') {
      orchData.strategy = 'Pipeline';
      const steps = buildStepsFromGroup(targetNode.id, previousSteps);
      if (!orchData.pipeline) orchData.pipeline = { steps: [] };
      orchData.pipeline.steps = steps;
    } else if (groupData.blockType === 'team') {
      orchData.strategy = 'Hierarchical';
    } else if (groupData.blockType === 'parallel') {
      orchData.strategy = 'Pipeline';
      const subSteps = buildStepsFromGroup(targetNode.id, previousSteps?.[0]?.steps);
      const gid = groupData._id || targetNode.id;
      if (!orchData.pipeline) orchData.pipeline = { steps: [] };
      orchData.pipeline.steps = [{ name: groupData.name, type: 'parallel', _groupId: gid, steps: subSteps }];
    }

    // Add all descendant agents to orchestrator.agents[]
    const allAgents = collectAgentNames(targetNode.id);
    for (const name of allAgents) {
      if (!orchData.agents?.includes(name)) {
        orchData.agents = [...(orchData.agents || []), name];
      }
    }
    updateNodeData(sourceNode.id, orchData, { replace: true });
  }
});

onNodeClick((event) => {
  const clickedNode = event.node;
  const clickedParent = clickedNode.parentNode ?? null;
  const isMultiClick = event.event.metaKey || event.event.ctrlKey;

  if (isMultiClick) {
    // Multi-select: enforce same-level scope
    if (clickedParent !== selectionScope.value) {
      // Different level — deselect this node
      nextTick(() => {
        nodes.value = nodes.value.map(n =>
          n.id === clickedNode.id ? { ...n, selected: false } : n
        );
      });
      return;
    }
    // If selecting a group, deselect its descendants
    if (clickedNode.type === 'group') {
      nextTick(() => {
        const groupId = clickedNode.id;
        const selIds = new Set(getSelectedNodes.value.map(s => s.id));
        nodes.value = nodes.value.map(n => {
          if (selIds.has(n.id) && isDescendantOf(n.id, groupId)) {
            return { ...n, selected: false };
          }
          return n;
        });
      });
    }
  } else {
    // Single click: always works on any node, deselect others
    // This allows clicking nodes inside groups without entering scope
    nextTick(() => {
      nodes.value = nodes.value.map(n => ({
        ...n,
        selected: n.id === clickedNode.id,
      }));
    });
  }

  if (canvasMode.value !== 'design') {
    // In execution modes: show exec detail panel for exec nodes
    const isExec = (clickedNode.data as Record<string, unknown>)._execNode;
    if (isExec) {
      execDetailNodeId.value = execDetailNodeId.value === clickedNode.id ? null : clickedNode.id;
    } else {
      monitorCardNodeId.value = monitorCardNodeId.value === clickedNode.id ? null : clickedNode.id;
    }
    return;
  }

  // Results mode: exec nodes open detail panel, design nodes open MonitorCard
  if (showResults.value) {
    const isExec = (clickedNode.data as Record<string, unknown>)._execNode;
    if (isExec) {
      execDetailNodeId.value = execDetailNodeId.value === clickedNode.id ? null : clickedNode.id;
      return;
    }
    if ((clickedNode.data as Record<string, unknown>)._runStatus) {
      monitorCardNodeId.value = monitorCardNodeId.value === clickedNode.id ? null : clickedNode.id;
    }
  }

  selectedNodeRef.value = clickedNode;
  showEditor.value = true;
  showSettings.value = false;
  dropMenu.value = null;
});

// Check if nodeId is a descendant of groupId (any depth)
function isDescendantOf(nodeId: string, groupId: string): boolean {
  let current = nodes.value.find(n => n.id === nodeId);
  while (current?.parentNode) {
    if (current.parentNode === groupId) return true;
    current = nodes.value.find(n => n.id === current!.parentNode);
  }
  return false;
}

// Double-click a group to enter it (scope into its children)
// In debug/monitor/results mode, double-click any node → drill down to advanced debugger
onNodeDoubleClick((event) => {
  if (canvasMode.value === 'debug' || canvasMode.value === 'monitor' || showResults.value) {
    // Drill down: switch to Debugger tab focused on this agent
    const data = event.node.data as Record<string, unknown>;
    // Exec nodes carry agentName; design agent nodes carry name
    const agentName = (data._execNode ? data.agentName : data.name) as string | undefined;
    if (agentName) {
      // Pass tree roots so the debugger can display them (especially in results mode
      // where the debugger has no live events)
      const roots = selectedSnapshot.value?.treeRoots
        ?? (debugTreeRoots.value.length > 0 ? debugTreeRoots.value : undefined);
      emit('drill-down', agentName, roots);
    }
    return;
  }
  if (event.node.type === 'group') {
    const data = event.node.data as GroupConfig;
    if (data.viewMode === 'compact' || data.collapsed) {
      // Double-click compact → switch to mixed
      data.viewMode = 'mixed';
      data.collapsed = false;
      nextTick(() => scheduleAutoFit(event.node.id));
    } else {
      enterGroupScope(event.node.id);
    }
  }
});

// Context menu node actions
function contextMenuCopySettings() {
  if (!contextMenu.value?.nodeId) { contextMenu.value = null; return; }
  const node = nodes.value.find(n => n.id === contextMenu.value!.nodeId);
  if (!node) { contextMenu.value = null; return; }
  nodeClipboard.value = {
    type: node.type || '',
    data: JSON.parse(JSON.stringify(node.data)),
  };
  contextMenu.value = null;
}

function contextMenuPasteSettings() {
  if (!contextMenu.value?.nodeId || !nodeClipboard.value) { contextMenu.value = null; return; }
  const node = nodes.value.find(n => n.id === contextMenu.value!.nodeId);
  if (!node) { contextMenu.value = null; return; }
  const clip = nodeClipboard.value.data;
  const current = node.data as Record<string, unknown>;

  // Paste settings but preserve identity fields (_id, name)
  const preserveKeys = ['_id', 'name', '_providerId'];
  const merged = { ...current };
  for (const [k, v] of Object.entries(clip)) {
    if (!preserveKeys.includes(k)) {
      merged[k] = JSON.parse(JSON.stringify(v));
    }
  }
  updateNodeData(node.id, merged, { replace: true });
  contextMenu.value = null;
}

function contextMenuDuplicate() {
  if (isMultiSelect.value) {
    duplicateSelection();
  } else {
    duplicateSelectedNode();
  }
  contextMenu.value = null;
}

function contextMenuDelete() {
  saveSnapshot();
  if (isMultiSelect.value) {
    deleteSelection();
  } else if (contextMenu.value?.nodeId) {
    const id = contextMenu.value.nodeId;
    const node = nodes.value.find(n => n.id === id);
    const parentGroup = node?.parentNode ? findRootGroup(node.parentNode) : null;
    nodes.value = nodes.value.filter(n => n.id !== id);
    edges.value = edges.value.filter(e => e.source !== id && e.target !== id);
    showEditor.value = false;
    if (parentGroup) syncGroupToOrchestrator(parentGroup);
  }
  contextMenu.value = null;
}

function contextMenuUngroup() {
  if (contextMenu.value?.nodeId) {
    ungroupNode(contextMenu.value.nodeId);
  }
  contextMenu.value = null;
}

function contextMenuDetachFromGroup() {
  if (!contextMenu.value?.nodeId) { contextMenu.value = null; return; }
  const nodeId = contextMenu.value.nodeId;
  const node = nodes.value.find(n => n.id === nodeId);
  if (!node?.parentNode) { contextMenu.value = null; return; }

  const parentGroup = nodes.value.find(n => n.id === node.parentNode);
  const absPos = getAbsolutePosition(node);

  nodes.value = nodes.value.map(n => {
    if (n.id === nodeId) {
      return {
        ...n,
        parentNode: undefined,
        extent: undefined,
        expandParent: undefined,
        position: absPos,
      };
    }
    return n;
  });

  // Recalc the group we just removed from
  if (parentGroup) scheduleAutoFit(parentGroup.id);
  contextMenu.value = null;
}

function contextMenuGroupSelection(blockType: GroupConfig['blockType']) {
  groupSelection(blockType);
  contextMenu.value = null;
}

// Hierarchy panel: select node from tree
function handleHierarchySelect(nodeId: string, multi = false) {
  const node = nodes.value.find(n => n.id === nodeId);
  if (!node) return;

  if (multi) {
    // Cmd+click in hierarchy: toggle selection
    nodes.value = nodes.value.map(n => {
      if (n.id === nodeId) return { ...n, selected: !getSelectedNodes.value.some(s => s.id === nodeId) };
      return n;
    });
  } else {
    // Single click: select only this node
    nodes.value = nodes.value.map(n => ({ ...n, selected: n.id === nodeId }));
  }

  selectedNodeRef.value = node;
  showEditor.value = true;
  showSettings.value = false;
  nextTick(() => fitView({ nodes: [nodeId], padding: 0.5 }));
}

// Search: focus on result
function handleSearchFocus(direction: 'next' | 'prev') {
  const node = direction === 'next' ? nextResult() : prevResult();
  if (node) {
    selectedNodeRef.value = node;
    showEditor.value = true;
    nextTick(() => fitView({ nodes: [node.id], padding: 0.5 }));
  }
}

function handlePaneClick() {
  selectedNodeRef.value = null;
  showEditor.value = false;
  dropMenu.value = null;
}

// --- Drag-to-group with hover highlight + overlay menu ---
const dragHoverGroupId = ref<string | null>(null);
const dropMenu = ref<{ x: number; y: number; groupId: string; groupName: string; nodeIds: string[] } | null>(null);

// Get absolute position of a node (resolving parentNode chain)
function getAbsolutePosition(node: Node): { x: number; y: number } {
  let x = node.position.x;
  let y = node.position.y;
  let current = node;
  while (current.parentNode) {
    const parent = nodes.value.find(n => n.id === current.parentNode);
    if (!parent) break;
    x += parent.position.x;
    y += parent.position.y;
    current = parent;
  }
  return { x, y };
}

// Find the deepest group that contains the dragged nodes
// Excludes groups that are parents of the dragged nodes (can't drop into own parent)
function findGroupAtPosition(nodeList: Node[]): Node | null {
  const draggedIds = new Set(nodeList.map(n => n.id));
  const groupNodes = nodes.value.filter(n => n.type === 'group' && !draggedIds.has(n.id));

  // Calculate depth of each group (deeper = more nested = higher priority)
  function groupDepth(g: Node): number {
    let d = 0;
    let c = g;
    while (c.parentNode) { d++; c = nodes.value.find(n => n.id === c.parentNode) || c; if (!c.parentNode) break; }
    return d;
  }

  // Sort by depth descending — prefer deepest (most specific) match
  const sorted = [...groupNodes].sort((a, b) => groupDepth(b) - groupDepth(a));

  for (const group of sorted) {
    const gPos = getAbsolutePosition(group);
    const style = group.style as Record<string, string> | undefined;
    const gw = parseInt(style?.width || '400', 10);
    const gh = parseInt(style?.height || '300', 10);

    const overlaps = nodeList.some(n => {
      const nPos = getAbsolutePosition(n);
      const cx = nPos.x + 100;
      const cy = nPos.y + 40;
      return cx > gPos.x && cx < gPos.x + gw && cy > gPos.y && cy < gPos.y + gh;
    });

    // Don't allow dropping into own current parent (no-op)
    if (overlaps && !nodeList.every(n => n.parentNode === group.id)) {
      return group;
    }
  }
  return null;
}

function handleNodeDrag(event: { node: Node; nodes: Node[] }) {
  const draggedNodes = event.nodes.length > 0 ? event.nodes : [event.node];
  if (draggedNodes.length === 0) { dragHoverGroupId.value = null; return; }

  const target = findGroupAtPosition(draggedNodes);
  const newId = target?.id ?? null;
  if (newId !== dragHoverGroupId.value) {
    // Remove highlight from old group
    if (dragHoverGroupId.value) {
      const old = nodes.value.find(n => n.id === dragHoverGroupId.value);
      if (old) old.class = ((old.class as string) || '').replace('drop-target', '').trim();
    }
    // Add highlight to new group
    if (newId) {
      const tgt = nodes.value.find(n => n.id === newId);
      if (tgt) tgt.class = (((tgt.class as string) || '') + ' drop-target').trim();
    }
    dragHoverGroupId.value = newId;
  }
}

function handleNodeDragStop(event: { node: Node; nodes: Node[]; event: MouseEvent | TouchEvent }) {
  // Clear hover highlight
  if (dragHoverGroupId.value) {
    const old = nodes.value.find(n => n.id === dragHoverGroupId.value);
    if (old) old.class = ((old.class as string) || '').replace('drop-target', '').trim();
  }
  dragHoverGroupId.value = null;

  const draggedNodes = event.nodes.length > 0 ? event.nodes : [event.node];
  if (draggedNodes.length === 0) return;

  const target = findGroupAtPosition(draggedNodes);
  if (!target) return;

  // Prevent dropping a group into itself or its own descendants
  const draggedGroupIds = new Set(draggedNodes.filter(n => n.type === 'group').map(n => n.id));
  if (draggedGroupIds.has(target.id)) return;
  for (const gid of draggedGroupIds) {
    if (isDescendantOf(target.id, gid)) return;
  }

  const groupData = target.data as GroupConfig;
  dropMenu.value = {
    x: event.event instanceof MouseEvent ? event.event.clientX : (event.event as TouchEvent).changedTouches?.[0]?.clientX ?? 300,
    y: event.event instanceof MouseEvent ? event.event.clientY : (event.event as TouchEvent).changedTouches?.[0]?.clientY ?? 300,
    groupId: target.id,
    groupName: groupData.name,
    nodeIds: draggedNodes.map(n => n.id),
  };
}

function confirmDropToGroup() {
  saveSnapshot();
  if (!dropMenu.value) return;
  const { groupId, nodeIds } = dropMenu.value;
  const group = nodes.value.find(n => n.id === groupId);
  if (!group) { dropMenu.value = null; return; }

  const groupAbs = getAbsolutePosition(group);

  nodes.value = nodes.value.map(n => {
    if (nodeIds.includes(n.id)) {
      // Get absolute position before re-parenting
      const absPos = getAbsolutePosition(n);
      return {
        ...n,
        parentNode: groupId,
        extent: 'parent' as const,
        expandParent: true,
        position: {
          x: absPos.x - groupAbs.x,
          y: absPos.y - groupAbs.y,
        },
      };
    }
    return n;
  });
  scheduleAutoFit(groupId);
  dropMenu.value = null;
  // Sync pipeline steps to connected orchestrator
  const rootGroup = findRootGroup(groupId);
  if (rootGroup) syncGroupToOrchestrator(rootGroup);
}

function cancelDropToGroup() {
  dropMenu.value = null;
}

// Counter for unique IDs
let nodeCounter = 0;

// When inside a group scope or a group is selected, new nodes auto-parent
function newNodeParentProps(): { position: { x: number; y: number }; parentNode?: string; extent?: 'parent'; expandParent?: boolean } {
  // Priority 1: inside a group scope
  if (selectionScope.value) {
    return {
      parentNode: selectionScope.value,
      extent: 'parent' as const,
      expandParent: true,
      position: { x: 40 + Math.random() * 100, y: 50 + Math.random() * 100 },
    };
  }
  // Priority 2: a single group is selected
  if (selectedNode.value?.type === 'group') {
    return {
      parentNode: selectedNode.value.id,
      extent: 'parent' as const,
      expandParent: true,
      position: { x: 40 + Math.random() * 100, y: 50 + Math.random() * 100 },
    };
  }
  return {
    position: { x: 100 + Math.random() * 200, y: 100 + Math.random() * 200 },
  };
}

function addAgentNode() {
  saveSnapshot();
  const id = `agent-${++nodeCounter}`;
  const defaultProvider = projectSettings.value.default_provider || 'openai';
  const defaultProviderDef = projectSettings.value.providers?.[defaultProvider];
  const defaultModel = defaultProviderDef?.default_model || 'gpt-4o';
  const newAgent: AgentConfig = {
    _id: generateEntityId(),
    _inherited: { model: true },
    _providerId: defaultProviderDef?._id,
    name: `Agent ${nodeCounter}`,
    role: 'worker',
    provider: defaultProvider,
    model: defaultModel,
    system_prompt: 'You are a helpful assistant.',
    tools: [],
  };

  const parentProps = newNodeParentProps();
  addNodes([{ id, type: 'agent', data: newAgent, ...parentProps }]);
  if (parentProps.parentNode) {
    const gid = parentProps.parentNode;
    nextTick(() => {
      scheduleAutoFit(gid);
      const root = findRootGroup(gid);
      if (root) syncGroupToOrchestrator(root);
    });
  }
}

function addToolNode() {
  saveSnapshot();
  const id = `tool-${++nodeCounter}`;
  const newTool: ToolConfig = {
    _id: generateEntityId(),
    name: `tool_${nodeCounter}`,
    type: 'cli',
    description: 'A tool description',
    command: 'echo "Hello"',
  };

  addNodes([{ id, type: 'tool', data: newTool, ...newNodeParentProps() }]);
}

function addOrchestratorNode() {
  saveSnapshot();
  const id = `orchestrator-${++nodeCounter}`;
  const defaultProv = projectSettings.value.default_provider || 'openai';
  const defaultProvDef = projectSettings.value.providers?.[defaultProv];
  const defaultMdl = defaultProvDef?.default_model || 'gpt-4o';
  const newOrchestrator: OrchestratorConfig = {
    _id: generateEntityId(),
    _inherited: { model: true },
    _providerId: defaultProvDef?._id,
    name: 'Orchestrator',
    role: 'supervisor',
    strategy: 'ReAct',
    provider: defaultProv,
    model: defaultMdl,
    system_prompt: 'You are the orchestrator.',
    agents: [],
  };

  addNodes([{ id, type: 'orchestrator', data: newOrchestrator, ...newNodeParentProps() }]);
}

function addSkillNode() {
  saveSnapshot();
  const id = `skill-${++nodeCounter}`;
  const newSkill: SkillConfig = {
    _id: generateEntityId(),
    name: `skill_${nodeCounter}`,
    description: 'A skill description',
    tools: [],
    prompt_template: 'Skill prompt template...',
  };
  addNodes([{ id, type: 'skill', data: newSkill, ...newNodeParentProps() }]);
}

function addGroupNode(blockType: GroupConfig['blockType'] = 'generic') {
  saveSnapshot();
  const id = `group-${++nodeCounter}`;
  const newGroup: GroupConfig = {
    _id: generateEntityId(),
    name: `Group ${nodeCounter}`,
    blockType,
    collapsed: false,
  };
  addNodes([
    {
      id,
      type: 'group',
      position: { x: 100 + Math.random() * 200, y: 100 + Math.random() * 200 },
      style: { width: '400px', height: '300px' },
      data: newGroup,
    },
  ]);
}

function groupSelection(blockType: GroupConfig['blockType'] = 'generic') {
  saveSnapshot();
  const sel = selectedNodes.value;
  if (sel.length < 2) return;

  // Calculate bounding box of selected nodes
  const padding = 40;
  const headerH = 34;
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const n of sel) {
    const w = 200; // approximate node width
    const h = 80;  // approximate node height
    if (n.position.x < minX) minX = n.position.x;
    if (n.position.y < minY) minY = n.position.y;
    if (n.position.x + w > maxX) maxX = n.position.x + w;
    if (n.position.y + h > maxY) maxY = n.position.y + h;
  }

  const groupId = `group-${++nodeCounter}`;
  const groupX = minX - padding;
  const groupY = minY - padding - headerH;
  const groupW = maxX - minX + padding * 2;
  const groupH = maxY - minY + padding * 2 + headerH;

  const newGroup: GroupConfig = {
    _id: generateEntityId(),
    name: `Group ${nodeCounter}`,
    blockType,
    collapsed: false,
  };

  // Add group node first
  addNodes([{
    id: groupId,
    type: 'group',
    position: { x: groupX, y: groupY },
    style: { width: `${groupW}px`, height: `${groupH}px` },
    data: newGroup,
  }]);

  // Reparent selected nodes — convert to relative positions
  nextTick(() => {
    nodes.value = nodes.value.map(n => {
      if (sel.some(s => s.id === n.id)) {
        return {
          ...n,
          parentNode: groupId,
          extent: 'parent' as const,
          expandParent: true,
          position: {
            x: n.position.x - groupX,
            y: n.position.y - groupY,
          },
        };
      }
      return n;
    });
    scheduleAutoFit(groupId);
  });
}

function ungroupNode(groupId: string) {
  saveSnapshot();
  const group = nodes.value.find(n => n.id === groupId && n.type === 'group');
  if (!group) return;

  // Convert children back to absolute positions
  nodes.value = nodes.value.map(n => {
    if (n.parentNode === groupId) {
      return {
        ...n,
        parentNode: undefined,
        extent: undefined,
        expandParent: undefined,
        position: {
          x: n.position.x + group.position.x,
          y: n.position.y + group.position.y,
        },
      };
    }
    return n;
  });

  // Clear connected orchestrator's link before removing group
  const groupData = group.data as GroupConfig;
  const gid = groupData._id || groupId;
  for (const n of nodes.value) {
    if (n.type === 'orchestrator' && (n.data as OrchestratorConfig)._connectedGroupId === gid) {
      (n.data as OrchestratorConfig)._connectedGroupId = undefined;
    }
  }

  // Remove the group node
  nodes.value = nodes.value.filter(n => n.id !== groupId);
}

function duplicateSelectedNode() {
  saveSnapshot();
  if (!selectedNode.value) return;

  const sourceNode = selectedNode.value;
  const newId = `${sourceNode.type}-${++nodeCounter}`;

  // Deep copy the data
  const newData = JSON.parse(JSON.stringify(sourceNode.data));
  if (newData._id) newData._id = generateEntityId();

  // Generate unique name for the copy
  if (newData.name) {
    newData.name = uniqueName(newData.name);
  }

  const newNodes: { id: string; type: string | undefined; position: { x: number; y: number }; data: any; parentNode?: string; extent?: 'parent'; expandParent?: boolean }[] = [
    {
      id: newId,
      type: sourceNode.type,
      position: {
        x: sourceNode.position.x + 50,
        y: sourceNode.position.y + 50
      },
      data: newData,
    },
  ];

  // If group, recursively duplicate children
  if (sourceNode.type === 'group') {
    const idMap = new Map<string, string>(); // old → new
    idMap.set(sourceNode.id, newId);

    function duplicateChildren(parentId: string, newParentId: string) {
      const children = nodes.value.filter(n => n.parentNode === parentId);
      for (const child of children) {
        const childNewId = `${child.type}-${++nodeCounter}`;
        const childData = JSON.parse(JSON.stringify(child.data));
        if (childData._id) childData._id = generateEntityId();
        if (childData.name) childData.name = uniqueName(childData.name);
        idMap.set(child.id, childNewId);
        newNodes.push({
          id: childNewId,
          type: child.type!,
          position: { ...child.position },
          parentNode: newParentId,
          extent: 'parent' as const,
          expandParent: true,
          data: childData,
        } as any);
        if (child.type === 'group') {
          duplicateChildren(child.id, childNewId);
        }
      }
    }
    duplicateChildren(sourceNode.id, newId);
  }

  addNodes(newNodes);
  if (sourceNode.type === 'group') {
    nextTick(() => scheduleAutoFit(newId));
  }
}

function handleNodeUpdate(nodeId: string, data: AgentConfig | ToolConfig | SkillConfig | OrchestratorConfig | GroupConfig) {
  // Detect name change and propagate to all references
  const existingNode = nodes.value.find(n => n.id === nodeId);
  if (existingNode) {
    const oldName = (existingNode.data as { name?: string }).name;
    const newName = (data as { name?: string }).name;
    if (oldName && newName && oldName !== newName) {
      propagateRename(existingNode.type as string, oldName, newName);
    }
  }
  updateNodeData(nodeId, data, { replace: true });
}

// Propagate a name change to all nodes that reference the old name
function propagateRename(entityType: string, oldName: string, newName: string) {
  nodes.value = nodes.value.map(n => {
    const d = { ...n.data } as Record<string, unknown>;
    let changed = false;

    if (entityType === 'tool') {
      // Agent.tools[] and Skill.tools[]
      if (n.type === 'agent' || n.type === 'skill') {
        const tools = d.tools as string[] | undefined;
        if (tools?.includes(oldName)) {
          d.tools = tools.map(t => t === oldName ? newName : t);
          changed = true;
        }
      }
    } else if (entityType === 'skill') {
      // Agent.skills[]
      if (n.type === 'agent') {
        const skills = d.skills as string[] | undefined;
        if (skills?.includes(oldName)) {
          d.skills = skills.map(s => s === oldName ? newName : s);
          changed = true;
        }
      }
    } else if (entityType === 'agent') {
      // Orchestrator.agents[] and pipeline step agent references
      if (n.type === 'orchestrator') {
        const agents = d.agents as string[] | undefined;
        if (agents?.includes(oldName)) {
          d.agents = agents.map(a => a === oldName ? newName : a);
          changed = true;
        }
        // Pipeline steps
        const pipeline = d.pipeline as { steps?: Array<{ agent?: string }> } | undefined;
        if (pipeline?.steps) {
          for (const step of pipeline.steps) {
            if (step.agent === oldName) {
              step.agent = newName;
              changed = true;
            }
          }
        }
      }
    }

    return changed ? { ...n, data: d } : n;
  });
}

function handleAutoAddProvider(name: string) {
  if (!name || (projectSettings.value.providers && name in projectSettings.value.providers)) return;
  if (!projectSettings.value.providers) projectSettings.value.providers = {};
  // Guess type from name
  const knownTypes: Record<string, string> = { openai: 'openai', anthropic: 'anthropic', gemini: 'gemini', ollama: 'ollama', litellm: 'openai' };
  const guessedType = knownTypes[name.toLowerCase()] ?? 'openai';
  projectSettings.value.providers[name] = { type: guessedType };
}

function closeEditor() {
  showEditor.value = false;
  selectedNodeRef.value = null;
}

// --- Multi-select helpers ---

const selectionTypeCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const n of selectedNodes.value) {
    counts.set(n.type!, (counts.get(n.type!) || 0) + 1);
  }
  return Array.from(counts.entries());
});

const selectionCommonType = computed(() => {
  const types = new Set(selectedNodes.value.map(n => n.type));
  return types.size === 1 ? types.values().next().value : null;
});

function bulkCommonValue(field: string): string {
  const values = new Set(
    selectedNodes.value.map(n => (n.data as Record<string, unknown>)[field] as string).filter(Boolean)
  );
  return values.size === 1 ? values.values().next().value! : '';
}

function applyBulkField(field: string, value: string) {
  if (!value) return;
  const selIds = new Set(selectedNodes.value.map(n => n.id));
  nodes.value = nodes.value.map(n => {
    if (!selIds.has(n.id)) return n;
    return { ...n, data: { ...n.data, [field]: value, _inherited: { ...(n.data as Record<string, unknown>)._inherited as Record<string, boolean> | undefined, [field]: false } } };
  });
}

function duplicateSelection() {
  saveSnapshot();
  const sel = selectedNodes.value;
  if (sel.length === 0) return;
  const selIds = new Set(sel.map(n => n.id));
  const offset = 50;
  const idMap = new Map<string, string>();

  // Clone nodes
  const newNodes = sel.map(n => {
    const newId = `${n.type}-${++nodeCounter}`;
    idMap.set(n.id, newId);
    return {
      id: newId,
      type: n.type!,
      position: { x: n.position.x + offset, y: n.position.y + offset },
      data: { ...JSON.parse(JSON.stringify(n.data)), _id: generateEntityId() },
    };
  });

  // Clone internal edges
  const newEdges: Edge[] = edges.value
    .filter(e => selIds.has(e.source) && selIds.has(e.target))
    .map(e => ({
      ...e,
      id: `e-${idMap.get(e.source)}-${idMap.get(e.target)}-${Date.now()}`,
      source: idMap.get(e.source)!,
      target: idMap.get(e.target)!,
    }));

  addNodes(newNodes);
  addNewEdges(newEdges);
}

function deleteSelection() {
  saveSnapshot();
  const sel = selectedNodes.value;
  if (sel.length === 0) return;
  if (!confirm(`Delete ${sel.length} selected nodes?`)) return;
  // Collect affected groups before deletion
  const affectedGroups = new Set<string>();
  for (const n of sel) {
    if (n.parentNode) {
      const root = findRootGroup(n.parentNode);
      if (root) affectedGroups.add(root);
    }
  }
  removeNodes(sel);
  showEditor.value = false;
  selectedNodeRef.value = null;
  // Sync pipeline steps for affected groups
  for (const gid of affectedGroups) {
    syncGroupToOrchestrator(gid);
  }
}

// Track Shift key — disable group dragging when Shift held (allows box-select inside groups)
const shiftHeld = ref(false);

function handleKeydownShift(e: KeyboardEvent) {
  if (e.key === 'Shift' && !shiftHeld.value) {
    shiftHeld.value = true;
    // pointer-events: none on all nodes so box-select drag passes through to pane
    nodes.value = nodes.value.map(n => ({
      ...n,
      class: (((n.class as string) || '') + ' shift-passthrough').trim(),
    }));
  }
}

function handleKeyupShift(e: KeyboardEvent) {
  if (e.key === 'Shift') {
    shiftHeld.value = false;
    nodes.value = nodes.value.map(n => ({
      ...n,
      class: ((n.class as string) || '').replace(/shift-passthrough/g, '').trim(),
    }));
  }
}

// Keyboard shortcuts
function handleKeydown(e: KeyboardEvent) {
  handleKeydownShift(e);
  // Don't handle if typing in an input
  if ((e.target as HTMLElement).tagName === 'INPUT' || (e.target as HTMLElement).tagName === 'TEXTAREA') return;

  if (e.key === 'Delete' || e.key === 'Backspace') {
    if (selectedNodes.value.length > 0) {
      e.preventDefault();
      deleteSelection();
    }
  } else if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
    e.preventDefault();
    toggleSearch();
  } else if ((e.metaKey || e.ctrlKey) && e.shiftKey && (e.key === 'z' || e.key === 'Z')) {
    e.preventDefault();
    redo();
  } else if ((e.metaKey || e.ctrlKey) && (e.key === 'z' || e.key === 'Z')) {
    e.preventDefault();
    undo();
  } else if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === 'h') {
    e.preventDefault();
    showHierarchy.value = !showHierarchy.value;
  } else if (e.key === 'Escape') {
    if (searchOpen.value) { closeSearch(); return; }
    // Exit full view first
    if (fullViewGroupId.value) {
      const gn = nodes.value.find(n => n.id === fullViewGroupId.value);
      if (gn) (gn.data as GroupConfig).viewMode = 'mixed';
      fullViewGroupId.value = null;
      // Unhide all (interval will handle cleanup)
      for (const c of nodes.value) {
        if (!compactGroupIds.has(c.parentNode ?? '')) {
          c.class = ((c.class as string) || '').replace('compact-hidden', '').trim();
        }
      }
      return;
    }
    // Escape: exit current group scope (go up one level)
    if (selectionScope.value) {
      const currentGroup = nodes.value.find(n => n.id === selectionScope.value);
      exitToScope(currentGroup?.parentNode ?? null);
    } else {
      // At root — deselect all
      nodes.value = nodes.value.map(n => ({ ...n, selected: false }));
      showEditor.value = false;
    }
  }
}

// --- Shared helpers ---

function collectCanvasData(): {
  agents: AgentConfig[];
  tools: ToolConfig[];
  skills: SkillConfig[];
  orchestrator?: OrchestratorConfig;
  orchestrators?: OrchestratorConfig[];
} {
  const agentNodes = nodes.value.filter((n) => n.type === 'agent');
  const toolNodes = nodes.value.filter((n) => n.type === 'tool');
  const skillNodes = nodes.value.filter((n) => n.type === 'skill');
  const orchestratorNodes = nodes.value.filter((n) => n.type === 'orchestrator');

  const agents: AgentConfig[] = agentNodes.map((n) => JSON.parse(JSON.stringify(n.data)) as AgentConfig);
  const tools: ToolConfig[] = toolNodes.map((n) => JSON.parse(JSON.stringify(n.data)) as ToolConfig);
  const skills: SkillConfig[] = skillNodes.map((n) => JSON.parse(JSON.stringify(n.data)) as SkillConfig);

  let orchestrator: OrchestratorConfig | undefined;
  let orchestrators: OrchestratorConfig[] | undefined;

  if (orchestratorNodes.length > 0) {
    // Clone all orchestrator data
    const orchEntries = orchestratorNodes.map(n => ({
      id: n.id,
      data: JSON.parse(JSON.stringify(n.data)) as OrchestratorConfig,
    }));

    // For non-pipeline orchestrators, collect agents from outgoing edges.
    // Pipeline orchestrators already have agents/steps maintained by group sync helpers.
    for (const entry of orchEntries) {
      if (entry.data.strategy === 'Pipeline' && entry.data.pipeline?.steps?.length) {
        // Pipeline: data.agents and data.pipeline are already set by syncGroupToOrchestrator
        continue;
      }
      // Non-pipeline (Hierarchical/ReAct): collect agents from edges
      const connectedIds = edges.value
        .filter((e) => e.source === entry.id)
        .map((e) => e.target);
      entry.data.agents = nodes.value
        .filter((n) => connectedIds.includes(n.id))
        .map((n) => {
          const d = n.data as AgentConfig | OrchestratorConfig;
          return (d as AgentConfig).name || `node-${n.id}`;
        });
    }

    // Determine main vs sub: a sub-orchestrator's name appears in another
    // orchestrator's agents list or pipeline steps.
    const orchNames = new Set(orchEntries.map(e => e.data.name));
    const subNames = new Set<string>();
    for (const entry of orchEntries) {
      for (const name of entry.data.agents || []) {
        if (orchNames.has(name)) subNames.add(name);
      }
      const markStepRefs = (steps: { agent?: string; steps?: any[] }[]) => {
        for (const s of steps || []) {
          if (s.agent && orchNames.has(s.agent)) subNames.add(s.agent);
          if (s.steps) markStepRefs(s.steps);
        }
      };
      if (entry.data.pipeline?.steps) markStepRefs(entry.data.pipeline.steps);
    }

    // Main = not referenced by any other orchestrator
    const mainEntry = orchEntries.find(e => !subNames.has(e.data.name)) || orchEntries[0];
    if (mainEntry) {
      orchestrator = mainEntry.data;
    }

    // Sub-orchestrators = all others
    const subEntries = orchEntries.filter(e => e !== mainEntry);
    if (subEntries.length > 0) {
      orchestrators = subEntries.map(e => e.data);
    }
  }

  for (const agent of agents) {
    const agentNode = agentNodes.find((n) => (n.data as AgentConfig).name === agent.name);
    if (agentNode) {
      const connectedTargetIds = edges.value
        .filter((e) => e.source === agentNode.id)
        .map((e) => e.target);

      agent.tools = nodes.value
        .filter((n) => connectedTargetIds.includes(n.id) && n.type === 'tool')
        .map((n) => (n.data as ToolConfig).name);

      const connectedSkills = nodes.value
        .filter((n) => connectedTargetIds.includes(n.id) && n.type === 'skill')
        .map((n) => (n.data as SkillConfig).name);

      if (connectedSkills.length > 0) {
        agent.skills = connectedSkills;
      }
    }
  }

  return { agents, tools, skills, orchestrator, orchestrators };
}

// --- Auto-arrange: layered layout that avoids overlaps ---
// --- Auto-arrange: layered layout with recursive nested group support ---
function autoArrange() {
  const NODE_W = 220;
  const NODE_H = 140;
  const PAD_X = 40;
  const PAD_Y = 60;
  const GROUP_PAD = 20;
  const GROUP_HEADER = 50;

  type FlowNode = (typeof nodes.value)[number];

  // Build parent→children map
  const childrenOf = new Map<string, FlowNode[]>();
  for (const n of nodes.value) {
    if (!n.parentNode) continue;
    const list = childrenOf.get(n.parentNode) || [];
    list.push(n);
    childrenOf.set(n.parentNode, list);
  }

  // Recursively arrange a group's contents, returns { width, height }
  // Preserves existing child order (by current position) — never reorders pipeline steps
  function arrangeGroup(groupNode: FlowNode): { w: number; h: number } {
    const groupData = groupNode.data as GroupConfig;
    // If this group is compact, return its small fixed size — don't arrange children
    if (groupData.viewMode === 'compact' || groupData.collapsed) {
      return { w: 200, h: 44 };
    }

    const children = childrenOf.get(groupNode.id) || [];
    if (children.length === 0) return { w: 250, h: 120 };

    // Filter out compact-hidden children
    const visible = children.filter(c => !((c.class as string) || '').includes('compact-hidden'));
    if (visible.length === 0) return { w: 250, h: 120 };

    // Keep existing order: sort by current x position (preserves pipeline step sequence)
    const ordered = [...visible].sort((a, b) => a.position.x - b.position.x || a.position.y - b.position.y);

    // Pre-compute sizes for nested groups (bottom-up)
    const childSizes = new Map<string, { w: number; h: number }>();
    for (const c of ordered) {
      if (c.type === 'group') {
        childSizes.set(c.id, arrangeGroup(c));
      }
    }

    // Layout children left-to-right in their original order
    let x = GROUP_PAD;
    let maxH = NODE_H;

    for (const c of ordered) {
      const sz = childSizes.get(c.id);
      c.position = { x, y: GROUP_HEADER };
      if (sz) {
        c.style = { width: `${sz.w}px`, height: `${sz.h}px` };
        x += sz.w + PAD_X;
        maxH = Math.max(maxH, sz.h);
      } else {
        x += NODE_W + PAD_X;
      }
    }

    const totalW = Math.max(250, x - PAD_X + GROUP_PAD);
    const totalH = Math.max(120, GROUP_HEADER + maxH + GROUP_PAD);
    return { w: totalW, h: totalH };
  }

  // Find all top-level nodes (no parent)
  const topLevel = nodes.value.filter(n => !n.parentNode);

  // Separate by type
  type NodeArray = FlowNode[];
  const layers: Record<string, NodeArray> = {
    orchestrator: [], group: [], agent: [], tool: [], skill: [],
  };
  for (const n of topLevel) {
    const key = n.type as string;
    const bucket = layers[key] || layers.group!;
    bucket.push(n);
  }

  // Arrange all top-level groups recursively first (to know their sizes)
  const groupSizes = new Map<string, { w: number; h: number }>();
  for (const g of layers.group!) {
    groupSizes.set(g.id, arrangeGroup(g));
  }

  // Layer order: orchestrator → group → agent → tool → skill
  const layerOrder = ['orchestrator', 'group', 'agent', 'tool', 'skill'];
  let currentY = 40;

  for (const layerKey of layerOrder) {
    const layerNodes = layers[layerKey];
    if (!layerNodes || layerNodes.length === 0) continue;

    // Calculate widths for centering
    let totalWidth = 0;
    for (const n of layerNodes) {
      const sz = groupSizes.get(n.id);
      totalWidth += (sz ? sz.w : NODE_W) + PAD_X;
    }
    totalWidth -= PAD_X;
    let x = Math.max(40, (800 - totalWidth) / 2);

    let maxH = NODE_H;
    for (const n of layerNodes) {
      const sz = groupSizes.get(n.id);
      n.position = { x, y: currentY };
      if (sz) {
        n.style = { width: `${sz.w}px`, height: `${sz.h}px` };
        x += sz.w + PAD_X;
        maxH = Math.max(maxH, sz.h);
      } else {
        x += NODE_W + PAD_X;
      }
    }
    currentY += maxH + PAD_Y;
  }

  nextTick(() => { autoFitAllGroups(); fitView({ padding: 0.15 }); });
}

function importConfig(config: Config) {
  saveSnapshot();
  // Clear existing canvas
  nodes.value = [];
  edges.value = [];
  nodeCounter = 0;

  if (config.name) {
    projectName.value = config.name;
  }
  projectId.value = config.project_id || crypto.randomUUID();
  projectVersion.value = config.version || '1.0';
  projectDescription.value = config.description || '';
  projectInteractive.value = !!config.interactive;

  // Load settings (deep merge to preserve nested defaults)
  const defaults = createDefaultSettings();
  if (config.settings) {
    const s = config.settings;
    projectSettings.value = {
      default_provider: s.default_provider || defaults.default_provider,
      providers: s.providers || defaults.providers,
      api_keys: s.api_keys || defaults.api_keys,
      base_urls: s.base_urls || defaults.base_urls,
      credentials_files: s.credentials_files || defaults.credentials_files,
      allowed_commands: s.allowed_commands || defaults.allowed_commands,
      defaults: { ...defaults.defaults, ...(s.defaults || {}) },
      execution: { ...defaults.execution, ...(s.execution || {}) },
      logging: { ...defaults.logging, ...(s.logging || {}) },
      hub_url: s.hub_url || defaults.hub_url,
      spawn: s.spawn,
      // Carried through for the same reason spawn is: this whitelist is
      // hand-maintained, so a key missing from it is silently deleted on the
      // next save — the same defect already fixed for settings.spawn.
      agent_chat: s.agent_chat,
    };
  } else {
    projectSettings.value = defaults;
  }

  const toolY = 400;
  const agentY = 200;
  const orchestratorY = 50;
  const centerX = 400;
  const spacing = 250;

  // Filter out $ref placeholders (empty entries from modular configs)
  const validTools = (config.tools || []).filter(t => t.name);
  const validAgents = (config.agents || []).filter(a => a.name);
  const validSkills = (config.skills || []).filter(s => s.name);

  // Add tools first (bottom row)
  const toolIdMap: Record<string, string> = {};
  for (const tool of validTools) {
    const id = `tool-${++nodeCounter}`;
    toolIdMap[tool.name] = id;

    nodes.value.push({
      id,
      type: 'tool',
      position: { x: centerX - validTools.length * spacing / 2 + Object.keys(toolIdMap).length * spacing, y: toolY },
      data: tool,
    });
  }

  // Add skills (below tools)
  const skillIdMap: Record<string, string> = {};
  const skillY = 500;
  let skillIndex = 0;
  for (const skill of validSkills) {
    const id = `skill-${++nodeCounter}`;
    skillIdMap[skill.name] = id;
    skillIndex++;

    nodes.value.push({
      id,
      type: 'skill',
      position: { x: centerX - validSkills.length * spacing / 2 + skillIndex * spacing, y: skillY },
      data: skill,
    });
  }

  // Resolve default provider for agents/orchestrator
  const defaultProvider = projectSettings.value.default_provider || 'openai';
  const defaultModel = projectSettings.value.defaults?.model || '';

  // Add agents (middle row)
  const agentIdMap: Record<string, string> = {};
  for (const agent of validAgents) {
    if (!agent.provider) agent.provider = defaultProvider;
    if (!agent.model && defaultModel) agent.model = defaultModel;

    const id = `agent-${++nodeCounter}`;
    agentIdMap[agent.name] = id;

    nodes.value.push({
      id,
      type: 'agent',
      position: { x: centerX - validAgents.length * spacing / 2 + Object.keys(agentIdMap).length * spacing, y: agentY },
      data: agent,
    });

    for (const toolName of agent.tools || []) {
      const toolId = toolIdMap[toolName];
      if (toolId) {
        edges.value.push({
          id: `edge-${Date.now()}-${Math.random()}`,
          source: id,
          target: toolId,
          animated: true,
        });
      }
    }

    for (const skillName of agent.skills || []) {
      const skillId = skillIdMap[skillName];
      if (skillId) {
        edges.value.push({
          id: `edge-${Date.now()}-${Math.random()}`,
          source: id,
          target: skillId,
          animated: true,
        });
      }
    }
  }

  // Add sub-orchestrators (from orchestrators: array) — create nodes and register in agentIdMap
  // so the main orchestrator can wire to them by name.
  if (config.orchestrators?.length) {
    let subOrchX = centerX - (config.orchestrators.length - 1) * spacing / 2;
    for (const subOrch of config.orchestrators) {
      if (!subOrch.provider) subOrch.provider = defaultProvider;
      const subId = `orchestrator-${++nodeCounter}`;
      nodes.value.push({
        id: subId,
        type: 'orchestrator',
        position: { x: subOrchX, y: agentY - 80 },
        data: subOrch,
      });
      agentIdMap[subOrch.name] = subId;
      subOrchX += spacing;
    }
  }

  // Add orchestrator (top)
  if (config.orchestrator) {
    if (!config.orchestrator.provider) config.orchestrator.provider = defaultProvider;

    const id = `orchestrator-${++nodeCounter}`;

    nodes.value.push({
      id,
      type: 'orchestrator',
      position: { x: centerX, y: orchestratorY },
      data: config.orchestrator,
    });

    // Create group nodes from pipeline steps
    if (config.orchestrator.strategy === 'Pipeline' && config.orchestrator.pipeline?.steps) {
      const pipelineGroupId = `group-${++nodeCounter}`;
      const steps = config.orchestrator.pipeline.steps;
      const groupPadding = 40;
      const stepSpacing = 200;

      // Create a pipeline group containing all steps
      nodes.value.push({
        id: pipelineGroupId,
        type: 'group',
        position: { x: centerX - 200, y: agentY - 60 },
        style: { width: `${Math.max(steps.length * stepSpacing + groupPadding * 2, 500)}px`, height: '400px' },
        data: {
          _id: generateEntityId(),
          name: config.orchestrator.name || 'Pipeline',
          blockType: 'pipeline',
          collapsed: false,
        } as GroupConfig,
      });

      // Connect orchestrator to pipeline group
      edges.value.push({
        id: `edge-${Date.now()}-orch-pipeline`,
        source: id,
        target: pipelineGroupId,
        animated: true,
      });

      let stepX = groupPadding;
      for (const step of steps) {
        if (step.type === 'parallel' && step.steps) {
          // Parallel group inside pipeline
          const parallelGroupId = `group-${++nodeCounter}`;
          const parallelW = Math.max(step.steps.length * stepSpacing, 300);

          nodes.value.push({
            id: parallelGroupId,
            type: 'group',
            parentNode: pipelineGroupId,
            extent: 'parent' as const,
            expandParent: true,
            position: { x: stepX, y: 50 },
            style: { width: `${parallelW}px`, height: '280px' },
            data: {
              _id: generateEntityId(),
              name: step.name || 'Parallel',
              blockType: 'parallel',
              collapsed: false,
            } as GroupConfig,
          });

          // Move agents into parallel group
          let subX = groupPadding;
          for (const subStep of step.steps) {
            const agId = agentIdMap[subStep.agent || ''];
            if (agId) {
              const agNode = nodes.value.find(n => n.id === agId);
              if (agNode) {
                agNode.parentNode = parallelGroupId;
                agNode.extent = 'parent' as const;
                agNode.expandParent = true;
                agNode.position = { x: subX, y: 50 };
              }
            }
            subX += stepSpacing;
          }

          stepX += parallelW + groupPadding;
        } else if (step.type === 'loop' && step.steps) {
          // Loop group inside pipeline
          const loopGroupId = `group-${++nodeCounter}`;
          const loopW = Math.max((step.steps?.length || 1) * stepSpacing, 300);

          nodes.value.push({
            id: loopGroupId,
            type: 'group',
            parentNode: pipelineGroupId,
            extent: 'parent' as const,
            expandParent: true,
            position: { x: stepX, y: 50 },
            style: { width: `${loopW}px`, height: '280px' },
            data: {
              _id: generateEntityId(),
              name: step.name || 'Loop',
              blockType: 'loop',
              collapsed: false,
              maxIterations: step.max_iterations || 5,
              conditionAgent: step.condition_agent || '',
              conditionPrompt: step.condition_prompt || '',
            } as GroupConfig,
          });

          // Move agents into loop group
          let subX = groupPadding;
          for (const subStep of step.steps) {
            const agId = agentIdMap[subStep.agent || ''];
            if (agId) {
              const agNode = nodes.value.find(n => n.id === agId);
              if (agNode) {
                agNode.parentNode = loopGroupId;
                agNode.extent = 'parent' as const;
                agNode.expandParent = true;
                agNode.position = { x: subX, y: 50 };
              }
            }
            subX += stepSpacing;
          }

          stepX += loopW + groupPadding;
        } else if (step.agent) {
          // Sequential step — move agent into pipeline group
          const agId = agentIdMap[step.agent];
          if (agId) {
            const agNode = nodes.value.find(n => n.id === agId);
            if (agNode) {
              agNode.parentNode = pipelineGroupId;
              agNode.extent = 'parent' as const;
              agNode.expandParent = true;
              agNode.position = { x: stepX, y: 50 };
            }
          }
          stepX += stepSpacing;
        }
      }
    } else {
      // Non-pipeline: connect orchestrator directly to agents
      for (const agentName of config.orchestrator.agents || []) {
        const agentId = agentIdMap[agentName];
        if (agentId) {
          edges.value.push({
            id: `edge-${Date.now()}-${Math.random()}`,
            source: id,
            target: agentId,
            animated: true,
          });
        }
      }
    }
  }

  // Detect orchestrator→group edges and set _connectedGroupId
  for (const edge of edges.value) {
    const srcNode = nodes.value.find(n => n.id === edge.source);
    const tgtNode = nodes.value.find(n => n.id === edge.target);
    if (srcNode?.type === 'orchestrator' && tgtNode?.type === 'group') {
      const orchData = srcNode.data as OrchestratorConfig;
      const grpData = tgtNode.data as GroupConfig;
      orchData._connectedGroupId = grpData._id || tgtNode.id;
    }
  }

  // Auto-fit groups + relayout after import
  // Wait for VueFlow to render and measure node dimensions, then relayout
  nextTick(() => {
    autoFitAllGroups();
    // First fitView with raw positions, then relayout with actual dimensions
    fitView({ padding: 0.1 });
    setTimeout(() => relayoutDesign(), 200);
  });

  // Auto-open Settings panel if settings were imported
  if (config.settings) {
    showSettings.value = true;
    showEditor.value = false;
    selectedNodeRef.value = null;
  }
}

// --- Export ---

function exportYaml() {
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  const yamlContent = generateYaml(projectName.value, projectId.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators, projectVersion.value, projectDescription.value, projectInteractive.value);
  downloadYaml(yamlContent, `${projectName.value.toLowerCase().replace(/\s+/g, '-')}.yaml`);
  showExportMenu.value = false;
  isDirty.value = false;
}

async function exportModular() {
  const { agents, tools, skills, orchestrator, orchestrators } = collectCanvasData();
  const fileSet = generateModularFiles(
    projectName.value, agents, tools, skills, orchestrator, projectSettings.value, orchestrators
  );
  await downloadModularZip(fileSet, projectName.value);
  showExportMenu.value = false;
  isDirty.value = false;
}

// --- Import ---

function triggerImport() {
  fileInput.value?.click();
  showImportMenu.value = false;
}

function triggerDirectoryImport() {
  dirInput.value?.click();
  showImportMenu.value = false;
}

function handleImport(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  if (isDirty.value && nodes.value.length > 0) {
    if (!confirm('You have unsaved changes. Import will replace the current project. Continue?')) {
      input.value = '';
      return;
    }
  }

  const reader = new FileReader();
  reader.onload = (e) => {
    const content = e.target?.result as string;
    const config = parseYaml(content);
    if (!config) {
      alert('Failed to parse YAML file');
      return;
    }
    importConfig(config);
  };
  reader.readAsText(file);
  input.value = '';
}

async function handleDirectoryImport(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = input.files;
  if (!files || files.length === 0) return;
  if (isDirty.value && nodes.value.length > 0) {
    if (!confirm('You have unsaved changes. Import will replace the current project. Continue?')) {
      input.value = '';
      return;
    }
  }

  const entries: { path: string; content: string }[] = [];
  for (const file of Array.from(files)) {
    // Only process config-related files
    if (!/\.(yaml|yml|md|txt|prompt)$/i.test(file.name)) continue;

    // Strip the root directory name from webkitRelativePath
    const parts = file.webkitRelativePath.split('/');
    const relativePath = parts.slice(1).join('/');

    const content = await file.text();
    entries.push({ path: relativePath, content });
  }

  const config = parseModularFiles(entries);
  if (!config) {
    alert('Failed to parse modular config directory');
    return;
  }
  importConfig(config);
  input.value = '';
}

// --- Dropdown close ---

function closeMenus() {
  showImportMenu.value = false;
  showExportMenu.value = false;
  showScaffoldMenu.value = false;
}

// --- Scaffold Templates ---

interface ScaffoldTemplate {
  id: string;
  name: string;
  description: string;
  category: 'basic' | 'multi-agent' | 'pipeline';
  build: () => void;
}

const showScaffoldMenu = ref(false);

const scaffoldTemplates: ScaffoldTemplate[] = [
  {
    id: 'llm-chat',
    name: 'LLM Chat',
    description: 'Simple conversational assistant (no tools)',
    category: 'basic',
    build: () => scaffoldLLMChat(),
  },
  {
    id: 'single-agent',
    name: 'Single Agent + Tools',
    description: 'One agent with CLI and filesystem tools',
    category: 'basic',
    build: () => scaffoldSingleAgent(),
  },
  {
    id: 'code-review',
    name: 'Code Review',
    description: 'Code reviewer with file-system read tools',
    category: 'basic',
    build: () => scaffoldCodeReview(),
  },
  {
    id: 'data-analysis',
    name: 'Data Analysis',
    description: 'Data analyst with file-system and CLI tools',
    category: 'basic',
    build: () => scaffoldDataAnalysis(),
  },
  {
    id: 'rag-assistant',
    name: 'RAG Assistant',
    description: 'Search knowledge base and synthesize answers',
    category: 'basic',
    build: () => scaffoldRAGAssistant(),
  },
  {
    id: 'dev-team',
    name: 'Dev Team (Pipeline)',
    description: 'Planner → Developer → Reviewer pipeline',
    category: 'multi-agent',
    build: () => scaffoldPipeline(),
  },
  {
    id: 'qa-pipeline',
    name: 'QA Pipeline',
    description: 'Test generator → Validator pipeline',
    category: 'multi-agent',
    build: () => scaffoldQAPipeline(),
  },
  {
    id: 'research-team',
    name: 'Research Team (ReAct)',
    description: 'Researcher + Coder + Reviewer with orchestrator',
    category: 'multi-agent',
    build: () => scaffoldResearchTeam(),
  },
  {
    id: 'hierarchical',
    name: 'Hierarchical',
    description: 'Supervisor delegates to worker agents',
    category: 'multi-agent',
    build: () => scaffoldHierarchical(),
  },
];

function applyScaffold(template: ScaffoldTemplate) {
  if (isDirty.value && nodes.value.length > 0) {
    if (!confirm('You have unsaved changes. Scaffold will replace the canvas nodes. Your settings will be kept. Continue?')) return;
  }
  // Clear only canvas — preserve settings
  nodes.value = [];
  edges.value = [];
  nodeCounter = 0;
  showEditor.value = false;
  selectedNodeRef.value = null;
  template.build();
  showScaffoldMenu.value = false;
  isDirty.value = true;
  // Detect orchestrator→group edges and set _connectedGroupId
  for (const edge of edges.value) {
    const srcNode = nodes.value.find(n => n.id === edge.source);
    const tgtNode = nodes.value.find(n => n.id === edge.target);
    if (srcNode?.type === 'orchestrator' && tgtNode?.type === 'group') {
      const orchData = srcNode.data as OrchestratorConfig;
      const grpData = tgtNode.data as GroupConfig;
      orchData._connectedGroupId = grpData._id || tgtNode.id;
    }
  }
  nextTick(() => { autoFitAllGroups(); fitView(); });
}

function scaffoldLLMChat() {
  projectName.value = 'LLM Chat';
  projectId.value = crypto.randomUUID();
  const agentId = `agent-${++nodeCounter}`;
  addNodes([
    { id: agentId, type: 'agent', position: { x: 200, y: 80 }, data: {
      name: 'Assistant', role: 'worker', provider: '', model: '',
      system_prompt: 'You are a helpful, friendly assistant. Answer questions clearly and concisely. If you are unsure about something, say so honestly.',
      tools: [], settings: { max_iterations: 15 } as any,
    } as AgentConfig },
  ]);
}

function scaffoldCodeReview() {
  projectName.value = 'Code Review';
  projectId.value = crypto.randomUUID();
  const readFile = `tool-${++nodeCounter}`;
  const listFiles = `tool-${++nodeCounter}`;
  const agent = `agent-${++nodeCounter}`;

  addNodes([
    { id: agent, type: 'agent', position: { x: 180, y: 50 }, data: {
      name: 'CodeReviewer', role: 'worker', provider: '', model: '',
      system_prompt: 'You are an expert code reviewer. Read code and provide structured feedback: correctness, security, performance, style. Cite line numbers and prioritize by severity.',
      tools: ['read_file', 'list_files'],
      settings: { max_iterations: 20, reflection: { enabled: true, mode: 'before_answer', frequency: 'always' } } as any,
    } as AgentConfig },
    { id: readFile, type: 'tool', position: { x: 80, y: 250 }, data: {
      name: 'read_file', type: 'fs', description: 'Read a source file for review', operation: 'read',
    } as ToolConfig },
    { id: listFiles, type: 'tool', position: { x: 330, y: 250 }, data: {
      name: 'list_files', type: 'fs', description: 'List files in a directory', operation: 'list',
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: agent, target: readFile, animated: true },
    { id: `e-${Date.now() + 1}`, source: agent, target: listFiles, animated: true },
  ]);
}

function scaffoldDataAnalysis() {
  projectName.value = 'Data Analysis';
  projectId.value = crypto.randomUUID();
  const readFile = `tool-${++nodeCounter}`;
  const searchFiles = `tool-${++nodeCounter}`;
  const runCmd = `tool-${++nodeCounter}`;
  const agent = `agent-${++nodeCounter}`;

  addNodes([
    { id: agent, type: 'agent', position: { x: 200, y: 40 }, data: {
      name: 'DataAnalyst', role: 'worker', provider: '', model: '',
      system_prompt: 'You are a data analyst. Explore data, identify patterns, compute statistics, and present clear insights. Use shell tools for counting and filtering. Always show your work.',
      tools: ['read_file', 'search_files', 'run_command'],
      settings: { max_iterations: 25, reflection: { enabled: true, mode: 'before_answer', frequency: 'always' } } as any,
    } as AgentConfig },
    { id: readFile, type: 'tool', position: { x: 30, y: 240 }, data: {
      name: 'read_file', type: 'fs', description: 'Read data files (CSV, JSON, text)', operation: 'read',
    } as ToolConfig },
    { id: searchFiles, type: 'tool', position: { x: 230, y: 240 }, data: {
      name: 'search_files', type: 'fs', description: 'Search for patterns in files', operation: 'search',
    } as ToolConfig },
    { id: runCmd, type: 'tool', position: { x: 430, y: 240 }, data: {
      name: 'run_command', type: 'cli', description: 'Run analysis commands',
      command: '{{command}}', sandbox: { type: 'local_restricted', allowed_commands: ['awk', 'sort', 'uniq', 'wc', 'head', 'tail', 'cat'] },
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: agent, target: readFile, animated: true },
    { id: `e-${Date.now() + 1}`, source: agent, target: searchFiles, animated: true },
    { id: `e-${Date.now() + 2}`, source: agent, target: runCmd, animated: true },
  ]);
}

function scaffoldRAGAssistant() {
  projectName.value = 'RAG Assistant';
  projectId.value = crypto.randomUUID();
  const searchFiles = `tool-${++nodeCounter}`;
  const readFile = `tool-${++nodeCounter}`;
  const skill = `skill-${++nodeCounter}`;
  const agent = `agent-${++nodeCounter}`;

  addNodes([
    { id: agent, type: 'agent', position: { x: 180, y: 40 }, data: {
      name: 'RAGAssistant', role: 'worker', provider: '', model: '',
      system_prompt: 'You are a knowledge assistant. Search the knowledge base, read relevant files, and synthesize cited answers. If no relevant info exists, say so.',
      tools: ['search_files', 'read_file'], skills: ['summarise'],
      settings: { max_iterations: 20, reflection: { enabled: true, mode: 'before_answer', frequency: 'always' } } as any,
    } as AgentConfig },
    { id: searchFiles, type: 'tool', position: { x: 60, y: 250 }, data: {
      name: 'search_files', type: 'fs', description: 'Search knowledge base files', operation: 'search',
    } as ToolConfig },
    { id: readFile, type: 'tool', position: { x: 300, y: 250 }, data: {
      name: 'read_file', type: 'fs', description: 'Read knowledge base document', operation: 'read',
    } as ToolConfig },
    { id: skill, type: 'skill', position: { x: 460, y: 130 }, data: {
      name: 'summarise', description: 'Summarize and synthesize retrieved information',
      tools: ['search_files', 'read_file'],
      prompt_template: 'Given the retrieved documents, synthesize a clear, accurate answer. Cite source files for each key fact.',
    } as SkillConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: agent, target: searchFiles, animated: true },
    { id: `e-${Date.now() + 1}`, source: agent, target: readFile, animated: true },
    { id: `e-${Date.now() + 2}`, source: agent, target: skill, animated: true },
  ]);
}

function scaffoldQAPipeline() {
  projectName.value = 'QA Pipeline';
  projectId.value = crypto.randomUUID();
  const readFile = `tool-${++nodeCounter}`;
  const writeFile = `tool-${++nodeCounter}`;
  const testGen = `agent-${++nodeCounter}`;
  const validator = `agent-${++nodeCounter}`;
  const orch = `orchestrator-${++nodeCounter}`;

  addNodes([
    { id: orch, type: 'orchestrator', position: { x: 230, y: 20 }, data: {
      name: 'QALead', role: 'supervisor', strategy: 'Pipeline', provider: '', model: '',
      system_prompt: 'Coordinate test generation and validation.',
      agents: ['TestGenerator', 'Validator'],
      pipeline: { steps: [
        { name: 'generate', type: 'sequential', agent: 'TestGenerator' },
        { name: 'validate', type: 'sequential', agent: 'Validator' },
      ] },
    } as OrchestratorConfig },
    { id: testGen, type: 'agent', position: { x: 80, y: 180 }, data: {
      name: 'TestGenerator', role: 'worker', provider: '', model: '',
      system_prompt: 'Read source code and generate comprehensive tests: unit tests, edge cases, error scenarios. Write tests to disk.',
      tools: ['read_file', 'write_file'], settings: { max_iterations: 25 } as any,
    } as AgentConfig },
    { id: validator, type: 'agent', position: { x: 380, y: 180 }, data: {
      name: 'Validator', role: 'worker', provider: '', model: '',
      system_prompt: 'Review generated tests for coverage, quality, and missing edge cases. Provide pass/fail verdict with coverage score (0-100).',
      tools: ['read_file'], settings: { max_iterations: 15 } as any,
    } as AgentConfig },
    { id: readFile, type: 'tool', position: { x: 80, y: 350 }, data: {
      name: 'read_file', type: 'fs', description: 'Read source or test files', operation: 'read',
    } as ToolConfig },
    { id: writeFile, type: 'tool', position: { x: 380, y: 350 }, data: {
      name: 'write_file', type: 'fs', description: 'Write test files', operation: 'write',
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: orch, target: testGen, animated: true },
    { id: `e-${Date.now() + 1}`, source: orch, target: validator, animated: true },
    { id: `e-${Date.now() + 2}`, source: testGen, target: readFile, animated: true },
    { id: `e-${Date.now() + 3}`, source: testGen, target: writeFile, animated: true },
    { id: `e-${Date.now() + 4}`, source: validator, target: readFile, animated: true },
  ]);
}

function scaffoldSingleAgent() {
  projectName.value = 'Single Agent';
  projectId.value = crypto.randomUUID();
  const toolId = `tool-${++nodeCounter}`;
  const tool2Id = `tool-${++nodeCounter}`;
  const agentId = `agent-${++nodeCounter}`;

  addNodes([
    { id: toolId, type: 'tool', position: { x: 50, y: 250 }, data: {
      name: 'cli', type: 'cli', description: 'Execute shell commands',
      command: '{{command}}', sandbox: { type: 'local_restricted', allowed_commands: ['grep', 'find', 'cat', 'ls'] },
    } as ToolConfig },
    { id: tool2Id, type: 'tool', position: { x: 300, y: 250 }, data: {
      name: 'files', type: 'fs', description: 'Read, write, and list files',
    } as ToolConfig },
    { id: agentId, type: 'agent', position: { x: 150, y: 50 }, data: {
      name: 'assistant', role: 'worker', provider: '', model: '',
      system_prompt: 'You are a helpful assistant with access to CLI and file tools.',
      tools: ['cli', 'files'],
    } as AgentConfig },
  ]);
  addNewEdges([
    { id: `edge-${Date.now()}`, source: agentId, target: toolId, animated: true },
    { id: `edge-${Date.now() + 1}`, source: agentId, target: tool2Id, animated: true },
  ]);
}

function scaffoldResearchTeam() {
  projectName.value = 'Research Team';
  projectId.value = crypto.randomUUID();
  const toolSearch = `tool-${++nodeCounter}`;
  const toolFiles = `tool-${++nodeCounter}`;
  const researcher = `agent-${++nodeCounter}`;
  const coder = `agent-${++nodeCounter}`;
  const reviewer = `agent-${++nodeCounter}`;
  const orch = `orchestrator-${++nodeCounter}`;

  addNodes([
    { id: orch, type: 'orchestrator', position: { x: 250, y: 20 }, data: {
      name: 'Orchestrator', role: 'supervisor', strategy: 'ReAct', provider: '', model: '',
      system_prompt: 'You coordinate a research team. First research, then code, then review.',
      agents: ['researcher', 'coder', 'reviewer'],
    } as OrchestratorConfig },
    { id: researcher, type: 'agent', position: { x: 30, y: 180 }, data: {
      name: 'researcher', role: 'worker', provider: '', model: '',
      system_prompt: 'Search codebases and documentation. Summarize findings clearly.', tools: ['search'],
    } as AgentConfig },
    { id: coder, type: 'agent', position: { x: 250, y: 180 }, data: {
      name: 'coder', role: 'worker', provider: '', model: '',
      system_prompt: 'Implement changes based on research. Write clean, minimal code.', tools: ['files'],
    } as AgentConfig },
    { id: reviewer, type: 'agent', position: { x: 470, y: 180 }, data: {
      name: 'reviewer', role: 'worker', provider: '', model: '',
      system_prompt: 'Review code for bugs, security issues, and style. Be concise.', tools: ['files'],
    } as AgentConfig },
    { id: toolSearch, type: 'tool', position: { x: 30, y: 350 }, data: {
      name: 'search', type: 'cli', description: 'Search for patterns in code',
      command: 'grep -rn "{{query}}" {{path}}',
    } as ToolConfig },
    { id: toolFiles, type: 'tool', position: { x: 360, y: 350 }, data: {
      name: 'files', type: 'fs', description: 'Read, write, and list files',
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: orch, target: researcher, animated: true },
    { id: `e-${Date.now() + 1}`, source: orch, target: coder, animated: true },
    { id: `e-${Date.now() + 2}`, source: orch, target: reviewer, animated: true },
    { id: `e-${Date.now() + 3}`, source: researcher, target: toolSearch, animated: true },
    { id: `e-${Date.now() + 4}`, source: coder, target: toolFiles, animated: true },
    { id: `e-${Date.now() + 5}`, source: reviewer, target: toolFiles, animated: true },
  ]);
}

function scaffoldPipeline() {
  projectName.value = 'Dev Team';
  projectId.value = crypto.randomUUID();
  const toolCli = `tool-${++nodeCounter}`;
  const toolFs = `tool-${++nodeCounter}`;
  const step1 = `agent-${++nodeCounter}`;
  const step2 = `agent-${++nodeCounter}`;
  const step3 = `agent-${++nodeCounter}`;
  const orch = `orchestrator-${++nodeCounter}`;

  addNodes([
    { id: orch, type: 'orchestrator', position: { x: 200, y: 20 }, data: {
      name: 'Pipeline', role: 'supervisor', strategy: 'Pipeline', provider: '', model: '',
      system_prompt: 'Execute the pipeline steps in order.', agents: ['analyze', 'generate', 'review'],
      pipeline: { steps: [
        { name: 'step-1', type: 'sequential', agent: 'analyze' },
        { name: 'step-2', type: 'sequential', agent: 'generate' },
        { name: 'step-3', type: 'sequential', agent: 'review' },
      ] },
    } as OrchestratorConfig },
    { id: step1, type: 'agent', position: { x: 30, y: 180 }, data: {
      name: 'analyze', role: 'worker', provider: '', model: '',
      system_prompt: 'Analyze the input and produce a structured plan.', tools: ['cli'],
    } as AgentConfig },
    { id: step2, type: 'agent', position: { x: 250, y: 180 }, data: {
      name: 'generate', role: 'worker', provider: '', model: '',
      system_prompt: 'Generate output based on the analysis.', tools: ['files'],
    } as AgentConfig },
    { id: step3, type: 'agent', position: { x: 470, y: 180 }, data: {
      name: 'review', role: 'worker', provider: '', model: '',
      system_prompt: 'Review the generated output for quality.', tools: ['files'],
    } as AgentConfig },
    { id: toolCli, type: 'tool', position: { x: 30, y: 350 }, data: {
      name: 'cli', type: 'cli', description: 'Execute commands', command: '{{command}}',
    } as ToolConfig },
    { id: toolFs, type: 'tool', position: { x: 360, y: 350 }, data: {
      name: 'files', type: 'fs', description: 'File operations',
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: orch, target: step1, animated: true },
    { id: `e-${Date.now() + 1}`, source: orch, target: step2, animated: true },
    { id: `e-${Date.now() + 2}`, source: orch, target: step3, animated: true },
    { id: `e-${Date.now() + 3}`, source: step1, target: toolCli, animated: true },
    { id: `e-${Date.now() + 4}`, source: step2, target: toolFs, animated: true },
    { id: `e-${Date.now() + 5}`, source: step3, target: toolFs, animated: true },
  ]);
}

function scaffoldHierarchical() {
  projectName.value = 'Hierarchical Team';
  projectId.value = crypto.randomUUID();
  const toolCli = `tool-${++nodeCounter}`;
  const supervisor = `orchestrator-${++nodeCounter}`;
  const worker1 = `agent-${++nodeCounter}`;
  const worker2 = `agent-${++nodeCounter}`;
  const worker3 = `agent-${++nodeCounter}`;

  addNodes([
    { id: supervisor, type: 'orchestrator', position: { x: 250, y: 20 }, data: {
      name: 'Supervisor', role: 'supervisor', strategy: 'Hierarchical', provider: '', model: '',
      system_prompt: 'You are a supervisor. Break tasks into subtasks and delegate to workers.',
      agents: ['planner', 'implementer', 'tester'],
    } as OrchestratorConfig },
    { id: worker1, type: 'agent', position: { x: 30, y: 180 }, data: {
      name: 'planner', role: 'worker', provider: '', model: '',
      system_prompt: 'Plan the approach for the given task. Output a step-by-step plan.', tools: [],
    } as AgentConfig },
    { id: worker2, type: 'agent', position: { x: 250, y: 180 }, data: {
      name: 'implementer', role: 'worker', provider: '', model: '',
      system_prompt: 'Implement the planned steps using available tools.', tools: ['cli'],
    } as AgentConfig },
    { id: worker3, type: 'agent', position: { x: 470, y: 180 }, data: {
      name: 'tester', role: 'worker', provider: '', model: '',
      system_prompt: 'Test the implementation. Report any issues found.', tools: ['cli'],
    } as AgentConfig },
    { id: toolCli, type: 'tool', position: { x: 250, y: 350 }, data: {
      name: 'cli', type: 'cli', description: 'Execute commands', command: '{{command}}',
    } as ToolConfig },
  ]);
  addNewEdges([
    { id: `e-${Date.now()}`, source: supervisor, target: worker1, animated: true },
    { id: `e-${Date.now() + 1}`, source: supervisor, target: worker2, animated: true },
    { id: `e-${Date.now() + 2}`, source: supervisor, target: worker3, animated: true },
    { id: `e-${Date.now() + 3}`, source: worker2, target: toolCli, animated: true },
    { id: `e-${Date.now() + 4}`, source: worker3, target: toolCli, animated: true },
  ]);
}

// Trigger for RunBar to clear its persisted state (incremented on clear/new project)
const runBarResetTrigger = ref(0);

function clearCanvas() {
  saveSnapshot();
  nodes.value = [];
  edges.value = [];
  nodeCounter = 0;
  showEditor.value = false;
  showSettings.value = false;
  selectedNodeRef.value = null;
  projectSettings.value = createDefaultSettings();
  projectId.value = crypto.randomUUID(); // New project
  isDirty.value = false;
  showResults.value = false;
  runBarResetTrigger.value++;
  // Clear exec tree (new project has no runs yet)
  clearExecTree();
}

function confirmClear() {
  if (isDirty.value && nodes.value.length > 0) {
    if (!confirm('You have unsaved changes. Clear will remove everything. Continue?')) return;
  }
  clearCanvas();
}

// --- Inspect from history: load config into canvas ---
// Placed at end of setup so all refs (nodes, edges, etc.) are initialized
watch(() => props.pendingInspect, (data) => {
  if (data) {
    if (isDirty.value && nodes.value.length > 0) {
      if (!confirm('Builder has unsaved changes. Replace with session config?')) {
        emit('inspect-consumed');
        return;
      }
    }
    const config = parseYaml(data.yaml);
    if (config) {
      importConfig(config);
      // Adopt project ID from session if available (overrides config's project_id)
      if (data.projectId) projectId.value = data.projectId;
      isDirty.value = false;
    }
    emit('inspect-consumed');
  }
}, { immediate: true });

// Apply search highlight/dim classes to nodes (watch query, not matchedNodeIds to avoid loop)
watch([searchQuery, searchField, searchOpen], () => {
  const matched = matchedNodeIds.value;
  const isSearching = searchOpen.value && searchQuery.value.trim();
  // Mutate class in place to avoid creating new node objects (prevents reactivity loop)
  for (const n of nodes.value) {
    let cls = ((n.class as string) || '').replace(/search-dim|search-highlight/g, '').trim();
    if (isSearching) {
      cls += matched.has(n.id) ? ' search-highlight' : ' search-dim';
    }
    n.class = cls.trim();
  }
  for (const e of edges.value) {
    if (isSearching) {
      const match = matched.has(e.source) || matched.has(e.target);
      e.class = match ? '' : 'search-dim';
    } else {
      e.class = '';
    }
  }
});

// --- Auto-save document to localStorage (survives browser restart, clears on server restart) ---
const CANVAS_STORAGE_KEY = 'rakitsu-canvas-state';
const BOOT_ID_KEY = 'rakitsu-canvas-boot-id';
let autoSaveTimer: ReturnType<typeof setTimeout> | null = null;
let autoSaveEnabled = false;

// localStorage outlives the session and is readable by any script on this
// origin, so credentials never go there: provider api_key/credentials_file,
// the flat api_keys/credentials_files maps, a tool's api_key, and any tool
// env value that is a literal rather than a ${VAR} reference. Applied on
// save and on restore, so state written by older versions is cleaned too.
function isEnvRef(v: unknown): boolean {
  return typeof v === 'string' && v.startsWith('${') && v.endsWith('}');
}

function stripSecretsFromSettings(settings: Record<string, any>): Record<string, any> {
  const providers: Record<string, any> = {};
  for (const [name, p] of Object.entries(settings.providers ?? {})) {
    const { api_key: _k, credentials_file: _c, ...rest } = (p ?? {}) as Record<string, any>;
    providers[name] = rest;
  }
  return { ...settings, api_keys: {}, credentials_files: {}, providers };
}

function stripSecretsFromNodeData(data: Record<string, any>): Record<string, any> {
  const { api_key: _k, ...rest } = data ?? {};
  if (rest.env && typeof rest.env === 'object') {
    rest.env = Object.fromEntries(Object.entries(rest.env).filter(([, v]) => isEnvRef(v)));
  }
  return rest;
}

function saveCanvasToStorage() {
  if (!autoSaveEnabled) return;
  try {
    const state = {
      nodes: nodes.value.map(n => ({
        id: n.id, type: n.type, position: n.position,
        data: stripSecretsFromNodeData({ ...n.data, _runStatus: undefined, _tokenCount: undefined, _iterationCount: undefined, _lastThought: undefined, _errorMessage: undefined, _hasBreakpoint: undefined }),
        parentNode: n.parentNode, extent: n.extent, expandParent: n.expandParent,
        style: n.style, hidden: n.hidden,
      })),
      edges: edges.value.map(e => ({
        id: e.id, source: e.source, target: e.target,
        sourceHandle: e.sourceHandle, targetHandle: e.targetHandle, label: e.label,
      })),
      projectName: projectName.value,
      projectId: projectId.value,
      settings: stripSecretsFromSettings(projectSettings.value as Record<string, any>),
    };
    localStorage.setItem(CANVAS_STORAGE_KEY, JSON.stringify(state));
  } catch { /* localStorage may be full or unavailable */ }
}

function debouncedAutoSave() {
  if (autoSaveTimer) clearTimeout(autoSaveTimer);
  autoSaveTimer = setTimeout(saveCanvasToStorage, 2000);
}

function restoreCanvasFromStorage(): boolean {
  try {
    const raw = localStorage.getItem(CANVAS_STORAGE_KEY);
    if (!raw) return false;
    const state = JSON.parse(raw);
    if (!state.nodes?.length) return false;
    nodes.value = state.nodes.map((n: any) => ({ ...n, data: stripSecretsFromNodeData(n.data) }));
    edges.value = state.edges ?? [];
    if (state.projectName) projectName.value = state.projectName;
    if (state.projectId) projectId.value = state.projectId;
    if (state.settings) projectSettings.value = { ...createDefaultSettings(), ...stripSecretsFromSettings(state.settings) };
    return true;
  } catch {
    return false;
  }
}

// Watch for changes and auto-save (debounced)
watch([nodes, edges, projectName, projectId, projectSettings], debouncedAutoSave, { deep: true });

onMounted(async () => {
  window.addEventListener('keydown', handleKeydown);
  window.addEventListener('keyup', handleKeyupShift);

  // Check server boot_id — if server restarted, clear saved state
  try {
    const res = await fetch(`http://${selfUrl.value}/api/status`);
    const data = await res.json();
    const serverBootId = data.boot_id;
    const savedBootId = localStorage.getItem(BOOT_ID_KEY);
    if (serverBootId && savedBootId && serverBootId === savedBootId) {
      // Same server session — restore canvas
      restoreCanvasFromStorage();
    } else {
      // Server restarted or first visit — clear old state, store new boot_id
      localStorage.removeItem(CANVAS_STORAGE_KEY);
      if (serverBootId) localStorage.setItem(BOOT_ID_KEY, serverBootId);
    }
  } catch {
    // Server unavailable — try restoring anyway (offline editing)
    restoreCanvasFromStorage();
  }
  autoSaveEnabled = true;
});
onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown);
  window.removeEventListener('keyup', handleKeyupShift);
  if (autoSaveTimer) clearTimeout(autoSaveTimer);
});
</script>

<template>
  <div class="visual-builder" @click="closeMenus">
    <div class="builder-header">
      <div class="header-left">
        <h2>🎨 Visual Builder</h2>
        <input
          v-model="projectName"
          class="project-name-input"
          placeholder="Project Name"
        />
        <span v-if="isDirty" class="dirty-badge" title="Unsaved changes">*</span>
      </div>
      <div class="header-right">
        <input
          ref="fileInput"
          type="file"
          accept=".yaml,.yml"
          @change="handleImport"
          style="display: none"
        />
        <input
          ref="dirInput"
          type="file"
          webkitdirectory
          multiple
          @change="handleDirectoryImport"
          style="display: none"
        />

        <div class="dropdown">
          <button @click.stop="showImportMenu = !showImportMenu; showExportMenu = false" class="btn btn-outline">
            Import ▾
          </button>
          <div v-if="showImportMenu" class="dropdown-menu" @click.stop>
            <button class="dropdown-item" @click="triggerImport">Import YAML</button>
            <button class="dropdown-item" @click="triggerDirectoryImport">Import Directory</button>
          </div>
        </div>

        <div class="dropdown">
          <button @click.stop="showExportMenu = !showExportMenu; showImportMenu = false" class="btn btn-primary">
            Export ▾
          </button>
          <div v-if="showExportMenu" class="dropdown-menu" @click.stop>
            <button class="dropdown-item" @click="exportYaml">Export YAML</button>
            <button class="dropdown-item" @click="exportModular">Export Modular (ZIP)</button>
          </div>
        </div>

        <button @click="toggleSettings" class="btn btn-outline" :class="{ active: showSettings }">
          Settings
        </button>
        <div class="dropdown">
          <button @click.stop="showScaffoldMenu = !showScaffoldMenu; showImportMenu = false; showExportMenu = false" class="btn btn-outline">
            Scaffold &#x25BE;
          </button>
          <div v-if="showScaffoldMenu" class="dropdown-menu scaffold-menu" @click.stop>
            <div v-for="t in scaffoldTemplates" :key="t.id" class="scaffold-item" @click="applyScaffold(t)">
              <div class="scaffold-name">{{ t.name }}</div>
              <div class="scaffold-desc">{{ t.description }}</div>
            </div>
          </div>
        </div>
        <button @click="confirmClear" class="btn btn-outline">
          Clear
        </button>
      </div>
    </div>

    <div class="builder-toolbar">
      <div class="toolbar-section">
        <span class="toolbar-label">Add Nodes:</span>
        <button @click="addOrchestratorNode" class="btn btn-node orchestrator">
          🎯 Orchestrator
        </button>
        <button @click="addAgentNode" class="btn btn-node agent">
          🤖 Agent
        </button>
        <button @click="addToolNode" class="btn btn-node tool">
          🔧 Tool
        </button>
        <button @click="addSkillNode" class="btn btn-node skill">
          * Skill
        </button>
      </div>
      <div class="toolbar-section">
        <button @click="addGroupNode('pipeline')" class="btn btn-node group-btn pipeline">Pipeline</button>
        <button @click="addGroupNode('parallel')" class="btn btn-node group-btn parallel">Parallel</button>
        <button @click="addGroupNode('team')" class="btn btn-node group-btn team">Team</button>
        <button @click="addGroupNode('loop')" class="btn btn-node group-btn loop">Loop</button>
        <button @click="addGroupNode('generic')" class="btn btn-node group-btn generic">Group</button>
      </div>
      <div class="toolbar-section">
        <button
          @click="isMultiSelect ? duplicateSelection() : duplicateSelectedNode()"
          class="btn btn-node duplicate"
          :disabled="!selectedNode && !isMultiSelect"
        >
          Duplicate
        </button>
        <button
          v-if="isMultiSelect"
          @click="groupSelection('generic')"
          class="btn btn-node group-btn"
        >
          Group ({{ selectedNodes.length }})
        </button>
        <button
          v-if="selectedNode?.type === 'group'"
          @click="ungroupNode(selectedNode.id)"
          class="btn btn-node"
        >
          Ungroup
        </button>
      </div>
      <div class="toolbar-section">
        <span class="toolbar-hint">
          Shift+drag select • Cmd+click toggle • Cmd+F search • Cmd+Shift+H hierarchy
        </span>
      </div>
      <div class="toolbar-section">
        <button class="btn btn-node" :class="{ active: showHierarchy }" @click="showHierarchy = !showHierarchy" title="Toggle hierarchy panel (Cmd+Shift+H)">
          Hierarchy
        </button>
        <button class="btn btn-node" :class="{ active: searchOpen }" @click="toggleSearch" title="Search canvas (Cmd+F)">
          Search
        </button>
        <button class="btn btn-node" @click="autoArrange" title="Auto-arrange nodes to avoid overlaps and fit view">
          Arrange
        </button>
        <button class="btn btn-node" :disabled="!canUndo" @click="undo" title="Undo (Cmd+Z)">
          Undo
        </button>
        <button class="btn btn-node" :disabled="!canRedo" @click="redo" title="Redo (Cmd+Shift+Z)">
          Redo
        </button>
      </div>
    </div>

    <!-- Tokenizer panel -->
    <TokenizerPanel
      :self-url="selfUrl"
      :agents="tokenizerAgents"
      :query="tokenizerQuery"
      :visible="showTokenizer"
      @close="showTokenizer = false"
    />

    <!-- Node context menu -->
    <div
      v-if="contextMenu"
      class="node-context-menu"
      :style="{ top: `${contextMenu.y}px`, left: `${contextMenu.x}px` }"
      @click.stop
    >
      <div class="context-menu-title">{{ contextMenu.agentName }}</div>

      <!-- Edit actions -->
      <button class="context-menu-item" @click="contextMenuCopySettings">Copy Settings</button>
      <button
        v-if="nodeClipboard && nodeClipboard.type === contextMenu.nodeType"
        class="context-menu-item"
        @click="contextMenuPasteSettings"
      >Paste Settings</button>
      <div class="context-menu-divider" />
      <button class="context-menu-item" @click="contextMenuDuplicate">Duplicate</button>
      <button v-if="contextMenu.parentNode" class="context-menu-item" @click="contextMenuDetachFromGroup">Remove from group</button>
      <button class="context-menu-item danger" @click="contextMenuDelete">Delete</button>

      <!-- Group actions -->
      <template v-if="contextMenu.nodeType === 'group'">
        <div class="context-menu-divider" />
        <button class="context-menu-item" @click="contextMenuUngroup">Ungroup</button>
      </template>
      <template v-else-if="isMultiSelect">
        <div class="context-menu-divider" />
        <button class="context-menu-item" @click="contextMenuGroupSelection('pipeline')">Group as Pipeline</button>
        <button class="context-menu-item" @click="contextMenuGroupSelection('parallel')">Group as Parallel</button>
        <button class="context-menu-item" @click="contextMenuGroupSelection('team')">Group as Team</button>
        <button class="context-menu-item" @click="contextMenuGroupSelection('loop')">Group as Loop</button>
      </template>

      <!-- Debug breakpoints (agent/orchestrator only) -->
      <template v-if="contextMenu.nodeType === 'agent' || contextMenu.nodeType === 'orchestrator'">
        <div class="context-menu-divider" />
        <button class="context-menu-item" @click="addBreakpointForNode('pre_thought')">
          Break before thought
        </button>
        <button class="context-menu-item" @click="addBreakpointForNode('pre_tool')">
          Break before tool
        </button>
        <button class="context-menu-item" @click="addBreakpointForNode('pre_agent')">
          Break before agent
        </button>
        <button class="context-menu-item danger" @click="clearBreakpointsForNode">
          Clear breakpoints
        </button>
      </template>
    </div>

    <!-- Drop-to-group overlay menu -->
    <div
      v-if="dropMenu"
      class="node-context-menu"
      :style="{ top: `${dropMenu.y}px`, left: `${dropMenu.x}px` }"
      @click.stop
    >
      <div class="context-menu-title">Add to "{{ dropMenu.groupName }}"?</div>
      <div class="drop-menu-info">{{ dropMenu.nodeIds.length }} node(s)</div>
      <button class="context-menu-item" @click="confirmDropToGroup">Add to group</button>
      <button class="context-menu-item" @click="cancelDropToGroup">Cancel</button>
    </div>

    <div class="builder-main" @click="closeContextMenu(); dropMenu = null">
      <!-- Hierarchy panel (left sidebar) -->
      <HierarchyPanel
        :nodes="nodes"
        :selected-node-ids="selectedNodes.map(n => n.id)"
        :selection-scope="selectionScope"
        :is-open="showHierarchy"
        @select-node="handleHierarchySelect"
        @enter-scope="enterGroupScope"
        @toggle="showHierarchy = !showHierarchy"
      />

      <div class="builder-canvas" :class="{ 'has-wallpaper': wallpaperEnabled }">
        <WallpaperHost />
        <!-- Canvas search bar -->
        <div v-if="searchOpen" class="canvas-search-bar">
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Search nodes..."
            class="search-input"
            ref="searchInputRef"
            @keydown.enter="handleSearchFocus('next')"
            @keydown.escape="closeSearch"
          />
          <select v-model="searchField" class="search-field-select">
            <option value="all">All</option>
            <option value="name">Name</option>
            <option value="type">Type</option>
            <option value="provider">Provider</option>
            <option value="model">Model</option>
          </select>
          <span class="search-count" v-if="searchQuery.trim()">
            {{ matchCount > 0 ? `${currentMatchIndex + 1}/${matchCount}` : '0' }}
          </span>
          <button class="search-nav-btn" @click="handleSearchFocus('prev')" :disabled="matchCount === 0" title="Previous">&#x25B2;</button>
          <button class="search-nav-btn" @click="handleSearchFocus('next')" :disabled="matchCount === 0" title="Next">&#x25BC;</button>
          <button class="search-close-btn" @click="closeSearch">x</button>
        </div>
        <VueFlow
          v-model:nodes="nodes"
          v-model:edges="edges"
          :node-types="mergedNodeTypes as any"
          :edge-types="edgeTypes as any"
          :selection-key-code="'Shift'"
          :multi-selection-key-code="'Meta'"
          :snap-to-grid="true"
          :snap-grid="[16, 16]"
          :default-edge-options="{ type: 'smoothstep' }"
          @node-drag="handleNodeDrag"
          @node-drag-stop="handleNodeDragStop"
          fit-view-on-init
          class="vue-flow-container"
          @node-context-menu="handleNodeContextMenu"
          @pane-click="handlePaneClick"
        >
          <Background v-if="miniMapReady" variant="dots" :gap="16" :size="1" />
          <Controls />
          <MiniMap v-if="miniMapReady" />

          <!-- Scope navigation widget (canvas overlay) -->
          <div class="scope-widget">
            <!-- Current scope path -->
            <button
              class="scope-btn"
              :class="{ active: !selectionScope }"
              @click="exitToScope(null)"
              title="Canvas — select top-level nodes"
            >
              Canvas
            </button>

            <!-- Show scope path when inside a group -->
            <template v-for="crumb in scopeBreadcrumb.slice(1)" :key="crumb.id">
              <span class="scope-arrow">/</span>
              <button
                class="scope-btn scope-group-btn"
                :class="{ active: selectionScope === crumb.id }"
                @click="enterGroupScope(crumb.id!)"
              >
                {{ crumb.name }}
              </button>
            </template>

            <!-- Available groups to enter (when at root) -->
            <template v-if="!selectionScope && canvasGroupNodes.length > 0">
              <span class="scope-sep">|</span>
              <span class="scope-label">Enter:</span>
              <button
                v-for="g in canvasGroupNodes"
                :key="g.id"
                class="scope-btn scope-enter-btn"
                @click="enterGroupScope(g.id)"
                :title="`Enter ${(g.data as any).name}`"
              >
                {{ (g.data as any).name }}
              </button>
            </template>

            <!-- Up button when inside a group -->
            <button
              v-if="selectionScope"
              class="scope-btn scope-up-btn"
              @click="exitToScope(nodes.find(n => n.id === selectionScope)?.parentNode ?? null)"
              title="Go up one level (Escape)"
            >
              ↑ Up
            </button>
          </div>
        </VueFlow>
      </div>
      
      <!-- Single node editor -->
      <NodeEditor
        v-if="showEditor && selectedNode && !isMultiSelect"
        :node-id="selectedNode.id"
        :node-type="selectedNode.type as 'agent' | 'tool' | 'skill' | 'orchestrator' | 'group'"
        :node-data="selectedNode.data"
        :provider-names="Object.keys(projectSettings.providers || {})"
        :provider-map="projectSettings.providers || {}"
        :tool-names="canvasToolNames"
        :agent-names="canvasAgentNames"
        :skill-names="canvasSkillNames"
        :all-names="canvasAllNames"
        :base-urls="mergedBaseUrls"
        :api-keys="mergedApiKeys"
        :default-settings="projectSettings.defaults"
        :execution-settings="projectSettings.execution"
        :group-children="selectedNode?.type === 'group' ? nodes.filter(n => n.parentNode === selectedNode?.id).sort((a, b) => a.position.y - b.position.y || a.position.x - b.position.x).map(n => ({ id: n.id, type: n.type || '', name: (n.data as any)?.name || n.id, _agentId: (n.data as any)?._id })) : []"
        :group-step-defaults="selectedNode?.type === 'orchestrator' ? getConnectedGroupDefaults(selectedNode) : {}"
        @update="handleNodeUpdate"
        @close="closeEditor"
        @add-provider="handleAutoAddProvider"
        @wire-toggle="handleWireToggle"
        @reorder-children="handleReorderChildren"
      />

      <!-- Multi-select bulk editor -->
      <div v-else-if="showEditor && isMultiSelect" class="bulk-editor">
        <div class="bulk-header">
          <span class="bulk-count">{{ selectedNodes.length }} nodes selected</span>
          <button class="btn-close" @click="closeEditor">x</button>
        </div>
        <div v-if="selectionScope" class="selection-level">
          Scope: {{ (nodes.find(n => n.id === selectionScope)?.data as any)?.name || 'Canvas' }}
        </div>
        <div class="bulk-type-summary">
          <span v-for="[type, count] in selectionTypeCounts" :key="type" class="type-chip" :class="type">
            {{ count }} {{ type }}{{ count > 1 ? 's' : '' }}
          </span>
        </div>

        <!-- Bulk edit fields (only when all same type) -->
        <template v-if="selectionCommonType === 'agent' || selectionCommonType === 'orchestrator'">
          <div class="bulk-section">
            <div class="form-group">
              <label>Provider (all)</label>
              <ComboBox
                :model-value="bulkCommonValue('provider')"
                :options="Object.keys(projectSettings.providers || {})"
                placeholder="mixed"
                @update:model-value="applyBulkField('provider', $event)"
              />
            </div>
            <div class="form-group">
              <label>Model (all)</label>
              <ComboBox
                :model-value="bulkCommonValue('model')"
                :options="[]"
                placeholder="mixed"
                :allow-custom="true"
                @update:model-value="applyBulkField('model', $event)"
              />
            </div>
          </div>
        </template>

        <!-- Actions -->
        <div class="bulk-actions">
          <button class="btn btn-action" @click="duplicateSelection">Duplicate</button>
          <button class="btn btn-action btn-danger" @click="deleteSelection">Delete</button>
        </div>
      </div>

      <SettingsPanel
        v-if="showSettings"
        :settings="projectSettings"
        :version="projectVersion"
        :description="projectDescription"
        :interactive="projectInteractive"
        @update="handleSettingsUpdate"
        @close="showSettings = false"
        @provider-renamed="handleProviderRenamed"
        @apply-provider-to-all="handleApplyProviderToAll"
        @update:version="projectVersion = $event"
        @update:description="projectDescription = $event"
        @update:interactive="projectInteractive = $event"
      />

      <!-- Exec detail: right panel showing execution data for clicked exec nodes -->
      <div v-if="execDetailNode" class="exec-detail-panel">
        <div class="exec-detail-header">
          <span class="exec-detail-title">
            {{ execDetailNode.type.toUpperCase() }}: {{ execDetailNode.agentName || execDetailNode.label }}
          </span>
          <span v-if="execDetailNode.status" class="exec-detail-status" :class="'status-' + execDetailNode.status">
            {{ execDetailNode.status }}
          </span>
          <button class="exec-detail-close" @click="execDetailNodeId = null">&times;</button>
        </div>
        <div class="exec-detail-body">
          <DetailPanel
            :node="execDetailNode"
            :render-key="debugRenderKey"
            :is-connected="canvasMode !== 'design'"
            :is-history="!!selectedSnapshot"
            :is-paused="isPaused"
            :self-url="selfUrl"
            @close="execDetailNodeId = null"
          />
        </div>
      </div>
    </div>

    <!-- RunBar: persistent run form / execution controls -->
    <RunBar
      :canvas-mode="canvasMode"
      :self-url="selfUrl"
      :initial-workdir="runWorkdir"
      :total-token-count="totalTokenCount"
      :is-paused="isPaused"
      :paused-at="pausedCheckpoint ?? undefined"
      :show-results="showResults"
      :reset-trigger="runBarResetTrigger"
      :has-breakpoints="breakpoints.length > 0"
      :exec-view="execView"
      @run="handleRunBarRun"
      @debug="handleRunBarDebug"
      @stop="handleStop"
      @attach-debugger="handleAttachDebugger"
      @pause="debugPause()"
      @resume="debugResume('resume')"
      @step="debugResume('step')"
      @clear-results="clearResults"
      @expand-all="expandAllExec"
      @collapse-all="collapseAllExec"
      @set-exec-view="setExecView"
      @update:query="tokenizerQuery = $event"
      @open-tokenizer="showTokenizer = true"
    />

    <!-- Run history strip — shows after first completed run -->
    <div v-if="runSnapshots.length > 0" class="run-history-strip">
      <span class="run-history-label">Runs</span>
      <div class="run-history-items">
        <button
          v-for="snap in runSnapshots"
          :key="snap.id"
          class="run-history-chip"
          :class="{ active: selectedSnapshot?.id === snap.id, error: snap.summary.status === 'error' }"
          :title="`${snap.query.slice(0, 60)}\n${snap.summary.totalIterations} iters, ${snap.summary.totalToolCalls} tools, ${snap.summary.totalTokens} tokens`"
          @click="toggleRunSnapshot(snap.id)"
        >
          <span class="chip-status">{{ snap.summary.status === 'error' ? 'x' : 'ok' }}</span>
          <span class="chip-label">{{ snap.debug ? 'D' : 'R' }}{{ runSnapshots.indexOf(snap) + 1 }}</span>
          <span class="chip-meta">{{ snap.summary.totalIterations }}i {{ snap.summary.totalToolCalls }}t</span>
          <span class="chip-remove" @click.stop="handleRemoveSnapshot(snap.id)">&times;</span>
        </button>
      </div>
      <button v-if="runSnapshots.length > 1" class="run-history-clear" @click="confirmClearSnapshots" title="Clear all run history">Clear</button>
    </div>

    <!-- MonitorCard: floating node detail in monitor/replay/results modes -->
    <MonitorCard
      v-if="monitorCardNodeId && (canvasMode === 'monitor' || showResults)"
      :node="nodes.find(n => n.id === monitorCardNodeId) ?? null"
      @close="monitorCardNodeId = null"
    />

    <!-- DebugCard: opens when paused at a breakpoint -->
    <DebugCard
      v-if="debugCardNodeId && canvasMode === 'debug'"
      :node="nodes.find(n => n.id === debugCardNodeId) ?? null"
      :paused-checkpoint="pausedCheckpoint"
      :paused-context="pausedContext"
      @close="debugCardNodeId = null"
      @resume="debugResume('resume')"
      @step="debugResume('step')"
      @stop="handleStop"
    />

    <!-- Run dialog (legacy — kept for keyboard shortcut / toolbar button compat) -->
    <div v-if="showRunDialog" class="run-overlay" @click.self="closeRunDialog">
      <div class="run-dialog">
        <h3 class="run-dialog-title">Run Agent</h3>
        <div class="run-field">
          <label class="run-label">Workspace</label>
          <div class="workdir-row">
            <input
              v-model="runWorkdir"
              type="text"
              class="run-workdir"
              placeholder="/path/to/project"
            />
            <button class="btn btn-outline btn-browse" @click="browseWorkdir" title="Browse folders">...</button>
          </div>
          <div v-if="browsingDir" class="dir-browser">
            <div class="dir-browser-header">
              <span class="dir-path">{{ browsingDir }}</span>
              <button class="btn-small" @click="browseParent">..</button>
              <button class="btn-small btn-select" @click="runWorkdir = browsingDir; browsingDir = ''">Select</button>
              <button class="btn-small" @click="browsingDir = ''">Cancel</button>
            </div>
            <div class="dir-list">
              <div v-for="d in browseDirs" :key="d" class="dir-item" @click="browseInto(d)">
                {{ d }}/
              </div>
              <div v-if="browseDirs.length === 0" class="dir-empty">No subdirectories</div>
            </div>
          </div>
          <div v-if="!runWorkdir.trim() && !browsingDir" class="run-workdir-warning">
            No workspace set — tools will run in the server's current directory
          </div>
        </div>
        <div class="run-field">
          <label class="run-label">Prompt</label>
          <textarea
            v-model="runQuery"
            class="run-prompt"
            placeholder="Enter your prompt..."
            rows="3"
            @keydown.meta.enter="handleRunSubmit(false)"
            @keydown.ctrl.enter="handleRunSubmit(false)"
          ></textarea>
        </div>
        <div class="run-field run-field-inline">
          <label class="run-label">Timeout</label>
          <input v-model.number="runTimeout" type="number" class="run-timeout" min="10" max="3600" />
          <span class="run-unit">s</span>
        </div>
        <div class="run-field">
          <label class="run-label">Environment Variables</label>
          <div v-for="(env, i) in runEnvVars" :key="i" class="env-row">
            <input v-model="env.key" type="text" class="env-key" placeholder="VAR_NAME" />
            <input v-model="env.value" type="password" class="env-val" placeholder="value" />
            <button class="env-del" @click="runEnvVars.splice(i, 1)">x</button>
          </div>
          <button class="btn-env-add" @click="runEnvVars.push({ key: '', value: '' })">+ Add Variable</button>
        </div>
        <div class="run-actions">
          <button class="btn btn-outline" @click="closeRunDialog">Cancel</button>
          <button class="btn btn-run" :disabled="!runQuery.trim()" @click="handleRunSubmit(false)">Run</button>
          <button class="btn btn-debug" :disabled="!runQuery.trim()" @click="handleRunSubmit(true)" title="Run with debugger attached — breakpoints, pause/resume, param override">Debug</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.visual-builder {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--surface-0);
}

.builder-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 20px;
  background: var(--surface-2);
  border-bottom: 1px solid var(--border-subtle);
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.header-left h2 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}

.dirty-badge {
  color: var(--status-paused);
  font-size: 18px;
  font-weight: 700;
  line-height: 1;
}

.project-name-input {
  padding: 6px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 13px;
  width: 200px;
  background: var(--surface-1);
  color: var(--text-primary);
}

.project-name-input:focus {
  outline: none;
  border-color: var(--border-focus);
}

.header-right {
  display: flex;
  gap: 8px;
}

.btn {
  padding: 6px 14px;
  border-radius: var(--node-radius);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  border: 1px solid var(--border-default);
  background: var(--surface-3);
  color: var(--text-primary);
  transition: all 0.15s;
}

.btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--accent-agent);
  color: #fff;
  border-color: var(--accent-agent);
}

.btn-primary:hover:not(:disabled) {
  background: #4a7ad8;
}

.btn-outline {
  background: transparent;
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
}

.btn-outline:hover:not(:disabled) {
  background: var(--surface-4);
  color: var(--text-primary);
}

.btn-outline.active {
  background: var(--accent-agent);
  color: #fff;
  border-color: var(--accent-agent);
}

.builder-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 20px;
  background: var(--surface-2);
  border-bottom: 1px solid var(--border-subtle);
  flex-wrap: wrap;
  gap: 10px;
}

.toolbar-section {
  display: flex;
  align-items: center;
  gap: 10px;
}

.toolbar-label {
  font-size: 10px;
  color: var(--text-muted);
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-family: var(--font-mono);
}

.btn-node {
  padding: 5px 10px;
  font-size: 11px;
  font-family: var(--font-mono);
  border-radius: var(--node-radius);
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.btn-node:hover:not(:disabled) {
  background: var(--surface-4);
}

.btn-node.orchestrator {
  border-color: var(--accent-orch);
  color: var(--accent-orch);
}

.btn-node.agent {
  border-color: var(--accent-agent);
  color: var(--accent-agent);
}

.btn-node.tool {
  border-color: var(--accent-tool);
  color: var(--accent-tool);
}

.btn-node.skill {
  border-color: var(--accent-skill);
  color: var(--accent-skill);
}

.btn-node.duplicate {
  border-color: var(--status-paused);
  color: var(--status-paused);
}

.toolbar-hint {
  font-size: 10px;
  color: var(--text-muted);
  font-family: var(--font-mono);
}

.builder-main {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.builder-canvas {
  flex: 1;
  position: relative;
}

/* --- Exec Detail Panel (right sidebar) --- */
.exec-detail-panel {
  width: 360px;
  min-width: 280px;
  max-width: 420px;
  background: var(--surface-2);
  border-left: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.exec-detail-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--surface-3);
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}

.exec-detail-title {
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: 0.03em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.exec-detail-status {
  font-family: var(--font-mono);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: 2px;
  flex-shrink: 0;
}
.exec-detail-status.status-running { color: var(--status-running); background: rgba(34, 197, 94, 0.15); }
.exec-detail-status.status-paused { color: var(--status-paused); background: rgba(245, 158, 11, 0.15); }
.exec-detail-status.status-success { color: var(--status-running); background: rgba(34, 197, 94, 0.1); }
.exec-detail-status.status-error { color: var(--status-error); background: rgba(239, 68, 68, 0.15); }

.exec-detail-close {
  margin-left: auto;
  background: none;
  border: none;
  color: var(--text-secondary);
  font-size: 16px;
  cursor: pointer;
  padding: 0 4px;
  line-height: 1;
}
.exec-detail-close:hover { color: var(--text-primary); }

.exec-detail-body {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

.exec-detail-body :deep(.detail-panel) {
  background: transparent;
  border: none;
  color: var(--text-primary);
}

.exec-detail-body :deep(.detail-section) {
  background: var(--surface-3);
  border: 1px solid var(--node-border);
  border-radius: var(--node-radius);
  margin-bottom: 8px;
}

.exec-detail-body :deep(.section-title) {
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.exec-detail-body :deep(.section-value),
.exec-detail-body :deep(.detail-value) {
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 12px;
}

.exec-detail-body :deep(pre),
.exec-detail-body :deep(code) {
  background: var(--surface-1);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: 2px;
  font-family: var(--font-mono);
  font-size: 12px;
}

.exec-detail-body :deep(button) {
  background: var(--surface-3);
  border: 1px solid var(--node-border);
  color: var(--text-primary);
  border-radius: var(--node-radius);
  cursor: pointer;
}
.exec-detail-body :deep(button:hover) { background: var(--surface-4); }

.exec-detail-body :deep(input),
.exec-detail-body :deep(select),
.exec-detail-body :deep(textarea) {
  background: var(--surface-1);
  border: 1px solid var(--node-border);
  color: var(--text-primary);
  border-radius: var(--node-radius);
}

.exec-detail-body :deep(.token-bar) { background: var(--surface-1); }
.exec-detail-body :deep(label) {
  color: var(--text-muted);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.vue-flow-container {
  width: 100%;
  height: 100%;
  background: var(--canvas-bg);
}

.builder-canvas.has-wallpaper .vue-flow-container {
  background: transparent;
}
.builder-canvas.has-wallpaper :deep(.vue-flow__background) {
  background: transparent;
}
.builder-canvas.has-wallpaper :deep(.vue-flow__pane) {
  background: transparent;
}

.dropdown {
  position: relative;
  display: inline-block;
}

.dropdown-menu {
  position: absolute;
  top: 100%;
  left: 0;
  z-index: 100;
  margin-top: 4px;
  min-width: 180px;
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
  overflow: hidden;
}

.dropdown-item {
  display: block;
  width: 100%;
  padding: 8px 14px;
  border: none;
  background: transparent;
  text-align: left;
  font-size: 12px;
  color: var(--text-primary);
  cursor: pointer;
}

.dropdown-item:hover {
  background: var(--surface-4);
}

.dropdown-item + .dropdown-item {
  border-top: 1px solid var(--border-subtle);
}

.scaffold-menu {
  width: 260px;
}
.scaffold-item {
  padding: 8px 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--border-subtle);
}
.scaffold-item:hover {
  background: var(--surface-4);
}
.scaffold-item:last-child {
  border-bottom: none;
}
.scaffold-name {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-primary);
}
.scaffold-desc {
  font-size: 11px;
  color: var(--text-secondary);
  margin-top: 2px;
}

.btn-run {
  background: var(--status-running);
  color: #000;
  border-color: var(--status-running);
}
.btn-run:hover:not(:disabled) {
  background: #1aad4a;
}
.btn-run:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.btn-debug {
  background: var(--status-paused);
  color: #000;
  border-color: var(--status-paused);
}
.btn-debug:hover:not(:disabled) {
  background: #d98f0a;
}
.btn-debug:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.run-overlay {
  position: absolute; /* was fixed — escaped tab container causing ghost overlay */
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 200;
}
.run-dialog {
  background: var(--surface-2);
  border-radius: 6px;
  padding: 24px;
  width: 420px;
  max-width: 90vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
  border: 1px solid var(--border-default);
}
.run-dialog-title {
  margin: 0 0 16px;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}
.run-field { margin-bottom: 12px; }
.run-field-inline {
  display: flex;
  align-items: center;
  gap: 6px;
}
.run-label {
  display: block;
  font-size: 10px;
  font-weight: 500;
  color: var(--text-secondary);
  margin-bottom: 4px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.run-field-inline .run-label { margin-bottom: 0; }
.run-workdir {
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 12px;
  font-family: var(--font-mono);
  outline: none;
  box-sizing: border-box;
  background: var(--surface-1);
  color: var(--text-primary);
}
.run-workdir:focus { border-color: var(--border-focus); }
.workdir-row { display: flex; gap: 4px; }
.workdir-row .run-workdir { flex: 1; }
.btn-browse { padding: 4px 10px; font-size: 14px; font-weight: 700; }
.dir-browser {
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  margin-top: 4px;
  max-height: 200px;
  overflow-y: auto;
  background: var(--surface-1);
}
.dir-browser-header {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  background: var(--surface-3);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 11px;
  font-family: var(--font-mono);
}
.dir-path { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); }
.btn-small { font-size: 10px; padding: 1px 6px; border: 1px solid var(--border-default); border-radius: var(--node-radius); background: var(--surface-3); cursor: pointer; color: var(--text-secondary); }
.btn-small:hover { background: var(--surface-4); }
.btn-select { color: var(--accent-agent); border-color: var(--accent-agent); }
.dir-list { padding: 2px 0; }
.dir-item {
  padding: 3px 12px;
  font-size: 12px;
  font-family: var(--font-mono);
  cursor: pointer;
  color: var(--text-secondary);
}
.dir-item:hover { background: var(--surface-4); color: var(--text-primary); }
.dir-empty { padding: 8px 12px; font-size: 11px; color: var(--text-muted); font-style: italic; }
.run-workdir-warning {
  font-size: 11px;
  color: var(--status-paused);
  margin-top: 4px;
}
.run-prompt {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 13px;
  font-family: inherit;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background: var(--surface-1);
  color: var(--text-primary);
}
.run-prompt:focus { border-color: var(--border-focus); }
.run-timeout {
  width: 70px;
  padding: 4px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 12px;
  outline: none;
  background: var(--surface-1);
  color: var(--text-primary);
}
.run-timeout:focus { border-color: var(--border-focus); }
.run-unit { font-size: 12px; color: var(--text-secondary); }
.env-row {
  display: flex;
  gap: 4px;
  margin-bottom: 4px;
}
.env-key {
  flex: 0 0 40%;
  padding: 5px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 12px;
  font-family: var(--font-mono);
  background: var(--surface-1);
  color: var(--text-primary);
}
.env-val {
  flex: 1;
  padding: 5px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 12px;
  background: var(--surface-1);
  color: var(--text-primary);
}
.env-del {
  padding: 2px 6px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  background: var(--surface-3);
  font-size: 11px;
  cursor: pointer;
  color: var(--text-muted);
}
.env-del:hover { border-color: var(--status-error); color: var(--status-error); }
.btn-env-add {
  width: 100%;
  padding: 4px;
  background: none;
  border: 1px dashed var(--border-default);
  border-radius: var(--node-radius);
  color: var(--accent-agent);
  cursor: pointer;
  font-size: 11px;
}
.btn-env-add:hover { border-color: var(--accent-agent); background: rgba(91, 141, 239, 0.1); }

.run-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}

/* --- Node context menu --- */
.node-context-menu {
  position: fixed;
  z-index: 500;
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.5);
  min-width: 200px;
  overflow: hidden;
}

.context-menu-title {
  padding: 8px 14px 6px;
  font-size: 10px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-family: var(--font-mono);
  border-bottom: 1px solid var(--border-subtle);
}

.context-menu-item {
  display: block;
  width: 100%;
  padding: 8px 14px;
  border: none;
  background: transparent;
  text-align: left;
  font-size: 12px;
  color: var(--text-primary);
  cursor: pointer;
}

.context-menu-item:hover {
  background: var(--surface-4);
}

.context-menu-item.danger {
  color: var(--status-error);
}

.context-menu-item.danger:hover {
  background: rgba(239, 68, 68, 0.1);
}

.context-menu-divider {
  height: 1px;
  background: var(--border-subtle);
  margin: 2px 0;
}

/* Scope navigation widget (canvas overlay) */
/* Canvas search bar */
.canvas-search-bar {
  position: absolute;
  top: 10px;
  right: 16px;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 4px;
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  padding: 4px 8px;
  box-shadow: 0 2px 8px rgba(0,0,0,0.3);
}
.canvas-search-bar .search-input {
  border: none;
  outline: none;
  font-size: 12px;
  font-family: var(--font-mono);
  width: 160px;
  padding: 4px;
  background: transparent;
  color: var(--text-primary);
}
.search-field-select {
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 10px;
  padding: 2px 4px;
  background: var(--surface-1);
  color: var(--text-secondary);
}
.search-count {
  font-size: 11px;
  color: var(--text-secondary);
  font-family: var(--font-mono);
  min-width: 30px;
  text-align: center;
}
.search-nav-btn {
  background: none;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  cursor: pointer;
  font-size: 10px;
  padding: 2px 6px;
  color: var(--text-secondary);
}
.search-nav-btn:hover:not(:disabled) { background: var(--surface-4); }
.search-nav-btn:disabled { opacity: 0.3; }
.search-close-btn {
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  color: var(--text-muted);
  padding: 0 4px;
}
.search-close-btn:hover { color: var(--text-primary); }

/* Shift held: nodes become click-through so box-select works everywhere */
:deep(.vue-flow__node.shift-passthrough) { pointer-events: none !important; }

/* Node search highlight/dim (applied via Vue Flow node class) */
:deep(.vue-flow__node.search-dim) { opacity: 0.15 !important; }
:deep(.vue-flow__node.search-highlight) { box-shadow: 0 0 0 2px var(--status-paused) !important; }
:deep(.vue-flow__edge.search-dim) { opacity: 0.1 !important; }

/* Edge styling — muted orthogonal wires */
:deep(.vue-flow__edge-path) { stroke: #4a4a60; stroke-width: 1.5; }
:deep(.vue-flow__edge.animated .vue-flow__edge-path) { stroke: var(--status-running); }
:deep(.vue-flow__connection-line) { stroke: var(--accent-agent); stroke-width: 1.5; }

/* Execution mode: dim design edges */
:deep(.vue-flow__edge.design-dim .vue-flow__edge-path) { opacity: 0.12; }
:deep(.vue-flow__edge.exec-active .vue-flow__edge-path) { stroke: var(--status-running); stroke-width: 2.5; opacity: 1; }

/* Exec tree edges are rendered by the custom RakitsuExecEdge component
   which applies inline styles (dual-path glow + main stroke). Keep only
   the anchor-edge override here (that's a separate overlay wire). */
:deep(.vue-flow__edge.exec-anchor-edge .vue-flow__edge-path) {
  stroke: var(--accent-agent);
  stroke-width: 2;
  opacity: 0.85;
  filter: drop-shadow(0 0 4px rgba(91, 141, 239, 0.5));
}

/* Exec tree nodes — smaller, no interaction chrome */
:deep(.vue-flow__node.exec-tree-node) { z-index: 5 !important; }

/* --- Run history strip --- */
.run-history-strip {
  position: absolute;
  bottom: 56px;
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 16px;
  z-index: 20;
  max-width: 80vw;
  overflow-x: auto;
}
.run-history-label {
  font-size: 9px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  flex-shrink: 0;
}
.run-history-items {
  display: flex;
  gap: 4px;
}
.run-history-chip {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 10px;
  border: 1px solid var(--border-subtle);
  background: var(--surface-3);
  color: var(--text-secondary);
  font-size: 10px;
  font-family: var(--font-mono);
  cursor: pointer;
  transition: background 0.15s, border-color 0.15s;
  white-space: nowrap;
}
.run-history-chip:hover { background: var(--surface-4); }
.run-history-chip.active {
  border-color: var(--accent-agent);
  background: rgba(91, 141, 239, 0.12);
  color: var(--accent-agent);
}
.run-history-chip.error .chip-status { color: var(--status-error); }
.chip-status {
  font-weight: 700;
  font-size: 9px;
  color: var(--status-success);
}
.chip-label { font-weight: 600; }
.chip-meta { color: var(--text-muted); font-size: 9px; }
.chip-remove {
  font-size: 12px;
  color: var(--text-muted);
  cursor: pointer;
  margin-left: 2px;
  line-height: 1;
}
.chip-remove:hover { color: var(--status-error); }
.run-history-clear {
  font-size: 9px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  background: none;
  border: none;
  cursor: pointer;
  flex-shrink: 0;
}
.run-history-clear:hover { color: var(--status-error); }

/* MiniMap + Controls — dark theme */
:deep(.vue-flow__minimap) { background: var(--surface-0); border: 1px solid var(--border-subtle); border-radius: var(--node-radius); }
:deep(.vue-flow__controls) { background: var(--surface-3); border: 1px solid var(--border-default); border-radius: var(--node-radius); box-shadow: none; }
:deep(.vue-flow__controls-button) { background: var(--surface-3); border-bottom: 1px solid var(--border-subtle); color: var(--text-secondary); fill: var(--text-secondary); }
:deep(.vue-flow__controls-button:hover) { background: var(--surface-4); }
:deep(.vue-flow__background) { background: var(--canvas-bg); }

.scope-widget {
  position: absolute;
  top: 10px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 2px;
  background: var(--surface-3);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  padding: 3px 6px;
  box-shadow: 0 2px 8px rgba(0,0,0,0.3);
  font-size: 11px;
}
.scope-btn {
  background: none;
  border: 1px solid transparent;
  border-radius: var(--node-radius);
  padding: 3px 8px;
  cursor: pointer;
  color: var(--text-secondary);
  font-size: 11px;
  font-weight: 500;
  transition: all 0.15s;
  white-space: nowrap;
}
.scope-btn:hover { background: var(--surface-4); color: var(--text-primary); }
.scope-btn.active {
  background: var(--accent-agent);
  color: #fff;
  border-color: var(--accent-agent);
}
.scope-icon { font-size: 13px; }
.scope-arrow { color: var(--text-muted); font-size: 11px; margin: 0 1px; }
.scope-sep { color: var(--border-default); margin: 0 4px; }
.scope-label { color: var(--text-muted); font-size: 10px; }
.scope-up-btn {
  margin-left: 4px;
  border-left: 1px solid var(--border-default);
  padding-left: 8px;
  color: var(--accent-agent);
}
.scope-group-btn { max-width: 120px; overflow: hidden; text-overflow: ellipsis; }
.scope-enter-btn {
  color: var(--accent-agent);
  font-size: 10px;
  padding: 2px 6px;
  border: 1px dashed var(--accent-agent);
  border-radius: var(--node-radius);
}
.scope-enter-btn:hover { background: rgba(91, 141, 239, 0.1); }

.selection-level {
  font-size: 11px;
  color: var(--accent-agent);
  background: rgba(91, 141, 239, 0.1);
  padding: 3px 8px;
  border-radius: var(--node-radius);
  margin-bottom: 8px;
}

.drop-menu-info {
  font-size: 11px;
  color: #888;
  padding: 2px 12px 4px;
}

/* Group block buttons */
.group-btn.pipeline { border-color: #667eea; color: #667eea; }
.group-btn.parallel { border-color: #11998e; color: #11998e; }
.group-btn.team { border-color: #f093fb; color: #f093fb; }
.group-btn.loop { border-color: #f59e0b; color: #f59e0b; }
.group-btn.generic { border-color: #888; color: #888; }

/* Bulk editor */
.bulk-editor {
  position: absolute;
  top: 0;
  right: 0;
  width: 320px;
  height: 100%;
  background: white;
  border-left: 1px solid #e0e0e0;
  box-shadow: -4px 0 12px rgba(0,0,0,0.06);
  display: flex;
  flex-direction: column;
  z-index: 20;
  padding: 16px;
  overflow-y: auto;
}
.bulk-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.bulk-count {
  font-size: 15px;
  font-weight: 600;
  color: #333;
}
.bulk-type-summary {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.type-chip {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 600;
}
.type-chip.agent { background: rgba(102, 126, 234, 0.15); color: #667eea; }
.type-chip.tool { background: rgba(17, 153, 142, 0.15); color: #11998e; }
.type-chip.skill { background: rgba(240, 147, 251, 0.15); color: #f093fb; }
.type-chip.orchestrator { background: rgba(255, 152, 0, 0.15); color: #ff9800; }
.bulk-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 16px;
}
.bulk-actions {
  display: flex;
  gap: 8px;
  margin-top: auto;
  padding-top: 12px;
  border-top: 1px solid #e0e0e0;
}
.btn-danger { color: #f44336 !important; border-color: #f44336 !important; }
.btn-danger:hover { background: rgba(244, 67, 54, 0.1) !important; }

</style>