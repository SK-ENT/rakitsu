import assert from 'node:assert/strict'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const puppeteer = (await import(process.env.UI_LAB_PUPPETEER
  ? pathToFileURL(process.env.UI_LAB_PUPPETEER).href
  : 'puppeteer-core')).default
const root = new URL('../', import.meta.url)
const intake = process.argv.includes('--intake')
const settings = intake ? {
  title: 'Intake Watch', site: 'Harbor Utilities', firstMetric: 'Flow rate',
  secondMetric: 'Pumps online', openMetric: 'Open flags', heading: 'Pump',
  metricValues: ['860 L/s', '5 / 6'], rows: ['Pump M1', 'Pump N2', 'Pump P4'],
  statuses: ['Pressure drift', 'Intake blocked', 'Nominal'],
  review: 'Review flag', save: 'Save triage', done: 'Triaged',
} : {
  title: 'Sort Control', site: 'Parcel Operations', firstMetric: 'Parcels per hour',
  secondMetric: 'Active lanes', openMetric: 'Open alerts', heading: 'Lane',
  metricValues: ['1,240', '7 / 8'], rows: ['Lane A1', 'Lane B2', 'Lane C4'],
  statuses: ['Jam detected', 'Scanner offline', 'Running'],
  review: 'Review alert', save: 'Save acknowledgement', done: 'Acknowledged',
}
const assets = new Map([
  ['/kit/tokens.css', 'text/css'],
  ['/kit/components.js', 'text/javascript'],
  ...['apps/observatory', 'apps/rail-yard', 'generated/baseline', 'generated/recall', 'generated/repaired', 'generated/intake-control', 'generated/intake-learned']
    .map(dir => ['/' + dir + '/index.html', 'text/html']),
])
const server = createServer(async (request, response) => {
  if (request.url === '/favicon.ico') return response.writeHead(204).end()
  try {
    assert(assets.has(request.url))
    response.writeHead(200, {
      'Content-Type': assets.get(request.url),
      'Content-Security-Policy': "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'",
    })
    response.end(await readFile(new URL(request.url.slice(1), root)))
  } catch {
    response.end()
  }
})
await new Promise((done, reject) => server.once('error', reject).listen(0, '127.0.0.1', done))
const origin = 'http://127.0.0.1:' + server.address().port
const out = new URL(intake ? '../followup/evidence/' : '../evidence/', import.meta.url)
await mkdir(out, { recursive: true })
const results = {}
const selector = {
  filter: 'ops-queue input',
  open: 'dialog[open]',
}
const labels = {
  observatory: ['Observatory control', 'Perseus drift', 'Review observation', 'Observation review'],
  'rail-yard': ['Rail yard control desk', 'Northbound produce', 'New movement', 'New movement'],
}
const note = '<img src=x onerror=alert(1)>'
const browser = await puppeteer.launch({
  executablePath: process.env.UI_LAB_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
  args: ['--no-first-run', '--disable-background-networking'],
}).catch(error => { server.close(); throw error })

