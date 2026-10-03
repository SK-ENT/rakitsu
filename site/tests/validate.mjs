// usage: node site/tests/validate.mjs   (no network, no deps beyond playwright for the browser part)
// Browser part opens the site over file:// ONLY to read the link index? No: fetch() is blocked on file://,
// so this script serves site/ itself on an ephemeral port using node:http.
import fs from 'fs';
import path from 'path';
import http from 'http';
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
const here = path.dirname(fileURLToPath(import.meta.url));
const site = path.resolve(here, '..');
let pass = 0, fail = 0;
const ok = (c, m) => { if (c) pass++; else { fail++; console.log('FAIL', m); } };

// ---- 1. data files
const dataDir = path.join(site, 'data');
const vj = JSON.parse(fs.readFileSync(path.join(dataDir, 'versions.json'), 'utf8'));
const files = fs.readdirSync(dataDir).filter(f => f.endsWith('.json') && f !== 'versions.json').map(f => f.replace(/\.json$/, '')).sort();
ok(JSON.stringify([...vj.versions].sort()) === JSON.stringify(files), 'versions.json lists exactly the data files present');
ok(vj.versions.includes(vj.default), 'versions.default is in versions');
const bi = o => o && typeof o === 'object' && typeof o.en === 'string' && o.en && typeof o.ja === 'string' && o.ja;
const docVersions = [];
for (const v of files) {
  let d; try { d = JSON.parse(fs.readFileSync(path.join(dataDir, v + '.json'), 'utf8')); ok(true); } catch (e) { ok(false, v + ' parses: ' + e); continue; }
  ok(d.tag === v, v + ' tag field matches file name');
  ok(d.commit === null ? d.commit_note === 'tag not present in local clone' : /^[0-9a-f]{40}$/.test(d.commit), v + ' commit is a 40-hex hash or null with commit_note');
  if (!d.docsGenerated) continue;
  docVersions.push(v);
  ok(Array.isArray(d.features) && d.features.length > 0, v + ' has features');
  for (const ft of d.features) {
    ok(bi(ft.name), `${v}/${ft.id} name has EN and JA`);
    ok(bi(ft.simple), `${v}/${ft.id} simple has EN and JA`);
    ft.detail.forEach((b, i) => {
      const where = `${v}/${ft.id} detail[${i}] (${b.t})`;
      if (b.t === 'p' || b.t === 'h') ok(bi(b), where + ' EN and JA');
      else if (b.t === 'ul') b.items.forEach((it, j) => ok(bi(it), where + ` item ${j} EN and JA`));
      else if (b.t === 'kv') b.rows.forEach((r, j) => r.forEach((c, k) => { if (c && typeof c === 'object') ok(bi(c), where + ` row ${j} cell ${k} EN and JA`); }));
      else if (b.t === 'code') ok(typeof b.text === 'string', where + ' text');
      else if (b.t === 'refs') ok(Array.isArray(b.keys), where + ' keys');
      else ok(false, where + ' unknown block type');
    });
  }
}
ok(docVersions.length >= 1, 'at least one doc-generated version');

