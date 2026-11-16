import assert from 'node:assert/strict';
import puppeteer from 'puppeteer-core';

const base = process.env.RAKITSU_BASE_URL || 'http://127.0.0.1:9100';
const fixture = process.env.RAKITSU_TIMELINE_CONFIG;
assert(fixture, 'Set RAKITSU_TIMELINE_CONFIG to a local Builder YAML fixture');

const browser = await puppeteer.launch({
  executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

try {
  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 900 });
  await page.evaluateOnNewDocument(() => {
    localStorage.setItem('rakitsu-license-accepted', '1');
    localStorage.setItem('rakitsu-welcome-dismissed', '1');
  });
  await page.goto(base);
  const input = await page.waitForSelector('input[accept=".yaml,.yml"]');
  await input.uploadFile(fixture);
  await page.waitForSelector('.builder-canvas .agent-node', { visible: true });
  if (await page.$('.settings-panel .close-btn')) {
    await page.click('.settings-panel .close-btn');
  }
  const report = {};
  for (const layout of ['debug', 'replay']) {
    await page.select('[aria-label="Workspace layout"]', layout);
    await page.waitForSelector('.run-timeline', { visible: true });
    report[layout] = await page.evaluate(() => {
      const bounds = selector => document.querySelector(selector).getBoundingClientRect();
      const { width, height } = bounds('.builder-canvas');
      return { width, height, toolbar: bounds('.builder-toolbar').height };
    });
    if (process.env.RAKITSU_BUILDER_SHOTS) {
      await page.screenshot({ path: `${process.env.RAKITSU_BUILDER_SHOTS}/${layout}.png` });
    }
  }
  console.log(JSON.stringify(report));

  assert(report.debug.toolbar <= 48, 'Debug toolbar must fit on a single row');
  assert(report.replay.toolbar <= 48, 'Replay toolbar must fit on a single row');
  const add = page.locator('.builder-toolbar button::-p-text(+ Add)');
  await add.click();
  const addChoices = await page.$$eval('.builder-toolbar button', buttons =>
    buttons.filter(button => button.getClientRects().length).map(button => button.textContent.trim()));
  for (const label of ['Agent', 'Tool', 'Decision', 'Retrieval', 'Pipeline', 'Loop']) {
    assert(addChoices.some(choice => choice.endsWith(label)), label + ' is missing from + Add');
  }
  const initialAgents = await page.$$eval('.builder-canvas .agent-node', nodes => nodes.length);
  await page.locator('.builder-toolbar button.agent').click();
  await page.waitForFunction(count => document.querySelectorAll('.builder-canvas .agent-node').length === count + 1, {}, initialAgents);

  await add.click();
  await page.keyboard.press('Escape');
  assert.equal(await page.$eval('.builder-toolbar button[aria-expanded]:focus', el => el.getAttribute('aria-expanded')), 'false');
  await page.locator('.run-bar--design textarea').fill('Keep this draft');
  await page.locator('button[aria-label="Collapse run composer"]').click();
  await page.waitForSelector('.run-bar--design textarea', { hidden: true });
  await page.locator('button[aria-label="Expand run composer"]').click();
  assert.equal(await page.$eval('.run-bar--design textarea', el => el.value), 'Keep this draft');
  assert.equal(await page.$eval('.rbar-btn--run', el => el.textContent.trim()), 'Run');
  await page.setViewport({ width: 390, height: 844 });
  await page.waitForFunction(() => {
    const builder = document.querySelector('.visual-builder');
    return builder.clientWidth <= 390 && builder.scrollWidth <= builder.clientWidth;
  });
  await add.click();
  await page.waitForSelector('.builder-toolbar button.tool', { visible: true });
  await page.evaluate(() => {
    localStorage.setItem('rakitsu-debug-breakpoints', JSON.stringify([
      { event_type: 'pre_thought', agent_name: 'Reviewer' },
    ]));
  });
  await page.reload();
  await page.waitForSelector('.rbar-btn--debug', { visible: true, timeout: 5000 });
  assert.equal(await page.$eval('.rbar-btn--debug', el => el.textContent.trim()), 'Debug');
  console.log('PASS compact toolbar, node choices, Escape, draft preservation, and narrow viewport');
} finally {
  await browser.close();
}
