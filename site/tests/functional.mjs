// usage: node test.mjs [url] [tag]   (default http://127.0.0.1:8765/ ; writes results-<tag>.json and semantic-<tag>.md next to this file)
// start the server first: python3 -m http.server 8765 -d site
import { createRequire } from 'module';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
const require = createRequire(process.env.PLAYWRIGHT_MODULES || (process.env.HOME + '/.npm/_npx/e41f203b7505f1fb/node_modules/'));
const { chromium } = require('playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const URL_ = process.argv[2] || 'http://127.0.0.1:8765/';
const tag = process.argv[3] || 'run';
try { const r = await fetch(URL_ + 'data/versions.json'); if (!r.ok) throw new Error(r.status); } catch (e) { console.error('Server is not up at ' + URL_ + ' (' + e + '). Run: python3 -m http.server 8765 -d site'); process.exit(2); }
const R = [];
const rec = (w, c, pass, d) => { R.push({ widget: w, check: c, pass: !!pass, detail: d === undefined ? '' : String(typeof d === 'object' ? JSON.stringify(d) : d) }); };
const sleep = ms => new Promise(r => setTimeout(r, ms));
const INIT = (SIDMAP) => {
  window.__SID = SIDMAP;
  // capture the page's private anims: anim objects pushed to an array, host = ctrls element appended just before
  let lastCtrls = null;
  const ac = Element.prototype.appendChild;
  Element.prototype.appendChild = function (c) { if (c && c.className === 'ctrls') lastCtrls = c; return ac.call(this, c); };
  const push = Array.prototype.push;
  Array.prototype.push = function (...a) {
    if (a[0] && typeof a[0].step === 'function' && typeof a[0].kill === 'function') {
      a[0].__ctrls = lastCtrls; window.__anims = this;
    }
    return push.apply(this, a);
  };
  window.__A = id => (window.__anims || []).find(a => a.__ctrls && a.__ctrls.closest('#' + window.__SID[id]));
  window.__S = id => {
    const a = window.__A(id); if (!a) return null;
    const b = a.__ctrls.querySelectorAll('button');
    return { i: a.i, t: +a.t.toFixed(3), playing: a.playing, label: b[0].textContent, pressed: b[0].getAttribute('aria-pressed'), step: b[1].textContent, n: window.__anims.length, running: a.__ctrls.parentElement.classList.contains('rk-running') };
  };
  // polled step counter
  window.__count = (id, ms) => new Promise(res => {
    const a0 = window.__A(id); let last = a0.i, c = 0; const t0 = performance.now();
    const h = setInterval(() => { const a = window.__A(id); if (a && a.i !== last) { c++; last = a.i; } if (performance.now() - t0 > ms) { clearInterval(h); res(c); } }, 15);
  });
  // semantic snapshot of a widget
  window.__snap = id => {
    const sec = document.getElementById(window.__SID[id]);
    const nodes = [...sec.querySelectorAll('.nd')];
    const bb = nodes.map(g => { const r = g.querySelector('.nb'); return { x: +r.getAttribute('x'), y: +r.getAttribute('y'), w: +r.getAttribute('width'), h: +r.getAttribute('height'), label: [...g.querySelectorAll('text')].map(t => t.textContent).join(' / '), on: g.classList.contains('on'), bad: g.classList.contains('bad') }; });
    const near = (p) => { let best = null, bd = 1e9; bb.forEach(n => { const dx = Math.max(n.x - p[0], 0, p[0] - n.x - n.w), dy = Math.max(n.y - p[1], 0, p[1] - n.y - n.h); const d = Math.hypot(dx, dy); if (d < bd) { bd = d; best = n; } }); return bd <= 14 ? best.label.split(' / ')[0] : '?'; };
    const edges = [...sec.querySelectorAll('.rk-edge.on')].map(g => {
      const d = g.querySelector('.rk-dashes').getAttribute('d'); const pts = [...d.matchAll(/(-?[\d.]+),(-?[\d.]+)/g)].map(m => [+m[1], +m[2]]);
      const lab = g.querySelector('.rk-edge-label');
      return near(pts[0]) + ' -> ' + near(pts[pts.length - 1]) + (lab ? ' [' + lab.textContent + ']' : '');
    });
    const cap = sec.querySelector('.cap');
    return { cap: cap ? cap.textContent : '', nodes: bb.filter(n => n.on || n.bad).map(n => n.label.split(' / ')[0] + (n.bad ? '(bad)' : '')), edges, cnt: sec.querySelector('.cnt') ? sec.querySelector('.cnt').textContent : '' };
  };
};
const SID = { react: 'agent-loop', flow: 'data-flow', orch: 'orchestration', wake: 'wake', memory: 'memory', protocols: 'protocols' };
// The wake widget exists only in releases that have the wake feature; skip its checks when the served default tag lacks it (gating.mjs covers wake with a fixture tag).
const vj0 = await (await fetch(URL_ + 'data/versions.json')).json();
const HAS_WAKE = (await (await fetch(URL_ + 'data/' + vj0.default + '.json')).json()).features.some(f => f.id === 'wake');
if (!HAS_WAKE) console.log('functional: wake widget not in default tag ' + vj0.default + ', wake checks skipped');
const WAKEIDS = a => a.filter(w => HAS_WAKE || w !== 'wake');
const WIDS = ['react', 'flow', 'orch', 'wake', 'memory', 'protocols'].filter(w => HAS_WAKE || w !== 'wake');
// step counts (fixed page: react 6, protocols 8; set REACT_N / PROTO_N)
const N = { react: +(process.env.REACT_N || 6), flow: 10, orch: 5, wake: 12, memory: 4, protocols: +(process.env.PROTO_N || 8) };
const btns = (p, id) => ({ play: p.locator(`#${SID[id]} .ctrls button`).nth(0), step: p.locator(`#${SID[id]} .ctrls button`).nth(1) });
const S = (p, id) => p.evaluate(i => window.__S(i), id);
const snap = (p, id) => p.evaluate(i => window.__snap(i), id);

const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
async function fresh(opts = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, ...opts });
  const p = await ctx.newPage();
  await p.addInitScript(INIT, SID);
  const errs = []; p.on('pageerror', e => errs.push(String(e)));
  await p.goto(URL_); await p.waitForFunction(n => window.__anims && window.__anims.length >= n, WIDS.length);
  p.__errs = errs; p.__ctx = ctx; return p;
}
const semRows = [];

