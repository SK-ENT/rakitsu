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
const srv = http.createServer((q, r) => {
  const u = decodeURIComponent(q.url.split('?')[0]); const f = path.join(site, u.endsWith('/') ? u + 'index.html' : u);
  if (!f.startsWith(site) || !fs.existsSync(f) || fs.statSync(f).isDirectory()) { r.writeHead(404); return r.end(); }
  r.writeHead(200, { 'content-type': mime[path.extname(f)] || 'application/octet-stream' }); r.end(fs.readFileSync(f));
}).listen(0, '127.0.0.1');
await new Promise(r => srv.on('listening', r));
const base = `http://127.0.0.1:${srv.address().port}/`;
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
const WITH = 'v0.3.0-alpha.12', WITHOUT = ['v0.3.0-alpha.14', 'v0.3.0-alpha.13'];
const GATED = ['wake', 'wake-summary', 'wake-tick-1', 'ref-wake', 'ref-monitors', 'card-long-running-monitor', 'rough-wake-monitors'];
async function open(ver, lang, hash) {
  const ctx = await browser.newContext(); const p = await ctx.newPage(); p.__errs = []; p.on('pageerror', e => p.__errs.push(String(e)));
  await p.addInitScript(([l, v]) => localStorage.setItem('rk-spec', JSON.stringify({ lang: l, view: 'detail', ver: v })), [lang, ver]);
  await p.goto(base + (hash || '')); await p.waitForFunction(() => window.__rkLoaded === true); await p.waitForTimeout(100); return p;
}
const snap = p => p.evaluate(() => ({
  ids: [...document.querySelectorAll('#lidxlist a')].map(a => a.getAttribute('href').replace(/^#(en|ja)\./, '')),
  dom: [...document.querySelectorAll('[id]')].map(e => e.id),
  text: document.querySelector('main').innerText.toLowerCase(),
  nav: [...document.querySelectorAll('#nav a')].map(a => a.getAttribute('href')),
  note: document.getElementById('gatenote') ? document.getElementById('gatenote').innerText : null,
}));

{ const p = await open(WITH, 'en'); const s = await snap(p);
  for (const id of GATED) ok(s.ids.includes(id), WITH + ' still indexes ' + id);
  ok(s.dom.includes('wake') && s.text.includes('wake timer') && s.text.includes('/healthz'), WITH + ' still teaches wake and healthz');
  ok(s.note === null && p.__errs.length === 0, WITH + ' no gate note, no errors'); }

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

// deep links to gated ids: notice, no blank page, no error, link to a tag that has it
for (const h of ['#en.wake', '#ja.wake', '#wake-ref', '#en.ref-monitors', '#en.wake-tick-3', '#en.card-long-running-monitor', '#en.rough-wake-monitors', '#f-wake']) {
  const p = await open(WITHOUT[0], h.startsWith('#ja') ? 'ja' : 'en', h), s = await snap(p);
  ok(p.__errs.length === 0, h + ' dead link: no page error', p.__errs);
  ok(s.note && s.note.includes(WITHOUT[0]), h + ' dead link: notice names the tag', s.note);
  ok(await p.evaluate(() => !!document.querySelector('#gatenote a') && document.querySelector('#gatenote a').textContent.includes('v0.3.0-alpha.12')), h + ' dead link: offers a tag that has it');
  ok(s.dom.includes('guides') && s.ids.length > 150, h + ' dead link: page still rendered');
}
{ // following the offer switches to the tag and reveals the section
  const p = await open(WITHOUT[0], 'en', '#en.wake'); await p.locator('#gatenote a').click(); await p.waitForTimeout(500);
  const s = await snap(p);
  ok(s.dom.includes('wake') && s.note === null && p.__errs.length === 0, 'offer link switches to a tag that has wake', p.__errs);
}
{ // a normal id on a gated-out tag shows no notice; unknown ids show none either
  for (const h of ['#en.memory', '#en.no-such-id']) { const p = await open(WITHOUT[1], 'en', h); ok((await snap(p)).note === null && p.__errs.length === 0, h + ' no notice'); }
  const p = await open(WITHOUT[0], 'en', '#en.wake'); await p.evaluate(() => { location.hash = '#en.memory'; }); await p.waitForTimeout(300);
  ok((await snap(p)).note === null, 'notice clears when moving to a live id');
}
await browser.close(); srv.close();
console.log(`gating: ${pass} pass, ${fail} fail`);
process.exit(fail ? 1 : 0);
