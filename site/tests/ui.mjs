// usage: node site/tests/ui.mjs [url]   (server must be up: python3 -m http.server 8765 -d site)
// Link UI, hashchange, language/hash rewriting, auto Simple->Detail, state keep across EN/JA, real screenshots.
import fs from 'fs';
import path from 'path';
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
const here = path.dirname(fileURLToPath(import.meta.url));
const URL_ = process.argv[2] || 'http://127.0.0.1:8765/';
try { const r = await fetch(URL_ + 'data/versions.json'); if (!r.ok) throw new Error(r.status); } catch (e) { console.error('Server is not up at ' + URL_ + '. Run: python3 -m http.server 8765 -d site'); process.exit(2); }
const require = createRequire(process.env.PLAYWRIGHT_MODULES || (process.env.HOME + '/.npm/_npx/e41f203b7505f1fb/node_modules/'));
const { chromium } = require('playwright');
let pass = 0, fail = 0;
const ok = (c, m, d) => { if (c) pass++; else { fail++; console.log('FAIL', m, d === undefined ? '' : JSON.stringify(d)); } };
const sleep = ms => new Promise(r => setTimeout(r, ms));
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
async function open(opts = {}, init, url = URL_) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, ...opts });
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(String(e))); p.__errs = errs; p.__ctx = ctx;
  if (init) await p.addInitScript(init[0], init[1]);
  await p.goto(url); await p.waitForFunction(() => window.__rkLoaded === true); return p;
}
const inView = (p, id) => p.evaluate(i => { const e = document.getElementById(i); if (!e) return false; const r = e.getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0; }, id);
const hash = p => p.evaluate(() => location.hash);

