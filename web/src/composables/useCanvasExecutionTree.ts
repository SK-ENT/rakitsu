import { ref, watch, nextTick, type Ref } from 'vue';
import type { Node, Edge } from '@vue-flow/core';
import type { DebugTreeNode } from '../types';

// Re-export shared layout for consumers that only need the pure functions
export type { TreeSpacing, LayoutResult } from './useExecutionTreeLayout';

import {
  DEFAULT_TREE_GAP,
  DEFAULT_SPACING,
  dimCache,
  getDesignBounds,
  layoutCallTree,
} from './useExecutionTreeLayout';

export function useCanvasExecutionTree(
  treeRoots: Ref<DebugTreeNode[]>,
  designNodes: Ref<Node[]>,
  canvasMode: Ref<string>,
  renderKey: Ref<number>,
  findNodeById?: (id: string) => Node | undefined,
) {
  const execNodes = ref<Node[]>([]) as Ref<Node[]>;
  const execEdges = ref<Edge[]>([]) as Ref<Edge[]>;
  const expandedAgents = ref<Set<string>>(new Set());

  // When true, rebuild() won't clear exec nodes (snapshot/results mode)
  const frozen = ref(false);

  // Cached snapshot roots for re-layout when toggling expand in frozen mode
  let frozenTreeRoots: DebugTreeNode[] | null = null;

  function toggleExpand(agentName: string) {
    const s = new Set(expandedAgents.value);
    if (s.has(agentName)) s.delete(agentName);
    else s.add(agentName);
    expandedAgents.value = s;
    // Re-layout frozen tree if in results/snapshot mode
    if (frozen.value) rebuildFrozen();
  }

  /** Expand all agents in the current tree */
  function expandAll() {
    const roots = frozen.value && frozenTreeRoots ? frozenTreeRoots : treeRoots.value;
    const names = new Set<string>();
    function collect(node: DebugTreeNode) {
      if (node.type === 'agent') names.add(node.agentName);
      for (const c of node.children) collect(c);
    }
    for (const r of roots) collect(r);
    expandedAgents.value = names;
    if (frozen.value) rebuildFrozen();
  }

  /** Collapse all agents */
  function collapseAll() {
    expandedAgents.value = new Set();
    if (frozen.value) rebuildFrozen();
  }

  function clearExecTree() {
    frozen.value = false;
    execNodes.value = [];
    execEdges.value = [];
    expandedAgents.value = new Set();
  }

  function findRendered(nodeId: string): { w: number; h: number } | null {
    if (!findNodeById) return null;
    const n = findNodeById(nodeId);
    const dim = (n as any)?.dimensions;
    if (dim && dim.width > 0 && dim.height > 0) {
      return { w: dim.width, h: dim.height };
    }
    return null;
  }

  function rebuild() {
    // Don't touch exec nodes when frozen (results/snapshot mode)
    if (frozen.value) return;

    if (canvasMode.value === 'design') {
      if (execNodes.value.length > 0) clearExecTree();
      return;
    }

    // Auto-expand running agents
    for (const root of treeRoots.value) {
      autoExpandRunning(root);
    }

    const bounds = getDesignBounds(designNodes.value);
    const originX = bounds.maxRight + DEFAULT_TREE_GAP;
    const originY = bounds.minTop;

    const result = layoutCallTree(
      treeRoots.value, expandedAgents.value,
      originX, originY, DEFAULT_SPACING, findRendered,
    );

    execNodes.value = result.nodes;
    execEdges.value = result.edges;
  }

  /** Auto-expand agents that are currently running or paused */
  function autoExpandRunning(node: DebugTreeNode) {
    if (node.type === 'agent') {
      if (node.status === 'running' || node.status === 'paused') {
        if (!expandedAgents.value.has(node.agentName)) {
          const s = new Set(expandedAgents.value);
          s.add(node.agentName);
          expandedAgents.value = s;
        }
      }
    }
    for (const c of node.children) autoExpandRunning(c);
  }

  /** Count total nodes in tree (cheap, used for change detection) */
  function countTreeNodes(roots: DebugTreeNode[]): number {
    let count = 0;
    function walk(n: DebugTreeNode) { count++; for (const c of n.children) walk(c); }
    for (const r of roots) walk(r);
    return count;
  }

  // Watch for debug tree changes — renderKey is the primary trigger
  watch([renderKey, () => treeRoots.value.length, () => countTreeNodes(treeRoots.value), expandedAgents], () => {
    rebuild();
  });

  // Watch canvas mode changes
  watch(canvasMode, () => {
    rebuild();
  });

  // After exec nodes are rendered, re-layout with actual dimensions
  let relayoutPending = false;
  watch(execNodes, () => {
    if (relayoutPending || execNodes.value.length === 0) return;
    relayoutPending = true;
    nextTick(() => {
      setTimeout(() => {
        relayoutPending = false;
        let changed = false;
        for (const n of execNodes.value) {
          const live = findRendered(n.id);
          if (live) {
            const prev = dimCache.get(n.id);
            if (!prev || Math.abs(prev.h - live.h) > 4 || Math.abs(prev.w - live.w) > 4) {
              dimCache.set(n.id, live);
              changed = true;
            }
          }
        }
        if (changed && !frozen.value) {
          rebuild();
        }
      }, 100);
    });
  });

  /**
   * Freeze exec tree — prevents rebuild() from clearing nodes.
   * Used when entering results mode to keep the tree visible.
   */
  function freeze() {
    // Cache current tree roots for re-layout on expand/collapse
    frozenTreeRoots = treeRoots.value.length > 0 ? treeRoots.value : null;
    frozen.value = true;
  }

  /** Re-layout frozen tree (called when expand/collapse changes in results mode) */
  function rebuildFrozen() {
    const roots = frozenTreeRoots ?? treeRoots.value;
    if (roots.length === 0) return;

    const bounds = getDesignBounds(designNodes.value);
    const originX = bounds.maxRight + DEFAULT_TREE_GAP;
    const originY = bounds.minTop;

    const result = layoutCallTree(
      roots, expandedAgents.value,
      originX, originY, DEFAULT_SPACING, findRendered,
    );

    for (const n of result.nodes) {
      n.class = `${n.class ?? ''} exec-frozen`.trim();
    }

    execNodes.value = result.nodes;
    execEdges.value = result.edges;
  }

  /**
   * Load exec tree from a saved snapshot's treeRoots.
   * Expands all agents and freezes the tree.
   */
  function loadFromSnapshot(snapshotTreeRoots: DebugTreeNode[]) {
    // Cache for re-layout on expand/collapse
    frozenTreeRoots = snapshotTreeRoots;

    // Expand all agents in the snapshot
    const names = new Set<string>();
    function collectAgents(node: DebugTreeNode) {
      if (node.type === 'agent') names.add(node.agentName);
      for (const c of node.children) collectAgents(c);
    }
    for (const r of snapshotTreeRoots) collectAgents(r);
    expandedAgents.value = names;

    const bounds = getDesignBounds(designNodes.value);
    const originX = bounds.maxRight + DEFAULT_TREE_GAP;
    const originY = bounds.minTop;

    const result = layoutCallTree(
      snapshotTreeRoots, names,
      originX, originY, DEFAULT_SPACING, findRendered,
    );

    // Mark all as frozen (snapshot, not live)
    for (const n of result.nodes) {
      n.class = `${n.class ?? ''} exec-frozen`.trim();
    }

    execNodes.value = result.nodes;
    execEdges.value = result.edges;
    frozen.value = true;
  }

  /** Force re-layout the tree (e.g. when switching from graph view back to tree view) */
  function relayoutTree() {
    if (frozen.value) {
      rebuildFrozen();
    } else {
      // Temporarily unfreeze to allow rebuild
      rebuild();
    }
  }

  return {
    execNodes,
    execEdges,
    expandedAgents,
    toggleExpand,
    expandAll,
    collapseAll,
    clearExecTree,
    freeze,
    loadFromSnapshot,
    relayoutTree,
  };
}
