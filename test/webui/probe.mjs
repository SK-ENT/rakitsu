// WebUI probe for Rakitsu — drives the browser to validate that the
// SessionPicker (and other multi-session UI) renders correctly against a
// live backend. Run with:
//
//   cd test/webui && npm install
//   npm run probe
//
// Or from repo root:
//   node test/webui/probe.mjs
//
// Env vars:
//   RAKITSU_BASE_URL   default http://localhost:9100
//   RAKITSU_CHAT_CONFIG  default ba616ec02aa0 (Chat WS Test) -- a maintainer
//                      config ID that won't resolve on a fresh clone; upload
//                      any chat-capable config first and override this (see
//                      README.md).
//   CHROME_PATH        default /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
//   HEADLESS           default true; set HEADLESS=false for visible browser
//   SCREENSHOTS_DIR    default ./artifacts
//
// Exit code is non-zero on any failed assertion. Artifacts go to
// SCREENSHOTS_DIR so failures are debuggable after the fact.

import puppeteer from 'puppeteer-core';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const BASE_URL = process.env.RAKITSU_BASE_URL || 'http://localhost:9100';
const CHAT_CONFIG = process.env.RAKITSU_CHAT_CONFIG || 'ba616ec02aa0';
const CHROME_PATH = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const HEADLESS = (process.env.HEADLESS ?? 'true') !== 'false';
const ARTIFACTS = process.env.SCREENSHOTS_DIR || 'artifacts';

const results = [];
let browser;
let page;

function log(tag, msg) {
  const stamp = new Date().toISOString().slice(11, 23);
  console.log(`[${stamp}] ${tag} ${msg}`);
}

async function record(name, ok, detail = '') {
  results.push({ name, ok, detail });
  log(ok ? 'PASS' : 'FAIL', `${name}${detail ? ' — ' + detail : ''}`);
  if (page) {
    try {
      await mkdir(ARTIFACTS, { recursive: true });
      await page.screenshot({ path: join(ARTIFACTS, `${name.replace(/\s+/g, '_')}.png`) });
    } catch {
      /* ignore screenshot errors */
    }
  }
}

