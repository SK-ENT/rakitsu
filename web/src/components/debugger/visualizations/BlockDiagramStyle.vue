<script setup lang="ts">
import { computed, ref } from 'vue';
import type { AgentEvent, DebugTreeNode } from '../../../types';

const props = defineProps<{
  roots: DebugTreeNode[];
  activeIds: Set<string>;
  selectedId?: string;
  renderKey?: number;
  visibleUpTo: number;
  totalEvents: number;
  events: AgentEvent[];
}>();

const emit = defineEmits<{ 'node-click': [nodeId: string] }>();

const cardWidth = 180;
const cardHeight = 80;
const columnGap = 38;
const rowGap = 14;
const inset = 16;
const collapsed = ref(new Set<string>());
const viewport = ref<HTMLElement | null>(null);
const zoom = ref(1);
const zoomStep = 1.25;
const minZoom = 0.1;
const maxZoom = 2;

interface Card {
  node: DebugTreeNode;
  x: number;
  y: number;
  parent?: Card;
  children: Card[];
}

const labels: Record<DebugTreeNode['type'], string> = {
  pipeline: 'Pipeline', step: 'Step', agent: 'Agent', iteration: 'Iteration',
  thought: 'Model', tool_call: 'Tool', reflection: 'Reflection',
  ground_check: 'Ground check', handoff: 'Handoff', message: 'Message',
  error: 'Error', chat_turn: 'Chat turn',
};

const statusLabels: Record<DebugTreeNode['status'], string> = {
  pending: 'Pending', running: 'In progress', success: 'Done', error: 'Failed',
  idle: 'Idle', paused: 'Paused', salvaged: 'Recovered',
};

const eventIndex = computed(() => props.visibleUpTo < 0 ? props.events.length - 1 : props.visibleUpTo);
const currentEvent = computed(() => props.events[eventIndex.value]);

const eventPositions = computed(() => new Map(props.events.map((event, index) => [event.id, index])));

const graph = computed(() => {
  // The debug tree mutates some nodes in place during playback.
  void props.renderKey;
  const cards: Card[] = [];
  let row = 0;
  let width = 0;
  function place(node: DebugTreeNode, depth: number, parent?: Card): Card {
    const card: Card = { node, x: inset + depth * (cardWidth + columnGap), y: 0, parent, children: [] };
    cards.push(card);
    width = Math.max(width, card.x + cardWidth + inset);
    if (!collapsed.value.has(node.id)) {
      card.children = node.children.map(child => place(child, depth + 1, card));
    }
    const first = card.children[0];
    const last = card.children[card.children.length - 1];
    card.y = first && last
      ? (first.y + last.y) / 2
      : inset + row++ * (cardHeight + rowGap);
    return card;
  }
  props.roots.forEach(root => place(root, 0));
  return { cards, width, height: row * (cardHeight + rowGap) - rowGap + inset * 2 };
});

const activePath = computed(() => {
  const event = currentEvent.value;
  const cards = graph.value.cards;
  let selected: Card | undefined;
  let latestStart = -1;
  for (const card of cards) {
    const { node } = card;
    if (event && (node.startEvent.id === event.id || node.endEvent?.id === event.id)) {
      selected = card;
      latestStart = Infinity;
    } else if (event && node.agentName === event.agent_name && latestStart !== Infinity) {
      const start = eventPositions.value.get(node.startEvent.id) ?? -1;
      const end = node.endEvent ? eventPositions.value.get(node.endEvent.id) : undefined;
      if (start >= latestStart && start <= eventIndex.value && (end === undefined || end >= eventIndex.value)) {
        selected = card;
        latestStart = start;
      }
    } else if (!event && props.activeIds.has(node.id)) {
      selected = card;
    }
  }
  const ids = new Set<string>();
  for (let card = selected; card; card = card.parent) ids.add(card.node.id);
  return { current: selected?.node.id, ids };
});

