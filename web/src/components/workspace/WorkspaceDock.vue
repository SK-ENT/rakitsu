<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { workspaceViews, type WorkspacePreset, type WorkspaceView } from './workspaceViews';

type Pane = { id: number; tabs: WorkspaceView[]; active: WorkspaceView; row?: 1 };
type FloatRect = { x: number; y: number; width: number; height: number; z: number };
type Drop = { paneId: number; position: 'left' | 'right' | 'tabs' };
type Drag = {
  view: WorkspaceView;
  kind: 'move' | 'resize';
  pointerId: number;
  startX: number;
  startY: number;
  rect: FloatRect;
  moved: boolean;
};

const activeView = defineModel<WorkspaceView>({ required: true });
const views = Object.keys(workspaceViews) as WorkspaceView[];
const surface = ref<HTMLElement | null>(null);
const surfaceWidth = ref(window.innerWidth);
const stacked = computed(() => !!preset.value && surfaceWidth.value < 1120);
const panes = ref<Pane[]>([]);
const floats = ref<Partial<Record<WorkspaceView, FloatRect>>>({});
const openMenu = ref<WorkspaceView | null>(null);
const preset = ref<WorkspacePreset | null>(null);
const maximized = ref<WorkspaceView | null>(null);
const rememberedFloat: Partial<Record<WorkspaceView, Omit<FloatRect, 'z'>>> = {};
const drag = ref<Drag | null>(null);
const drop = ref<Drop | null>(null);
let nextPaneId = 0;
let nextZ = 1;
let resizeObserver: ResizeObserver | undefined;

function reset() {
  preset.value = null;
  const first: Pane = { id: ++nextPaneId, tabs: ['builder', 'inspector', 'debugger', 'sessions', 'run-log', 'run-timeline'], active: 'builder' };
  if ((surface.value?.clientWidth ?? window.innerWidth) < 920) {
    first.tabs.push('chat');
    panes.value = [first];
  } else {
    panes.value = [first, { id: ++nextPaneId, tabs: ['chat'], active: 'chat' }];
  }
  floats.value = {};
  maximized.value = null;
  for (const view of views) delete rememberedFloat[view];
  cancelDrag();
  closeMenu();
  activeView.value = 'builder';
}
reset();
function applyPreset(layout: WorkspacePreset) {
  const bottom: WorkspaceView[] = layout === 'debug'
    ? ['run-log', 'run-timeline'] : ['run-timeline', 'run-log'];
  preset.value = layout;
  panes.value = [
    { id: ++nextPaneId, tabs: ['builder', 'inspector', 'sessions', 'chat'], active: 'builder' },
    { id: ++nextPaneId, tabs: ['debugger'], active: 'debugger' },
    ...bottom.map(view => ({ id: ++nextPaneId, tabs: [view], active: view, row: 1 as const })),
  ];
  floats.value = {};
  maximized.value = null;
  cancelDrag();
  closeMenu();
  activeView.value = 'builder';
}

defineExpose({ reset, applyPreset });

function paneFor(view: WorkspaceView) {
  return panes.value.find(pane => pane.tabs.includes(view));
}

function activate(view: WorkspaceView) {
  if (maximized.value && maximized.value !== view) maximized.value = null;
  const pane = paneFor(view);
  if (pane) pane.active = view;
  const rect = floats.value[view];
  if (rect) rect.z = ++nextZ;
  activeView.value = view;
}

watch(activeView, view => {
  activate(view);
  closeMenu();
});

function closeMenu() {
  openMenu.value = null;
}

function finishMenuAction(view: WorkspaceView) {
  const restoreFocus = openMenu.value === view;
  closeMenu();
  if (restoreFocus) {
    nextTick(() => surface.value?.querySelector<HTMLButtonElement>(`[data-layout-for="${view}"]`)?.focus({ preventScroll: true }));
  }
}

