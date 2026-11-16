<script setup lang="ts">
import { computed, inject, onUnmounted, ref, watch, type ComputedRef } from 'vue';
import type { Edge, Node, XYPosition } from '@vue-flow/core';

type BuilderNode = Node & { selected?: boolean };
const props = defineProps<{ nodes: BuilderNode[]; edges: Edge[]; editable: boolean }>();
const focus = inject<ComputedRef<{ agent: string; tool: string } | null>>('builderWorkflowFocus');
const emit = defineEmits<{
  select: [id: string, event: MouseEvent | KeyboardEvent];
  move: [id: string, position: XYPosition, event: PointerEvent, groupId?: string];
  context: [payload: { node: Node; event: MouseEvent }];
  connect: [source: string, target: string, sourceHandle?: string, targetHandle?: string];
  fold: [id: string];
}>();
type Point = XYPosition;
type Kind = 'flow' | 'tool' | 'loop';
type Segment = [Point, Point];
type Reserved = { segment: Segment; kind: Kind; input?: string };
const laneGap = 12;
const colors = { flow: 'var(--accent-agent)', tool: 'var(--accent-tool)', loop: 'var(--status-paused)' };
const svg = ref<SVGSVGElement | null>(null);
const camera = ref({ x: 0, y: 0, k: 1 });
const auto = ref(true);
const pending = ref<string | null>(null);
const draft = ref<Point | null>(null);
const preview = ref<{ id: string; position: Point } | null>(null);
let drag: { pointer: number; id?: string; x: number; y: number; origin: Point; moved: boolean } | undefined;
let suppressClick = false;
let wireDrag: { pointer: number; x: number; y: number; moved: boolean } | undefined;
let animation = 0;

const pt = (x: number, y: number): Point => ({ x, y });
const byId = computed(() => new Map(props.nodes.map(node => [node.id, node])));
const folded = (node: Node) => node.type === 'group' && (node.data?.viewMode === 'compact' || node.data?.collapsed === true);
function absolute(node: Node): Point {
  const own = preview.value?.id === node.id ? preview.value.position : node.position;
  const parent = node.parentNode ? byId.value.get(node.parentNode) : undefined;
  if (!parent) return own;
  const origin = absolute(parent);
  return pt(own.x + origin.x, own.y + origin.y);
}
function displayId(id: string) {
  let visible = id;
  for (let node = byId.value.get(id); node?.parentNode;) {
    node = byId.value.get(node.parentNode);
    if (node && folded(node)) visible = node.id;
  }
  return visible;
}
const entries = computed(() => props.nodes
  .filter(node => !node.hidden && !node.id.startsWith('exec_') && displayId(node.id) === node.id)
  .map(node => {
    const position = absolute(node);
    const group = node.type === 'group';
    const style = node.style && typeof node.style === 'object' ? node.style : {};
    const width = group ? parseFloat(String(style.width ?? 240)) : 184;
    const height = group ? parseFloat(String(style.height ?? 120)) : 72;
    return { node, ...position, width: folded(node) ? 184 : width, height: folded(node) ? 44 : height, group };
  }));
type Entry = (typeof entries.value)[number];
const groups = computed(() => entries.value.filter(entry => entry.group && !folded(entry.node)));
const cards = computed(() => entries.value.filter(entry => !entry.group || folded(entry.node)));
const ports = computed(() => new Map(entries.value.map(entry => [entry.node.id, entry])));
const output = (entry: Entry, kind?: Kind) => kind === 'tool' && !entry.group
  ? pt(entry.x + entry.width / 2, entry.y + entry.height)
  : pt(entry.x + entry.width, entry.y + (entry.group ? 22 : 44));
const input = (entry: Entry, kind?: Kind) => kind === 'tool' && !entry.group
  ? pt(entry.x + entry.width / 2, entry.y)
  : pt(entry.x, entry.y + (entry.group ? 22 : 44));
