# WebUI Probe

Browser-driven sanity checks for Rakitsu's web UI. Uses `puppeteer-core`
against a locally-installed Chrome so we don't download Chromium on every
run. Companion to `test/stress/registry_stress_test.go` which covers the
same surface at the HTTP layer.

## Usage

```bash
cd test/webui
npm install              # one-time
npm run probe            # runs ./probe.mjs
```

Or from repo root:

```bash
node test/webui/probe.mjs
```

To measure Builder canvas height in Debug and Replay and check the compact
controls, point the probe at a serving build and a YAML config containing an
agent. The browser imports the config and measures the layout without running it.

```bash
RAKITSU_BASE_URL=http://127.0.0.1:9100 \
RAKITSU_TIMELINE_CONFIG=/path/to/config.yaml \
node test/webui/compact-builder.mjs
```

Set `RAKITSU_BUILDER_SHOTS` to an existing directory to save the two layouts
as PNGs.

## Env vars

| Name | Default | Purpose |
|------|---------|---------|
| `RAKITSU_BASE_URL` | `http://localhost:9100` | Server to probe |
| `RAKITSU_CHAT_CONFIG` | `ba616ec02aa0` (Chat WS Test) | Config used for spawned chat sessions |
| `CHROME_PATH` | `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` | Chrome binary |
| `HEADLESS` | `true` | Set to `false` to watch the browser |
| `SCREENSHOTS_DIR` | `./artifacts` | Where PNG artifacts go |

The default `RAKITSU_CHAT_CONFIG` is a config ID from the maintainer's own
config store, so it won't resolve on a fresh clone. Upload any chat-capable
config first (e.g. `examples/single/01-chat/config.yaml`, via the web UI's
"Upload Config" or `POST /api/configs/inline`), then set
`RAKITSU_CHAT_CONFIG` to the ID the server returns.

## What it verifies

The probe is currently scoped to Phase 1 (multi-session debug) deliverables:

1. **picker-mounted** — `.session-picker` exists in the DOM.
2. **picker-opens** — clicking the trigger reveals `.picker-menu`.
3. **picker-reflects-chat-starts** — after `/api/chat/start`, the dropdown
   shows the new session within the poll interval.
4. **picker-stop-button-clickable** — the per-entry Stop button exists
   (feature-detected; skipped if the running binary predates the Stop
   button).
5. **stop-button-drains-target-from-registry** — after clicking Stop, the
   targeted session disappears from `/api/runtime/sessions`.
6. **our-spawned-chats-drain** — shutting down the chats via API drains
   them from the registry.

On failure the probe exits non-zero, keeps screenshots of every test in
`artifacts/`, and prints a summary with per-test detail strings.

## Adding tests

Drop new assertions into `probe.mjs` using the existing `record(name, ok,
detail)` helper. It automatically snapshots the page on every step. Keep
assertions idempotent — the server may have unrelated sessions already.