function toggleMenu(view: WorkspaceView) {
  openMenu.value = openMenu.value === view ? null : view;
}

function remove(view: WorkspaceView) {
  const rect = floats.value[view];
  if (rect) rememberedFloat[view] = { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
  delete floats.value[view];
  maximized.value = null;
  const pane = paneFor(view);
  if (!pane) return;
  pane.tabs = pane.tabs.filter(tab => tab !== view);
  const first = pane.tabs[0];
  if (!first) {
    panes.value = panes.value.filter(item => item !== pane);
  } else if (pane.active === view) {
    pane.active = first;
  }
}

function withinSurface(rect: FloatRect): FloatRect {
  const width = surface.value?.clientWidth ?? window.innerWidth;
  const height = surface.value?.clientHeight ?? window.innerHeight;
  const windowWidth = Math.min(Math.max(280, rect.width), width);
  const windowHeight = Math.min(Math.max(180, rect.height), height);
  return {
    width: windowWidth,
    height: windowHeight,
    x: Math.max(0, Math.min(rect.x, width - windowWidth)),
    y: Math.max(0, Math.min(rect.y, height - windowHeight)),
    z: rect.z,
  };
}

function defaultFloat(view: WorkspaceView): FloatRect {
  const remembered = rememberedFloat[view];
  if (remembered) return { ...remembered, z: nextZ };
  const offset = Object.keys(floats.value).length * 24;
  const size = view === 'builder' ? { width: 800, height: 660 } : { width: 580, height: 440 };
  return { x: 36 + offset, y: 48 + offset, ...size, z: nextZ };
}

function floatView(view: WorkspaceView, rect = defaultFloat(view)) {
  remove(view);
  floats.value[view] = withinSurface(rect);
  activate(view);
  finishMenuAction(view);
}

function dockView(view: WorkspaceView, position: Drop['position'], targetId?: number) {
  const target = panes.value.find(pane => pane.id === targetId);
  if (target?.tabs.includes(view) && (position === 'tabs' || target.tabs.length === 1)) {
    activate(view);
    finishMenuAction(view);
    return;
  }
  remove(view);
  if (position === 'tabs' && target) {
    target.tabs.push(view);
  } else {
    const index = target ? panes.value.indexOf(target) : position === 'left' ? 0 : panes.value.length;
    panes.value.splice(target && position === 'right' ? index + 1 : index, 0, {
      id: ++nextPaneId, tabs: [view], active: view, row: target ? target.row : position === 'right' ? panes.value[panes.value.length - 1]?.row : undefined,
    });
  }
  activate(view);
  finishMenuAction(view);
}

const visiblePane = computed(() => new Map(panes.value.map((pane, index) => [pane.active, index + 1])));
const gridTracks = computed(() => {
  if (maximized.value || !panes.value.length) return 'minmax(0, 1fr)';
  if (stacked.value) return 'minmax(0, 1fr)';
  if (preset.value) return 'repeat(12, minmax(0, 1fr))';
  return panes.value.map(pane =>
    `minmax(min(300px, 100%), ${pane.tabs.includes('builder') ? '1.6fr' : '1fr'})`,
  ).join(' ');
});

const gridRows = computed(() => stacked.value && !maximized.value && panes.value.length
  ? panes.value.map(pane => pane.row === 1 ? 'var(--strip-height) 260px' : 'var(--strip-height) minmax(500px, 75vh)').join(' ')
  : preset.value && !maximized.value && panes.value.some(pane => pane.row === 1)
  ? 'var(--strip-height) minmax(0, 2fr) var(--strip-height) minmax(0, 1fr)'
  : 'var(--strip-height) minmax(0, 1fr)');

function panePlacement(pane: Pane, part: 'strip' | 'body' | 'preview') {
  const row = stacked.value ? panes.value.indexOf(pane) * 2 + 1 : preset.value && pane.row === 1 ? 3 : 1;
  const gridRow = part === 'strip' ? row : part === 'body' ? row + 1 : `${row} / ${row + 2}`;
  if (maximized.value) return { gridColumn: 1, gridRow: part === 'body' ? 2 : 1 };
  if (stacked.value) return { gridColumn: 1, gridRow };
  if (!preset.value) return { gridColumn: panes.value.indexOf(pane) + 1, gridRow };
  const siblings = panes.value.filter(item => item.row === pane.row);
  const index = siblings.indexOf(pane);
  const split = preset.value === 'replay' && siblings.length === 2 ? [0, 5, 12] : null;
  const boundary = (n: number) => (split?.[n] ?? Math.round(12 * n / siblings.length)) + 1;
  return { gridColumn: `${boundary(index)} / ${boundary(index + 1)}`, gridRow };
}

function toggleMaximize(view: WorkspaceView) {
  activate(view);
  maximized.value = maximized.value === view ? null : view;
  finishMenuAction(view);
}

function visible(view: WorkspaceView) {
  return maximized.value ? maximized.value === view : visiblePane.value.has(view) || !!floats.value[view];
}

function panelStyle(view: WorkspaceView) {
  if (maximized.value === view) return { gridColumn: 1, gridRow: floats.value[view] ? '1 / 3' : 2 };
  const rect = floats.value[view];
  if (rect) {
    return { left: `${rect.x}px`, top: `${rect.y}px`, width: `${rect.width}px`, height: `${rect.height}px`, zIndex: rect.z };
  }
  const pane = paneFor(view);
  return pane ? panePlacement(pane, 'body') : {};
}

function beginDrag(event: PointerEvent, view: WorkspaceView, kind: Drag['kind'] = 'move') {
  if (!event.isPrimary || event.button !== 0 || maximized.value) return;
  drag.value = {
    view, kind, pointerId: event.pointerId, startX: event.clientX, startY: event.clientY,
    rect: { ...(floats.value[view] ?? defaultFloat(view)) }, moved: false,
  };
}

function findDrop(x: number, y: number): Drop | null {
  const root = surface.value;
  if (!root) return null;
  const bounds = root.getBoundingClientRect();
  if (y < bounds.top || y > bounds.bottom || x < bounds.left || x > bounds.right) return null;
  const first = panes.value[0];
  const last = panes.value[panes.value.length - 1];
  if (!preset.value && x - bounds.left < 28) return { paneId: first?.id ?? 0, position: 'left' };
  if (!preset.value && bounds.right - x < 28) return { paneId: last?.id ?? 0, position: 'right' };

  for (const strip of root.querySelectorAll<HTMLElement>('[data-pane]')) {
    const { left, right, top, bottom } = strip.getBoundingClientRect();
    const paneId = Number(strip.dataset.pane);
    const pane = panes.value.find(item => item.id === paneId);
    const panelBottom = root.querySelector<HTMLElement>('.pane-panel[aria-label="' + (pane ? workspaceViews[pane.active] : '') + '"]')?.getBoundingClientRect().bottom ?? bottom;
    if (x >= left && x <= right && y >= top && y <= bottom) return { paneId, position: 'tabs' };
    if (y > bottom && y <= panelBottom && x >= left && x < left + 32) return { paneId, position: 'left' };
    if (y > bottom && y <= panelBottom && x <= right && x > right - 32) return { paneId, position: 'right' };
  }
  return null;
}

function moveDrag(event: PointerEvent) {
  const current = drag.value;
  if (!current || current.pointerId !== event.pointerId) return;
  const dx = event.clientX - current.startX;
  const dy = event.clientY - current.startY;
  if (!current.moved && Math.hypot(dx, dy) < 6) return;
  if (!current.moved) {
    current.moved = true;
    closeMenu();
    if (floats.value[current.view]) {
      activate(current.view);
      current.rect.z = nextZ;
    }
  }
  const { rect, kind, view } = current;
  if (kind === 'resize') {
    resizeFloat(view, rect, dx, dy);
    return;
  }
  drop.value = findDrop(event.clientX, event.clientY);
  if (floats.value[view]) {
    floats.value[view] = withinSurface({ ...rect, x: rect.x + dx, y: rect.y + dy });
  }
}

function endDrag(event: PointerEvent) {
  const current = drag.value;
  if (!current || current.pointerId !== event.pointerId) return;
  if (event.type !== 'pointercancel' && current.moved && current.kind === 'move') {
    const target = findDrop(event.clientX, event.clientY);
    if (target) {
      dockView(current.view, target.position, target.paneId);
    } else if (!floats.value[current.view]) {
      const bounds = surface.value?.getBoundingClientRect();
      if (bounds && event.clientX >= bounds.left && event.clientX <= bounds.right &&
          event.clientY >= bounds.top && event.clientY <= bounds.bottom) {
        floatView(current.view, { ...defaultFloat(current.view), x: event.clientX - bounds.left - 40, y: event.clientY - bounds.top - 16 });
      }
    }
  }
  cancelDrag();
}

function resizeFloat(view: WorkspaceView, rect: FloatRect, dx: number, dy: number) {
  const anchor = withinSurface(rect);
  const maxWidth = (surface.value?.clientWidth ?? window.innerWidth) - anchor.x;
  const maxHeight = (surface.value?.clientHeight ?? window.innerHeight) - anchor.y;
  floats.value[view] = {
    ...anchor,
    width: Math.min(maxWidth, Math.max(280, rect.width + dx)),
    height: Math.min(maxHeight, Math.max(180, rect.height + dy)),
  };
}

function keyboardResize(event: KeyboardEvent, view: WorkspaceView) {
  const rect = floats.value[view];
  if (!rect || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
  event.preventDefault();
  event.stopPropagation();
  const step = event.shiftKey ? 32 : 8;
  const dx = event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0;
  const dy = event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0;
  resizeFloat(view, rect, dx, dy);
}

function cancelDrag() {
  drag.value = null;
  drop.value = null;
}

function escape(event: KeyboardEvent) {
  if (event.key !== 'Escape') return;
  cancelDrag();
  if (openMenu.value) finishMenuAction(openMenu.value);
}

onMounted(() => {
  window.addEventListener('pointermove', moveDrag);
  window.addEventListener('pointerup', endDrag);
  window.addEventListener('pointercancel', endDrag);
  window.addEventListener('blur', cancelDrag);
  if (!surface.value) return;
  resizeObserver = new ResizeObserver(([entry]) => {
    if (entry) surfaceWidth.value = entry.contentRect.width;
    for (const view of views) {
      const rect = floats.value[view];
      if (rect) floats.value[view] = withinSurface(rect);
    }
  });
  resizeObserver.observe(surface.value);
});

onBeforeUnmount(() => {
  window.removeEventListener('pointermove', moveDrag);
  window.removeEventListener('pointerup', endDrag);
  window.removeEventListener('pointercancel', endDrag);
  window.removeEventListener('blur', cancelDrag);
  resizeObserver?.disconnect();
});
</script>

<template>
  <div
    ref="surface"
    class="dock"
    :class="{ 'is-dragging': drag?.moved, 'has-preset': preset }"
    :style="{ gridTemplateColumns: gridTracks, gridTemplateRows: gridRows }"
    @pointerdown="closeMenu"
    @keydown="escape"
  >
    <div
      v-for="(pane, index) in panes"
      :key="pane.id"
      :data-pane="pane.id"
      v-show="!maximized || pane.active === maximized"
      :style="panePlacement(pane, 'strip')"
      :class="{ 'is-focused': pane.active === activeView }"
      class="pane-strip"
    >
      <div class="pane-tabs" :aria-label="'Pane ' + (index + 1)">
        <button
          v-for="tab in pane.tabs"
          :key="tab"
          type="button"
          class="pane-tab"
          :aria-pressed="pane.active === tab"
          :class="{ 'is-selected': pane.active === tab }"
          @pointerdown="beginDrag($event, tab)"
          @click="activate(tab)"
        >{{ workspaceViews[tab] }}</button>
      </div>
      <button
        type="button"
        class="pane-more"
        :data-layout-for="pane.active"
        :aria-label="workspaceViews[pane.active] + ' layout options'"
        :aria-expanded="openMenu === pane.active"
        @pointerdown.stop
        @click.stop="toggleMenu(pane.active)"
      >⋯</button>
      <div v-if="openMenu === pane.active" class="pane-menu" @pointerdown.stop>
        <button type="button" @click="toggleMaximize(pane.active)">{{ maximized ? 'Restore layout' : 'Maximize' }}</button>
        <button type="button" @click="floatView(pane.active)">Float</button>
        <button v-if="pane.tabs.length > 1" type="button" @click="dockView(pane.active, 'right', pane.id)">Dock as new pane</button>
        <button v-if="pane.tabs.length > 1 || index !== 0" type="button" @click="dockView(pane.active, 'left', panes[0]?.id)">Snap left</button>
        <button v-if="pane.tabs.length > 1 || index !== panes.length - 1" type="button" @click="dockView(pane.active, 'right', panes[panes.length - 1]?.id)">Snap right</button>
        <template v-for="other in panes" :key="other.id">
          <button v-if="other.id !== pane.id" type="button" @click="dockView(pane.active, 'tabs', other.id)">Join {{ workspaceViews[other.active] }} tabs</button>
        </template>
      </div>
    </div>

    <div
      v-for="view in views"
      v-show="visible(view)"
      :key="view"
      :class="{ 'pane-floating': floats[view] && maximized !== view, 'pane-full-window': floats[view] && maximized === view, 'is-focused': view === activeView }"
      :style="panelStyle(view)"
      class="pane-panel"
      :aria-label="workspaceViews[view]"
      @pointerdown="activate(view)"
      @focusin="activate(view)"
    >
      <div v-if="floats[view]" class="float-title" @pointerdown="beginDrag($event, view)">
        <span>{{ workspaceViews[view] }}</span>
        <button
          type="button"
          class="pane-more"
          :data-layout-for="view"
          :aria-label="workspaceViews[view] + ' layout options'"
          :aria-expanded="openMenu === view"
          @pointerdown.stop
          @click.stop="toggleMenu(view)"
        >⋯</button>
        <div v-if="openMenu === view" class="pane-menu" @pointerdown.stop>
          <button type="button" @click="toggleMaximize(view)">{{ maximized ? 'Restore layout' : 'Maximize' }}</button>
          <button type="button" @click="dockView(view, 'right')">Dock as new pane</button>
          <button type="button" @click="dockView(view, 'left', panes[0]?.id)">Snap left</button>
          <button type="button" @click="dockView(view, 'right', panes[panes.length - 1]?.id)">Snap right</button>
          <button v-for="pane in panes" :key="pane.id" type="button" @click="dockView(view, 'tabs', pane.id)">Join {{ workspaceViews[pane.active] }} tabs</button>
        </div>
      </div>
      <div class="pane-body">
        <slot :name="view" :visible="visible(view)" />
      </div>
      <button
        v-if="floats[view] && maximized !== view"
        type="button"
        class="float-resize"
        :aria-label="'Resize ' + workspaceViews[view]"
        title="Drag or use the arrow keys to resize"
        @pointerdown="beginDrag($event, view, 'resize')"
        @keydown="keyboardResize($event, view)"
      />
    </div>

    <div
      v-for="pane in panes"
      v-show="!maximized && drop?.paneId === pane.id"
      :key="'drop-' + pane.id"
      :class="drop?.position"
      :style="panePlacement(pane, 'preview')"
      class="dock-preview"
      aria-hidden="true"
    />
    <div v-if="drop?.paneId === 0" class="dock-preview" :class="drop.position" style="grid-column: 1" aria-hidden="true" />
  </div>
</template>

<style scoped>
.dock {
  --strip-height: 38px;
  position: relative;
  isolation: isolate;
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-rows: var(--strip-height) minmax(0, 1fr);
  gap: 0 6px;
  overflow: auto;
  background: var(--surface-0);
}

.dock.has-preset { column-gap: 0; }

.pane-strip {
  position: relative;
  grid-row: 1;
  min-width: 0;
  z-index: 1;
}
.pane-strip, .float-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
}
.pane-tabs {
  display: flex;
  align-items: stretch;
  align-self: stretch;
  gap: 2px;
  min-width: 0;
  overflow: auto hidden;
  scrollbar-width: thin;
}
.pane-tab, .pane-more, .pane-menu button {
  border: 0;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}
.pane-tab {
  flex: none;
  padding: 0 12px;
  border-bottom: 2px solid transparent;
  touch-action: none;
  white-space: nowrap;
}
.pane-tab.is-selected {
  color: var(--text-primary);
  background: var(--surface-3);
}
.pane-strip.is-focused .pane-tab.is-selected { border-color: var(--accent-agent); }
.pane-tab:hover, .pane-more:hover, .pane-menu button:hover { background: var(--surface-4); color: var(--text-primary); }
button:focus-visible { outline: 2px solid var(--accent-agent); outline-offset: -2px; }
.pane-more {
  flex: none;
  width: 32px;
  height: 28px;
  margin: 0 4px;
  border-radius: 4px;
  font-size: 20px;
  line-height: 1;
}
.pane-menu {
  position: absolute;
  top: calc(100% - 2px);
  right: 4px;
  z-index: 2;
  display: grid;
  min-width: 160px;
  padding: 4px;
  border: 1px solid var(--border-default);
  border-radius: 6px;
  background: var(--surface-2);
  box-shadow: 0 6px 16px #0003;
}
.pane-menu button {
  text-align: left;
  padding: 7px 10px;
  white-space: nowrap;
  border-radius: 3px;
}
.pane-panel {
  isolation: isolate;
  grid-row: 2;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--surface-0);
  border-right: 1px solid var(--border-subtle);
}
.pane-body {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: auto;
}
.pane-floating {
  position: absolute;
  grid-row: auto;
  border: 1px solid var(--border-default);
  border-radius: 8px;
  box-shadow: 0 12px 30px #0005;
}
.pane-full-window { grid-row: 1 / 3; }
.pane-floating.is-focused { border-color: var(--accent-agent); }
.float-title {
  position: relative;
  min-height: var(--strip-height);
  flex: none;
  padding-left: 12px;
  border-radius: 8px 8px 0 0;
  font-size: 13px;
  font-weight: 600;
  cursor: grab;
  touch-action: none;
}
.float-resize {
  position: absolute;
  right: 1px;
  bottom: 1px;
  width: 18px;
  height: 18px;
  cursor: nwse-resize;
  padding: 0;
  border: 0;
  touch-action: none;
  background: linear-gradient(135deg, transparent 48%, var(--text-muted) 50%, transparent 57%, transparent 67%, var(--text-muted) 69%, transparent 76%);
}
.dock-preview {
  grid-row: 1 / 3;
  z-index: 2147483647;
  pointer-events: none;
  border: 2px solid var(--accent-agent);
  background: color-mix(in srgb, var(--accent-agent) 15%, transparent);
}
.dock-preview.left { width: 30%; justify-self: start; }
.dock-preview.right { width: 30%; justify-self: end; }
.dock-preview.tabs { height: var(--strip-height); }
.is-dragging { cursor: grabbing; user-select: none; }
</style>