const span = (a: number,b: number,c: number,d: number) =>
  Math.max(0, Math.min(Math.max(a,b),Math.max(c,d))-Math.max(Math.min(a,b),Math.min(c,d)));
function segments(points: Point[]): Segment[] {
  return points.slice(1).flatMap((p,i) => {
    const q = points[i]!;
    return p.x === q.x && p.y === q.y ? [] : [[q,p]];
  });
}
const path = (points: Point[]) => points.map((p,i) => `${i ? 'L' : 'M'}${p.x} ${p.y}`).join(' ');
const overlap = ([a,b]: Segment, [c,d]: Segment) => a.y === b.y && c.y === d.y && a.y === c.y
  ? span(a.x,b.x,c.x,d.x)
  : a.x === b.x && c.x === d.x && a.x === c.x ? span(a.y,b.y,c.y,d.y) : 0;

function crosses([a,b]: Segment, [c,d]: Segment) {
  const horizontal = a.y === b.y;
  if (horizontal === (c.y === d.y)) return false;
  const [left,right,top,bottom] = horizontal ? [a,b,c,d] : [c,d,a,b];
  const x = top.x, y = left.y;
  return x >= Math.min(left.x,right.x) && x <= Math.max(left.x,right.x)
    && y >= Math.min(top.y,bottom.y) && y <= Math.max(top.y,bottom.y);
}