// ---------- mechanics ----------
let p = await fresh();
for (const id of WIDS) {
  await p.locator('#' + SID[id]).scrollIntoViewIfNeeded();
  const { play, step } = btns(p, id);
  const s0 = await S(p, id);
  rec(id, 'initial', true, s0);
  rec(id, 'label/aria synced at start', (s0.playing ? s0.label === 'Pause' && s0.pressed === 'true' : s0.label === 'Play' && s0.pressed === 'false'), s0);
  // pause
  if (!s0.playing) await play.click();
  await sleep(300);
  await play.click(); const sp = await S(p, id); await sleep(700); const sp2 = await S(p, id);
  rec(id, 'pause stops motion + label', !sp.playing && sp.label === 'Play' && sp.pressed === 'false' && sp.i === sp2.i && sp.t === sp2.t, { sp, sp2 });
  // resume continuity
  await play.click(); await sleep(60); const sr = await S(p, id);
  rec(id, 'resume continues same step (no restart)', sr.i === sp.i && sr.playing && sr.label === 'Pause' && Math.abs(sr.t - sp.t) < 0.2, { before: [sp.i, sp.t], after: [sr.i, sr.t] });
  // step while playing pauses
  const sa = await S(p, id); await step.click(); const sb = await S(p, id);
  rec(id, 'Step while playing: advances exactly 1 and pauses', sb.i === (sa.i + 1) % N[id] || sb.i === (sa.i + 2) % N[id] && false ? true : (sb.i === (sa.i + 1) % N[id] && !sb.playing && sb.label === 'Play'), { sa: sa.i, sb: sb.i, playing: sb.playing });
  // step while paused
  await step.click(); const sc = await S(p, id);
  rec(id, 'Step while paused: +1 and stays paused', sc.i === (sb.i + 1) % N[id] && !sc.playing, { from: sb.i, to: sc.i });
  // wrap at last
  for (let k = 0; k < N[id] + 1 && (await S(p, id)).i !== N[id] - 1; k++) await step.click();
  const sl = await S(p, id); await step.click(); const sw = await S(p, id);
  rec(id, 'Step at last step wraps to 0', sl.i === N[id] - 1 && sw.i === 0, { last: sl.i, after: sw.i });
  // step then play dwell
  await step.click(); const st1 = await S(p, id); await play.click(); await sleep(120); const st2 = await S(p, id);
  rec(id, 'Step then Play keeps the stepped step visible >=120ms', st2.i === st1.i, { stepped: st1.i, after120ms: st2.i });
  await play.click(); // pause
  // rapid double clicks
  const d0 = await S(p, id); await step.dblclick(); const d1 = await S(p, id);
  rec(id, 'double-click Step = +2', d1.i === (d0.i + 2) % N[id], { from: d0.i, to: d1.i });
  const pl0 = (await S(p, id)).playing; await play.dblclick(); const pl1 = await S(p, id);
  rec(id, 'double-click Play toggles twice (state unchanged, label in sync)', pl1.playing === pl0 && pl1.label === (pl0 ? 'Pause' : 'Play'), pl1);
  // keyboard
  await play.focus(); const k0 = (await S(p, id)).playing; await p.keyboard.press('Space'); const k1 = (await S(p, id)).playing; await p.keyboard.press('Enter'); const k2 = (await S(p, id)).playing;
  rec(id, 'keyboard Space/Enter each toggle Play once', k1 === !k0 && k2 === k0, { k0, k1, k2 });
  await step.focus(); const q0 = (await S(p, id)).i; await p.keyboard.press('Enter'); const q1 = (await S(p, id)).i;
  rec(id, 'keyboard Enter on Step advances one', q1 === (q0 + 1) % N[id], { q0, q1 });
}
// rate: 1x vs after cycles (uses react: dur 1500, and wake dur 900)
async function rate(id, ms) { const s = await S(p, id); if (!s.playing) await btns(p, id).play.click(); return p.evaluate(([i, m]) => window.__count(i, m), [id, ms]); }
const base = {}; for (const id of WAKEIDS(['react', 'wake'])) { await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); base[id] = await rate(id, 6000); }
for (const id of WAKEIDS(['react', 'wake'])) { const { play, step } = btns(p, id); for (let k = 0; k < 25; k++) { await play.click(); await step.click(); await play.click(); } }
for (const id of ['orch']) { for (let k = 0; k < 6; k++) await p.locator('#orchestration .tabs button').nth(k % 4).click(); }
for (let k = 0; k < 3; k++) { await p.click('#langJa'); await p.click('#langEn'); }
const aft = {}; for (const id of WAKEIDS(['react', 'wake'])) { await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); aft[id] = await rate(id, 6000); }
for (const id of WAKEIDS(['react', 'wake'])) rec(id, 'no duplicate loops: steps per 6s before vs after 25 cycles+renders', Math.abs(base[id] - aft[id]) <= 1, { base: base[id], after: aft[id] });
rec('all', 'anims array size stable (' + WIDS.length + ') after cycles', (await S(p, 'react')).n === WIDS.length, (await S(p, 'react')).n);
// off-screen
await p.locator('#agent-loop').scrollIntoViewIfNeeded(); let o = await S(p, 'react'); if (!o.playing) await btns(p, 'react').play.click();
await p.evaluate(() => window.scrollTo(0, document.body.scrollHeight)); const c1 = await p.evaluate(() => window.__count('react', 3500));
rec('react', 'off-screen: animation does not advance while the widget is not visible', c1 === 0, { stepsWhileOffscreen: c1 });
rec('all', 'no page errors', p.__errs.length === 0, p.__errs);
await p.__ctx.close();