// 1. copy-link button (clipboard works)
{
  const p = await open({ permissions: ['clipboard-read', 'clipboard-write'] });
  await p.locator('#wake h2 .lnk').first().click(); await sleep(200);
  const clip = await p.evaluate(() => navigator.clipboard.readText());
  ok(clip === URL_ + '#en.wake', 'copy-link puts absolute URL with #en.<id> on clipboard', clip);
  ok(await hash(p) === '#en.wake', 'copy-link sets location.hash');
  const msg = await p.locator('#linkmsg').textContent();
  ok(!(await p.locator('#linkmsg').isHidden()) && /Copied/.test(msg), 'copy-link shows Copied message', msg);
  ok((await p.locator('#linkmsg input').count()) === 0, 'no fallback input when clipboard works');
  ok(p.__errs.length === 0, 'copy-link no page errors', p.__errs); await p.__ctx.close();
}
// 1b. fallback path: clipboard rejects, and clipboard missing
for (const mode of ['reject', 'missing']) {
  const p = await open({}, [m => { if (m === 'reject') Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.reject(new Error('denied')) }, configurable: true }); else Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true }); }, mode]);
  await p.locator('#memory h2 .lnk').first().click(); await sleep(300);
  const inp = p.locator('#linkmsg input');
  ok((await inp.count()) === 1, `fallback input shown (${mode})`);
  if (await inp.count()) {
    ok((await inp.inputValue()) === URL_ + '#en.memory', `fallback input holds the full link (${mode})`, await inp.inputValue());
    ok(await inp.evaluate(e => e.readOnly && document.activeElement === e), `fallback input read-only and focused (${mode})`);
  }
  ok(/Copy this link/.test(await p.locator('#linkmsg').textContent()), `fallback message text (${mode})`);
  ok(p.__errs.length === 0, `fallback no page errors (${mode})`, p.__errs); await p.__ctx.close();
}
// 2. hashchange after load; 3. language toggle rewrites hash
{
  const p = await open();
  await p.evaluate(() => { location.hash = '#en.wake'; }); await sleep(400);
  ok(await inView(p, 'wake'), 'hashchange after load scrolls to #en.wake');
  ok(await p.evaluate(() => document.querySelector('#wake').classList.contains('hl')), 'target highlighted');
  await p.click('#langJa'); await sleep(400);
  ok(await hash(p) === '#ja.wake', 'language toggle rewrites hash to #ja.wake', await hash(p));
  ok(await p.evaluate(() => document.documentElement.lang) === 'ja', 'html lang = ja');
  ok(await inView(p, 'wake'), 'still scrolled to wake after toggle');
  await p.click('#langEn'); await sleep(300);
  ok(await hash(p) === '#en.wake', 'toggle back rewrites hash to #en.wake');
  // hash with other language switches the UI language
  await p.evaluate(() => { location.hash = '#ja.memory'; }); await sleep(500);
  ok(await p.evaluate(() => document.documentElement.lang) === 'ja' && await inView(p, 'memory'), 'hashchange to #ja.memory switches language and scrolls');
  // bare/alias token
  await p.evaluate(() => { location.hash = '#protocols'; }); await sleep(400);
  ok(await inView(p, 'protocols'), 'bare #protocols works');
  await p.evaluate(() => { location.hash = '#en.no-such-thing'; }); await sleep(300);
  ok(p.__errs.length === 0, 'unknown hash: no page errors', p.__errs); await p.__ctx.close();
}
// initial load with hash
{
  const p = await open({}, null, URL_ + '#ja.orchestration'); await sleep(500);
  ok(await p.evaluate(() => document.documentElement.lang) === 'ja' && await inView(p, 'orchestration'), 'load with #ja.orchestration: JA and scrolled');
  await p.__ctx.close();
}
// 4. Simple -> Detail auto switch on a Detail-only target
{
  const p = await open({}, [() => localStorage.setItem('rk-spec', JSON.stringify({ lang: 'en', view: 'simple' })), 0]);
  ok(await p.evaluate(() => document.body.dataset.view) === 'simple', 'starts in simple view');
  const target = await p.evaluate(() => { const a = [...document.querySelectorAll('#lidxlist a')].map(x => x.getAttribute('href').slice(4)); return a.find(id => { const e = document.getElementById(id); return e && e.closest('.detail'); }); });
  ok(!!target, 'found a detail-only target id', target);
  await p.evaluate(t => { location.hash = '#en.' + t; }, target); await sleep(600);
  ok(await p.evaluate(() => document.body.dataset.view) === 'detail', 'auto-switched to Detail', target);
  ok(await inView(p, target), 'detail-only target scrolled into view', target);
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('rk-spec')).view) === 'detail', 'view saved as detail');
  ok(p.__errs.length === 0, 'simple->detail no page errors', p.__errs); await p.__ctx.close();
}
// 5. state preserved across EN/JA and Simple/Detail
{
  const p = await open();
  await p.locator('#orchestration .tabs button').nth(1).click(); await p.check('#gatecb');
  await p.locator('#memory').scrollIntoViewIfNeeded();
  await p.fill('#memory input[type=text], #memory input:not([type])', 'rollback only'); 
  await p.locator('#data-flow .nd').nth(3).click();
  const flowTitle = await p.$eval('#data-flow .info h4', e => e.textContent);
  const before = await p.evaluate(() => ({ q: document.querySelector('#memory input:not([type=checkbox])').value }));
  await p.click('#langJa'); await sleep(300); await p.click('#viewSimple'); await sleep(300); await p.click('#langEn'); await sleep(300); await p.click('#viewDetail'); await sleep(300);
  ok(await p.locator('#orchestration .tabs button').nth(1).getAttribute('aria-pressed') === 'true', 'orchestration mode kept');
  ok(await p.isChecked('#gatecb'), 'pipeline checkbox kept');
  ok(await p.evaluate(() => document.querySelector('#memory input:not([type=checkbox])').value) === before.q, 'memory query kept', before);
  ok(await p.$eval('#data-flow .info h4', e => e.textContent) === flowTitle, 'selected flow node kept', flowTitle);
  ok(p.__errs.length === 0, 'state-keep no page errors', p.__errs); await p.__ctx.close();
}
// 6. load error (data unreachable) shows localized message
{
  const ctx = await browser.newContext(); const p = await ctx.newPage();
  await p.route('**/data/versions.json', r => r.abort());
  await p.goto(URL_); await sleep(800);
  ok(await p.locator('#loaderr').isVisible() && /python3 -m http.server 8765 -d site/.test(await p.locator('#loaderr').textContent()), 'fetch failure shows error with the server command');
  await ctx.close();
  const ctx2 = await browser.newContext(); const p2 = await ctx2.newPage();
  await p2.addInitScript(() => localStorage.setItem('rk-spec', JSON.stringify({ lang: 'ja', view: 'detail' })));
  await p2.route('**/data/versions.json', r => r.abort()); await p2.goto(URL_); await sleep(800);
  ok(/読み込めませんでした/.test(await p2.locator('#loaderr').textContent()), 'error message is localized (JA)');
  await ctx2.close();
}
// 6b. no third-party requests; self-hosted fonts load
{
  const ctx = await browser.newContext(); const p = await ctx.newPage(); const hosts = new Set();
  p.on('request', r => { const u = new URL(r.url()); if (/^https?:$/.test(u.protocol)) hosts.add(u.hostname); });
  await p.goto(URL_); await p.waitForSelector('footer, #license', { state: 'attached' }); await sleep(800);
  ok([...hosts].every(h => h === '127.0.0.1' || h === 'localhost'), 'page load only contacts localhost', [...hosts]);
  await p.evaluate(() => Promise.all(['400 14px "IBM Plex Sans"', '500 14px "IBM Plex Sans"', '600 14px "IBM Plex Sans"', '400 14px "IBM Plex Mono"', '500 14px "IBM Plex Mono"'].map(f => document.fonts.load(f))));
  ok(await p.evaluate(() => document.fonts.check('500 14px "IBM Plex Sans"')), 'IBM Plex Sans 500 loaded');
  ok(await p.evaluate(() => document.fonts.check('500 14px "IBM Plex Mono"')), 'IBM Plex Mono 500 loaded');
  ok(await p.evaluate(() => [...document.fonts].filter(f => f.status === 'loaded').length) >= 2, 'font faces report loaded');
  await ctx.close();
}
// 7. screenshots
fs.mkdirSync(path.join(here, 'shots'), { recursive: true });
const shots = [['1280-en-light', 1280, 900, 'en', 'light'], ['1280-ja-dark', 1280, 900, 'ja', 'dark'], ['400-en-light', 400, 800, 'en', 'light'], ['400-ja-dark', 400, 800, 'ja', 'dark']];
const stats = async (p, buf) => p.evaluate(async b64 => {
  const img = new Image(); img.src = 'data:image/png;base64,' + b64; await img.decode();
  const c = document.createElement('canvas'); c.width = img.width; c.height = img.height; const x = c.getContext('2d'); x.drawImage(img, 0, 0);
  const d = x.getImageData(0, 0, c.width, c.height).data; let s = 0, s2 = 0, n = 0; const cols = new Set();
  for (let i = 0; i < d.length; i += 4) { const l = 0.299 * d[i] + 0.587 * d[i + 1] + 0.114 * d[i + 2]; s += l; s2 += l * l; n++; if (cols.size < 500) cols.add(d[i] + ',' + d[i + 1] + ',' + d[i + 2]); }
  return { variance: s2 / n - (s / n) ** 2, colors: cols.size };
}, buf.toString('base64'));
const blankCtx = await browser.newContext({ viewport: { width: 400, height: 800 } }); const bp = await blankCtx.newPage(); await bp.goto('about:blank');
const blank = await stats(bp, await bp.screenshot());
ok(blank.variance < 1, 'reference blank PNG has ~zero variance', blank);
for (const [name, w, h, lang, scheme] of shots) {
  const p = await open({ viewport: { width: w, height: h }, colorScheme: scheme }, [l => localStorage.setItem('rk-spec', JSON.stringify({ lang: l, view: 'detail' })), lang]);
  for (const [suffix, sel] of [['top', null], ['orchestration', '#orchestration']]) {
    if (sel) { await p.locator(sel).scrollIntoViewIfNeeded(); await sleep(400); } else await sleep(400);
    const file = path.join(here, 'shots', `${name}-${suffix}.png`); const buf = await p.screenshot({ path: file });
    const s = await stats(bp, buf);
    ok(s.variance > 50 && s.colors > 8, `screenshot ${name}-${suffix} is not blank`, s);
  }
  const ovf = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ok(ovf <= 1, `no horizontal overflow at ${w}px (${lang}/${scheme})`, ovf);
  const bg = await p.evaluate(() => getComputedStyle(document.body).backgroundColor);
  ok(scheme === 'dark' ? /rgb\((\d+), (\d+), (\d+)\)/.exec(bg).slice(1).every(v => +v < 80) : /rgb\((\d+), (\d+), (\d+)\)/.exec(bg).slice(1).every(v => +v > 200), `body background matches ${scheme}`, bg);
  ok(p.__errs.length === 0, `screenshots ${name} no page errors`, p.__errs); await p.__ctx.close();
}
await blankCtx.close(); await browser.close();
console.log(`ui: ${pass} pass, ${fail} fail`);
process.exit(fail ? 1 : 0);