function chooseRoute(source: Entry, target: Entry, start: Point, end: Point, kind: Kind, used: Reserved[], inputKey: string) {
  const a = kind === 'tool' && !source.group ? pt(start.x,start.y+14) : pt(start.x+14,start.y);
  const b = kind === 'tool' && !target.group ? pt(end.x,end.y-14) : pt(end.x-14,end.y);
  const occupied = used.flatMap(({ segment }) => segment);
  const lead = Math.max(14,Math.min(42,(Math.abs(start.x-end.x)+Math.abs(start.y-end.y))*.15));
  const xs = new Set([(a.x+b.x)/2,a.x,b.x,...cards.value.flatMap(n=>[n.x-14,n.x+n.width+14])]);
  const ys = new Set([(a.y+b.y)/2,a.y,b.y,...cards.value.flatMap(n=>[n.y-14,n.y+n.height+14])]);
  xs.add(start.x+lead); xs.add(end.x-lead);
  ys.add(start.y+lead); ys.add(end.y-lead);
  for (const p of occupied) {
    xs.add(p.x-laneGap); xs.add(p.x+laneGap);
    ys.add(p.y-laneGap); ys.add(p.y+laneGap);
  }
  const crossingCost = 2000;
  let best: Point[] | undefined, weight = Infinity;
  function candidate(middle: Point[]) {
    const points = [start,a,...middle,b,end];
    const legs = segments(points);
    const straightLead = (fromEnd: boolean) => {
      const ordered = fromEnd ? [...legs].reverse() : legs;
      const first = ordered[0];
      if (!first) return 0;
      const horizontal = first[0].y === first[1].y;
      let length = 0;
      for (const [p,q] of ordered) {
        if ((p.y === q.y) !== horizontal) break;
        length += Math.abs(p.x-q.x)+Math.abs(p.y-q.y);
      }
      return length;
    };
    let cost = (Math.max(0,lead-straightLead(false))+Math.max(0,lead-straightLead(true)))*3;
    for (const [i, leg] of legs.entries()) {
      const [p,q] = leg;
      if (legs.slice(0,i).some(previous => overlap(leg,previous))) return;
      if (used.some(wire => overlap(leg,wire.segment)
        && !(i === legs.length-1 && wire.input === inputKey))) return;
      const h = p.y === q.y;
      cost += Math.abs(p.x-q.x)+Math.abs(p.y-q.y)+10;
      // Prefer a neighboring lane on a substantial, parallel run in the
      // same direction. Score each leg once so bundles do not compound.
      let aligned = 0;
      for (const wire of used) {
        if (crosses(leg,wire.segment) && wire.input !== inputKey) cost += crossingCost;
        if (wire.kind !== kind) continue;
        const [u,v] = wire.segment;
        if (h !== (u.y === v.y)) continue;
        const axis = h ? 'x' : 'y', across = h ? 'y' : 'x';
        if (Math.sign(q[axis]-p[axis]) !== Math.sign(v[axis]-u[axis])) continue;
        const gap = Math.abs(p[across]-u[across]);
        if (gap > 0 && gap < laneGap) cost += span(p[axis],q[axis],u[axis],v[axis]) * .8;
        if (Math.abs(gap-laneGap) < 1) {
          aligned = Math.max(aligned,span(p[axis],q[axis],u[axis],v[axis]));
        }
      }
      cost -= aligned * .55;
      for (const n of cards.value) {
        if (h ? p.y>n.y && p.y<n.y+n.height && span(p.x,q.x,n.x,n.x+n.width)
          : p.x>n.x && p.x<n.x+n.width && span(p.y,q.y,n.y,n.y+n.height)) cost += 100000;
      }
    }
    if (cost < weight) { best = points; weight = cost; }
  }
  if (kind === 'loop' && start.x >= end.x) {
    const bottom = Math.max(start.y,end.y,...cards.value.map(n=>n.y+n.height),...occupied.map(p=>p.y))+22;
    candidate([pt(a.x,bottom),pt(b.x,bottom)]);
  }
  if (!best || weight >= crossingCost) {
    for (const y of ys) candidate([pt(a.x,y),pt(b.x,y)]);
    for (const x of xs) candidate([pt(x,a.y),pt(x,b.y)]);
  }
  if (!best || weight >= crossingCost) {
    // Try a second bend direction when a direct lane is occupied.
    for (const x of xs) for (const y of ys) {
      candidate([pt(x,a.y),pt(x,y),pt(b.x,y)]);
      candidate([pt(a.x,y),pt(x,y),pt(x,b.y)]);
    }
  }
  return best;
}
function wireKind(edge: Edge): Kind {
  const target = byId.value.get(edge.target);
  if (target?.data?.blockType === 'loop') return 'loop';
  return target?.type === 'tool' || target?.type === 'skill' ? 'tool' : 'flow';
}
const wires = computed(() => {
  const used: Reserved[] = [];
  const blocked: string[] = [];
  const links = props.edges.filter(edge => !edge.id.startsWith('exec_edge_')).flatMap(edge => {
    const source = ports.value.get(displayId(edge.source)), target = ports.value.get(displayId(edge.target));
    return source && target && source !== target ? [{ edge, kind: wireKind(edge), source, target }] : [];
  });
  type Link = (typeof links)[number];
  function terminal(link: Link, side: 'source' | 'target') {
    const entry = link[side];
    const other = side === 'source' ? 'target' : 'source';
    const toolPort = link.kind === 'tool' && !entry.group;
    const axis = toolPort ? 'x' : 'y';
    const peers = links.filter(item => item[side] === entry && (item.kind === 'tool' && !entry.group) === toolPort)
      .sort((a,b) => a[other][axis]-b[other][axis] || a.edge.id.localeCompare(b.edge.id));
    const spacing = Math.min(11, (entry.group ? 16 : toolPort ? entry.width-30 : 40)/Math.max(1,peers.length-1));
    const offset = (peers.indexOf(link)-(peers.length-1)/2)*spacing;
    const anchor = side === 'source' ? output(entry,link.kind) : input(entry,link.kind);
    return { ...anchor, [axis]: anchor[axis]+offset };
  }
  const fanDistance = (link: Link) => link.kind === 'tool'
    ? Math.abs(link.target.x-link.source.x)
    : Math.abs(link.target.y-link.source.y);
  const routed = [...links].sort((a,b) =>
    Number(b.kind==='loop')-Number(a.kind==='loop') || fanDistance(a)-fanDistance(b)).flatMap(link => {
    const start = terminal(link,'source'), end = terminal(link,'target');
    const inputKey = JSON.stringify([link.target.node.id, link.edge.target, link.edge.targetHandle ?? '', end.x, end.y]);
    const points = chooseRoute(link.source,link.target,start,end,link.kind,used,inputKey);
    if (!points) { blocked.push(link.edge.id); return []; }
    const legs = segments(points);
    used.push(...legs.map((segment,i) => ({ segment, kind: link.kind, input: i===legs.length-1 ? inputKey : undefined })));
    return [{ ...link, start, end, points, d:path(points) }];
  });
  return { routed, blocked };
});
function inGroup(id: string, groupId: string) {
  for (let node = byId.value.get(id); node?.parentNode;) {
    if (node.parentNode === groupId) return true;
    node = byId.value.get(node.parentNode);
  }
  return false;
}
const activeIds = computed(() => {
  const current = focus?.value;
  return new Set(props.nodes.filter(node => current && (node.type === 'tool'
    ? node.data?.name === current.tool : node.type !== 'group' && node.data?.name === current.agent)).map(node => node.id));
});
function activeGroup(id: string) {
  return [...activeIds.value].some(active => inGroup(active,id));
}
function activeWire(edge: Edge) {
  const current = focus?.value;
  if (!current) return false;
  if (current.tool) return activeIds.value.has(edge.source) && activeIds.value.has(edge.target)
    && byId.value.get(edge.target)?.type === 'tool';
  return !activeIds.value.has(edge.source) && (activeIds.value.has(edge.target) || activeGroup(edge.target));
}
const frames = computed(() => groups.value.map(entry => {
  const id = entry.node.id;
  const members = entries.value.filter(child => inGroup(child.node.id,id));
  const internal = wires.value.routed.filter(wire => inGroup(wire.edge.source,id) && inGroup(wire.edge.target,id));
  const points = [...members.flatMap(member => [pt(member.x,member.y),pt(member.x+member.width,member.y+member.height)]),
    ...internal.flatMap(wire => wire.points)];
  const x = Math.min(entry.x,...points.map(point => point.x-16));
  const y = Math.min(entry.y,...points.map(point => point.y-40));
  const right = Math.max(entry.x+entry.width,...points.map(point => point.x+16));
  const bottom = Math.max(entry.y+entry.height,...points.map(point => point.y+16));
  return { entry,x,y,width:right-x,height:bottom-y };
}));
function fit() {
  const pane = svg.value;
  if (!pane || !entries.value.length) return;
  const points = [...entries.value.flatMap(e=>[pt(e.x,e.y),pt(e.x+e.width,e.y+e.height)]),...wires.value.routed.flatMap(w=>w.points),...frames.value.flatMap(f=>[pt(f.x,f.y),pt(f.x+f.width,f.y+f.height)])];
  const left=Math.min(...points.map(p=>p.x)), top=Math.min(...points.map(p=>p.y));
  const width=Math.max(...points.map(p=>p.x))-left, height=Math.max(...points.map(p=>p.y))-top;
  const k=Math.min(1.3,Math.max(1,pane.clientWidth-64)/width,Math.max(1,pane.clientHeight-70)/height);
  const to = {x:(pane.clientWidth-width*k)/2-left*k,y:(pane.clientHeight-height*k)/2-top*k,k};
  cancelAnimationFrame(animation);
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) {camera.value=to;return;}
  const from=camera.value,start=performance.now();
  function frame(now:number) {
    const t=Math.min(1,(now-start)/370), e=1-(1-t)**3;
    camera.value={x:from.x+(to.x-from.x)*e,y:from.y+(to.y-from.y)*e,k:from.k+(to.k-from.k)*e};
    if(t<1) animation=requestAnimationFrame(frame);
  }
  animation=requestAnimationFrame(frame);
}
function follow(value: boolean) { auto.value=value; if(value) fit(); else cancelAnimationFrame(animation); }
watch(() => [entries.value.map(n=>[n.node.id,n.x,n.y,n.width,n.height]), wires.value.routed.map(w=>w.d)],
  ()=>{if(auto.value&&!drag&&!wireDrag)fit();});
