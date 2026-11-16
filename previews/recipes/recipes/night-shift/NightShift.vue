<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { jobs, newWorkshop, talk, type Job, type Role } from './nightShift';

const workshop = ref(newWorkshop());
const position = ref({ x: 5, y: 6 });
const requested = ref<Job>('clinic');
const moves = ref(0);
const work = ref(0);
const busy = ref(false);
const hasOrder = ref(false);
const speaker = ref('Workshop intercom');
const message = ref('The town is dark. Find Mara at the dispatch desk to start your shift.');
const sites = [
  { id: 'dispatch', x: 2, y: 2, name: 'Mara', label: 'Dispatcher', color: '#db9860', face: '#cf8968', hair: '#6a382d' },
  { id: 'supply', x: 8, y: 2, name: 'Ivo', label: 'Quartermaster', color: '#7ca99c', face: '#9d6e52', hair: '#282235' },
  { id: 'clinic', x: 2, y: 5, name: 'Clinic radio', label: '◈', color: '#cfd278' },
  { id: 'shelter', x: 8, y: 5, name: 'Shelter heater', label: '▤', color: '#dc8c65' },
] as const;
const walls = new Set(['4,2', '5,2', '6,2']);
const nearby = computed(() => sites.find(site => Math.abs(site.x - position.value.x) + Math.abs(site.y - position.value.y) === 1));
const won = computed(() => workshop.value.progress.clinic === 3 && workshop.value.progress.shelter === 3);

function move(dx: number, dy: number) {
  const x = position.value.x + dx;
  const y = position.value.y + dy;
  if (busy.value || x < 1 || x > 9 || y < 1 || y > 6 ||
      walls.has(`${x},${y}`) || sites.some(site => site.x === x && site.y === y)) return;
  position.value = { x, y };
  moves.value++;
}

async function interact() {
  const site = nearby.value;
  if (!site || busy.value) return;
  speaker.value = site.name;
  if (site.id === 'dispatch' || site.id === 'supply') {
    if (site.id === 'supply' && !hasOrder.value) {
      message.value = 'Get a work order from Mara first.';
      return;
    }
    const role: Role = site.id === 'dispatch' ? 'Dispatcher' : 'Quartermaster';
    busy.value = true;
    message.value = 'Thinking…';
    try {
      const result = await talk('', role, workshop.value, requested.value);
      workshop.value = result.state;
      hasOrder.value = true;
      message.value = result.consequence;
    } catch (error) {
      message.value = error instanceof Error ? error.message : 'Could not reach the workshop service.';
    } finally {
      busy.value = false;
    }
    return;
  }
  if (!hasOrder.value) {
    message.value = 'Check in with Mara before working on the equipment.';
    return;
  }
  const state = workshop.value;
  if (state.progress[site.id] === 3) {
    message.value = 'Running again. This job is complete.';
    return;
  }
  work.value++;
  if (state.battery === site.id) {
    state.progress[site.id] = 3;
    state.battery = 'spent';
    message.value = 'Spare battery installed. The equipment is back online.';
  } else {
    state.progress[site.id]++;
    message.value = state.progress[site.id] === 3
      ? 'Manual repair complete. The equipment is back online.'
      : `Manual repair step ${state.progress[site.id]} of 3.`;
  }
}

function key(event: KeyboardEvent) {
  if (event.target instanceof HTMLElement && event.target.closest('select,input,textarea')) return;
  const directions: Record<string, [number, number]> = {
    arrowup: [0,-1], arrowdown: [0,1], arrowleft: [-1,0], arrowright: [1,0],
    w: [0,-1], s: [0,1], a: [-1,0], d: [1,0],
  };
  const pressed = event.key.toLowerCase();
  const direction = directions[pressed];
  if (direction) { event.preventDefault(); move(...direction); }
  if (pressed === 'e' && !event.repeat) { event.preventDefault(); void interact(); }
}
onMounted(() => window.addEventListener('keydown', key));
onBeforeUnmount(() => window.removeEventListener('keydown', key));

function reset() {
  workshop.value = newWorkshop();
  position.value = { x: 5, y: 6 };
  requested.value = 'clinic';
  moves.value = work.value = 0;
  hasOrder.value = false;
  speaker.value = 'Workshop intercom';
  message.value = 'Find Mara at the dispatch desk to start your shift.';
}
</script>

