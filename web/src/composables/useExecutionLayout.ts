import type { Node, Edge } from '@vue-flow/core';

// Minimum fallback dimensions per type
const MIN_DIMS: Record<string, { w: number; h: number }> = {
  agent:            { w: 200, h: 90 },
  orchestrator:     { w: 200, h: 80 },
  tool:             { w: 170, h: 60 },
  skill:            { w: 170, h: 60 },
  group:            { w: 240, h: 100 },
  'exec-iteration': { w: 130, h: 34 },
  'exec-thought':   { w: 220, h: 64 },
  'debug-tool':     { w: 180, h: 52 },
  'debug-generic':  { w: 180, h: 44 },
  default:          { w: 180, h: 70 },
};

/** Get actual rendered dimensions from a VueFlow node, or fall back to minimums */
function getNodeDims(node: Node): { w: number; h: number } {
  const dim = (node as any).dimensions;
  const min = MIN_DIMS[node.type ?? 'default'] ?? MIN_DIMS.default!;
  return {
    w: Math.max(dim?.width ?? 0, min.w),
    h: Math.max(dim?.height ?? 0, min.h),
  };
}

export type ExecutionPositions = Map<string, { x: number; y: number }>;

export interface EdgeRoute {
  startPoint: { x: number; y: number };
  bendPoints: Array<{ x: number; y: number }>;
  endPoint: { x: number; y: number };
}

export type EdgeRoutes = Map<string, EdgeRoute>;

export interface LayoutResult {
  positions: ExecutionPositions;
  edgeRoutes: EdgeRoutes;
}

// Lazy-load ELK (~1.5MB) — only loaded when graph layout is first used
let elkInstance: any = null;
async function getElk() {
  if (!elkInstance) {
    const ELK = (await import('elkjs/lib/elk.bundled.js')).default;
    elkInstance = new ELK();
  }
  return elkInstance;
}

export function useExecutionLayout() {
  /**
   * Compute a structured ELK layout for the given VueFlow nodes/edges.
   * Returns node positions (top-left) and edge routing with bend points.
   *
   * Uses actual rendered node dimensions when available (from VueFlow's
   * node.dimensions) so the layout adapts to content-driven node sizes.
   */
  async function computeLayout(nodes: Node[], edges: Edge[], direction: 'DOWN' | 'RIGHT' = 'DOWN'): Promise<LayoutResult> {
    const emptyResult: LayoutResult = { positions: new Map(), edgeRoutes: new Map() };
    const topLevel = nodes.filter(n => !n.parentNode);
    const topLevelIds = new Set(topLevel.map(n => n.id));

    if (topLevel.length === 0) return emptyResult;

    const elkChildren = topLevel.map(node => {
      const { w, h } = getNodeDims(node);
      return { id: node.id, width: w, height: h };
    });

    const elkEdges = edges
      .filter(e => topLevelIds.has(e.source) && topLevelIds.has(e.target))
      .map(e => ({
        id: e.id,
        sources: [e.source],
        targets: [e.target],
      }));

    const graph = {
      id: 'root',
      layoutOptions: {
        'elk.algorithm': 'layered',
        'elk.direction': direction,
        'elk.spacing.nodeNode': '60',
        'elk.layered.spacing.nodeNodeBetweenLayers': '100',
        'elk.padding': '[top=60,left=60,bottom=60,right=60]',
        'elk.layered.crossingMinimization.strategy': 'LAYER_SWEEP',
        'elk.edgeRouting': 'ORTHOGONAL',
      },
      children: elkChildren,
      edges: elkEdges,
    };

    try {
      const elk = await getElk();
      const result = await elk.layout(graph);

      const positions: ExecutionPositions = new Map();
      for (const child of result.children ?? []) {
        positions.set(child.id, { x: child.x ?? 0, y: child.y ?? 0 });
      }

      const edgeRoutes: EdgeRoutes = new Map();
      for (const edge of result.edges ?? []) {
        const section = (edge as any).sections?.[0];
        if (section) {
          edgeRoutes.set(edge.id, {
            startPoint: section.startPoint,
            bendPoints: section.bendPoints ?? [],
            endPoint: section.endPoint,
          });
        }
      }

      return { positions, edgeRoutes };
    } catch {
      return emptyResult;
    }
  }

  return { computeLayout };
}