watch(svg,(pane,_,cleanup)=>{
  if(!pane)return;
  const observer=new ResizeObserver(()=>{if(auto.value)fit();});
  observer.observe(pane);
  cleanup(()=>observer.disconnect());
});
onUnmounted(()=>cancelAnimationFrame(animation));
function down(e:PointerEvent,node?:Node) {
  if(e.button!==0)return;
  suppressClick=false;
  drag={pointer:e.pointerId,id:props.editable?node?.id:undefined,x:e.clientX,y:e.clientY,
    origin:node&&props.editable?node.position:camera.value,moved:false};
}
function move(e:PointerEvent) {
  if (wireDrag?.pointer === e.pointerId && !wireDrag.moved
    && Math.hypot(e.clientX-wireDrag.x,e.clientY-wireDrag.y) >= 4) {
    wireDrag.moved = true;
    svg.value?.setPointerCapture(e.pointerId);
  }
  if(pending.value&&svg.value){
    const b=svg.value.getBoundingClientRect(),c=camera.value;
    draft.value=pt((e.clientX-b.left-c.x)/c.k,(e.clientY-b.top-c.y)/c.k);
  }
  if (wireDrag?.pointer === e.pointerId) return;
  if(drag?.pointer!==e.pointerId)return;
  const dx=e.clientX-drag.x,dy=e.clientY-drag.y;
  if(!drag.moved&&Math.hypot(dx,dy)<4)return;
  if(!drag.moved){drag.moved=true;follow(false);svg.value?.setPointerCapture(e.pointerId);}
  if(drag.id)preview.value={id:drag.id,position:pt(drag.origin.x+dx/camera.value.k,drag.origin.y+dy/camera.value.k)};
  else camera.value={...camera.value,x:drag.origin.x+dx,y:drag.origin.y+dy};
}
const dropTarget = computed(() => {
  const id = preview.value?.id;
  if (!id) return;
  const moving = ports.value.get(id);
  if (!moving) return;
  const ancestors = new Set<string>();
  for (let parent = moving.node.parentNode; parent; parent = byId.value.get(parent)?.parentNode) {
    ancestors.add(parent);
  }
  return [...frames.value].reverse().find(frame => {
    const groupId = frame.entry.node.id;
    if (groupId === id || ancestors.has(groupId)) return false;
    for (let parent = frame.entry.node.parentNode; parent; parent = byId.value.get(parent)?.parentNode) {
      if (parent === id) return false;
    }
    const x = moving.x + moving.width / 2;
    const y = moving.y + moving.height / 2;
    return x > frame.x && x < frame.x + frame.width && y > frame.y && y < frame.y + frame.height;
  });
});