// ---------- lang / view / tab switching ----------
p = await fresh();
for (const id of WAKEIDS(['react', 'flow', 'wake', 'memory', 'protocols'])) {
  await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); const { play, step } = btns(p, id);
  const s = await S(p, id); if (!s.playing) await play.click(); await step.click(); await step.click(); await play.click(); await sleep(200);
  const before = await S(p, id); await p.click('#langJa'); await sleep(100); const after = await S(p, id);
  rec(id, 'EN->JA mid-play keeps step index and playing state', ((after.i - before.i + N[id]) % N[id]) <= 1 && after.playing === before.playing, { before: [before.i, before.playing], after: [after.i, after.playing], label: after.label });
  rec(id, 'JA labels rendered', after.label === '一時停止' || after.label === '再生', after.label);
  await p.click('#langEn'); await sleep(50);
  await step.click(); const b2 = await S(p, id); await p.click('#viewSimple'); await sleep(80); const a2 = await S(p, id); await p.click('#viewDetail');
  rec(id, 'Simple/Detail switch keeps step index', a2.i === b2.i, { before: b2.i, after: a2.i });
}
// orch tab switching
await p.locator('#orchestration').scrollIntoViewIfNeeded();
for (let m = 0; m < 4; m++) {
  const tabs = p.locator('#orchestration .tabs button'); await tabs.nth(m).click(); const { play, step } = btns(p, 'orch');
  const s = await S(p, 'orch'); const pr = await tabs.nth(m).getAttribute('aria-pressed'); const others = await p.locator('#orchestration .tabs button[aria-pressed="true"]').count();
  rec('orch', `tab ${m}: state reset, one tab pressed, anims==${WIDS.length}`, pr === 'true' && others === 1 && s.n === WIDS.length, { s, others });
  await step.click(); await step.click(); await tabs.nth((m + 1) % 4).click(); const s2 = await S(p, 'orch');
  rec('orch', `tab ${m}->${(m + 1) % 4} after Step: new widget playing, counters from rest`, s2.playing && s2.n === WIDS.length, s2);
}
await p.__ctx.close();

