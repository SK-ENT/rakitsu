import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import puppeteer from 'puppeteer-core';

const base = process.env.RAKITSU_BASE_URL || 'http://127.0.0.1:9100';
const fixture = process.env.RAKITSU_TIMELINE_CONFIG;
assert(fixture, 'Set RAKITSU_TIMELINE_CONFIG to a runnable local test config');

async function api(path, body) {
  const response = await fetch(base + path, body === undefined ? undefined : {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  assert(response.ok, `${path} returned ${response.status}`);
  return response.json();
}

const query = `Workspace layout verification ${Date.now()}`;
const { id: configId } = await api('/api/configs/inline', { yaml: await readFile(fixture, 'utf8') });
await api('/api/run', { config_id: configId, query, timeout: 60 });
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
  args: ['--disable-dev-shm-usage'],
});

try {
  const page = await browser.newPage();
  await page.setViewport({ width: 1500, height: 980 });
  await page.goto(base);
  const layout = '[aria-label="Workspace layout"]';
  await page.select(layout, 'debug');
  const panels = ['Builder', 'Debugger', 'Run Log', 'Run Timeline'];
  const panel = name => `.pane-panel[aria-label="${name}"]`;
  for (const name of panels) await page.waitForSelector(panel(name), { visible: true });
  await page.waitForFunction(async () => (await (await fetch('/api/run')).json()).status === 'completed');
  const run = (await api('/api/sessions')).find(session => session.query === query);
  assert(run?.id);
  const selector = '.run-timeline select';
  await page.locator('.run-timeline button::-p-text(Refresh runs)').click();
  await page.waitForFunction((s, id) => [...document.querySelector(s).options].some(option => option.value === id), {}, selector, run.id);
  await page.select(selector, run.id);
  const cursor = '[aria-label="Replay position"]';
  await page.waitForSelector(cursor, { visible: true });
  await page.locator('.run-timeline button::-p-text(Next event)').click();
  const verify = async () => {
    assert.equal(await page.$eval(selector, input => input.value), run.id);
    assert.equal(await page.$eval(cursor, input => Number(input.value)), 1);
    await page.waitForSelector('.debug-view .execution-diagram', { visible: true });
    await page.waitForFunction(() => Number(document.querySelector('.debug-view input[type="range"]')?.value) === 1);
  };
  await verify();
  for (const preset of ['replay', 'debug', 'replay']) {
    await page.select(layout, preset);
    await verify();
    const bounds = await Promise.all(panels.map(async name => (await page.$(panel(name))).boundingBox()));
    assert(bounds.every(Boolean));
    assert(bounds[0].x < bounds[1].x && bounds[0].y === bounds[1].y);
    assert(bounds[2].y > bounds[0].y && bounds[2].y === bounds[3].y);
    assert.equal(bounds[2].x < bounds[3].x, preset === 'debug');
  }
  const transport = await page.evaluate((layoutSelector, cursorSelector) => {
    const input = document.querySelector(cursorSelector);
    const select = document.querySelector(layoutSelector);
    const timeline = input.closest('.run-timeline');
    const play = [...timeline.querySelectorAll('button')].find(button => button.textContent.trim() === 'Play');
    play.click();
    select.value = 'debug';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    return input.value;
  }, layout, cursor);
  assert.equal(Number(transport), 1);
  await page.waitForFunction(selector => Number(document.querySelector(selector)?.value) > 1, {}, cursor);
  await page.evaluate(selector => {
    const slider = document.querySelector(selector);
    slider.value = '1';
    slider.dispatchEvent(new Event('input', { bubbles: true }));
  }, cursor);
  await page.select(layout, 'replay');
  await verify();

  for (const name of panels) {
    const options = `button[aria-label="${name} layout options"]`;
    await page.locator(options).click();
    await page.locator('.pane-menu button::-p-text(Float)').click();
    await page.waitForSelector(panel(name) + '.pane-floating', { visible: true });
    if (name === 'Run Timeline') {
      const picker = await page.waitForSelector('.builder-canvas .wallpaper-picker', { visible: true });
      const title = await page.waitForSelector(panel(name) + ' .float-title', { visible: true });
      const a = await picker.boundingBox();
      const b = await title.boundingBox();
      assert(a && b);
      await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
      await page.mouse.down();
      await page.mouse.move(a.x + a.width / 2, a.y - 40, { steps: 12 });
      await page.mouse.up();
      const overlap = await page.evaluate(() => {
        const picker = document.querySelector('.builder-canvas .wallpaper-picker');
        const timeline = document.querySelector('.pane-floating[aria-label="Run Timeline"]');
        if (!picker || !timeline) return 'missing wallpaper picker or floating timeline';
        const a = picker.getBoundingClientRect();
        const b = timeline.getBoundingClientRect();
        const left = Math.max(a.left, b.left);
        const right = Math.min(a.right, b.right);
        const top = Math.max(a.top, b.top);
        const bottom = Math.min(a.bottom, b.bottom);
        if (right <= left || bottom <= top) return 'picker and timeline do not overlap';
        const hit = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
        return timeline.contains(hit) ? null : 'the floating timeline lost pointer input';
      });
      assert.equal(overlap, null);
    }
    await page.locator(options).click();
    await page.locator('.pane-menu button::-p-text(Dock as new pane)').click();
    await page.waitForSelector(panel(name) + ':not(.pane-floating)', { visible: true });
    await verify();
  }
  await page.select(layout, 'replay');
  for (const width of [1024, 768, 390]) {
    await page.setViewport({ width, height: 850 });
    await page.waitForFunction(() => {
      const panel = name => document.querySelector(`.pane-panel[aria-label="${name}"]`);
      const builder = panel('Builder').getBoundingClientRect();
      const graph = panel('Debugger').getBoundingClientRect();
      return graph.top >= builder.bottom && graph.width === builder.width;
    });
    await verify();
    for (const name of panels) {
      assert(await page.$eval(panel(name) + ' .pane-body', el => el.scrollWidth <= el.clientWidth + 1),
        `${name} overflows at ${width}px`);
    }
  }
  console.log('PASS narrow presets stack without horizontal pane overflow and preserve replay');
  console.log('PASS Debug and Replay preserve the same recording and event cursor, and all four panes float and redock');
} finally {
  await browser.close();
}