const connectorCards = computed(() => graph.value.cards.filter(card => card.parent).sort(
  (a, b) => Number(activePath.value.ids.has(a.node.id)) - Number(activePath.value.ids.has(b.node.id)),
));

function connector(parent: Card, child: Card) {
  const fromX = parent.x + cardWidth;
  const fromY = parent.y + cardHeight / 2;
  const toY = child.y + cardHeight / 2;
  const trunk = fromX + columnGap / 2;
  return `M ${fromX} ${fromY} H ${trunk} V ${toY} H ${child.x}`;
}

function zoomOut() {
  zoom.value = Math.max(minZoom, zoom.value / zoomStep);
}

function zoomIn() {
  zoom.value = Math.min(maxZoom, zoom.value * zoomStep);
}

function fitToView() {
  const pane = viewport.value;
  if (!pane || !graph.value.cards.length) return;
  zoom.value = Math.min(1, pane.clientWidth / graph.value.width, pane.clientHeight / graph.value.height);
  pane.scrollTo(0, 0);
}

function toggle(node: DebugTreeNode) {
  if (collapsed.value.has(node.id)) collapsed.value.delete(node.id);
  else collapsed.value.add(node.id);
}

function collapseAll() {
  collapsed.value = new Set(graph.value.cards.filter(card => card.node.children.length).map(card => card.node.id));
}

function summary(node: DebugTreeNode) {
  if (node.streamingText) return node.streamingText;
  if (node.durationMs !== undefined) return `${node.durationMs} ms`;
  return statusLabels[node.status];
}
</script>

<template>
  <section class="execution-diagram" aria-label="Execution graph">
    <header class="diagram-toolbar">
      <div>
        <h3>Execution path</h3>
        <p v-if="currentEvent">
          {{ currentEvent.event_type.replace(/_/g, ' ').toLowerCase() }}
          <template v-if="currentEvent.agent_name"> · {{ currentEvent.agent_name }}</template>
        </p>
      </div>
      <div class="diagram-actions">
        <span v-if="currentEvent">{{ eventIndex + 1 }} / {{ totalEvents }}</span>
        <div v-if="graph.cards.length" class="diagram-zoom" role="group" aria-label="Graph zoom">
          <button type="button" aria-label="Zoom out" :disabled="zoom <= minZoom" @click="zoomOut">−</button>
          <span>{{ Math.round(zoom * 100) }}%</span>
          <button type="button" aria-label="Zoom in" :disabled="zoom >= maxZoom" @click="zoomIn">+</button>
          <button type="button" aria-label="Fit to view" @click="fitToView">Fit</button>
        </div>
        <button type="button" @click="collapsed.clear()">Expand all</button>
        <button type="button" @click="collapseAll">Collapse all</button>
      </div>
    </header>
    <div v-if="graph.cards.length" ref="viewport" class="diagram-scroll">
      <div class="diagram-layout" :style="{ width: graph.width + 'px', height: graph.height + 'px', zoom }">
        <svg :width="graph.width" :height="graph.height" class="diagram-connectors" aria-hidden="true">
          <template v-for="card in connectorCards" :key="card.node.id">
            <path
              v-if="card.parent"
              :d="connector(card.parent, card)"
              :data-kind="card.node.type"
              :class="{ active: activePath.ids.has(card.node.id) }"
            />
          </template>
        </svg>
        <div
          v-for="card in graph.cards"
          :key="card.node.id"
          class="diagram-node"
          :class="{ current: activePath.current === card.node.id, active: activePath.ids.has(card.node.id) }"
          :data-kind="card.node.type"
          :style="{ left: card.x + 'px', top: card.y + 'px', width: cardWidth + 'px', height: cardHeight + 'px' }"
        >
          <button
            class="node-details"
            type="button"
            :aria-pressed="selectedId === card.node.id"
            :aria-current="activePath.current === card.node.id ? 'step' : undefined"
            @click="emit('node-click', card.node.id)"
          >
            <span class="node-kind">{{ labels[card.node.type] }}</span>
            <strong>{{ card.node.label }}</strong>
            <small>{{ summary(card.node) }}</small>
          </button>
          <button
            v-if="card.node.children.length"
            class="node-expand"
            type="button"
            :aria-label="`${collapsed.has(card.node.id) ? 'Expand' : 'Collapse'} ${card.node.label}`"
            :aria-expanded="!collapsed.has(card.node.id)"
            @click="toggle(card.node)"
          >{{ collapsed.has(card.node.id) ? '+' : '−' }}</button>
        </div>
      </div>
    </div>
    <p v-else class="empty-diagram">The execution path appears when the run starts.</p>
  </section>