function release(e:PointerEvent,cancel=false) {
  if (wireDrag?.pointer === e.pointerId) {
    const moved = wireDrag.moved;
    wireDrag = undefined;
    if (cancel || moved) {
      suppressClick = moved;
      const target = !cancel ? document.elementFromPoint(e.clientX,e.clientY)?.closest('[data-port="in"]') : null;
      const id = target?.getAttribute('data-port-node');
      if (id && target && svg.value?.contains(target) && pending.value && id !== pending.value) emit('connect',pending.value,id);
      pending.value = null;
      draft.value = null;
    }
    return;
  }
  suppressClick=Boolean(drag?.moved);
  if (!cancel && preview.value) {
    const { id, position } = preview.value;
    emit('move', id, position, e, dropTarget.value?.entry.node.id);
  }
  preview.value=null;drag=undefined;
}
function openContext(node: Node, event: MouseEvent) {
  if (props.editable) emit('context', { node, event });
}

function zoom(e:WheelEvent) {
  e.preventDefault();
  follow(false);
  const rect=svg.value!.getBoundingClientRect(),x=e.clientX-rect.left,y=e.clientY-rect.top;
  const c=camera.value,k=Math.max(.1,Math.min(3,c.k*Math.exp(-e.deltaY*.0013)));
  camera.value={x:x-(x-c.x)*k/c.k,y:y-(y-c.y)*k/c.k,k};
}
function select(id:string,e:MouseEvent|KeyboardEvent) {
  if(suppressClick&&e instanceof MouseEvent){suppressClick=false;return;}
  emit('select',id,e);
}
function startWire(e:PointerEvent,id:string) {
  if(!props.editable||e.button!==0)return;
  suppressClick=false;
  pending.value=id;
  wireDrag={pointer:e.pointerId,x:e.clientX,y:e.clientY,moved:false};
}
function port(id:string,side:'in'|'out') {
  if(suppressClick){suppressClick=false;return;}
  if(!props.editable)return;
  if(side==='out'){pending.value=id;return;}
  if(pending.value&&pending.value!==id)emit('connect',pending.value,id);
  pending.value=null;draft.value=null;
}
</script>

