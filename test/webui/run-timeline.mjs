import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';

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

function completeChatTurn(id, prompt) {
  const url = new URL(`/ws/chat/${id}?events=1`, base);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url);
    const timeout = setTimeout(() => finish(new Error('Chat turn timed out')), 12000);
    function finish(error) {
      clearTimeout(timeout);
      socket.close();
      if (error) reject(error);
      else resolve();
    }
    socket.addEventListener('open', () => socket.send(JSON.stringify({ type: 'turn', text: prompt })));
    socket.addEventListener('error', () => finish(new Error('Chat websocket failed')));
    socket.addEventListener('message', ({ data }) => {
      const message = JSON.parse(data);
      if (message.type === 'turn_done') finish(message.error ? new Error(message.error) : undefined);
      if (message.type === 'error') finish(new Error(message.error ?? 'Chat turn failed'));
    });
  });
}

const { id: configId } = await api('/api/configs/inline', { yaml: await readFile(fixture, 'utf8') });
const query = `Record events for the timeline test ${Date.now()}.`;
const { session_id: liveRunId } = await api('/api/run', {
  config_id: configId,
  query,
  timeout: 20,
});
assert(liveRunId);
const { id: chatId } = await api('/api/chat/start', { config_id: configId });
assert(chatId);

const browser = await puppeteer.launch({
  executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
  args: ['--disable-dev-shm-usage'],
});

