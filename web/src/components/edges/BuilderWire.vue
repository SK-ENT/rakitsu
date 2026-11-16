<script setup lang="ts">
import { computed, inject, type ComputedRef } from 'vue';
import { BaseEdge, Position, useVueFlow, type EdgeProps } from '@vue-flow/core';

const props = defineProps<EdgeProps>();
const { getNodes, getEdges } = useVueFlow();
const workflowFocus = inject<ComputedRef<{ agent: string; tool: string } | null>>('builderWorkflowFocus');
const executing = computed(() => {
  const focus = workflowFocus?.value;
  return Boolean(focus?.tool && props.sourceNode.data?.name === focus.agent && props.targetNode.data?.name === focus.tool);
});
type Point = { x: number; y: number };
const directions: Record<Position, Point> = {
  [Position.Left]: { x: -1, y: 0 }, [Position.Right]: { x: 1, y: 0 },
  [Position.Top]: { x: 0, y: -1 }, [Position.Bottom]: { x: 0, y: 1 },
};
const kind = computed(() => {
  if (props.targetNode.type === 'tool' || props.targetNode.type === 'skill') return 'tool';
  if (props.targetNode.data?.blockType === 'loop') return 'loop';
  return 'flow';
});
const route = computed(() => {
  const start = { x: props.sourceX, y: props.sourceY };
  const end = { x: props.targetX, y: props.targetY };
  const from = directions[props.sourcePosition];
  const to = directions[props.targetPosition];
  const siblings = getEdges.value.filter(edge =>
    (edge.source === props.source && (edge.sourceHandle ?? '') === (props.sourceHandleId ?? ''))
    || (edge.target === props.target && (edge.targetHandle ?? '') === (props.targetHandleId ?? '')));
  const lane = Math.max(0, siblings.findIndex(edge => edge.id === props.id));
  const a = { x: start.x + from.x * (14 + lane * 7), y: start.y + from.y * (14 + lane * 7) };
  const b = { x: end.x + to.x * 14, y: end.y + to.y * 14 };
  const cards = getNodes.value
    .filter(node => !node.hidden && node.type !== 'group' && !node.id.startsWith('exec_'))
    .map(node => {
      const { x, y } = node.computedPosition;
      return { left: x, right: x + node.dimensions.width, top: y, bottom: y + node.dimensions.height };
    });
  const offset = ((lane % 2 ? 1 : -1) * Math.ceil(lane / 2)) * 12;
  const columns = [(a.x + b.x) / 2 + offset, a.x, b.x, ...cards.flatMap(card => [card.left - 13, card.right + 13])];
  const rows = [(a.y + b.y) / 2 + offset, a.y, b.y, ...cards.flatMap(card => [card.top - 13, card.bottom + 13])];
  const candidates = [
    ...columns.map(x => [start, a, { x, y: a.y }, { x, y: b.y }, b, end]),
    ...rows.map(y => [start, a, { x: a.x, y }, { x: b.x, y }, b, end]),
  ];
  function score(points: Point[]) {
    let cost = 0;
    for (let i = 1; i < points.length; i++) {
      const p = points[i - 1]!, q = points[i]!;
      if (p.x === q.x && p.y === q.y) continue;
      cost += Math.abs(p.x - q.x) + Math.abs(p.y - q.y) + 10;
      for (const card of cards) {
        const crosses = p.y === q.y
          ? p.y > card.top && p.y < card.bottom
            && Math.max(p.x, q.x) > card.left + 2 && Math.min(p.x, q.x) < card.right - 2
          : p.x > card.left && p.x < card.right
            && Math.max(p.y, q.y) > card.top + 2 && Math.min(p.y, q.y) < card.bottom - 2;
        if (crosses) cost += 10000;
      }
    }
    return cost;
  }
  let best = candidates[0]!, bestCost = Infinity;
  for (const candidate of candidates) {
    const cost = score(candidate);
    if (cost < bestCost) { best = candidate; bestCost = cost; }
  }
  return best;
});
const path = computed(() => route.value.map(({ x, y }, i) => `${i ? 'L' : 'M'} ${x} ${y}`).join(' '));
const arrow = computed(() => {
  const { targetX: x, targetY: y } = props;
  const { x: dx, y: dy } = directions[props.targetPosition];
  return `M ${x} ${y} l ${dx * 8 - dy * 3} ${dy * 8 + dx * 3} l ${dy * 6} ${-dx * 6} Z`;
});
const midpoint = computed(() => route.value[2]!);
const customMarker = computed(() => props.markerEnd && props.markerEnd !== "url('#')" ? props.markerEnd : undefined);
</script>

<template>
  <g class="builder-wire" :class="{ executing }" :data-wire-kind="kind">
    <path :d="path" class="wire-clearance" aria-hidden="true" />
    <BaseEdge
      :id="id" :path="path" :label="label" :label-x="midpoint.x" :label-y="midpoint.y"
      :label-style="labelStyle" :label-show-bg="labelShowBg" :label-bg-style="labelBgStyle"
      :label-bg-padding="labelBgPadding" :label-bg-border-radius="labelBgBorderRadius"
      :style="style" :marker-start="markerStart" :marker-end="customMarker" :interaction-width="interactionWidth"
    />
    <path v-if="!customMarker" :d="arrow" class="wire-direction" aria-hidden="true" />
  </g>
</template>

<style scoped>
.builder-wire { --wire-color: var(--accent-agent); }
.builder-wire[data-wire-kind="tool"] { --wire-color: var(--accent-tool); }
.builder-wire[data-wire-kind="loop"] { --wire-color: var(--status-paused); }
.wire-clearance { fill: none; stroke: var(--canvas-bg); stroke-width: 6; opacity: .85; pointer-events: none; }
.wire-direction { fill: var(--wire-color); pointer-events: none; }
.builder-wire :deep(.vue-flow__edge-path) { stroke: var(--wire-color); stroke-width: 1.6; opacity: .75; }
.builder-wire[data-wire-kind="loop"] :deep(.vue-flow__edge-path) { stroke-dasharray: 6 4; }
.vue-flow__edge:not(.exec-active) .builder-wire:not(.executing) :deep(.vue-flow__edge-path) { animation: none; }
.vue-flow__edge:is(:hover,.selected) .builder-wire :deep(.vue-flow__edge-path) { opacity: 1; stroke-width: 2.7; }
.builder-wire.executing, .vue-flow__edge.exec-active .builder-wire { --wire-color: var(--status-running); }
.builder-wire.executing :deep(.vue-flow__edge-path), .vue-flow__edge.exec-active .builder-wire :deep(.vue-flow__edge-path) {
  opacity: 1; stroke-width: 2.7; stroke-dasharray: 8 5; animation: builder-flow .7s linear infinite;
}
@keyframes builder-flow { to { stroke-dashoffset: -26; } }
@media (prefers-reduced-motion: reduce) {
  .vue-flow__edge .builder-wire :deep(.vue-flow__edge-path) { animation: none !important; }
}
</style>
