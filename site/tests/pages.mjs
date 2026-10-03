// usage: node site/tests/pages.mjs
// Builds the Pages tree into a temp dir, serves it under the subpath /rakitsu/ and checks redirects, per-tag pages, selector, deep links, 404.
import fs from 'fs';
import os from 'os';
import path from 'path';
import http from 'http';
import { execFileSync } from 'child_process';
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const require = createRequire(process.env.PLAYWRIGHT_MODULES || (process.env.HOME + '/.npm/_npx/e41f203b7505f1fb/node_modules/'));
const { chromium } = require('playwright');
let pass = 0, fail = 0;
const ok = (c, m, d) => { if (c) pass++; else { fail++; console.log('FAIL', m, d === undefined ? '' : JSON.stringify(d)); } };

const out = fs.mkdtempSync(path.join(os.tmpdir(), 'rk-pages-test-'));
execFileSync(path.join(root, 'scripts/build-pages.sh'), [out], { stdio: 'pipe' });
const vj = JSON.parse(fs.readFileSync(path.join(root, 'site/data/versions.json'), 'utf8'));
const PFX = '/rakitsu';
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml' };
const srv = http.createServer((req, res) => {
  const u = decodeURIComponent(req.url.split('?')[0].split('#')[0]);
  let f = null;
  if (u === PFX || u.startsWith(PFX + '/')) {
    let rel = u.slice(PFX.length) || '/'; if (rel.endsWith('/')) rel += 'index.html';
    const p = path.join(out, rel);
    if (p.startsWith(out) && fs.existsSync(p) && fs.statSync(p).isFile()) f = p;
  }
  if (!f) { res.writeHead(404, { 'content-type': 'text/html' }); res.end(fs.readFileSync(path.join(out, '404.html'))); return; }
  res.writeHead(200, { 'content-type': types[path.extname(f)] || 'application/octet-stream' }); res.end(fs.readFileSync(f));
});
await new Promise(r => srv.listen(0, '127.0.0.1', r));
const B = 'http://127.0.0.1:' + srv.address().port + PFX;

// static checks
ok(fs.existsSync(path.join(out, '.nojekyll')), '.nojekyll present');
for (const t of vj.versions) { ok(fs.existsSync(path.join(out, t, 'index.html')), 'dir for ' + t); ok(fs.existsSync(path.join(out, 'data', t + '.json')), 'data for ' + t); }
for (const f of ['index.html', 'latest/index.html', '404.html']) ok(fs.existsSync(path.join(out, f)), f + ' present');
for (const a of ['app.css', 'app.js', 'viz.css', 'viz.js']) ok(fs.existsSync(path.join(out, 'assets', a)), 'assets/' + a);
for (const f of ['ibm-plex-sans-latin-400-normal.woff2', 'ibm-plex-sans-latin-500-normal.woff2', 'ibm-plex-sans-latin-600-normal.woff2', 'ibm-plex-mono-latin-400-normal.woff2', 'ibm-plex-mono-latin-500-normal.woff2', 'LICENSE-IBM-Plex.txt']) ok(fs.existsSync(path.join(out, 'assets/fonts', f)), 'assets/fonts/' + f + ' in Pages output');
for (const f of ['rakitsu-logo.svg', 'rakitsu-mark.svg']) ok(fs.existsSync(path.join(out, 'assets/brand', f)), 'assets/brand/' + f + ' in Pages output');
const walk = d => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.isDirectory() ? walk(path.join(d, e.name)) : [path.join(d, e.name)]);
for (const f of walk(out).filter(f => /\.(html|js|css)$/.test(f))) {
  const s = fs.readFileSync(f, 'utf8');
  ok(!/(href|src)="\/[^/]/.test(s) && !/["'(]\/(assets|data)\//.test(s), 'no absolute /assets or /data URL in ' + path.relative(out, f));
}
ok(!fs.existsSync(path.join(out, 'v0.3.0-alpha.9')) && !fs.existsSync(path.join(out, 'data/v0.3.0-alpha.9.json')), 'no placeholder tag output');
const sv = JSON.parse(fs.readFileSync(path.join(out, 'data/versions.json'), 'utf8'));
ok(sv.default === vj.default && sv.versions.length === vj.versions.length, 'versions.json copied');

const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM || (process.env.HOME + '/Library/Caches/ms-playwright/chromium_headless_shell-1228/chrome-headless-shell-mac-arm64/chrome-headless-shell') });
async function page() { const ctx = await browser.newContext(); const p = await ctx.newPage(); p.__errs = []; p.on('pageerror', e => p.__errs.push(String(e))); return p; }
const loaded = p => p.waitForFunction(() => window.__rkLoaded === true);
const def = vj.default;

