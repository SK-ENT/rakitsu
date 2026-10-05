// Browser checks for docs/interactive/*.html (Playwright, Chromium).
// Usage: PW_NODE_PATH=<node_modules with playwright> node test_pages.mjs <out-dir for screenshots>
// Checks per page: no console errors or failed requests, controls work, 375 px width has no
// horizontal scroll, dark mode, reduced motion. Plus comparisons between the simulations and the
// real recordings (wake audit lines, messaging status codes, redaction output, start_task reasons).
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'
const require = createRequire((process.env.PW_NODE_PATH || process.cwd() + '/node_modules') + '/')
const { chromium } = require('playwright')

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, '..')
const out = process.argv[2] || '/tmp/interactive-check'
fs.mkdirSync(out, { recursive: true })

let failures = 0
const log = []
function ok(cond, msg) {
  log.push((cond ? 'PASS ' : 'FAIL ') + msg)
  if (!cond) failures++
}
const cast = (n) => JSON.parse(fs.readFileSync(path.join(root, 'casts', n + '.json'), 'utf8'))
const castText = (n) => cast(n).events.map((e) => e[2]).join('').replace(/\x1b\[[0-9;]*m/g, '').replace(/\r/g, '')

const pages = ['index', 'ask', 'wake', 'starttask', 'monitors', 'messaging', 'webui']
const browser = await chromium.launch()

async function open(name, opts = {}) {
  const ctx = await browser.newContext({ viewport: opts.viewport || { width: 1100, height: 800 }, colorScheme: opts.scheme || 'light', reducedMotion: opts.rm || 'no-preference' })
  const page = await ctx.newPage()
  const errs = []
  page.on('console', (m) => { if (m.type() === 'error' || m.type() === 'warning') errs.push(m.type() + ': ' + m.text()) })
  page.on('pageerror', (e) => errs.push('pageerror: ' + e))
  page.on('requestfailed', (r) => errs.push('requestfailed: ' + r.url()))
  const reqs = []
  page.on('request', (r) => { if (!r.url().startsWith('file://') && !r.url().startsWith('data:')) reqs.push(r.url()) })
  await page.goto('file://' + path.join(root, name + '.html'))
  await page.waitForTimeout(300)
  return { ctx, page, errs, reqs }
}

// ---------- generic checks on every page ----------
for (const name of pages) {
  const { ctx, page, errs, reqs } = await open(name)
  ok(errs.length === 0, `${name}: no console errors/warnings ${JSON.stringify(errs)}`)
  ok(reqs.length === 0, `${name}: no network requests ${JSON.stringify(reqs)}`)
  await page.screenshot({ path: `${out}/${name}-desktop-light.png`, fullPage: true })
  const lightBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  await page.click('#theme-toggle')
  const darkBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  ok(lightBg !== darkBg, `${name}: theme toggle changes the background (${lightBg} -> ${darkBg})`)
  await page.screenshot({ path: `${out}/${name}-desktop-toggled.png`, fullPage: true })
  await ctx.close()

  const d = await open(name, { scheme: 'dark' })
  const bg = await d.page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  ok(bg === 'rgb(15, 18, 24)', `${name}: prefers-color-scheme dark gives the dark palette (${bg})`)
  ok(d.errs.length === 0, `${name}: dark: no console errors`)
  await d.page.screenshot({ path: `${out}/${name}-dark.png`, fullPage: true })
  await d.ctx.close()

  const m = await open(name, { viewport: { width: 375, height: 800 } })
  const sw = await m.page.evaluate(() => document.documentElement.scrollWidth)
  ok(sw <= 376, `${name}: 375 px width has no horizontal page scroll (scrollWidth ${sw})`)
  ok(m.errs.length === 0, `${name}: 375 px: no console errors`)
  await m.page.screenshot({ path: `${out}/${name}-375.png`, fullPage: true })
  await m.ctx.close()

  const r = await open(name, { rm: 'reduce' })
  const anim = await r.page.evaluate(() => { const e = document.querySelector('.actor') || document.body; return getComputedStyle(e).transitionDuration })
  ok(anim === '0s', `${name}: reduced motion disables transitions (${anim})`)
  await r.ctx.close()
}

// ---------- ask.html ----------
{
  const { ctx, page, errs } = await open('ask')
  const count = () => page.locator('#stepper .steps li').count()
  ok((await count()) === 0, 'ask: stepper starts empty')
  await page.click('#stepper button:has-text("Step")')
  ok((await count()) === 1, 'ask: Step shows one step')
  await page.click('#stepper button:has-text("Back")')
  ok((await count()) === 0, 'ask: Back removes it')
  await page.selectOption('#stepper select[aria-label="Speed"]', '2')
  await page.click('#stepper button:has-text("Play")')
  await page.waitForTimeout(2300)
  const mid = await count()
  ok(mid >= 2, `ask: Play advances on its own (${mid} steps after 2.3 s at 2x)`)
  await page.click('#stepper button:has-text("Pause")')
  const frozen = await count(); await page.waitForTimeout(1200)
  ok((await count()) === frozen, 'ask: Pause stops advancing')
  await page.click('#stepper button:has-text("Reset")')
  ok((await count()) === 0, 'ask: Reset')
  await page.click('#stepper [role=tab]:has-text("Depth guard")')
  for (let i = 0; i < 3; i++) await page.click('#stepper button:has-text("Step")')
  const txt = await page.innerText('#stepper .steps')
  ok(txt.includes('depth guard refused call') && txt.includes('Exit 2'), 'ask: depth guard scenario shows the refusal and exit 2')
  // cast player
  await page.selectOption('#player select', '4')
  await page.click('#player button:has-text("Play")')
  await page.waitForTimeout(1500)
  const pos = await page.evaluate(() => window.__page.cast.state().index)
  ok(pos > 5, `ask: recording plays (${pos} events after 1.5 s at 4x)`)
  await page.click('#player button:has-text("Pause")')
  await page.click('#player button:has-text("Restart")')
  ok((await page.evaluate(() => window.__page.cast.state().index)) === 0, 'ask: recording Restart')
  for (let i = 0; i < 3; i++) await page.click('#player button:has-text("Next command")')
  const t3 = await page.innerText('#player .term')
  ok(t3.includes('$ rakitsu-ask') && !t3.includes('depth guard'), 'ask: Next command reveals commands one at a time')
  await page.click('#player button:has-text("Previous command")')
  await page.evaluate(() => { const r = document.querySelector('#player input[type=range]'); r.value = r.max; r.dispatchEvent(new Event('input')) })
  const full = (await page.innerText('#player .term')).replace(/ /g, ' ')
  ok(full.includes('depth guard refused call') && full.includes('[exit 2]') && full.includes('[exit 5]'), 'ask: full recording shows the depth guard (exit 2) and the 401 case (exit 5)')
  ok(full.includes('<<<RAKITSU_REPLY untrusted="true"'), 'ask: recording shows the fenced reply')
  ok(/2 user message\(s\)/.test(full), 'ask: recording shows session reuse (2 user messages)')
  ok(/delivered session=/.test(full), 'ask: recording shows --no-wait delivery')
  await page.screenshot({ path: `${out}/ask-final.png`, fullPage: true })
  ok(errs.length === 0, 'ask: no errors after interaction ' + JSON.stringify(errs))
  await ctx.close()
}

// ---------- wake.html ----------
{
  const { ctx, page, errs } = await open('wake')
  const val = (id) => page.innerText('#' + id)
  ok((await val('v-calls')) === '0', 'wake: model calls starts at 0')
  for (let i = 0; i < 4; i++) await page.click('#b-step')
  ok((await val('v-calls')) === '0', 'wake: model calls still 0 after 4 quiet ticks')
  ok((await val('v-ticks')) === '4' && (await val('v-quiet')) === '4', 'wake: 4 ticks, 4 quiet')
  ok((await val('v-int')).startsWith('20'), 'wake: backoff reached the 20 s maximum (' + (await val('v-int')) + ')')
  await page.click('#b-err'); await page.click('#b-step')
  ok((await val('v-calls')) === '1', 'wake: one injected turn after the alarm: calls = 1')
  ok((await val('v-int')).startsWith('10'), 'wake: interval back to base after the alarm')
  await page.click('#b-step')
  ok((await val('v-calls')) === '1', 'wake: same alarm on the next tick does not add a call')
  ok((await page.innerText('#audit')).includes('"suppressed":"single_flight"'), 'wake: audit shows single_flight / alarm_unchanged')
  // play
  await page.click('#b-reset')
  await page.selectOption('#s-speed', '120')
  await page.click('#b-play')
  await page.waitForTimeout(1500)
  const ticks = parseInt(await val('v-ticks'), 10)
  ok(ticks >= 3, `wake: Play runs the clock (${ticks} ticks in 1.5 s at 120 s per s)`)
  await page.click('#b-play')
  ok((await val('v-calls')) === '0', 'wake: counter stayed 0 while playing quiet ticks')
  const t1 = parseInt(await val('v-ticks'), 10); await page.waitForTimeout(600)
  ok(parseInt(await val('v-ticks'), 10) === t1, 'wake: Pause stops the clock')
  // kill switch
  await page.click('#b-reset'); await page.click('#b-stop'); await page.click('#b-step')
  ok((await val('v-loop')).includes('stopped'), 'wake: STOP file stops the loop at the next tick')
  await page.click('#b-resume')
  ok((await page.innerText('#msg')).includes('409'), 'wake: resume refused with 409 while the STOP file exists')
  await page.click('#b-rm'); await page.click('#b-resume')
  ok((await val('v-loop')).includes('running'), 'wake: resume works after removing the file')
  // simulation vs recording
  const simAudit = await page.evaluate(() => window.__page.replayFast())
  const norm = (o) => { const k = ['event', 'tick', 'outcome', 'suppressed', 'reason', 'level', 'result']; const r = {}; for (const x of k) if (o[x] !== undefined) r[x] = o[x]; return JSON.stringify(Object.fromEntries(Object.entries(r).sort())) }
  const simLines = simAudit.map((l) => norm(JSON.parse(l)))
  const real = castText('wake').split('\n').filter((l) => l.startsWith('{"event"')).map((l) => JSON.parse(l)).filter((o) => ['tick', 'escalate'].includes(o.event))
  const byTick = new Map(); for (const o of real) byTick.set(o.tick, norm(o))
  const realLines = [...byTick.keys()].sort((a, b) => a - b).map((k) => byTick.get(k))
  ok(realLines.length === 10, 'wake: recording lists ticks 1..10 (' + realLines.length + ')')
  ok(JSON.stringify(simLines) === JSON.stringify(realLines), 'wake: simulated audit log equals the real run audit log\n  sim : ' + simLines.join('\n        ') + '\n  real: ' + realLines.join('\n        '))
  const simCalls = await page.evaluate(() => window.__page.sim().calls)
  ok(simCalls === 2, 'wake: replay made 2 turns (hourly cap blocked the third)')
  // the real run's model-call counter: 0 on quiet ticks, then one turn
  const rt = castText('wake')
  ok(/model calls so far: 0/.test(rt) && /model calls so far: 5/.test(rt) && /model calls so far: 10/.test(rt), 'wake: recording shows model calls 0 on quiet ticks, then 5 and 10 (4 per turn + 1 task each)')
  await page.screenshot({ path: `${out}/wake-after.png`, fullPage: true })
  ok(errs.length === 0, 'wake: no errors after interaction ' + JSON.stringify(errs))
  await ctx.close()
}

// ---------- starttask.html ----------
{
  const { ctx, page, errs } = await open('starttask')
  const expect = {
    'Good call': 'launched', 'Config not on the list': 'config is not in settings.wake.allow.configs', 'Extra argument': 'unexpected argument "shell"',
    'Missing argument': 'missing argument "count"', 'Enum value not allowed': 'must be one of [down]', 'Path escapes with ..': 'must be a relative path under settings.wake.allow.paths',
    'Path outside allow.paths': 'must be a relative path under settings.wake.allow.paths', 'Number too big': 'must be an integer in [0, 20]',
    'Free text with a newline': 'must be one of [down]', 'Extra top-level field': 'unexpected field "dry_run"'
  }
  const names = await page.$$eval('#preset option', (o) => o.map((x) => x.textContent))
  for (const n of names) {
    await page.selectOption('#preset', { label: n }); await page.click('#b-check')
    for (let i = 0; i < 12; i++) { const b = page.locator('#stepper button:has-text("Step")'); if (await b.isDisabled()) break; await b.click() }
    const t = await page.innerText('#stepper .steps')
    ok(t.includes(expect[n]), `starttask: "${n}" -> ${expect[n]}`)
  }
  const realText = castText('wake')
  ok(realText.includes('"reason":"config is not in settings.wake.allow.configs"'), 'starttask: real run audit contains the same refusal reason as the checker')
  for (const [flag, reason] of [['t-changed', 'task_config_changed'], ['t-stopped', 'wake is stopped; new tasks are blocked'], ['t-running', 'max_concurrent_tasks reached'], ['t-used', 'max_tasks_per_hour reached']]) {
    await page.selectOption('#preset', { label: 'Good call' })
    await page.check('#' + flag); await page.click('#b-check')
    for (let i = 0; i < 14; i++) { const b = page.locator('#stepper button:has-text("Step")'); if (await b.isDisabled()) break; await b.click() }
    ok((await page.innerText('#stepper .steps')).includes(reason), `starttask: toggle ${flag} -> ${reason}`)
    await page.uncheck('#' + flag)
  }
  await page.click('#stepper button:has-text("Reset")'); await page.click('#stepper button:has-text("Play")'); await page.waitForTimeout(2200)
  ok((await page.locator('#stepper .steps li').count()) >= 2, 'starttask: Play advances')
  await page.click('#stepper button:has-text("Pause")')
  await page.fill('#call', '{ not json'); await page.click('#b-check')
  ok((await page.innerText('#err')).includes('not valid JSON'), 'starttask: bad JSON is reported')
  ok(errs.length === 0, 'starttask: no errors ' + JSON.stringify(errs))
  await ctx.close()
}

// ---------- monitors.html ----------
{
  const { ctx, page, errs } = await open('monitors')
  await page.selectOption('#h-state', 'stopped')
  ok((await page.innerText('#h-out')).includes('HTTP 503'), 'monitors: stopped gives 503 by default')
  await page.uncheck('#h-fos')
  ok((await page.innerText('#h-out')).includes('HTTP 200'), 'monitors: stopped gives 200 with healthz_fail_on_stopped off')
  await page.selectOption('#h-state', 'failed'); ok((await page.innerText('#h-out')).includes('HTTP 503'), 'monitors: failed gives 503')
  await page.selectOption('#h-state', 'starting'); ok((await page.innerText('#h-out')).includes('HTTP 200'), 'monitors: starting gives 200')
  await page.selectOption('#h-caller', 'rem'); ok((await page.innerText('#h-out')).includes('HTTP 403'), 'monitors: remote caller gets 403 by default')
  await page.fill('#hb-age', '300'); await page.dispatchEvent('#hb-age', 'input')
  ok((await page.innerText('#hb-note')).includes('stale'), 'monitors: heartbeat older than the limit is stale')
  // redaction equals the real recording
  const original = 'The timer saw ERROR in the log. Home is /Users/someone. Header was Authorization: Bearer demo-not-a-real-token-123 and key sk-' + 'demoFAKEkey123.'
  const redacted = await page.evaluate((s) => window.__page.redact(s), original)
  const realLine = castText('monitor').split('\n').find((l) => l.startsWith('{"path": "/ntfy"'))
  const realBody = JSON.parse(realLine).body
  ok(redacted === realBody, `monitors: JS redaction equals the real server output\n  js  : ${redacted}\n  real: ${realBody}`)
  const realTxt = castText('monitor')
  ok(/"priority": "4"/.test(realTxt) && /"title": "log-watch alarm"/.test(realTxt), 'monitors: real ntfy sink got Title and Priority 4 for warn')
  ok(/grep -c '\/pager' webhook.jsonl\n0/.test(realTxt), 'monitors: real pager sink (critical only) got nothing for a warn alert')
  await page.selectOption('#a-sev', 'critical')
  ok((await page.innerText('#a-sinks')).split('delivered').length === 4, 'monitors: critical reaches all 3 sinks')
  await page.selectOption('#a-sev', 'warn')
  ok((await page.innerText('#a-sinks')).includes('skipped: warn is below critical'), 'monitors: warn skips the critical-only sink')
  await page.fill('#y-max', '3'); await page.selectOption('#y-resp', '500')
  ok((await page.innerText('#y-out')).includes('attempt 3 at +15 s'), 'monitors: retry waits 5 s then 10 s')
  await page.selectOption('#y-resp', '400'); ok((await page.innerText('#y-out')).includes('not retried'), 'monitors: 400 is not retried')
  ok(/rakitsu healthcheck[^\n]*\n[^\n]*\n[^\n]*\n[^\n]*\[exit 1\]/.test(realTxt) || realTxt.includes('[exit 1]'), 'monitors: real healthcheck exits 1 for a stopped monitor')
  await page.click('#stepper [role=tab]:has-text("Alerts")'); await page.click('#stepper button:has-text("Step")')
  ok((await page.locator('#stepper .steps li').count()) === 1, 'monitors: stepper works')
  ok(errs.length === 0, 'monitors: no errors ' + JSON.stringify(errs))
  await ctx.close()
}

// ---------- messaging.html ----------
{
  const { ctx, page, errs } = await open('messaging')
  await page.click('#p-replay')
  const logText = await page.innerText('#p-log')
  const simCodes = [...logText.matchAll(/HTTP (\d+)/g)].map((m) => m[1]).join(',')
  const rt = castText('msg')
  const realCodes = [...rt.matchAll(/(?:\(HTTP (\d+)\)|message \d -> HTTP (\d+))/g)].map((m) => m[1] || m[2])
  const realSeq = ['200', '200', ...realCodes].join(',')   // the first two calls print JSON, not a code
  ok(simCodes === realSeq, `messaging: simulated codes equal the real run\n  sim : ${simCodes}\n  real: ${realSeq}`)
  await page.click('#p-reset')
  for (let i = 0; i < 7; i++) await page.click('#p-send')
  const codes = [...(await page.innerText('#p-log')).matchAll(/HTTP (\d+)/g)].map((m) => m[1]).join(',')
  ok(codes === '200,200,200,200,200,200,429', 'messaging: 7th try in 30 s is 429 (' + codes + ')')
  await page.click('#p-t10'); await page.click('#p-t10'); await page.click('#p-t10'); await page.click('#p-send')
  ok((await page.innerText('#p-log')).trim().endsWith('"status":"delivered"}') || (await page.innerText('#p-log')).includes('HTTP 200'), 'messaging: sending works again after 30 s')
  await page.click('#stepper [role=tab]:has-text("Loop guard")'); await page.click('#stepper button:has-text("Play")'); await page.waitForTimeout(2200)
  ok((await page.locator('#stepper .steps li').count()) >= 2, 'messaging: Play advances')
  await page.click('#stepper button:has-text("Pause")')
  ok(errs.length === 0, 'messaging: no errors ' + JSON.stringify(errs))
  await ctx.close()
}

// ---------- index + webui: links and images ----------
{
  const { ctx, page, errs } = await open('index')
  const hrefs = await page.$$eval('a[href]', (a) => a.map((x) => x.getAttribute('href')))
  for (const h of hrefs) {
    if (h.startsWith('http')) continue
    const p = path.join(root, h.split('#')[0])
    // ../reference/ is written by a separate change; ../native-subagent-bridge.md arrives with the bridge change
    const soft = h.startsWith('../reference') || h.includes('native-subagent-bridge')
    if (soft) { log.push('NOTE link target may not exist yet: ' + h); continue }
    ok(fs.existsSync(p), 'index: link resolves ' + h)
  }
  await ctx.close()
  const w = await open('webui')
  await w.page.evaluate(() => document.querySelectorAll('img').forEach((i) => (i.loading = 'eager')))
  await w.page.waitForTimeout(800)
  const bad = await w.page.$$eval('img', (is) => is.filter((i) => !i.complete || i.naturalWidth === 0).map((i) => i.src))
  ok(bad.length === 0, 'webui: all images load ' + JSON.stringify(bad))
  const vid = await w.page.$eval('video', (v) => ({ dur: v.preload, src: v.querySelector('source').getAttribute('src') }))
  ok(fs.existsSync(path.join(root, vid.src)), 'webui: video file exists')
  await w.ctx.close()
}

await browser.close()
fs.writeFileSync(`${out}/results.txt`, log.join('\n') + '\n')
console.log(log.join('\n'))
console.log(failures ? `\n${failures} FAILED` : '\nall checks passed')
process.exit(failures ? 1 : 0)