async function api(path, opts = {}) {
  const res = await fetch(BASE_URL + path, opts);
  const text = await res.text();
  if (res.status >= 400) throw new Error(`${opts.method || 'GET'} ${path}: HTTP ${res.status}: ${text}`);
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function waitFor(pred, budgetMs = 5000, stepMs = 200) {
  const deadline = Date.now() + budgetMs;
  while (Date.now() < deadline) {
    if (await pred()) return true;
    await new Promise((r) => setTimeout(r, stepMs));
  }
  return false;
}

async function main() {
  log('INFO', `probing ${BASE_URL}`);
  log('INFO', `chrome=${CHROME_PATH} headless=${HEADLESS}`);

  // Sanity: server reachable
  await api('/api/status');
  log('INFO', 'server up');

  // Drain any lingering one-shot before we start — otherwise the picker
  // will show unrelated entries and confuse assertions.
  try {
    await api('/api/run/stop', { method: 'POST' });
  } catch {}

  browser = await puppeteer.launch({
    executablePath: CHROME_PATH,
    headless: HEADLESS,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });
  page = await browser.newPage();
  await page.setViewport({ width: 1400, height: 900 });

  page.on('pageerror', (err) => log('PAGE_ERROR', err.message));
  page.on('console', (msg) => {
    if (msg.type() === 'error') log('CONSOLE_ERR', msg.text());
  });

  await page.goto(BASE_URL, { waitUntil: 'networkidle2', timeout: 15000 });
  log('INFO', 'loaded SPA');

  // -------------------- Test 1: empty picker state --------------------
  // Navigate to Debug tab if there is one. The picker lives in DebugView.
  try {
    await page.evaluate(() => {
      // Click the Debug nav entry — Rakitsu uses various button labels
      // depending on the build. Try a few.
      const buttons = Array.from(document.querySelectorAll('button, a, [role="tab"]'));
      const debugBtn = buttons.find((b) => /debug/i.test(b.textContent || ''));
      if (debugBtn) debugBtn.click();
    });
  } catch {
    /* not fatal — picker is in the toolbar which should always be mounted */
  }
  await new Promise((r) => setTimeout(r, 500));

  const pickerVisible = await page.$('.session-picker');
  await record('picker-mounted', !!pickerVisible);
  if (!pickerVisible) {
    log('INFO', 'SessionPicker selector .session-picker not found — picker may not be in current view');
  }

  // -------------------- Test 2: open dropdown, read empty-or-current state --------------------
  if (pickerVisible) {
    await page.click('.session-picker .picker-trigger');
    await new Promise((r) => setTimeout(r, 300));
    const menuVisible = await page.$('.session-picker .picker-menu');
    await record('picker-opens', !!menuVisible);
    // Close for later tests
    await page.click('body');
    await new Promise((r) => setTimeout(r, 200));
  }

  // -------------------- Test 3: spawn 3 chats, verify picker picks them up --------------------
  const chatIds = [];
  for (let i = 0; i < 3; i++) {
    const r = await api('/api/chat/start', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ config_id: CHAT_CONFIG }),
    });
    chatIds.push(r.id);
  }
  log('INFO', `started ${chatIds.length} chats: ${chatIds.map((x) => x.slice(0, 8)).join(', ')}`);
  // Backend sanity check — if the registry doesn't have them, the UI can't either.
  await new Promise((r) => setTimeout(r, 300));
  const backendList = await api('/api/runtime/sessions');
  log('INFO', `backend /api/runtime/sessions returned ${backendList.length} entries`);
  const backendChatIds = backendList.filter((s) => s.mode === 'chat').map((s) => s.id);
  log('INFO', `  chat ids: ${backendChatIds.map((x) => x.slice(0, 8)).join(', ') || '(none)'}`);

  // The picker polls every 2s — give it up to 5s.
  // Open the dropdown and count the rendered items while it's open.
  const foundAll = await waitFor(async () => {
    // Open (idempotent — clicking an open trigger toggles, so re-query state first)
    const isOpen = await page.$('.session-picker .picker-menu');
    if (!isOpen) {
      await page.click('.session-picker .picker-trigger');
      await new Promise((r) => setTimeout(r, 200));
    }
    // Use a selector that works for both old (pre-Stop-button) and new
    // picker layouts: .item-id lives either directly under .picker-item or
    // inside .picker-item-main.
    const items = await page.$$eval('.session-picker .picker-menu .item-id', (els) =>
      els.map((e) => e.textContent?.trim() ?? ''),
    );
    const matched = chatIds.every((id) => items.some((label) => id.startsWith(label)));
    if (!matched) {
      log('DEBUG', `  picker items: [${items.join(', ')}] (looking for ${chatIds.map((x) => x.slice(0, 8)).join(', ')})`);
    }
    return matched;
  }, 5000);
  await record(
    'picker-reflects-chat-starts',
    foundAll,
    foundAll ? '3 chats visible in dropdown' : 'picker did not refresh within 5s',
  );

  // -------------------- Test 4: click Stop on one entry --------------------
  // Feature-detect: if the binary predates the Stop button, skip these
  // tests with a clear note instead of failing.
  const hasStopButton = await page.evaluate(() => !!document.querySelector('.session-picker .stop-btn'));
  if (foundAll && hasStopButton) {
    // Stop the first one we started specifically — matching by id tail.
    const stopTarget = chatIds[0];
    const stopClicked = await page.evaluate((tail) => {
      const rows = Array.from(document.querySelectorAll('.session-picker .picker-menu .picker-item'));
      for (const row of rows) {
        const id = row.querySelector('.item-id')?.textContent?.trim() ?? '';
        if (tail.startsWith(id)) {
          const btn = row.querySelector('.stop-btn');
          if (btn) {
            btn.click();
            return true;
          }
        }
      }
      return false;
    }, stopTarget);
    await record('picker-stop-button-clickable', stopClicked, `target=${stopTarget.slice(0, 8)}`);

    // Backend should have drained just that one.
    await new Promise((r) => setTimeout(r, 500));
    const list = await api('/api/runtime/sessions');
    const stillThere = list.some((s) => s.id === stopTarget);
    await record(
      'stop-button-drains-target-from-registry',
      !stillThere,
      stillThere ? `${stopTarget} still in registry` : 'target removed',
    );
  } else if (foundAll) {
    results.push({
      name: 'picker-stop-button-clickable',
      ok: true,
      detail: 'skipped — binary predates Stop button',
    });
    log('SKIP', 'picker-stop-button-clickable — binary predates Stop button');
  }

  // -------------------- Phase 2: URL hash deep link + picker-drives-pin --------------------
  // These tests validate the session-registry phase 2 plan. The picker should
  // (a) write `#debug/session/<id>` to window.location.hash when a session is
  // selected, and (b) restore that selection when the hash is already present
  // on page load. Unlike the Stop-button check above, there's no feature
  // detection here -- this records a straight pass/fail, so it assumes the
  // binary under test already includes Phase 2.
  const remaining = await api('/api/runtime/sessions').then((l) => l.filter((s) => chatIds.includes(s.id)));
  const pinTarget = remaining.length > 0 ? remaining[0] : null;

  if (pinTarget) {
    // ensure picker is mounted + menu visible
    if (pickerVisible) {
      const open = await page.$('.session-picker .picker-menu');
      if (!open) {
        await page.click('.session-picker .picker-trigger');
        await new Promise((r) => setTimeout(r, 250));
      }

      // Click the row whose id starts with pinTarget.id
      await page.evaluate((fullId) => {
        const rows = Array.from(document.querySelectorAll('.session-picker .picker-menu .picker-item'));
        for (const row of rows) {
          const id = row.querySelector('.item-id')?.textContent?.trim() ?? '';
          if (fullId.startsWith(id)) {
            const main = row.querySelector('.picker-item-main');
            if (main) main.click();
            return;
          }
        }
      }, pinTarget.id);

      await new Promise((r) => setTimeout(r, 300));
      const hash = await page.evaluate(() => window.location.hash || '');
      const expected = `#debug/session/${encodeURIComponent(pinTarget.id)}`;
      await record(
        'probe-picker-drives-debug',
        hash === expected,
        hash === expected
          ? 'location.hash updated on picker selection'
          : `expected ${expected}, got "${hash}"`,
      );

      // -------------------- probe-url-hash-persists-pin --------------------
      // Reload the page with the hash already set — picker should restore the
      // pin from the hash.
      await page.goto(`${BASE_URL}/${expected}`, { waitUntil: 'networkidle2', timeout: 15000 });
      await new Promise((r) => setTimeout(r, 500));
      // Navigate to Debug tab again post-reload.
      try {
        await page.evaluate(() => {
          const buttons = Array.from(document.querySelectorAll('button, a, [role="tab"]'));
          const debugBtn = buttons.find((b) => /debug/i.test(b.textContent || ''));
          if (debugBtn) debugBtn.click();
        });
      } catch {
        /* toolbar should be mounted anyway */
      }
      await new Promise((r) => setTimeout(r, 400));
      // The picker trigger should now display the pinned session.
      const triggerLabel = await page.evaluate(() => {
        const t = document.querySelector('.session-picker .picker-trigger .label');
        return t ? t.textContent?.trim() ?? '' : '';
      });
      // We tolerate both "[chat] …" (selected) and "… active" (not yet
      // reflected). A slice of the id must appear in either case for the
      // selection to count as restored.
      const idSlice = pinTarget.id.slice(0, 8);
      await record(
        'probe-url-hash-persists-pin',
        triggerLabel.includes(idSlice),
        triggerLabel.includes(idSlice)
          ? `picker restored pin for ${idSlice}`
          : `trigger="${triggerLabel}" — id ${idSlice} not reflected`,
      );

      // -------------------- probe-workspace-decoupled --------------------
      // useWorkspace keeps currentConfigId in-memory only — we observe a
      // proxy: the Builder tab's active config display. Picking a session
      // must not mutate that display. Because the Builder tab isn't visible
      // from the Debug tab, we approximate by querying any DOM node carrying
      // data-workspace-config-id. If the selector isn't present we skip —
      // this guards the invariant via code review, not DOM probing.
      const others = remaining.filter((s) => s.id !== pinTarget.id);
      if (others.length > 0) {
        const before = await page.evaluate(() => {
          const el = document.querySelector('[data-workspace-config-id]');
          return el ? el.getAttribute('data-workspace-config-id') : null;
        });
        const other = others[0];
        await page.evaluate((fullId) => {
          history.replaceState(null, '', `#debug/session/${encodeURIComponent(fullId)}`);
          window.dispatchEvent(new HashChangeEvent('hashchange'));
        }, other.id);
        await new Promise((r) => setTimeout(r, 300));
        const after = await page.evaluate(() => {
          const el = document.querySelector('[data-workspace-config-id]');
          return el ? el.getAttribute('data-workspace-config-id') : null;
        });
        if (before === null && after === null) {
          results.push({
            name: 'probe-workspace-decoupled',
            ok: true,
            detail: 'skipped — no [data-workspace-config-id] marker in DOM',
          });
          log('SKIP', 'probe-workspace-decoupled — no DOM marker');
        } else {
          await record(
            'probe-workspace-decoupled',
            before === after,
            before === after
              ? 'workspace config unchanged on pick'
              : `before="${before}" after="${after}"`,
          );
        }
      } else {
        results.push({
          name: 'probe-workspace-decoupled',
          ok: true,
          detail: 'skipped — only one session available to pin',
        });
      }
    } else {
      results.push({ name: 'probe-picker-drives-debug', ok: true, detail: 'skipped — picker not mounted' });
      results.push({ name: 'probe-url-hash-persists-pin', ok: true, detail: 'skipped — picker not mounted' });
      results.push({ name: 'probe-workspace-decoupled', ok: true, detail: 'skipped — picker not mounted' });
    }
  } else {
    // Every chat got stopped before we could pin — skip cleanly.
    results.push({ name: 'probe-picker-drives-debug', ok: true, detail: 'skipped — no live chat available to pin' });
    results.push({ name: 'probe-url-hash-persists-pin', ok: true, detail: 'skipped — no live chat available to pin' });
    results.push({ name: 'probe-workspace-decoupled', ok: true, detail: 'skipped — no live chat available to pin' });
  }

  // -------------------- Test 5: cleanup + verify our spawned chats drain --------------------
  // Don't assert the whole registry is empty — stray sessions from prior runs
  // or other tabs may exist. We only own the ones we started.
  for (const id of chatIds) {
    try {
      await api(`/api/chat/${id}/stop`, { method: 'POST' });
    } catch {
      /* already stopped, fine */
    }
  }
  const ourChatsGone = await waitFor(async () => {
    const list = await api('/api/runtime/sessions');
    const ours = list.filter((s) => chatIds.includes(s.id));
    return ours.length === 0;
  }, 5000);
  await record('our-spawned-chats-drain', ourChatsGone);

  // -------------------- Summary --------------------
  await browser.close();
  const passed = results.filter((r) => r.ok).length;
  const failed = results.length - passed;
  console.log('\n=====================================');
  console.log(`  Passed: ${passed} / ${results.length}`);
  console.log('=====================================');
  if (failed > 0) {
    console.log('Failures:');
    for (const r of results.filter((x) => !x.ok)) console.log(`  - ${r.name}: ${r.detail}`);
    process.exit(1);
  }
}

main().catch(async (err) => {
  console.error('FATAL:', err);
  try {
    if (page) {
      await mkdir(ARTIFACTS, { recursive: true });
      await page.screenshot({ path: join(ARTIFACTS, 'fatal.png'), fullPage: true });
    }
  } catch {}
  try {
    if (browser) await browser.close();
  } catch {}
  process.exit(1);
});