try {
  const page = await browser.newPage();
  await page.setViewport({ width: 1500, height: 980 });
  await page.goto(base);
  await page.waitForFunction(async () => (await (await fetch('/api/run')).json()).status === 'completed',
    { timeout: 12000 });
  await page.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await page.locator('.builder-toolbar button.agent').click();
  await page.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await page.locator('.builder-toolbar button.tool').click();
  const builderAgent = '.builder-canvas .agent-node';
  const builderTool = '.builder-canvas .tool-node';
  await page.waitForSelector(builderTool, { visible: true });
  const agentColor = await page.$eval(builderAgent, element => getComputedStyle(element).backgroundColor);
  const toolColor = await page.$eval(builderTool, element => getComputedStyle(element).backgroundColor);
  assert.notEqual(agentColor, toolColor);
  assert.equal(await page.$eval(builderAgent, element => getComputedStyle(element).borderRadius), '8px');
  await page.locator(builderAgent + ' .node-title').click();
  await page.waitForSelector('.node-editor', { visible: true });
  console.log('PASS Builder shows distinct Agent and Tool cards and still opens the node editor');

  const builderPage = await browser.newPage();
  await builderPage.setViewport({ width: 1500, height: 980 });
  await builderPage.goto(base);
  const fileInput = await builderPage.waitForSelector('input[accept=".yaml,.yml"]');
  await fileInput.uploadFile(resolve(fixture));
  await builderPage.locator('.run-bar--design textarea').fill('Check the Builder result graph.');
  await builderPage.locator('.rbar-timeout').fill('45');
  const canvasHeight = () => builderPage.$eval('.builder-canvas', el => el.getBoundingClientRect().height);
  const expandedHeight = await canvasHeight();
  await builderPage.locator('button[aria-label="Collapse run composer"]').click();
  await builderPage.waitForSelector('.run-bar--design textarea', { hidden: true });
  assert(await canvasHeight() > expandedHeight, 'Folding the composer gives the canvas more height');
  await builderPage.locator('button[aria-label="Expand run composer"]').click();
  assert.equal(await builderPage.$eval('.run-bar--design textarea', el => el.value), 'Check the Builder result graph.');
  assert.equal(await builderPage.$eval('.rbar-timeout', el => el.value), '45');
  console.log('PASS Builder composer folds, returns canvas space, and preserves the draft and timeout');
  await builderPage.locator('.pane-tab::-p-text(Run Log)').click();
  await builderPage.locator('button[aria-label="Run Log layout options"]').click();
  await builderPage.locator('.pane-menu button::-p-text(Join Chat tabs)').click();
  await builderPage.locator('.pane-tab::-p-text(Builder)').click();
  await builderPage.waitForSelector('.run-log', { visible: true });
  await builderPage.setRequestInterception(true);
  const eventSubscriptions = [];
  builderPage.on('request', request => {
    const url = new URL(request.url());
    if (url.pathname === '/events') eventSubscriptions.push(url.searchParams.get('session_id'));
  });
  const isRunRequest = request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/run';
  builderPage.on('request', request => {
    if (!isRunRequest(request)) void request.continue();
  });
  const pendingRun = builderPage.waitForRequest(isRunRequest);
  await builderPage.locator('.rbar-btn--run').click();
  const runRequest = await pendingRun;
  const { run_id: requestedRunId } = JSON.parse(runRequest.postData());
  assert(requestedRunId, 'Builder must choose an ID before subscribing');
  assert.deepEqual(eventSubscriptions, [`web-${requestedRunId}`]);
  const results = '.builder-result-graph';
  await builderPage.waitForSelector(results + ' .empty-diagram', { visible: true });
  await builderPage.waitForSelector('.builder-canvas .vue-flow', { hidden: true });
  await completeChatTurn(chatId, 'A separate chat while Builder waits to start.');
  await builderPage.waitForSelector('.run-bar--monitor', { visible: true });
  await builderPage.waitForSelector(results + ' .empty-diagram', { visible: true });
  await builderPage.locator('button::-p-text(Show Workflow)').click();
  await builderPage.waitForSelector('.builder-canvas .vue-flow', { visible: true });
  await runRequest.continue();
  await builderPage.waitForSelector('.run-log .log-events .event-item', { visible: true });
  await builderPage.waitForSelector(results + ' .diagram-node[data-kind="agent"]', { visible: true });
  await builderPage.waitForSelector('.run-bar--results', { visible: true });
  await builderPage.locator(results + ' button[aria-label="Fit to view"]').click();
  await builderPage.locator(results + ' .diagram-node[data-kind="agent"] .node-details').click();
  await builderPage.waitForSelector('.exec-detail-panel', { visible: true });
  await builderPage.locator('.run-bar--results button::-p-text(Back to Canvas)').click();
  const editable = '.builder-canvas .vue-flow__node-agent.draggable';
  await builderPage.waitForSelector(editable, { visible: true });
  const agent = await builderPage.$(editable);
  const start = await agent.boundingBox();
  const beforeDrag = await agent.evaluate(element => element.style.transform);
  await builderPage.mouse.move(start.x + start.width / 2, start.y + start.height / 2);
  await builderPage.mouse.down();
  await builderPage.mouse.move(start.x + start.width / 2 + 60, start.y + start.height / 2 + 35, { steps: 6 });
  await builderPage.mouse.up();
  assert.notEqual(await agent.evaluate(element => element.style.transform), beforeDrag);
  await builderPage.locator('.run-bar--results button::-p-text(Show Results)').click();
  await builderPage.waitForSelector(results, { visible: true });
  const resultCards = () => builderPage.$$eval(results + ' .diagram-node', nodes =>
    nodes.map(node => node.textContent.trim()));
  const ownCards = await resultCards();
  assert(ownCards.length > 0);
  await completeChatTurn(chatId, 'Another chat turn after Builder completes.');
  assert.deepEqual(await resultCards(), ownCards);
  console.log('PASS separate Chat turns do not populate or finish a pending Builder run or change its completed results');
  const builtRun = (await api('/api/sessions')).find(session => session.query === 'Check the Builder result graph.');
  assert(builtRun?.id);
  await builderPage.locator('.pane-tab::-p-text(Run Timeline)').click();
  await builderPage.locator('button[aria-label="Run Timeline layout options"]').click();
  await builderPage.locator('.pane-menu button::-p-text(Join Run Log tabs)').click();
  await builderPage.locator('.pane-tab::-p-text(Builder)').click();
  await builderPage.waitForFunction(id => [...document.querySelectorAll('.run-timeline option')].some(option => option.value === id), {}, builtRun.id);
  await builderPage.select('.run-timeline select', builtRun.id);
  await builderPage.locator('.run-bar--results button::-p-text(Back to Canvas)').click();
  await builderPage.waitForSelector('.agent-node[data-workflow-active="true"]', { visible: true });
  await builderPage.locator('button::-p-text(Follow replay)').click();
  await builderPage.waitForSelector('.agent-node[data-workflow-active="true"]', { hidden: true });
  console.log('PASS live workflow and docked log coexist, and the recorded timeline highlights the editable workflow');
  assert(await builderPage.$eval('.run-history-strip', strip => {
    const builder = strip.closest('.visual-builder').getBoundingClientRect();
    const bounds = strip.getBoundingClientRect();
    return getComputedStyle(strip).position === 'static' && bounds.left >= builder.left && bounds.right <= builder.right;
  }), 'Run history stays in the Builder footer');
  await builderPage.close();
  console.log('PASS Builder opens Execution Path before the run starts, then shows results and returns to draggable design nodes');

  const benchContext = await browser.createBrowserContext();
  const bench = await benchContext.newPage();
  await bench.setViewport({ width: 1600, height: 1000 });
  await bench.goto(base);
  const button = label => bench.locator(`.builder-toolbar button[aria-label="${label}"]`);
  await bench.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await bench.locator('.builder-toolbar button.agent').click();
  await bench.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await bench.locator('.builder-toolbar button.tool').click();
  await button('Arrange').click();
  const source = '.agent-node .handle-tools';
  const target = '.tool-node .handle-input';
  await bench.waitForSelector(source, { visible: true });
  const center = selector => bench.$eval(selector, el => {
    const rect = el.getBoundingClientRect();
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
  });
  const startPin = await center(source);
  const endPin = await center(target);
  await bench.mouse.move(startPin.x, startPin.y);
  await bench.mouse.down();
  await bench.mouse.move(endPin.x, endPin.y, { steps: 12 });
  await bench.mouse.up();
  await bench.waitForSelector('.builder-wire[data-wire-kind="tool"] .wire-direction', { visible: true });
  await bench.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await bench.locator('.builder-toolbar button.loop').click();
  await bench.waitForSelector('.group-node.loop', { visible: true });
  await bench.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await bench.locator('.builder-toolbar button.group-btn:not(.loop)::-p-text(Group)').click();
  await bench.waitForFunction(() => document.querySelectorAll('.group-node').length === 2);
  await button('Arrange').click();
  await bench.waitForFunction(() => !document.querySelector('.node--transitioning'));
  await bench.locator('.agent-node .node-title').click();
  const loopId = await bench.$eval('.vue-flow__node-group:has(.group-node.loop)', node => node.getAttribute('data-id'));
  await bench.select('select[aria-label="Move selected nodes to group"]', loopId);
  await bench.locator('.group-node.loop button[aria-label="Fold group"]').click();
  await bench.waitForSelector('.group-node.loop.collapsed');
  assert.equal(await bench.$$eval('.group-node.collapsed', nodes => nodes.length), 1);
  await button('Fold all').click();
  await bench.waitForFunction(() => document.querySelectorAll('.group-node.collapsed').length === 2);
  await button('Auto-fit Off').click();
  await bench.waitForSelector('.builder-toolbar button[aria-label="Auto-fit On"]');
  await button('Unfold all').click();
  await bench.waitForFunction(() => !document.querySelector('.group-node.collapsed'));
  await bench.waitForSelector('.agent-node', { visible: true });
  const viewport = '.builder-canvas .vue-flow__transformationpane';
  const beforeResize = await bench.$eval(viewport, node => node.style.transform);
  await bench.setViewport({ width: 1170, height: 810 });
  await bench.waitForFunction((selector, transform) => document.querySelector(selector)?.style.transform !== transform, {}, viewport, beforeResize);
  await bench.waitForSelector('.builder-toolbar button[aria-label="Auto-fit On"]');
  await bench.emulateMediaFeatures([{ name: "prefers-reduced-motion", value: "reduce" }]);

  await button('SVG workbench preview').click();
  const svgCanvas = '[data-builder-svg-canvas]';
  await bench.waitForSelector(svgCanvas, { visible: true });
  const svgAgent = `${svgCanvas} [data-type="agent"]`;
  await bench.waitForSelector(svgAgent, { visible: true });
  const svgWire = `${svgCanvas} [data-kind="tool"] .wire`;
  await bench.waitForSelector(svgWire, { visible: true });
  await bench.$eval(svgAgent, node => node.focus());
  await bench.keyboard.press('Enter');
  await bench.waitForSelector('.node-editor', { visible: true });
  await bench.locator(`${svgCanvas} [data-group="${loopId}"] [aria-label^="Fold"]`).click();
  await bench.waitForSelector(svgAgent, { hidden: true });
  const foldedLoop = `${svgCanvas} [data-node="${loopId}"] [aria-label^="Unfold"]`;
  await bench.locator(foldedLoop).click();
  await bench.waitForSelector(svgAgent, { visible: true });
  const svgCardBefore = await bench.$eval(svgAgent, node => node.getAttribute('transform'));
  const svgWireBefore = await bench.$eval(svgWire, node => node.getAttribute('d'));
  const cardCenter = await center(svgAgent);
  await bench.mouse.move(cardCenter.x, cardCenter.y);
  await bench.mouse.down();
  await bench.mouse.move(cardCenter.x + 36, cardCenter.y + 12, { steps: 8 });
  await bench.mouse.up();
  await bench.waitForFunction((selector, value) => document.querySelector(selector)?.getAttribute('transform') !== value,
    {}, svgAgent, svgCardBefore);
  assert.notEqual(await bench.$eval(svgWire, node => node.getAttribute('d')), svgWireBefore);
  await bench.setViewport({ width: 1800, height: 1000 });

  await bench.locator('.builder-canvas .wallpaper-picker .thumb-1').click();
  await bench.waitForSelector('.builder-canvas.has-wallpaper .wallpaper-canvas', { visible: true });
  await bench.waitForSelector(svgCanvas, { visible: true });
  await bench.locator('.builder-toolbar button::-p-text(+ Add)').click();
  await bench.locator('.builder-toolbar button.tool').click();
  await bench.waitForFunction(() => document.querySelectorAll('[data-builder-svg-canvas] [data-type="tool"]').length === 2);
  const newToolId = await bench.$$eval(`${svgCanvas} [data-type="tool"]`, tools => tools[tools.length - 1].getAttribute('data-node'));
  const existingWires = await bench.$$eval(svgWire, wires => wires.length);
  await bench.locator(".svg-controls button::-p-text(Fit view)").click();

  const sourcePort = `${svgAgent} [data-port="tool"]`;
  const targetPort = `${svgCanvas} [data-node="${newToolId}"] [data-port="in"]`;
  await bench.waitForFunction(selector => getComputedStyle(document.querySelector(selector)).opacity === '0', {}, targetPort);
  assert.equal(await bench.$eval(".wire-terminals", node => getComputedStyle(node).opacity), "0");
  const svgSourcePin = await center(sourcePort);
  const svgTargetPin = await center(targetPort);
  await bench.mouse.move(svgSourcePin.x, svgSourcePin.y);
  await bench.waitForFunction(selector => getComputedStyle(document.querySelector(selector)).opacity === '1', {}, sourcePort);
  await bench.mouse.down();
  await bench.waitForFunction(selector => getComputedStyle(document.querySelector(selector)).opacity === '1', {}, targetPort);
  assert.equal(await bench.$eval('.wire-terminals', node => getComputedStyle(node).opacity), '1');
  await bench.mouse.move(svgTargetPin.x, svgTargetPin.y, { steps: 12 });
  await bench.mouse.up();
  await bench.waitForFunction((selector, previous) => document.querySelectorAll(selector).length > previous, {}, svgWire, existingWires);
  const overlaps = await bench.$$eval(svgWire, paths => {
    const seen = [];
    const shared = (a, b, c, d) => Math.min(Math.max(a, b), Math.max(c, d)) > Math.max(Math.min(a, b), Math.min(c, d));
    for (const path of paths) {
      const points = [...path.getAttribute('d').matchAll(/[ML]([^ ]+) ([^ ]+)/g)].map(([, x, y]) => [Number(x), Number(y)]);
      for (let i = 1; i < points.length; i++) {
        const [a, b] = [points[i - 1], points[i]];
        if (a[0] === b[0] && a[1] === b[1]) continue;
        for (const [c, d] of seen) {
          const horizontal = a[1] === b[1] && c[1] === d[1] && a[1] === c[1] && shared(a[0], b[0], c[0], d[0]);
          const vertical = a[0] === b[0] && c[0] === d[0] && a[0] === c[0] && shared(a[1], b[1], c[1], d[1]);
          if (horizontal || vertical) return true;
        }
        seen.push([a, b]);
      }
    }
    return false;
  });
  assert.equal(overlaps, false, 'Distinct SVG tool connections must not share a segment');
  const newToolCard = `${svgCanvas} [data-node="${newToolId}"]`;
  await bench.$eval(newToolCard, node => node.focus());
  await bench.keyboard.press('Enter');
  await bench.select('select[aria-label="Move selected nodes to group"]', loopId);
  await bench.waitForFunction((groupId, wireSelector, toolSelector) => {
    const frame = document.querySelector(`[data-group="${groupId}"] .group-frame`);
    const tool = document.querySelector(toolSelector);
    const transform = tool?.getAttribute('transform')?.match(/translate\(([^ ]+) ([^)]+)\)/);
    const width = Number(tool?.querySelector('.node-body')?.getAttribute('width'));
    if (!frame || !transform || !width) return false;
    const targetX = Number(transform[1]) + width / 2, targetY = Number(transform[2]);
    const x = Number(frame.getAttribute('x')), y = Number(frame.getAttribute('y'));
    const right = x + Number(frame.getAttribute('width')), bottom = y + Number(frame.getAttribute('height'));
    return [...document.querySelectorAll(wireSelector)].some(wire => {
      const points = [...wire.getAttribute('d').matchAll(/[ML]([^ ]+) ([^ ]+)/g)].map(([, px, py]) => [Number(px), Number(py)]);
      const end = points[points.length - 1];
      return end?.[0] === targetX && end[1] === targetY
        && points.every(([px, py]) => px >= x && px <= right && py >= y && py <= bottom);
    });
  }, {}, loopId, svgWire, newToolCard);
  console.log('PASS SVG drag-to-connect, non-overlapping routes, and group frame around an internal wire');
  await button('Classic canvas').click();
  await bench.waitForSelector('.builder-wire[data-wire-kind="tool"]', { visible: true });
  console.log('PASS SVG canvas reads existing nodes and wires, opens the node editor, folds a loop, moves cards and wires, and keeps wallpaper and classic canvas');
  await benchContext.close();
  console.log('PASS real Builder wiring, loop membership, independent and bulk folding, and camera auto-fit on resize');

  const recordedRun = (await api('/api/sessions')).find(session => session.query === query);
  assert(recordedRun?.id, 'The completed run must be available in session history');
  const runId = recordedRun.id;
  await page.locator('.chat-view .session-row').click();
  const composer = '.chat-view .chat-input textarea';
  await page.locator(composer).fill('Unsent draft in a different session');

  await page.locator('.pane-tab::-p-text(Run Timeline)').click();
  await page.waitForFunction(id => [...document.querySelectorAll('.run-timeline option')]
    .some(option => option.value === id), {}, runId);
  await page.select('.run-timeline select', runId);
  await page.waitForFunction(id => document.querySelector('.run-identity')?.textContent.includes(id), {}, runId);

  const position = '[aria-label="Replay position"]';
  const currentPosition = () => page.$eval(position, element => Number(element.value));
  await page.locator('.run-timeline button::-p-text(Next event)').click();
  assert.equal(await currentPosition(), 1);
  await page.locator('.run-timeline button::-p-text(Previous event)').click();
  assert.equal(await currentPosition(), 0);
  await page.locator('.run-timeline button::-p-text(Play)').click();
  await page.waitForFunction(selector => Number(document.querySelector(selector)?.value) > 0, {}, position);
  await page.locator('.run-timeline button::-p-text(Pause)').click();
  await page.focus(position);
  await page.keyboard.press('End');
  const last = await currentPosition();
  assert(last > 1);
  assert(await page.$('.run-timeline [aria-current=step]'));

  async function layout(action) {
    await page.locator('button[aria-label="Run Timeline layout options"]').click();
    await page.locator(`.pane-menu button::-p-text(${action})`).click();
  }

  await layout('Float');
  assert(await page.$('.pane-floating[aria-label="Run Timeline"]'));
  assert.equal(await currentPosition(), last);
  await layout('Dock as new pane');
  assert.equal(await currentPosition(), last);
  assert.equal(await page.$eval('.run-timeline select', element => element.value), runId);
  assert.equal(await page.$eval(composer, element => element.value), 'Unsent draft in a different session');
  console.log('PASS recorded-run selection, event step/play/pause/scrub, float/redock, and independent Chat draft');

  await page.locator('.pane-tab::-p-text(Sessions)').click();
  await page.locator(`.session-list .card-query::-p-text(${query})`).click();
  await page.locator('.inspect-viewer .actions button::-p-text(Debug)').click();
  const debuggerSlider = '.debug-view .pb-slider';
  await page.waitForSelector(debuggerSlider, { visible: true });
  assert.equal(await page.$eval(debuggerSlider, element => Number(element.value)), last);
  await page.focus(debuggerSlider);
  await page.keyboard.press('Home');
  await page.waitForFunction(selector => Number(document.querySelector(selector)?.value) === 0, {}, position);
  await page.locator('.run-timeline button::-p-text(Next event)').click();
  await page.waitForFunction(selector => Number(document.querySelector(selector)?.value) === 1, {}, debuggerSlider);
  await page.locator('.style-selector button::-p-text(Block)').click();
  await page.waitForSelector('.execution-diagram .diagram-node.current', { visible: true });
  assert.equal(await page.$('.debug-view .vue-flow'), null);
  await page.locator('.run-timeline button::-p-text(Next event)').click();
  await page.waitForFunction(() => document.querySelector('.execution-diagram .diagram-node.current')?.dataset.kind === 'thought');
  assert(await page.$('.diagram-connectors path.active'));
  const accents = await page.$$eval('.diagram-node[data-kind=agent], .diagram-node[data-kind=thought]',
    elements => elements.map(element => getComputedStyle(element).getPropertyValue('--diagram-accent').trim()));
  assert.equal(new Set(accents).size, 2);
  await page.locator('.execution-diagram button::-p-text(Collapse all)').click();
  assert.equal(await page.$$eval('.diagram-node', elements => elements.length), 1);
  await page.locator('.execution-diagram button::-p-text(Expand all)').click();
  await page.waitForSelector('.diagram-node[data-kind=thought]', { visible: true });
  const graphCard = '.diagram-node[data-kind=thought]';
  const widthBeforeZoom = await page.$eval(graphCard, node => node.getBoundingClientRect().width);
  await page.locator('.execution-diagram button[aria-label="Zoom in"]').click();
  assert(await page.$eval(graphCard, node => node.getBoundingClientRect().width) > widthBeforeZoom);
  await page.locator('.execution-diagram button[aria-label="Zoom out"]').click();
  assert.equal(await page.$eval(graphCard, node => node.getBoundingClientRect().width), widthBeforeZoom);
  await page.locator('.execution-diagram button[aria-label="Fit to view"]').click();
  assert(await page.$eval('.diagram-scroll', pane =>
    pane.scrollWidth <= pane.clientWidth + 1 && pane.scrollHeight <= pane.clientHeight + 1));
  await page.locator(graphCard + ' .node-details').click();
  await page.waitForSelector('.debug-view .detail-side', { visible: true });
  const answer = '.debug-view .variant-answer .streaming-text.active';
  await page.waitForSelector(answer, { visible: true });
  const contrast = await page.$eval(answer, element => {
    const { color, backgroundColor } = getComputedStyle(element);
    const luminance = rgb => {
      const channels = rgb.match(/\d+/g).map(value => {
        const channel = Number(value) / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
    };
    return (luminance(color) + 0.05) / (luminance(backgroundColor) + 0.05);
  });
  assert(contrast >= 4.5, `Expected readable dark-theme answer text, got ${contrast}:1`);
  assert.equal(await currentPosition(), 2);
  assert.equal(await page.$eval(composer, element => element.value), 'Unsent draft in a different session');
  console.log('PASS Block renders a connected execution graph, animates the active route, and preserves node details');
  console.log('PASS Debugger and Run Timeline share the same recorded run cursor in both directions');

  const recordedSessions = await api('/api/sessions');
  const emptyRuns = ['empty-run-one', 'empty-run-two'].map(id => ({ ...recordedRun, id, name: id }));
  await page.setRequestInterception(true);
  page.on('request', request => {
    const path = new URL(request.url()).pathname;
    const emptyRun = emptyRuns.find(run => path === `/api/sessions/${run.id}` || path === `/api/sessions/${run.id}/events`);
    if (path === '/api/sessions' || emptyRun) {
      const body = emptyRun ? (path.endsWith('/events') ? [] : emptyRun) : [...recordedSessions, ...emptyRuns];
      void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    } else {
      void request.continue();
    }
  });
  await page.locator('.run-timeline button::-p-text(Refresh runs)').click();
  for (const run of emptyRuns) {
    await page.waitForFunction(id => document.querySelector(`.run-timeline option[value="${id}"]`), {}, run.id);
    await page.select('.run-timeline select', run.id);
    await page.waitForFunction(id => document.querySelector('.run-identity')?.textContent.includes(id)
      && document.querySelector('.run-timeline')?.textContent.includes('This run has no recorded events.')
      && !document.querySelector('[aria-label="Replay position"]'), {}, run.id);
  }
  await page.select('.run-timeline select', runId);
  await page.waitForFunction(() => Number(document.querySelector('[aria-label="Replay position"]')?.value) === 2);
  console.log('PASS switching through empty recordings clears playback and preserves the populated run cursor');

  const liveContext = await browser.createBrowserContext();
  const livePage = await liveContext.newPage();
  await livePage.goto(base);
  await livePage.locator('.pane-tab::-p-text(Debugger)').click();
  if (!await livePage.$('.debug-view .live-badge')) {
    await livePage.locator('.debug-view .toolbar-btn.connect, .debug-view .live-badge').wait();
    if (await livePage.$('.debug-view .toolbar-btn.connect')) {
      await livePage.locator('.debug-view .toolbar-btn.connect').click();
    }
  }
  await livePage.waitForSelector('.debug-view .live-badge', { visible: true });
  const liveTree = '.debug-view .hierarchy-tree';
  await api('/api/run', { config_id: configId, query: 'First live debugger run', timeout: 20 });
  await livePage.waitForFunction(selector => document.querySelector(selector)?.textContent.includes('Reviewer'), {}, liveTree);
  await livePage.waitForFunction(async () => (await (await fetch('/api/run')).json()).status === 'completed');
  const nextYaml = (await readFile(fixture, 'utf8')).replaceAll('Reviewer', 'NextAgent');
  const next = await api('/api/configs/inline', { yaml: nextYaml });
  await api('/api/run', { config_id: next.id, query: 'Second live debugger run', timeout: 20 });
  await livePage.waitForFunction(selector => {
    const tree = document.querySelector(selector)?.textContent ?? '';
    return tree.includes('NextAgent') && !tree.includes('Reviewer');
  }, {}, liveTree);
  await liveContext.close();
  console.log('PASS Debugger Live replaces the old tree on the next web run');

} finally {
  await browser.close();
}
