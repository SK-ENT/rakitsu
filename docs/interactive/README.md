# Interactive demos for v0.3.0

Static pages (no CDN, no dependencies, no external requests) that explain the v0.3.0 features. Open `index.html` in a browser.

Two kinds of content, always labeled on the page:

- **Simulation**: animated step-through diagrams and playgrounds written in plain JavaScript. They model the documented behavior; they do not run Rakitsu.
- **Real run**: terminal recordings in `casts/*.json`, captured from the real `rakitsu` binary against a tiny fake model server and local sinks on loopback. No API key, no private host, no home path.

## Layout

| Path | What |
|---|---|
| `*.html` | Generated pages. Do not edit by hand. |
| `tools/src/` | Page sources and shared `common.css` / `common.js`. |
| `tools/build_pages.py` | Builds the pages from `tools/src/` and the casts. |
| `tools/record_ask.py`, `record_wake.py`, `record_monitor.py`, `record_msg.py` | Record the casts with the real binary. |
| `tools/record_shots.py`, `tools/shots.mjs` | Screenshots and a short clip of the web UI (Playwright). |
| `tools/castlib.py`, `fake_llm.py`, `webhook_sink.py`, `demo/` | Helpers: cast writer, fake provider, alert sink, demo configs. |
| `tools/test_pages.mjs` | Browser checks (see below). |
| `casts/`, `shots/` | Recorded output. |

## Regenerate

Needs Go, Python 3 and curl. Build the web UI once (`make build-embedded`, or copy `internal/webui/dist` from a built checkout), then:

```sh
sh docs/interactive/tools/regenerate.sh          # casts + pages
sh docs/interactive/tools/regenerate.sh shots    # also screenshots (needs Playwright for node)
RAKITSU_BIN=/path/to/rakitsu sh docs/interactive/tools/regenerate.sh   # skip the go build
```

Check that the casts contain the expected markers (the test below does this), and that `python3 docs/interactive/tools/build_pages.py` leaves `git diff` empty when the casts are unchanged.

## Test

```sh
PW_NODE_PATH=/path/to/node_modules node docs/interactive/tools/test_pages.mjs /some/output/dir
```

Checks every page at 375 px and desktop width, light and dark, reduced motion, with no console errors and no external requests. It also compares the simulations with the real casts.

Links into `../reference/` resolve once the reference pages are merged.
