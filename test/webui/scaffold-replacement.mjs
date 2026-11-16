import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import puppeteer from 'puppeteer-core';

const browser = await puppeteer.launch({ executablePath: process.env.CHROME_PATH, headless: true });
try {
  const page = await browser.newPage();
  page.setDefaultTimeout(7000);
  page.on('dialog', dialog => dialog.accept());
  await page.setViewport({ width: 1600, height: 1000 });
  await page.goto(process.env.RAKITSU_BASE_URL);
  const upload = await page.waitForSelector('.visual-builder input[accept=".yaml,.yml"]');
  await upload.uploadFile(resolve(process.env.RAKITSU_TIMELINE_CONFIG));
  await page.waitForSelector('.group-node.loop');
  const toggle = label => page.locator(`.builder-toolbar button[aria-label="${label}"]`).click();
  await toggle('SVG workbench preview');
  const canvas = '[data-builder-svg-canvas]';
  const scaffold = async name => {
    await page.locator('.builder-header button[aria-label="Scaffold"]').click();
    await page.locator(`.scaffold-item::-p-text(${name})`).click();
    await page.waitForFunction(expected => document.querySelector('.project-name-input')?.value === expected, {}, name);
  };
  for (const [name, count, wires, expected] of [
    ['RAG Assistant', 4, 3, 'RAGAssistant'],
    ['LLM Chat', 1, 0, 'Assistant'],
    ['RAG Assistant', 4, 3, 'RAGAssistant'],
  ]) {
    await scaffold(name);
    await page.waitForFunction((selector, n, label) => {
      const graph = document.querySelector(selector);
      return graph?.querySelectorAll('[data-node]').length === n && graph.textContent.includes(label);
    }, {}, canvas, count, expected);
    assert.equal(await page.$$eval(canvas + ' [data-kind] .wire', items => items.length), wires);
    assert.equal(await page.$$eval(canvas + ' [data-group]', items => items.length), 0);
    await toggle('Classic canvas');
    await page.waitForFunction((n, e) => document.querySelectorAll('.builder-canvas .vue-flow__node').length === n
      && document.querySelectorAll('.builder-canvas .vue-flow__edge path.vue-flow__edge-path').length === e, {}, count, wires);
    const paths = await page.$$eval('.builder-canvas .vue-flow__edge path.vue-flow__edge-path', nodes => nodes.map(node => node.getAttribute('d')));
    assert(paths.every(path => path && !path.includes('NaN')));
    assert.equal(new Set(paths).size, wires, 'Each wire must have its own route');
    await toggle('SVG workbench preview');
    console.log('PASS scaffold replacement in SVG and Classic: ' + name);
  }
} finally {
  await browser.close();
}