<template>
  <div class="svg-workbench" :class="{ wiring: pending }">
    <div class="svg-controls">
      <button type="button" :aria-pressed="auto" @click="follow(!auto)">Auto-fit {{ auto ? 'on' : 'off' }}</button>
      <button type="button" @click="fit">Fit view</button>
      <span v-if="wires.blocked.length" role="status">{{ wires.blocked.length }} connection(s) need more space</span>
    </div>
    <svg ref="svg" class="builder-svg-canvas" data-builder-svg-canvas aria-label="Editable Builder blueprint"
      @pointerdown="down($event)" @pointermove="move" @pointerup="release($event)" @pointercancel="release($event,true)"
      @click="suppressClick && $event.stopPropagation()"
      @wheel="zoom" @dblclick.self="follow(true)" @keydown.esc="pending = null; draft = null">
      <defs>
        <pattern id="builder-svg-grid" width="20" height="20" patternUnits="userSpaceOnUse"><path d="M20 0H0V20" fill="none" stroke="currentColor" stroke-opacity=".11" /></pattern>
        <marker v-for="(color,kind) in colors" :id="`builder-svg-arrow-${kind}`" :key="kind" viewBox="0 0 8 8" refX="8" refY="4" markerUnits="userSpaceOnUse" markerWidth="7" markerHeight="5" orient="auto"><path d="M0 0L8 4L0 8Z" :fill="color" /></marker>
      </defs>
      <rect width="100%" height="100%" fill="url(#builder-svg-grid)" pointer-events="none" />
      <g :transform="`translate(${camera.x} ${camera.y}) scale(${camera.k})`" data-builder-world>
        <g v-for="{ entry,x,y,width,height } in frames" :key="entry.node.id" :data-group="entry.node.id" @contextmenu.stop="openContext(entry.node,$event)">
          <rect class="group-frame" :class="{ active: activeGroup(entry.node.id), 'drop-target': dropTarget?.entry.node.id === entry.node.id }" :x="x" :y="y" :width="width" :height="height" rx="8" />
          <rect class="group-header" :x="x" :y="y" :width="width" height="30" rx="6" @pointerdown.stop="down($event,entry.node)" @click.stop="select(entry.node.id,$event)" />
          <text class="node-label" :x="x+12" :y="y+20">{{ entry.node.data?.name }}</text>
          <rect class="port" data-port="in" role="button" tabindex="0" :aria-label="`Input on ${entry.node.data?.name || entry.node.id}`" @keydown.enter.stop="port(entry.node.id,'in')" :data-port-node="entry.node.id" :x="entry.x-5" :y="entry.y+17" width="10" height="10" @pointerdown.stop @click.stop="port(entry.node.id,'in')" />
          <rect class="port" data-port="out" role="button" tabindex="0" :aria-label="`Output on ${entry.node.data?.name || entry.node.id}`" @keydown.enter.stop="port(entry.node.id,'out')" :x="entry.x+entry.width-5" :y="entry.y+17" width="10" height="10" @pointerdown.stop="startWire($event,entry.node.id)" @click.stop="port(entry.node.id,'out')" />
          <g role="button" tabindex="0" :aria-label="`Fold ${entry.node.data?.name}`" @pointerdown.stop @click.stop="emit('fold',entry.node.id)" @keydown.enter.stop="emit('fold',entry.node.id)">
            <rect class="fold" :x="x+width-28" :y="y+4" width="23" height="22" rx="4" />
            <text class="node-label" :x="x+width-21" :y="y+20">−</text>
          </g>
        </g>
        <g v-for="wire in wires.routed" :key="wire.edge.id" :data-wire="wire.edge.id" :data-kind="wire.kind">
          <path class="wire-gap" :d="wire.d" />
          <path class="wire" :d="wire.d" :stroke="colors[wire.kind]" :stroke-dasharray="wire.kind==='loop' ? '6 4' : undefined"
            :marker-end="`url(#builder-svg-arrow-${wire.kind})`" />
          <path v-if="activeWire(wire.edge)" class="wire active-wire" :d="wire.d" />
        </g>
        <g v-for="entry in cards" :key="entry.node.id" class="node" :class="{ selected: entry.node.selected, active: activeIds.has(entry.node.id) || (entry.group && activeGroup(entry.node.id)) }"
          :data-node="entry.node.id" :data-type="entry.node.type" :transform="`translate(${entry.x} ${entry.y})`" tabindex="0" role="button"
          :aria-label="entry.node.data?.name || entry.node.id" @pointerdown.stop="down($event,entry.node)"
          @click.stop="select(entry.node.id,$event)" @keydown.enter.stop="select(entry.node.id,$event)"
          @contextmenu.stop="openContext(entry.node,$event)">
          <rect class="node-body" :width="entry.width" :height="entry.height" rx="7" />
          <text class="node-kind" x="12" y="16">{{ String(entry.node.data?.blockType || entry.node.type || 'node').toUpperCase() }}</text>
          <text class="node-label" x="12" :y="entry.group?35:38">{{ entry.node.data?.name || entry.node.id }}</text>
          <text v-if="!entry.group" class="node-detail" x="12" y="58">{{ entry.node.data?.model || entry.node.data?.type || entry.node.data?.strategy }}</text>
          <g v-if="entry.group" role="button" tabindex="0" :aria-label="`Unfold ${entry.node.data?.name}`"
            @pointerdown.stop @click.stop="emit('fold',entry.node.id)" @keydown.enter.stop="emit('fold',entry.node.id)">
            <rect class="fold" :x="entry.width-28" y="4" width="23" height="22" rx="4" /><text class="node-label" :x="entry.width-21" y="20">+</text>
          </g>
          <rect class="port" data-port="in" role="button" tabindex="0" :aria-label="`Input on ${entry.node.data?.name || entry.node.id}`" @keydown.enter.stop="port(entry.node.id,'in')" :data-port-node="entry.node.id" :x="entry.node.type==='tool'||entry.node.type==='skill'?entry.width/2-5:-5" :y="entry.node.type==='tool'||entry.node.type==='skill'?-5:entry.group?17:39" width="10" height="10" @pointerdown.stop @click.stop="port(entry.node.id,'in')" />
          <rect v-if="entry.node.type==='agent'" class="port tool-port" data-port="tool" role="button" tabindex="0" :aria-label="`Tool output on ${entry.node.data?.name || entry.node.id}`" @keydown.enter.stop="port(entry.node.id,'out')" :x="entry.width/2-5" :y="entry.height-5" width="10" height="10" @pointerdown.stop="startWire($event,entry.node.id)" @click.stop="port(entry.node.id,'out')" />
          <rect v-if="entry.node.type!=='tool'&&entry.node.type!=='skill'" class="port" data-port="out" role="button" tabindex="0" :aria-label="`Output on ${entry.node.data?.name || entry.node.id}`" @keydown.enter.stop="port(entry.node.id,'out')" :x="entry.width-5" :y="entry.group?17:39" width="10" height="10" @pointerdown.stop="startWire($event,entry.node.id)" @click.stop="port(entry.node.id,'out')" />
        </g>
        <g aria-hidden="true" class="wire-terminals">
          <template v-for="wire in wires.routed" :key="wire.edge.id">
            <circle :cx="wire.start.x" :cy="wire.start.y" r="2.2" :fill="colors[wire.kind]" />
            <circle :cx="wire.end.x" :cy="wire.end.y" r="2.2" :fill="colors[wire.kind]" />
          </template>
        </g>
        <path v-if="pending && draft && ports.get(pending)" class="wire" stroke="var(--accent-skill)" stroke-dasharray="4 4"
          :d="path([output(ports.get(pending)!),pt(draft.x,output(ports.get(pending)!).y),draft])" />
      </g>
    </svg>
  </div>