// ---- 2. forbidden strings
const walk = d => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.isDirectory() ? (e.name === 'shots' || e.name === 'node_modules' ? [] : walk(path.join(d, e.name))) : [path.join(d, e.name)]);
for (const f of walk(site)) {
  if (!/\.(html|css|js|json|md|mjs)$/.test(f)) continue;
  const t = fs.readFileSync(f, 'utf8'), rel = path.relative(site, f);
  if (rel === 'tests/validate.mjs') continue; // this file names the forbidden strings
  ok(!/github\.com\/(?!SK-ENT\/)[^\/\s"']+\/rakitsu/.test(t), rel + ' links only to the SK-ENT public repo');
  ok(!/Moving dots/.test(t), rel + ' has no "Moving dots"');
}

// ---- 3. browser: collect linkable ids
const require = createRequire(process.env.PLAYWRIGHT_MODULES || (process.env.HOME + '/.npm/_npx/e41f203b7505f1fb/node_modules/'));
const { chromium } = require('playwright');
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json' };
const srv = http.createServer((q, r) => {
  const u = decodeURIComponent(q.url.split('?')[0]); const f = path.join(site, u.endsWith('/') ? u + 'index.html' : u);
  if (!f.startsWith(site) || !fs.existsSync(f)) { r.writeHead(404); return r.end(); }
  r.writeHead(200, { 'content-type': mime[path.extname(f)] || 'application/octet-stream' }); r.end(fs.readFileSync(f));
}).listen(0, '127.0.0.1');
await new Promise(r => srv.on('listening', r));
const base = `http://127.0.0.1:${srv.address().port}/`;
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
try {
  const GRAMMAR = /^[a-z0-9]+(?:[-_][a-z0-9]+)*$/;
  for (const v of docVersions) for (const lang of ['en', 'ja']) for (const view of ['detail', 'simple']) {
    const ctx = await browser.newContext(); const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(String(e)));
    await p.addInitScript(([l, vw, ver]) => localStorage.setItem('rk-spec', JSON.stringify({ lang: l, view: vw, ver })), [lang, view, v]);
    await p.goto(base); await p.waitForFunction(() => window.__rkLoaded === true);
    const r = await p.evaluate(() => ({
      ids: [...document.querySelectorAll('#lidxlist a')].map(a => a.getAttribute('href')),
      domIds: [...document.querySelectorAll('[id]')].map(e => e.id),
    }));
    const tag = `${v}/${lang}/${view}`;
    ok(errs.length === 0, tag + ' no page errors ' + errs.join('|'));
    ok(r.ids.length > 150, tag + ' link index size ' + r.ids.length);
    const ids = r.ids.map(h => h.replace(/^#(en|ja)\./, ''));
    ok(r.ids.every(h => h.startsWith('#' + lang + '.')), tag + ' links use the active language prefix');
    ok(new Set(ids).size === ids.length, tag + ' linkable ids unique (dups: ' + ids.filter((x, i) => ids.indexOf(x) !== i).slice(0, 5) + ')');
    ok(ids.every(i => /^[\x20-\x7e]+$/.test(i)), tag + ' ids ASCII-only');
    ok(ids.every(i => GRAMMAR.test(i)), tag + ' ids match grammar ' + ids.filter(i => !GRAMMAR.test(i)).slice(0, 5));
    ok(new Set(r.domIds).size === r.domIds.length, tag + ' DOM ids unique (dups: ' + r.domIds.filter((x, i) => r.domIds.indexOf(x) !== i).slice(0, 5) + ')');
    // widget-step ids (react-*, wake-tick-*, ...) are virtual: they point at a widget, not at their own element, so no DOM-existence check.
    if (lang === 'en' && view === 'detail' && v === vj.default) fs.writeFileSync(path.join(here, 'link-ids.txt'), ids.join('\n') + '\n');
    await ctx.close();
  }
  // ids identical across language and view
  const idsOf = async (lang, view) => { const c = await browser.newContext(); const p = await c.newPage(); await p.addInitScript(([l, vw]) => localStorage.setItem('rk-spec', JSON.stringify({ lang: l, view: vw })), [lang, view]); await p.goto(base); await p.waitForFunction(() => window.__rkLoaded === true); const x = await p.evaluate(() => [...document.querySelectorAll('#lidxlist a')].map(a => a.getAttribute('href').replace(/^#(en|ja)\./, ''))); await c.close(); return x.join(','); };
  const a = await idsOf('en', 'detail'), b = await idsOf('ja', 'simple');
  ok(a === b, 'link ids identical across EN/JA and Simple/Detail');

  // ---- 4. parser
  const c = await browser.newContext(); const p = await c.newPage(); await p.goto(base); await p.waitForFunction(() => window.__rkLoaded === true);
  const cases = [
    ['#en.agent-loop', { lang: 'en', raw: 'agent-loop', id: 'agent-loop' }],
    ['#ja.wake', { lang: 'ja', raw: 'wake', id: 'wake' }],
    ['#wake', { lang: null, raw: 'wake', id: 'wake' }],
    ['wake', { lang: null, raw: 'wake', id: 'wake' }],
    ['#react', { lang: null, raw: 'react', id: 'agent-loop' }],       // alias
    ['#en.f-react', { lang: 'en', raw: 'f-react', id: 'agent-loop' }], // old id alias
    ['#fr.wake', { lang: null, raw: 'fr.wake', id: null }],            // unsupported language
    ['#en.', { lang: 'en', raw: '', id: null }],
    ['#', { lang: null, raw: '', id: null }],
    ['', { lang: null, raw: '', id: null }],
    ['#no-such-id', { lang: null, raw: 'no-such-id', id: null }],
    ['#__proto__', { lang: null, raw: '__proto__', id: null }],
    ['#constructor', { lang: null, raw: 'constructor', id: null }],
    ['#en.<script>', { lang: 'en', raw: '<script>', id: null }],
    ['#EN.wake', { lang: null, raw: 'EN.wake', id: null }],
  ];
  for (const [tok, want] of cases) {
    const got = await p.evaluate(t => window.RakitsuLinks.parseToken(t), tok);
    ok(JSON.stringify(got) === JSON.stringify(want), `parseToken(${JSON.stringify(tok)}) = ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
  ok(await p.evaluate(() => window.RakitsuLinks.mkLink('ja', 'wake')) === '#ja.wake', 'mkLink');
  await c.close();
} finally { await browser.close(); srv.close(); }
{ // no Google Fonts references anywhere in site/
  const walkS = d => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.isDirectory() ? (e.name === 'shots' || e.name === 'tests' ? [] : walkS(path.join(d, e.name))) : [path.join(d, e.name)]);
  for (const f of walkS(site).filter(f => /\.(html|css|js|mjs)$/.test(f))) ok(!/fonts\.(googleapis|gstatic)\.com/.test(fs.readFileSync(f, 'utf8')), 'no Google Fonts reference in ' + path.relative(site, f));
}
console.log(`validate: ${pass} pass, ${fail} fail`);
process.exit(fail ? 1 : 0);