async function inspect(dir) {
  const name = dir.split('/').at(-1)
  const page = await browser.newPage()
  const errors = []
  const loaded = new Set()
  const checks = results[name] = {}
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.ok()) loaded.add(new URL(response.url()).pathname) })
  await page.setViewport({ width: 1130, height: 730 })
  await page.goto(origin + '/' + dir + '/index.html')
  await page.screenshot({ path: resolve(intake ? 'followup/evidence' : 'evidence', name + '-desktop.png'), fullPage: true })
  const text = (selector = 'body') => page.$eval(selector, node => node.innerText)
  const check = async (id, test) => {
    try { await test(); checks[id] = { status: 'Pass' } }
    catch (error) { checks[id] = { status: 'Fail', detail: error.message } }
  }
  const clickText = async (label, scope = 'body') => {
    const buttons = await page.$$(scope + ' button')
    for (const button of buttons) {
      const match = await button.evaluate((node, label) =>
        node.innerText.trim().includes(label) && !node.disabled && node.getClientRects().length > 0, label)
      if (match) { await button.click(); return }
    }
    assert.fail('Missing visible button: ' + label)
  }
  const field = async value => {
    await page.$eval(selector.filter, (node, value) => {
      node.value = value
      node.dispatchEvent(new Event('input', { bubbles: true }))
    }, value)
  }
  await check('REU-01', async () => {
    assert(loaded.has('/kit/tokens.css') && loaded.has('/kit/components.js'), 'Shared assets were not loaded')
    assert(await page.evaluate(() => ['ops-queue', 'ops-metric', 'ops-drawer'].every(tag => {
      const registered = customElements.get(tag)
      const nodes = [...document.querySelectorAll(tag)]
      return registered && nodes.length > 0 && nodes.every(node => node instanceof registered)
    })), 'Expected three registered component types')
  })
  if (name in labels) {
    const [heading, row, trigger, dialogHeading] = labels[name]
    await check('Fixture interaction', async () => {
      assert((await text('h1')).includes(heading))
      await field('no matches')
      assert.match(await text('ops-queue'), /no matching/i)
      await field('')
      if (name === 'rail-yard') {
        await clickText(trigger)
        assert.match(await text(selector.open), new RegExp(dialogHeading))
        await clickText('Save changes', selector.open)
        await page.waitForFunction(() => document.querySelector('#new-job').innerText.includes('saved'), { timeout: 1500 })
        await clickText(row, 'ops-queue')
        assert.match(await text(selector.open), /Review YD-205/)
      } else {
        await clickText(row, 'ops-queue')
        assert.match(await text('#selected-title'), /Perseus drift/)
        await clickText(trigger)
        assert.match(await text(selector.open), new RegExp(dialogHeading))
        await clickText('Save changes', selector.open)
        await page.waitForFunction(() => document.querySelector('#saved').innerText.includes('OBS-416'), { timeout: 1500 })
      }
    })
  } else {
    const body = () => text()
    const dialog = () => text(selector.open)
    const { title, site, firstMetric, secondMetric, openMetric, heading, metricValues, rows, statuses, review, save, done } = settings
    const metricSelector = `ops-metric[label="${openMetric}" i]`
    const metric = () => text(metricSelector)
    const first = new RegExp(rows[0])
    const second = new RegExp(rows[1])
    const active = () => page.evaluate(() => document.activeElement?.innerText.trim())
    await check('UX-01', async () => {
      assert.equal((await text('h1')).trim(), title)
      for (const copy of [site, firstMetric, secondMetric, openMetric, heading + ' queue', heading + ' detail', ...metricValues, ...statuses]) {
        assert((await body()).includes(copy), 'Missing initial copy: ' + copy)
      }
      assert.match(await metric(), /\b2\b/)
      assert.match(await text('ops-queue [aria-pressed="true"]'), first)
    })
    await check('UX-02', async () => {
      await field(rows[1].split(' ').at(-1).toLowerCase())
      assert.doesNotMatch(await text('ops-queue'), first)
      assert.match(await body(), first)
      await field('no such record')
      assert.match(await text('ops-queue'), /no matching/i)
      await field('')
      assert.match(await text('ops-queue [aria-pressed="true"]'), first)
      await clickText(rows[2], 'ops-queue')
      assert(!(await page.$$eval('button', (buttons, review) => buttons.some(button =>
        button.innerText.trim() === review && !button.disabled && button.getClientRects().length), review)),
      'A nominal record offers a review action')
      await clickText(rows[1], 'ops-queue')
      assert.match(await text('ops-queue [aria-pressed="true"]'), second)
    })
    await check('A11Y-01', async () => {
      const button = await page.evaluateHandle(review => [...document.querySelectorAll('button')]
        .find(node => node.innerText.trim() === review && !node.disabled), review)
      await button.evaluate(node => node.focus())
      await page.keyboard.press('Enter')
      await page.waitForSelector(selector.open, { timeout: 1500 })
      assert(await page.$eval(selector.open, node => Boolean(node.getAttribute('aria-label') || document.getElementById(node.getAttribute('aria-labelledby'))?.textContent.trim())), 'Dialog has no accessible name')
      assert.match(await dialog(), second)
      if (intake) {
        const named = await page.evaluate(() => {
          const dialog = document.querySelector('dialog[open]')
          const fields = [dialog.querySelector('select'), dialog.querySelector('textarea, input:not([type="hidden"])'), document.querySelector('ops-queue input')]
          return fields.every(field => field && (field.getAttribute('aria-label')?.trim() ||
            document.getElementById(field.getAttribute('aria-labelledby'))?.textContent.trim() ||
            [...field.labels].some(label => label.textContent.trim())))
        })
        assert(named, 'Expected accessible names on priority, note, and filter')
      }
      const outside = []
      for (const direction of ['Tab', 'Shift+Tab']) {
        if (direction === 'Shift+Tab') await page.keyboard.down('Shift')
        for (let count = 0; count < 7; count++) {
          await page.keyboard.press('Tab')
          const escaped = await page.evaluate(() => {
            const active = document.activeElement
            return !document.querySelector('dialog[open]').contains(active) ? active?.tagName ?? 'none' : ''
          })
          if (escaped) outside.push(direction + ' → ' + escaped)
        }
        if (direction === 'Shift+Tab') await page.keyboard.up('Shift')
      }
      await page.keyboard.press('Escape')
      await page.waitForFunction(() => !document.querySelector('dialog[open]'), { timeout: 1500 })
      assert.equal(await active(), review)
      assert.match(await metric(), /\b2\b/)
      assert.deepEqual(outside, [], 'Focus left the dialog during keyboard cycling: ' + outside.join(', '))
    })
    await check('UX-03', async () => {
      if (await page.$(selector.open)) await page.keyboard.press('Escape')
      await clickText(rows[1], 'ops-queue')
      await clickText(review)
      assert.match(await dialog(), second)
      const noteField = await page.$('dialog[open] textarea, dialog[open] input:not([type="hidden"])')
      assert(noteField, 'No acknowledgement note field')
      if (intake) {
        const options = await page.$$eval('dialog[open] select option', items => items.map(item => item.textContent.trim()))
        assert.deepEqual(options, ['Monitor', 'Maintenance', 'Urgent'])
        const urgent = await page.$eval('dialog[open] select', node => node.options[2].value)
        await page.select('dialog[open] select', urgent)
      }
      await noteField.type('Discard this note')
      await clickText('Cancel', selector.open)
      assert.match(await metric(), /\b2\b/)
      assert.equal(await active(), review)
      await clickText(review)
      assert.equal(await page.$eval('dialog[open] textarea, dialog[open] input:not([type="hidden"])', element => element.value), '')
      if (intake) {
        const state = await page.$eval('dialog[open] select', node => ({
          current: node.selectedOptions[0].textContent.trim(), maintenance: node.options[1].value,
        }))
        assert.equal(state.current, 'Monitor', 'Cancel did not discard the changed priority')
        await page.select('dialog[open] select', state.maintenance)
      }
      await page.type('dialog[open] textarea, dialog[open] input:not([type="hidden"])', note)
      await clickText(save, selector.open)
      await page.waitForFunction(selector => /\b1\b/.test(document.querySelector(selector)?.innerText), { timeout: 1500 }, metricSelector)
      assert.match(await body(), new RegExp(done, 'i'))
      assert.match(await active(), second)
      assert.match(await text('ops-queue'), new RegExp(statuses[0], 'i'))
      await clickText(rows[0], 'ops-queue')
      await clickText(rows[1], 'ops-queue')
      assert((await body()).includes(note), 'Saved note not visible when returning to ' + rows[1])
      if (intake) assert.match(await body(), /Maintenance/)
      assert.match(await metric(), /\b1\b/)
    })
    await check('ROB-01', async () => {
      assert.deepEqual(errors, [])
      assert.equal(await page.$$eval('img[src="x"]', nodes => nodes.length), 0)
      assert.equal(await page.$$eval('[onerror="alert(1)"]', nodes => nodes.length), 0)
    })
  }
  await check('RESP-01', async () => {
    await page.reload()
    if (intake) {
      assert(await page.evaluate(review => {
        const queue = document.querySelector('ops-queue').getBoundingClientRect()
        const trigger = [...document.querySelectorAll('button')].find(node => node.innerText.trim() === review)
        return trigger && trigger.getBoundingClientRect().right < queue.left
      }, settings.review), 'Desktop pump detail is not left of the queue')
    }
    await page.setViewport({ width: 390, height: 844 })
    await page.reload()
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Page overflows at 390px')
    assert(await page.$('ops-queue input'))
    if (!(name in labels)) {
      assert(await page.evaluate(review => {
        const queue = document.querySelector('ops-queue').getBoundingClientRect()
        const metric = document.querySelector('ops-metric').getBoundingClientRect()
        const button = [...document.querySelectorAll('button')].find(node => node.innerText.trim() === review)
        return metric.top < queue.top && button?.getBoundingClientRect().top > queue.bottom
      }, settings.review), 'Mobile order differs from metrics, queue, detail')
    }
    await page.screenshot({ path: resolve(intake ? 'followup/evidence' : 'evidence', name + '-mobile.png'), fullPage: true })
  })
  if (name in labels) await check('Page errors', async () => assert.deepEqual(errors, []))
  await page.close()
}

try {
  const explicit = process.argv.slice(2).filter(arg => arg !== '--intake')
  const defaults = intake ? ['generated/intake-control', 'generated/intake-learned'] :
    ['apps/observatory', 'apps/rail-yard', 'generated/baseline', 'generated/recall', 'generated/repaired']
  for (const dir of explicit.length ? explicit : defaults) {
    await inspect(dir)
  }
  await writeFile(new URL('browser-checks.json', out), JSON.stringify(results, null, 2) + '\n')
  console.log(JSON.stringify(results, null, 2))
  if (Object.values(results).some(checks => Object.values(checks).some(check => check.status === 'Fail'))) process.exitCode = 1
} finally {
  await browser.close()
  server.close()
}