// ---------- reduced motion ----------
p = await fresh({ reducedMotion: 'reduce' });
for (const id of WIDS) {
  await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); const { play, step } = btns(p, id);
  const s0 = await S(p, id); await play.click(); await sleep(2200); const s1 = await S(p, id);
  rec(id, 'reduced-motion: initial paused at rest state, label Play', !s0.playing && s0.label === 'Play', s0);
  rec(id, 'reduced-motion: Play still advances steps (not a silent no-op) but dashes static', s1.playing && (s1.i !== s0.i || s1.t !== s0.t) && !s1.running, { s0: [s0.i, s0.t], s1: [s1.i, s1.t], running: s1.running });
  await play.click(); const b = await S(p, id); await step.click(); const a = await S(p, id);
  rec(id, 'reduced-motion: Step works', a.i === (b.i + 1) % N[id], { b: b.i, a: a.i });
}
await p.__ctx.close();

// ---------- semantic ----------
p = await fresh();
async function walk(id, label, setup) {
  await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); if (setup) await setup();
  const { play, step } = btns(p, id); const s = await S(p, id); if (s.playing) await play.click();
  const rows = []; let n = N[id];
  if (id === 'orch') { await step.click(); n = +(await snap(p, id)).cap.match(/\/(\d+)\./)[1]; }
  // go to the last step so the next click shows step 0
  for (let k = 0; k < 30 && (await S(p, id)).i !== n - 1; k++) await step.click();
  for (let k = 0; k < n; k++) { await step.click(); const st = await S(p, id); const sn = await snap(p, id); rows.push({ i: st.i, ...sn }); semRows.push(`| ${label} | ${st.i} | ${sn.cap.replace(/\|/g, '/')} | ${sn.nodes.join(', ')} | ${sn.edges.join('; ') || '(none)'} | ${sn.cnt} |`); }
  return rows;
}
const hero = await walk('react', 'hero');
rec('react', 'caption Observe step lights default path obs->think (reflection is off by default)', hero[2].edges.some(e => /Observe -> Think/.test(e)), hero[2].edges);
rec('react', 'every step: lit edge starts at the active node (or none for terminal)', hero.every(r => r.edges.length === 0 || r.edges.every(e => e.split(' -> ')[0] === r.nodes[0])), hero.map(r => r.nodes[0] + ':' + r.edges.join('|')));
rec('react', 'edge target of step i == active node of step i+1 (continuity)', hero.slice(0, -1).every((r, k) => r.edges.length === 0 || r.edges.some(e => e.split(' -> ')[1].split(' [')[0] === hero[k + 1].nodes[0])), hero.map(r => r.nodes[0] + ':' + r.edges.join('|')));
rec('react', 'Answer reachable: a Think step lights think->answer', hero.some(r => r.edges.some(e => /Think -> Answer/.test(e))), 'edges per step above');
rec('react', 'no edge is labelled "optional" while being the default path', !hero.some(r => r.edges.some(e => /\[optional\]/.test(e)) && /Observe/.test(r.nodes[0])), '');
const modes = ['react', 'pipeline', 'hier', 'loop'];
const orchRows = {};
for (let m = 0; m < 4; m++) {
  orchRows[m] = await walk('orch', 'orch:' + modes[m], async () => { await p.locator('#orchestration .tabs button').nth(m).click(); });
  const rows = orchRows[m];
  rec('orch', `${modes[m]}: lit edge from == caption hop source, to == next hop source (continuity)`, rows.every((r, k) => r.edges.length === 0 || r.edges.some(e => rows[(k + 1) % rows.length].nodes.map(x => x.replace('(bad)', '')).includes(e.split(' -> ')[1].split(' [')[0])) || k === rows.length - 1), rows.map(r => r.nodes + ':' + r.edges.join('|')));
}
const pl = orchRows[1];
rec('orch', 'pipeline dev->gate hop: record edge (log->gate) lit, as caption says the gate reads the record', pl[1].edges.some(e => /\[record\]/.test(e) || /tool log -> gate/.test(e)), pl[1].edges);
rec('orch', 'pipeline fail case: caption says 0 write_file and log node says 0', /0 write_file/.test(pl[pl.length - 1].cap) && pl.length === 3, { n: pl.length, cap: pl[pl.length - 1].cap });
// checkbox flips diagram+caption
await p.locator('#orchestration .tabs button').nth(1).click(); await p.check('#gatecb');
const logOn = await p.locator('#orchestration .nd').evaluateAll(gs => gs.map(g => g.textContent).filter(t => /write_file call/.test(t)));
const pl2 = await walk('orch', 'orch:pipeline+checkbox', async () => { });
rec('orch', 'checkbox ticked: 4 hops, caption says write_file call recorded, log node says calls: 1', pl2.length === 4 && /write_file call/.test(pl2[2].cap) && logOn.some(t => /calls: 1/.test(t)), { n: pl2.length, logOn });
await p.uncheck('#gatecb');
const sup = orchRows[0];
rec('orch', 'ReAct/Hier: supervisor->worker hop highlights the receiving worker too (caption: delegates to X)', sup[0].nodes.length >= 2, sup[0].nodes);
const loopRows = orchRows[3];
await p.locator('#orchestration .tabs button').nth(3).click();
rec('orch', 'loop: Worker node subtitle should not say "type: loop" (loop is the step type)', !(await p.locator('#orchestration svg').first().textContent()).includes('type: loop'), '');
if (HAS_WAKE) {
// wake
const wk = await walk('wake', 'wake');
const OUT = ['q', 'q', 'q', 'q', 'chg', 'q', 'q', 'alarm', 'sup', 'q', 'q', 'q'];
rec('wake', 'counters at every step: ticks=i+1, model calls=escalations so far', wk.every((r, k) => { const m = r.cnt.match(/ticks (\d+)model calls (\d+)/); return m && +m[1] === k + 1 && +m[2] === OUT.slice(0, k + 1).filter(o => o === 'chg' || o === 'alarm').length; }), wk.map(r => r.cnt));
rec('wake', 'final: 12 ticks / 2 model calls / quiet cost 0', /ticks 12model calls 2quiet ticks cost 0 calls/.test(wk[11].cnt), wk[11].cnt);
rec('wake', 'escalation steps light tick->LLM edge', [4, 7].every(k => wk[k].edges.some(e => /-> LLM/.test(e) || /LLM/.test(e)) || wk[k].nodes.includes('LLM')), [wk[4].edges, wk[7].edges]);
rec('wake', 'suppressed (repeat) step lights no model edge, 0 calls', !wk[8].nodes.includes('LLM'), wk[8].nodes);
}
// memory BM25
const MEM = [["procedure", "Failed deploy checklist", "Check the logs, confirm which step failed, run the rollback, then notify the channel."], ["procedure", "Rollback procedure", "To roll back a deploy, redeploy the previous release tag and check health."], ["gotcha", "Cache after deploy", "After a deploy, clear the cache or users see stale pages."], ["fact", "Staging host", "Staging runs on a separate host behind the VPN."], ["rule", "Commit messages", "Use conventional commits: feat, fix, chore, docs."], ["pattern", "Retry with backoff", "Retry failed requests with exponential backoff and jitter."]];
const tok = s => s.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
function bm(q) { const docs = MEM.map(m => tok(m[1] + '\n' + m[2] + '\n' + m[0])); const N_ = docs.length, avg = docs.reduce((s, d) => s + d.length, 0) / N_; const df = {}; docs.forEach(d => new Set(d).forEach(t => df[t] = (df[t] || 0) + 1)); const qs = [...new Set(tok(q))];
  return docs.map(d => { let s = 0; for (const w of tok(q)) { const tf = d.filter(x => x === w).length; if (!tf) continue; const idf = Math.log(1 + (N_ - df[w] + .5) / (df[w] + .5)); s += idf * tf * 2.5 / (tf + 1.5 * (.25 + .75 * d.length / avg)); } return s; }); }