</template>

<style scoped>
.svg-workbench { position: absolute; inset: 0; }
.svg-controls { position: absolute; z-index: 1; bottom: 12px; left: 12px; display: flex; gap: 6px; }
.svg-controls button { background: var(--surface-3); color: var(--text-primary); border: 1px solid var(--border-default); border-radius: 5px; padding: 5px 8px; cursor: pointer; }
.svg-controls button[aria-pressed="true"] { border-color: var(--accent-agent); }
.builder-svg-canvas { width: 100%; height: 100%; display: block; color: var(--text-secondary); touch-action: none; user-select: none; cursor: grab; }
.group-frame { fill: var(--surface-2); fill-opacity: .72; stroke: var(--text-muted); stroke-dasharray: 5 4; }
.group-header,.fold { fill: var(--surface-4); cursor: pointer; }
.fold { stroke: var(--text-muted); }
.node { cursor: move; --node-accent: var(--accent-agent); }
.node[data-type="tool"] { --node-accent: var(--accent-tool); }
.node[data-type="skill"] { --node-accent: var(--accent-skill); }
.node[data-type="orchestrator"] { --node-accent: var(--accent-orch); }
.node-body { fill: var(--surface-3); stroke: var(--node-accent); }
.node.selected .node-body,.node:focus .node-body { stroke-width: 3; }
.node-kind { fill: var(--text-secondary); font: 700 9px system-ui,sans-serif; letter-spacing: 1px; }
.node-label { fill: var(--text-primary); font: 600 14px system-ui,sans-serif; pointer-events: none; }
.node-detail { fill: var(--text-secondary); font: 10px var(--font-mono); pointer-events: none; }
.port { opacity: 0; transition: opacity .12s; fill: var(--node-accent); stroke: var(--surface-0); stroke-width: 2; cursor: crosshair; }
.node:hover .port:not([data-port="in"]), [data-group]:hover .port:not([data-port="in"]), .port:focus-visible, .wiring .port[data-port="in"] { opacity: 1; }
.port:hover, .port:focus-visible { stroke: white; outline: none; }
.tool-port { fill: var(--accent-tool); }
.wire { fill: none; stroke-width: 2; stroke-linejoin: round; pointer-events: none; }
.wire-terminals { opacity: 0; pointer-events: none; }
.wiring .wire-terminals { opacity: 1; }
.node.active .node-body,.group-frame.active { stroke: var(--status-running); stroke-width: 2.5; }
.group-frame.drop-target { fill: var(--accent-agent); fill-opacity: .16; stroke: var(--accent-agent); stroke-width: 3; stroke-dasharray: none; }
.active-wire { stroke: var(--status-running); stroke-width: 3; stroke-dasharray: 7 6; animation: svg-flow .65s linear infinite; }
@keyframes svg-flow { to { stroke-dashoffset: -26; } }
@media(prefers-reduced-motion:reduce) { .active-wire { animation: none; } }
.wire-gap { fill: none; stroke: var(--canvas-bg); stroke-width: 5; opacity: .85; pointer-events: none; }
</style>
