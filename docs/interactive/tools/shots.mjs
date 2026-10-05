// Playwright script: screenshots (and a short clip) of the web UI from a local serve.
// Usage: PW_NODE_PATH=<node_modules dir with playwright> node shots.mjs <base-url> <out-dir> <config.yaml to import>
// Called by record_shots.py, which starts the demo serve first.
import fs from 'node:fs'
import { createRequire } from 'node:module'
// ES modules ignore NODE_PATH, so resolve playwright from PW_NODE_PATH by hand.
const require = createRequire((process.env.PW_NODE_PATH || process.cwd() + '/node_modules') + '/')
const { chromium } = require('playwright')

const [base, out, yaml] = process.argv.slice(2)
fs.mkdirSync(out, { recursive: true })

const browser = await chromium.launch()
const ctx = await browser.newContext({
  viewport: { width: 1280, height: 760 },
  colorScheme: 'dark',
  recordVideo: { dir: out, size: { width: 960, height: 570 } },
})
const page = await ctx.newPage()
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
page.on('pageerror', (e) => errors.push(String(e)))

async function shot(name) {
  await page.screenshot({ path: `${out}/${name}.png` })
  console.log('shot', name)
}

await page.goto(base, { waitUntil: 'networkidle' })
await page.waitForTimeout(800)
// 1) Builder with a config imported from a YAML file.
await page.getByText('Import', { exact: false }).first().click()
await page.waitForTimeout(300)
await page.setInputFiles('input[type=file]', yaml)
await page.waitForTimeout(1200)
await page.mouse.click(30, 700) // close the menu
const close = page.locator('button', { hasText: /^x$/i })
if (await close.count()) await close.first().click()
await page.waitForTimeout(500)
await shot('builder')

// 2) Chat: the autostarted monitor session shows a timer-sourced turn.
await page.getByText('Chat', { exact: true }).first().click()
await page.waitForTimeout(1200)
await page.getByText('log-watch', { exact: true }).first().click()
await page.waitForTimeout(2500)
await shot('chat-monitor')

// 3) Chat: start a new session from the imported config and send one message.
await page.getByText('+ New').click()
await page.waitForTimeout(1500)
await page.locator('textarea').first().fill('Review this: 2 + 2 = 5')
await page.keyboard.press('Enter')
await page.waitForTimeout(2500)
await shot('chat-reply')

console.log('errors:', JSON.stringify(errors))
const video = page.video()
await ctx.close()
if (video) {
  const p = await video.path()
  const size = fs.statSync(p).size
  console.log('video bytes', size)
  if (size < 1_500_000) fs.renameSync(p, `${out}/ui-flow.webm`)
  else fs.unlinkSync(p)
}
await browser.close()
