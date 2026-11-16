import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import puppeteer from 'puppeteer-core';

const base = process.env.RAKITSU_BASE_URL;
const yaml = await readFile(process.env.RAKITSU_TIMELINE_CONFIG, 'utf8');
async function api(path, body) {
  const response = await fetch(base + path, body ? {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  } : undefined);
  assert(response.ok, `${path}: ${response.status}`);
  return response.json();
}

const browser = await puppeteer.launch({ executablePath: process.env.CHROME_PATH, headless: true });
const page = await browser.newPage();
const tree = '.debug-view .hierarchy-tree';
const graph = '.debug-view .execution-diagram';
const log = '.run-log .log-events';
const auditId = Date.now();
const queryFor = name => `Isolation audit ${auditId}: ${name}`;
const names = ['AuditAlpha', 'AuditBravo', 'AuditCharlie', 'AuditDelta'];
let video;
try {
  await page.setViewport({ width: 1600, height: 1000 });
  await page.goto(base);
  if (process.env.RAKITSU_AUDIT_VIDEO) {
    video = await page.screencast({
      path: process.env.RAKITSU_AUDIT_VIDEO, format: 'webm', ffmpegPath: process.env.RAKITSU_FFMPEG,
    });
  }
  await page.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await page.locator('.builder-toolbar button.agent').click();
  await page.keyboard.down('Control');
  await page.keyboard.press('f');
  await page.keyboard.up('Control');
  await page.waitForFunction(() => document.activeElement?.matches('.canvas-search-bar input'));
  await page.keyboard.press('Escape');

  const layout = '[aria-label="Workspace layout"]';
  const start = async (name, paused = false) => {
    const { id } = await api('/api/configs/inline', { yaml: yaml.replaceAll('Reviewer', name) });
    const run = await api('/api/run', {
      config_id: id, query: queryFor(name), timeout: 30, debug: paused,
      breakpoints: paused ? [{ event_type: 'pre_thought', agent_name: '*' }] : [],
    });
    assert.equal(run.status, 'running');
    return run.session_id;
  };
  const check = async (selector, name, previous) => {
    await page.waitForFunction((s, current, old) => {
      const content = document.querySelector(s)?.textContent ?? '';
      return content.includes(current) && old.every(agent => !content.includes(agent));
    }, {}, selector, name, previous);
  };
  await start(names[0], true);
  await page.waitForFunction(async () => (await (await fetch('/api/debug/state')).json()).state === 'paused');
  await page.select(layout, 'debug');
  await page.waitForSelector('.debug-view .paused-indicator', { visible: true });
  await check(graph, names[0], []);
  await page.locator('.style-selector button::-p-text(Tree)').click();
  await check(tree, names[0], []);
  await page.locator('.debug-view .tree-node::-p-text(AuditAlpha)').click();
  await page.waitForSelector('.debug-view .node-editor textarea', { visible: true });
  await page.locator('.debug-view .ctrl-btn.resume').click();

  for (const [i, name] of names.entries()) {
    if (i) await start(name);
    await check(tree, name, names.slice(0, i));
    await check(log, name, names.slice(0, i));
    await page.locator('.style-selector button::-p-text(Block)').click();
    await check(graph, name, names.slice(0, i));
    await page.waitForFunction(async () => (await (await fetch('/api/run')).json()).status === 'completed');
    console.log('PASS live tree, Block graph and Run Log isolate ' + name);
    if (video) await new Promise(resolve => setTimeout(resolve, 1500));
    await page.locator('.style-selector button::-p-text(Tree)').click();
  }
  await page.select(layout, 'replay');
  await page.locator('.run-timeline button::-p-text(Refresh runs)').click();
  const history = await api('/api/sessions');
  for (const name of [names[3], names[0], names[2], names[1]]) {
    const run = history.find(item => item.query === queryFor(name));
    assert(run?.id);
    await page.waitForFunction(id => [...document.querySelectorAll('.run-timeline option')].some(option => option.value === id), {}, run.id);
    await page.select('.run-timeline select', run.id);
    const slider = '[aria-label="Replay position"]';
    await page.waitForFunction(id => document.querySelector('.run-identity')?.textContent.includes(id), {}, run.id);
    await page.waitForSelector(slider, { visible: true });
    await page.focus(slider);
    await page.keyboard.press('End');
    const other = names.filter(agent => agent !== name);
    console.log('Checking replay for ' + name);
    await check(graph, name, other);
    await check(log, name, other);
    console.log('PASS recorded graph and log isolate ' + name);
    if (video) await new Promise(resolve => setTimeout(resolve, 1200));
  }
} finally {
  await video?.stop();
  await browser.close();
}