for (const start of ['/', '/latest/']) {
  const p = await page();
  await p.goto(B + start + '#ja.memory'); await p.waitForURL(u => u.pathname === `${PFX}/${def}/`);
  ok(new URL(p.url()).hash === '#ja.memory', 'redirect from ' + start + ' keeps hash', p.url());
  await loaded(p); ok(await p.evaluate(() => document.documentElement.lang) !== undefined, 'redirect target loads');
  await p.context().close();
}
{ const p = await page(); await p.goto(B + '/'); await p.waitForURL(u => u.pathname === `${PFX}/${def}/`); ok(true, 'bare / redirects'); await p.context().close(); }
{ const t = fs.readFileSync(path.join(out, 'index.html'), 'utf8'); ok(/<noscript>[\s\S]*href=/.test(t) && /http-equiv="refresh"/.test(t) && /location\.replace/.test(t), 'redirect has js + noscript + meta refresh'); }

for (const t of vj.versions) {
  const p = await page();
  await p.goto(`${B}/${t}/`); await loaded(p);
  const d = JSON.parse(fs.readFileSync(path.join(out, 'data', t + '.json'), 'utf8'));
  ok(await p.evaluate(() => document.getElementById('verSel').value) === t, 'selector shows ' + t);
  ok((await p.textContent('#verlabel')).includes(t), 'label shows ' + t);
  const n = await p.evaluate(() => document.querySelectorAll('#feats .feat').length);
  ok(n === d.features.length, t + ' feature count', [n, d.features.length]);
  ok(p.__errs.length === 0, t + ' no page errors', p.__errs);
  const html = fs.readFileSync(path.join(out, t, 'index.html'), 'utf8');
  const favHref = (html.match(/<link rel="icon"[^>]*href="([^"]+)"/) || [])[1];
  const logoSrc = (html.match(/class="herologo" src="([^"]+)"/) || [])[1];
  const markSrc = (html.match(/class="logo" src="([^"]+)"/) || [])[1];
  for (const [n, u] of [['favicon', favHref], ['hero logo', logoSrc], ['header mark', markSrc]]) {
    ok(!!u, t + ' has ' + n + ' url', u);
    if (u) { const r = await p.request.get(new URL(u, `${B}/${t}/`).href); ok(r.status() === 200, t + ' ' + n + ' resolves 200', [u, r.status()]); }
  }
  ok(await p.evaluate(() => { const i = document.querySelector('img.herologo'); return !!i && i.complete && i.naturalWidth > 0; }), t + ' hero logo renders');
  ok(await p.evaluate(() => { const i = document.querySelector('.brand img.logo'); return !!i && i.complete && i.naturalWidth > 0; }), t + ' header mark renders');
  await p.context().close();
}
{ // deep link
  const p = await page();
  await p.goto(`${B}/${def}/#ja.memory`); await loaded(p);
  await p.waitForTimeout(600);
  ok(await p.evaluate(() => /[ぁ-ん]/.test(document.querySelector('.hero h1').textContent)), 'deep link opens in Japanese');
  ok(await p.evaluate(() => { const e = document.getElementById('memory'); if (!e) return false; const r = e.getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0; }), 'deep link scrolled to memory section');
  // selector switch
  const other = vj.versions.find(v => v !== def);
  await p.selectOption('#verSel', other);
  await p.waitForURL(u => u.pathname === `${PFX}/${other}/`);
  ok(new URL(p.url()).hash === '#ja.memory', 'selector keeps token', p.url());
  await loaded(p); ok(await p.evaluate(() => document.getElementById('verSel').value) === other, 'selector switched page to ' + other);
  await p.context().close();
}
{ const p = await page(); const r = await p.goto(B + '/no/such/page'); ok(r.status() === 404, '404 status'); ok((await p.textContent('h1')) === 'Page not found', '404 page served'); await p.context().close(); }
const first = vj.versions[vj.versions.length - 1];
for (const u of ['/v9.9.9/', '/v0.3.0-alpha.9/', '/v0.3.0-alpha.11/index.html']) {
  const p = await page(); const r = await p.goto(B + u); ok(r.status() === 404, u + ' is 404');
  ok(await p.evaluate(() => !document.getElementById('notag').hidden), u + ' shows the no-docs message');
  const t = await p.textContent('#notag'); ok(t.includes('No docs for this release. Docs start at ' + first + '.') && t.includes('このリリースのドキュメントはありません'), u + ' message EN/JA', t);
  await p.context().close();
}
{ const p = await page(); await p.goto(B + '/no/such/page'); ok(await p.evaluate(() => document.getElementById('notag').hidden), 'plain 404 has no release message'); await p.context().close(); }