</template>

<style scoped>
.execution-diagram {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--surface-0);
}

.diagram-toolbar,
.diagram-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
}

.diagram-toolbar {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
}

.diagram-toolbar h3,
.diagram-toolbar p { margin: 0; }
.diagram-toolbar p,
.diagram-actions { color: var(--text-secondary); font-size: 12px; }
.diagram-scroll { flex: 1; min-height: 0; overflow: auto; }
.diagram-layout { position: relative; }
.diagram-connectors { position: absolute; inset: 0; pointer-events: none; }

[data-kind] { --diagram-accent: var(--accent-agent); }
[data-kind=pipeline], [data-kind=handoff] { --diagram-accent: var(--accent-orch); }
[data-kind=thought], [data-kind=reflection], [data-kind=ground_check] { --diagram-accent: var(--accent-skill); }
[data-kind=iteration], [data-kind=step] { --diagram-accent: var(--status-paused); }
[data-kind=tool_call] { --diagram-accent: var(--accent-tool); }
[data-kind=error] { --diagram-accent: var(--status-error); }

.diagram-connectors path {
  fill: none;
  stroke: color-mix(in srgb, var(--diagram-accent) 32%, var(--border-default));
  stroke-width: 2;
  stroke-linejoin: round;
}

.diagram-connectors path.active {
  stroke: var(--diagram-accent);
  stroke-dasharray: 6 5;
  animation: route-flow 900ms linear infinite;
}

.diagram-node {
  position: absolute;
  display: flex;
  background: color-mix(in srgb, var(--diagram-accent) var(--diagram-card-tint), var(--surface-2));
  border: 1px solid color-mix(in srgb, var(--diagram-accent) 40%, var(--border-default));
  border-radius: var(--diagram-card-radius);
  transition: border-color 160ms ease, background 160ms ease;
}

.diagram-node.active { border-color: var(--diagram-accent); }
.diagram-node.current {
  background: color-mix(in srgb, var(--diagram-accent) 23%, var(--surface-2));
  box-shadow: 0 0 0 2px var(--diagram-accent);
}

.execution-diagram button {
  color: var(--text-primary);
  background: transparent;
  border: 1px solid var(--border-default);
  border-radius: 4px;
  font: inherit;
  cursor: pointer;
}

.execution-diagram button:hover,
.execution-diagram button[aria-pressed=true] { background: var(--surface-4); }
.execution-diagram button:focus-visible { outline: 2px solid var(--accent-agent); outline-offset: -2px; }
.diagram-actions button { padding: 4px 7px; }
.diagram-zoom { display: flex; align-items: center; gap: 4px; }
.diagram-zoom span { min-width: 4ch; text-align: center; }
.diagram-zoom button:disabled { opacity: 0.45; cursor: default; }

.execution-diagram .node-details {
  display: grid;
  align-content: center;
  gap: 3px;
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  text-align: left;
  border: 0;
}

.node-details :is(strong, small) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-kind { font-size: 11px; color: var(--diagram-accent); }
.node-details small { color: var(--text-secondary); }
.node-expand { align-self: flex-start; margin: 5px 5px 0 0; width: 22px; }
.empty-diagram { padding: 12px; color: var(--text-secondary); }

@keyframes route-flow { to { stroke-dashoffset: -22; } }

@media (prefers-reduced-motion: reduce) {
  .diagram-connectors path.active, .diagram-node { animation: none; transition: none; }
}
</style>