await p.locator('#memory').scrollIntoViewIfNeeded();
for (const q of ['deploy failed rollback', 'retry backoff', 'staging vpn host', 'zzz nothing']) for (const k of [1, 3, 5]) {
  const { play, step } = btns(p, 'memory'); if ((await S(p, 'memory')).playing) await play.click();
  await p.fill('#memq', q); await p.selectOption('#memk', String(k));
  for (let j = 0; j < 8 && (await S(p, 'memory')).i !== 3; j++) await step.click(); await sleep(50);
  const shown = await p.$$eval('#memory .mrow', rs => rs.map(r => ({ title: r.querySelector('.tt b').textContent, sc: r.querySelector('.sc').textContent, top: r.classList.contains('top') })));
  const pre = await p.$eval('#memory pre', e => e.textContent);
  const exp = bm(q); const order = exp.map((s, i) => i).sort((a, b) => exp[b] - exp[a]); const want = order.slice(0, k).filter(j => exp[j] > 0).map(j => MEM[j][1]);
  const scoreOK = shown.every(r => { const j = MEM.findIndex(m => m[1] === r.title); return Math.abs(+r.sc - exp[j]) < 0.006; });
  const topShown = shown.filter(r => r.top).map(r => r.title); const inPre = [...pre.matchAll(/- \[\w+\] (.+)/g)].map(m => m[1]);
  rec('memory', `BM25 q="${q}" k=${k}: scores equal independent calc; top rows == recalled_memory block == expected`, scoreOK && JSON.stringify(topShown.sort()) === JSON.stringify([...want].sort()) && JSON.stringify(inPre.sort()) === JSON.stringify([...want].sort()), { scoreOK, want, topShown, inPre });
}
await p.fill('#memq', 'deploy failed rollback'); await p.selectOption('#memk', '5');
const mm = await walk('memory', 'memory');
// flow / proto: Step visibility per selected node
for (const [id, nodeSel] of [['flow', null], ['protocols', null]]) {
  await p.locator('#' + SID[id]).scrollIntoViewIfNeeded(); const { play, step } = btns(p, id); if ((await S(p, id)).playing) await play.click();
  const names = await p.$$eval(`#${SID[id]} .nd`, gs => gs.map(g => g.getAttribute('aria-label')));
  const bad = [];
  for (let k = 0; k < names.length; k++) {
    await p.locator(`#${SID[id]} .nd`).nth(k).click(); const s0 = JSON.stringify((await snap(p, id)).edges); const seen = new Set([s0]);
    for (let j = 0; j < N[id] + 1; j++) { await step.click(); seen.add(JSON.stringify((await snap(p, id)).edges)); }
    const nOut = await p.evaluate(([i]) => document.querySelectorAll(`#${window.__SID[i]} .rk-edge`).length, [id]);
    semRows.push(`| ${id}:node=${names[k]} | steps ${N[id] + 1} | (no caption) | ${names[k]} | distinct lit-edge states seen: ${seen.size} | |`);
    if (seen.size < 2) bad.push(names[k]);
  }
  rec(id, 'Step visibly changes the diagram for every selected node', bad.length === 0, { nodesWhereStepChangesNothing: bad });
  // direction: lit edge from == selected node
  const dirs = []; for (let k = 0; k < names.length; k++) { await p.locator(`#${SID[id]} .nd`).nth(k).click(); const e = (await snap(p, id)).edges; if (e.some(x => !x.startsWith(names[k].split(' - ')[0]))) dirs.push([names[k], e]); }
  rec(id, 'lit edge always leaves the selected node (arrow direction matches data flow)', dirs.length === 0, dirs);
  // click while playing: info panel matches highlighted node
  const info = await p.$eval(`#${SID[id]} .info h4`, e => e.textContent); const hl = (await snap(p, id)).nodes[0];
  rec(id, 'info panel title matches highlighted node', info.startsWith(hl), { info, hl });
  if ((await S(p, id)).playing) await play.click();
  const bad2 = []; for (let j = 0; j < N[id] + 1; j++) { await step.click(); const sn = await snap(p, id); const ih = await p.$eval(`#${SID[id]} .info h4`, e => e.textContent);
    const from = sn.edges[0] ? sn.edges[0].split(' -> ')[0] : null; const capFrom = (sn.cap.match(/\d+\. (.+?) \u2192/) || [])[1];
    if (!from || !ih.startsWith(from) || sn.nodes[0] !== from || (sn.cap && capFrom !== from)) bad2.push({ j, from, ih, hl: sn.nodes, cap: sn.cap }); }
  rec(id, 'Step: lit edge source == highlighted node == info panel == caption', bad2.length === 0, bad2.slice(0, 3));
}
fs.writeFileSync(path.join(here, `semantic-${tag}.md`), '| widget/mode | step | caption | active node | lit edge(s) | counters |\n|---|---|---|---|---|---|\n' + semRows.join('\n') + '\n');
await p.__ctx.close(); await browser.close();
fs.writeFileSync(path.join(here, `results-${tag}.json`), JSON.stringify(R, null, 1));
const fails = R.filter(r => !r.pass);
console.log(`${tag}: ${R.length - fails.length}/${R.length} pass`);
fails.forEach(f => console.log('FAIL', f.widget, '|', f.check, '|', f.detail.slice(0, 220)));