// ---- covers: built from a throwaway copy of scripts/ and site/ (a fixture entry; nothing is written to the repo)
const COV = 'v0.3.0-alpha.19', DOC = vj.versions[0];
function fixtureTree(covers) {
  const t = fs.mkdtempSync(path.join(os.tmpdir(), 'rk-covers-src-'));
  fs.mkdirSync(path.join(t, 'scripts')); fs.copyFileSync(path.join(root, 'scripts/build-pages.sh'), path.join(t, 'scripts/build-pages.sh'));
  fs.cpSync(path.join(root, 'site'), path.join(t, 'site'), { recursive: true, filter: s => !s.includes('/tests') });
  fs.writeFileSync(path.join(t, 'site/data/versions.json'), JSON.stringify({ ...vj, covers }));
  execFileSync('git', ['init', '-q', t]);
  return t;
}
const tryBuild = (covers, o) => { const t = fixtureTree(covers); try { execFileSync(path.join(t, 'scripts/build-pages.sh'), [o], { stdio: 'pipe' }); return null; } catch (e) { return String(e.stderr); } finally { fs.rmSync(t, { recursive: true, force: true }); } };
const bad1 = tryBuild({ [COV]: vj.versions[1] }, fs.mkdtempSync(path.join(os.tmpdir(), 'rk-covers-bad-')));
ok(bad1 && /nearest earlier/.test(bad1), 'build rejects a covers value that is not the nearest earlier doc version', bad1);
const bad2 = tryBuild({ [COV]: 'v0.3.0-alpha.9' }, fs.mkdtempSync(path.join(os.tmpdir(), 'rk-covers-bad-')));
ok(bad2 && /not a listed doc version/.test(bad2), 'build rejects a covers value that is not a doc version', bad2);
const out2 = fs.mkdtempSync(path.join(os.tmpdir(), 'rk-covers-out-'));
ok(tryBuild({ [COV]: DOC }, out2) === null, 'build with a covers fixture succeeds');
ok(fs.existsSync(path.join(out2, COV, 'index.html')) && !fs.existsSync(path.join(out2, 'data', COV + '.json')), 'covered tag has a page and no data file');
ok(/window\.RK_TAG="v0\.3\.0-alpha\.19"/.test(fs.readFileSync(path.join(out2, COV, 'index.html'), 'utf8')), 'covered page sets RK_TAG');
ok(fs.readFileSync(path.join(out2, '404.html'), 'utf8').includes(COV), '404 lists the covered tag');
const srv2 = http.createServer((req, res) => {
  const u = decodeURIComponent(req.url.split('?')[0]); let rel = u.slice(PFX.length) || '/'; if (rel.endsWith('/')) rel += 'index.html';
  const f = u.startsWith(PFX + '/') ? path.join(out2, rel) : null;
  if (!f || !fs.existsSync(f) || !fs.statSync(f).isFile()) { res.writeHead(404, { 'content-type': 'text/html' }); return res.end(fs.readFileSync(path.join(out2, '404.html'))); }
  res.writeHead(200, { 'content-type': types[path.extname(f)] || 'application/octet-stream' }); res.end(fs.readFileSync(f));
});
await new Promise(r => srv2.listen(0, '127.0.0.1', r));
const B2 = 'http://127.0.0.1:' + srv2.address().port + PFX;
for (const [lang, notice, label] of [['en', `This release has no doc changes. Showing the docs for ${DOC}.`, `${COV} (docs from ${DOC})`], ['ja', `このリリースにはドキュメントの変更がありません。${DOC} のドキュメントを表示しています。`, `${COV} (ドキュメント: ${DOC})`]]) {
  const p = await page(); await p.goto(`${B2}/${COV}/#${lang}.memory`); await loaded(p);
  ok(await p.evaluate(() => !document.getElementById('notice').hidden) && (await p.textContent('#notice')) === notice, 'covered page notice ' + lang, await p.textContent('#notice'));
  ok(await p.evaluate(() => document.getElementById('verSel').value) === COV, 'covered page selector on covered tag ' + lang);
  const opts = await p.evaluate(() => [...document.querySelectorAll('#verSel option')].map(o => o.textContent));
  ok(opts[0] === label, 'dropdown label for covered tag ' + lang, opts);
  ok(opts.slice(1).join() === vj.versions.join(), 'doc versions listed without suffix ' + lang, opts);
  const chip = await p.textContent('#verlabel'); ok(chip.includes(COV) && chip.includes(DOC), 'chip shows both ' + lang, chip);
  const dd = JSON.parse(fs.readFileSync(path.join(out2, 'data', DOC + '.json'), 'utf8'));
  ok(await p.evaluate(() => document.querySelectorAll('#feats .feat').length) === dd.features.length, 'covered page renders the doc version data ' + lang);
  ok(new URL(p.url()).hash === '#' + lang + '.memory' && p.__errs.length === 0, 'covered page keeps permalink grammar, no errors ' + lang, p.__errs);
  await p.context().close();
}
{ const p = await page(); await p.goto(`${B2}/${DOC}/`); await loaded(p); ok(await p.evaluate(() => document.getElementById('notice').hidden), 'doc version page has no covers notice');
  await p.selectOption('#verSel', COV); await p.waitForURL(u => u.pathname === `${PFX}/${COV}/`); await p.context().close(); }
await browser.close(); srv2.close(); for (const d of [out2]) fs.rmSync(d, { recursive: true, force: true });
srv.close(); fs.rmSync(out, { recursive: true, force: true });
console.log(`pages: ${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);


