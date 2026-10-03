// usage: node site/tests/gating.mjs   (serves site/ itself on an ephemeral port)
// Version gating: releases without the wake timer / monitors must not show them, and a dead deep link must explain itself.
import fs from 'fs';
import path from 'path';
import http from 'http';
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
const site = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(process.env.PLAYWRIGHT_MODULES || (process.env.HOME + '/.npm/_npx/e41f203b7505f1fb/node_modules/'));
const { chromium } = require('playwright');
let pass = 0, fail = 0;
const ok = (c, m, d) => { if (c) pass++; else { fail++; console.log('FAIL', m, d === undefined ? '' : JSON.stringify(d)); } };
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json' };
// Fixture tag: alpha.12 data plus wake + monitors features, served only by this test server (nothing on disk).
const FIX = 'v0.3.0-fixture.1';
const vj = JSON.parse(fs.readFileSync(path.join(site, 'data/versions.json'), 'utf8'));
const fixData = JSON.parse(fs.readFileSync(path.join(site, 'data/v0.3.0-alpha.12.json'), 'utf8'));
fixData.tag = FIX; fixData.version = FIX; { const tpl = fixData.features.find(f => f.id === 'memory'); for (const id of ['wake', 'monitors']) fixData.features.push({ ...JSON.parse(JSON.stringify(tpl)), id, name: { en: id, ja: id } }); }
let withFixture = false;
const srv = http.createServer((q, r) => {
  const u = decodeURIComponent(q.url.split('?')[0]);
  if (withFixture && u === '/data/versions.json') { r.writeHead(200, { 'content-type': 'application/json' }); return r.end(JSON.stringify({ ...vj, versions: [FIX, ...vj.versions] })); }
  if (withFixture && u === `/data/${FIX}.json`) { r.writeHead(200, { 'content-type': 'application/json' }); return r.end(JSON.stringify(fixData)); } const f = path.join(site, u.endsWith('/') ? u + 'index.html' : u);
  if (!f.startsWith(site) || !fs.existsSync(f) || fs.statSync(f).isDirectory()) { r.writeHead(404); return r.end(); }
  r.writeHead(200, { 'content-type': mime[path.extname(f)] || 'application/octet-stream' }); r.end(fs.readFileSync(f));
}).listen(0, '127.0.0.1');
await new Promise(r => srv.on('listening', r));
const base = `http://127.0.0.1:${srv.address().port}/`;
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
const WAKE_TAG = vj.versions.find(v => JSON.parse(fs.readFileSync(path.join(site, 'data/' + v + '.json'), 'utf8')).features.some(f => f.id === 'wake'));
const WITH = FIX, WITHOUT = ['v0.3.0-alpha.14', 'v0.3.0-alpha.13', 'v0.3.0-alpha.12'];
const GATED = ['wake', 'wake-summary', 'wake-tick-1', 'ref-wake', 'ref-monitors', 'card-long-running-monitor', 'rough-wake-monitors'];
async function open(ver, lang, hash) {
  const ctx = await browser.newContext(); const p = await ctx.newPage(); p.__errs = []; p.on('pageerror', e => p.__errs.push(String(e)));
  await p.addInitScript(([l, v]) => localStorage.setItem('rk-spec', JSON.stringify({ lang: l, view: 'detail', ver: v })), [lang, ver]);
  p.on('console', m => { if (m.type() === 'error') console.log('CONSOLE', m.text()); }); await p.goto(base + (hash || '')); await p.waitForFunction(() => window.__rkLoaded === true); await p.waitForTimeout(100); return p;
}
const snap = p => p.evaluate(() => ({
  ids: [...document.querySelectorAll('#lidxlist a')].map(a => a.getAttribute('href').replace(/^#(en|ja)\./, '')),
  dom: [...document.querySelectorAll('[id]')].map(e => e.id),
  text: document.querySelector('main').innerText.toLowerCase(),
  nav: [...document.querySelectorAll('#nav a')].map(a => a.getAttribute('href')),
  note: document.getElementById('gatenote') ? document.getElementById('gatenote').innerText : null,
}));

withFixture = true;
{ const p = await open(WITH, 'en'); const s = await snap(p);
  for (const id of GATED) ok(s.ids.includes(id), WITH + ' fixture indexes ' + id);
  ok(s.dom.includes('wake'), WITH + ' fixture has wake section'); ok(s.text.includes('wake timer'), WITH + ' fixture teaches wake', s.text.length);
  ok(s.note === null && p.__errs.length === 0, WITH + ' no gate note, no errors'); }
{ // dead link on a real tag, fixture available: offer targets the fixture tag and works
  const p = await open(WITHOUT[0], 'en', '#en.wake');
  ok(await p.evaluate(f => { const a = document.querySelector('#gatenote a'); return !!a && a.textContent.includes(f); }, FIX), 'offer names a tag that has wake');
  await p.locator('#gatenote a').click(); await p.waitForTimeout(500); const s = await snap(p);
  ok(s.dom.includes('wake') && s.note === null && p.__errs.length === 0, 'offer link switches to a tag that has wake', p.__errs); }
withFixture = false;

for (const v of WITHOUT) for (const lang of ['en', 'ja']) {
  const p = await open(v, lang), s = await snap(p), t = `${v}/${lang}`;
  ok(p.__errs.length === 0, t + ' no page errors', p.__errs);
  for (const id of GATED) { ok(!s.ids.includes(id), t + ' index excludes ' + id); ok(!s.dom.includes(id), t + ' DOM has no ' + id); }
  ok(!s.ids.some(i => /^(wake|monitors|ref-wake|ref-monitors)(-|$)/.test(i)), t + ' no wake/monitors-prefixed ids');
  ok(!s.dom.some(i => /^wake-tick-/.test(i)) && !s.nav.some(h => /wake|monitors/.test(h)), t + ' no wake nav or tick ids');
  ok(!s.text.includes('/healthz') && !s.text.includes('wake timer') && !s.text.includes('long-running-monitor') && !s.text.includes('ウェイクタイマー'), t + ' text does not teach wake or healthz');
  ok(await p.evaluate(() => !document.querySelector('#wake, .wakeviz, [data-wid="wake"]')), t + ' no wake widget');
  ok(s.ids.length > 150, t + ' index still sizeable: ' + s.ids.length);
  // the ACP node explanation must not mention wake timers either
  ok(s.note === null, t + ' no gate note without a dead link');
}

// deep links to gated ids: notice, no blank page, no error, and no broken link while no documented release has the feature
for (const h of ['#en.wake', '#ja.wake', '#wake-ref', '#en.ref-monitors', '#en.wake-tick-3', '#en.card-long-running-monitor', '#en.rough-wake-monitors', '#f-wake']) {
  const p = await open(WITHOUT[0], h.startsWith('#ja') ? 'ja' : 'en', h), s = await snap(p);
  ok(p.__errs.length === 0, h + ' dead link: no page error', p.__errs);
  ok(s.note && s.note.includes(WITHOUT[0]), h + ' dead link: notice names the tag', s.note);
  // a documented release that has wake + monitors (alpha.18) now exists, so the notice offers it
  ok(await p.evaluate(t => { const a = document.querySelector('#gatenote a'); return !!a && a.textContent.includes(t); }, WAKE_TAG), h + ' dead link: offer link names the first tag that has it', s.note);
  ok(s.dom.includes('guides') && s.ids.length > 150, h + ' dead link: page still rendered');
}
{ // a normal id on a gated-out tag shows no notice; unknown ids show none either
  for (const h of ['#en.memory', '#en.no-such-id']) { const p = await open(WITHOUT[1], 'en', h); ok((await snap(p)).note === null && p.__errs.length === 0, h + ' no notice'); }
  const p = await open(WITHOUT[0], 'en', '#en.wake'); await p.evaluate(() => { location.hash = '#en.memory'; }); await p.waitForTimeout(300);
  ok((await snap(p)).note === null, 'notice clears when moving to a live id');
}
{ // version select re-syncs to the page tag after a form restore / bfcache pageshow
  const p = await open('v0.3.0-alpha.12', 'en');
  const val = () => p.evaluate(() => document.getElementById('verSel').value);
  ok(await p.evaluate(() => document.getElementById('verSel').getAttribute('autocomplete') === 'off'), 'select has autocomplete=off');
  ok(await val() === 'v0.3.0-alpha.12', 'select starts on the page tag');
  await p.evaluate(() => { document.getElementById('verSel').value = 'v0.3.0-alpha.14'; });
  ok(await val() === 'v0.3.0-alpha.14', 'select was altered (restore simulated)');
  await p.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  ok(await val() === 'v0.3.0-alpha.12', 'pageshow re-syncs select to the page tag');
  await p.evaluate(() => { document.getElementById('verSel').value = 'v0.3.0-alpha.14'; document.getElementById('langJa').click(); });
  ok(await val() === 'v0.3.0-alpha.12', 'render re-syncs select');
}
await browser.close(); srv.close();
console.log(`gating: ${pass} pass, ${fail} fail`);
process.exit(fail ? 1 : 0);
