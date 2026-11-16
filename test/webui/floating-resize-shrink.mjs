import assert from 'node:assert/strict';
import puppeteer from 'puppeteer-core';

const baseUrl = process.env.RAKITSU_BASE_URL || 'http://127.0.0.1:9100';
const executablePath = process.env.CHROME_PATH ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--disable-dev-shm-usage'] });
try {
  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 900 });
  await page.goto(baseUrl);
  await page.locator('button[aria-label="Chat layout options"]').click();
  await page.locator('.pane-menu button::-p-text(Float)').click();
  const panel = '.pane-floating[aria-label=Chat]';
  const point = async selector => (await page.waitForSelector(selector, { visible: true })).clickablePoint();
  const title = await point(`${panel} .float-title span`);
  await page.mouse.move(title.x, title.y);
  await page.mouse.down();
  await page.mouse.move(title.x + 620, title.y + 220, { steps: 8 });
  await page.mouse.up();
  const grip = await point(`${panel} .float-resize`);
  await page.mouse.move(grip.x, grip.y);
  await page.mouse.down();
  await page.mouse.move(grip.x - 12, grip.y - 12);
  await page.setViewport({ width: 400, height: 250 });
  await page.waitForFunction(selector => document.querySelector(selector).getBoundingClientRect().left < 100, {}, panel);
  await page.mouse.move(350, 220, { steps: 5 });
  await page.mouse.up();
  const bounds = await page.$eval(panel, element => {
    const panelRect = element.getBoundingClientRect();
    const workspaceRect = element.closest('.dock').getBoundingClientRect();
    return {
      width: panelRect.width,
      height: panelRect.height,
      contained: panelRect.left >= workspaceRect.left && panelRect.top >= workspaceRect.top &&
        panelRect.right <= workspaceRect.right && panelRect.bottom <= workspaceRect.bottom,
      widthStyle: element.style.width,
      heightStyle: element.style.height,
    };
  });
  console.log(bounds);
  assert(bounds.contained && bounds.width >= 280 && bounds.height >= 180,
    'The floating panel must stay usable and in bounds when the viewport shrinks during resize');
} finally {
  await browser.close();
}