<template>
  <main>
    <header><div><p>Blackout workshop</p><h1>Night Shift</h1></div><button :disabled="busy" @click="reset">Restart</button></header>
    <p class="intro">Get the clinic radio and shelter heater running. One spare battery. Two repairs.</p>
    <div class="layout">
      <section class="room" aria-label="Workshop map">
        <div class="backwall"><span>DISPATCH</span><span>WORKSHOP 07</span><span>STORES</span></div>
        <div v-for="x in [4,5,6]" :key="x" class="crate" :style="{gridColumn:x+1,gridRow:3}"></div>
        <div v-for="site in sites" :key="site.id" class="site" :class="{near: nearby?.id === site.id}"
          :style="{gridColumn:site.x+1,gridRow:site.y+1,color:site.color}">
          <svg v-if="'face' in site" viewBox="0 0 16 17" aria-hidden="true" shape-rendering="crispEdges">
            <path :fill="site.hair" d="M4 1h8v2h1v6H3V3h1z"/>
            <path :fill="site.face" d="M5 4h6v5H5z"/>
            <path fill="#1c1824" d="M6 5h1v1H6zm3 0h1v1H9z"/>
            <path :fill="site.color" d="M4 9h8v1h2v4h-3v1H5v-1H2v-4h2z"/>
            <path fill="#26253b" d="M5 14h3v3H5zm4 0h3v3H9z"/>
          </svg>
          <span v-else class="machine">{{ site.label }}</span>
          <span class="name">{{ site.name }}</span>
        </div>
        <div class="player" :style="{gridColumn:position.x+1,gridRow:position.y+1}" :data-steps="moves" aria-label="You, the mechanic">
          <svg :class="{stride: moves % 2}" viewBox="0 0 16 17" aria-hidden="true" shape-rendering="crispEdges">
            <path fill="#d3ad62" d="M4 1h7v2h3v2H3V3h1z"/>
            <path fill="#e3b68b" d="M5 5h6v4H5z"/>
            <path fill="#1c1824" d="M6 5h1v1H6zm3 0h1v1H9z"/>
            <path fill="#8497cc" d="M4 9h8v1h2v4h-3v1H5v-1H2v-4h2z"/>
            <path fill="#201e30" d="M5 14h3v3H5zm4 0h3v3H9z"/>
          </svg>
          <span class="name">You</span>
        </div>
      </section>
      <aside>
        <h2>Work orders</h2>
        <p v-for="(label, job) in jobs" :key="job">{{ label }} <strong>{{ workshop.progress[job] === 3 ? 'Restored' : workshop.progress[job] + '/3' }}</strong></p>
        <p>Battery <strong>{{ workshop.battery === 'stock' ? 'In stock' : workshop.battery === 'spent' ? 'Installed' : 'For ' + jobs[workshop.battery] }}</strong></p>
        <p>Repair actions <strong>{{ work }}</strong></p>
        <label>Request priority <select v-model="requested" :disabled="busy"><option v-for="(label, job) in jobs" :key="job" :value="job">{{ label }}</option></select></label>
        <div class="keys" aria-label="Movement">
          <button aria-label="Move up" @click="move(0,-1)">↑</button>
          <button aria-label="Move left" @click="move(-1,0)">←</button>
          <button aria-label="Move down" @click="move(0,1)">↓</button>
          <button aria-label="Move right" @click="move(1,0)">→</button>
        </div>
        <button class="interact" :disabled="!nearby || busy" @click="interact">{{ nearby ? 'Interact · ' + nearby.name : 'Walk up to a character or machine' }}</button>
        <small>Arrow keys or WASD to walk. E to interact.</small>
      </aside>
    </div>
    <section class="dialogue" role="status">
      <h2>{{ won ? 'Shift complete' : speaker }}</h2>
      <p>{{ won ? 'Both repairs are back in service.' : message }}</p>
    </section>
  </main>
</template>

<style scoped>
main{max-width:1060px;margin:auto;padding:28px;color:#eedfc1}
header,.layout,aside p{display:flex;justify-content:space-between;gap:16px}
header{align-items:center}
h1,h2,p{margin:0}
header p,small,.intro{color:#c1ab8c}
h1{font:700 clamp(36px,7vw,70px)/1.1 Georgia,serif}
.intro{margin:12px 0 22px}
.room{min-width:0;flex:1;isolation:isolate;display:grid;grid-template:repeat(8,1fr)/repeat(11,1fr);aspect-ratio:11/8;position:relative;border:4px solid #765747;background:repeating-conic-gradient(#393444 0% 25%,#342f3f 0% 50%) 0/18.18% 25%;box-shadow:inset 0 0 28px #16131c}
.backwall{grid-area:1/1/2/-1;display:flex;justify-content:space-around;align-items:center;background:#211f30;border-bottom:4px solid #5e4751;color:#bb9a76;font:700 clamp(7px,1.3vw,11px) monospace}
.crate{margin:7%;background:repeating-linear-gradient(0deg,#60474a 0 26%,#3d303c 26% 33%);border:3px solid #7b5b55;box-shadow:2px 4px #211d2b}
.site,.player{position:relative;display:grid;place-items:center;align-self:stretch;justify-self:stretch}
svg{width:72%;max-height:100%;filter:drop-shadow(2px 4px #211a29)}
svg.stride{transform:translateY(-2px)}
.name{position:absolute;top:95%;white-space:nowrap;background:#211c2c;padding:1px 4px;font:700 clamp(7px,1.4vw,12px) monospace;z-index:2}
.near .name{outline:1px solid #f0c77b}
.machine{font-size:clamp(15px,4vw,42px);background:#211e30;border:2px solid currentColor;width:80%;text-align:center}
aside{width:min(275px,100%);background:#262233;padding:14px;box-sizing:border-box}
aside p{margin:10px 0;font-size:13px}
aside strong{color:#efc37c;text-align:right}
label,small{display:block;font-size:12px}
select,button{background:#33293d;color:#f4e2c7;border:1px solid #795b64;padding:8px;font:inherit}
select{width:100%;margin-top:4px}
button{cursor:pointer}
button:disabled{opacity:.62;cursor:default}
.keys{display:flex;gap:4px;margin:12px 0}
.keys button{flex:1}
.interact{width:100%;margin-bottom:8px}
.dialogue{margin-top:14px;background:#292435;border-left:4px solid #d9a768;padding:16px;min-height:63px}
h2{font-size:17px;margin-bottom:5px;color:#efc37c}
@media(max-width:720px){main{padding:12px}.layout{display:block}aside{width:100%;margin-top:12px}}
</style>
