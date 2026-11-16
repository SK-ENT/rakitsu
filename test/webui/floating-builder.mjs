import assert from 'node:assert/strict';
import puppeteer from 'puppeteer-core';

const baseUrl = process.env.RAKITSU_BASE_URL || 'http://127.0.0.1:9100';
const executablePath = process.env.CHROME_PATH ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const browser = await puppeteer.launch({
  executablePath,
  headless: true,
  args: ['--disable-dev-shm-usage'],
});

try {
  for (const viewport of [
    { width: 889, height: 994 },
    { width: 1024, height: 768 },
    { width: 1440, height: 900 },
  ]) {
    const page = await browser.newPage();
    try {
      await page.setViewport(viewport);
      await page.goto(baseUrl);
      await page.locator('button[aria-label="Builder layout options"]').click();
      await page.locator('.pane-menu button::-p-text(Float)').click();

      const floatingBuilder = '.pane-floating[aria-label=Builder]';
      await page.waitForSelector(`${floatingBuilder} .builder-canvas`, { visible: true });
      const layout = await page.$eval(floatingBuilder, element => {
        const bounds = element.getBoundingClientRect();
        const canvas = element.querySelector('.builder-canvas').getBoundingClientRect();
        return {
          window: [bounds.width, bounds.height],
          canvas: [canvas.width, canvas.height],
          inViewport: bounds.left >= 0 && bounds.top >= 0 &&
            bounds.right <= innerWidth && bounds.bottom <= innerHeight,
        };
      });
      console.log(viewport, layout);
      assert(layout.inViewport, 'The default floating window must fit the viewport');
      assert(layout.canvas[0] >= 300 && layout.canvas[1] >= 220,
        'The default floating Builder must offer at least 300 × 220 canvas pixels');
    } finally {
      await page.close();
    }
  }
} finally {
  await browser.close();
}
