# serve, web UI and hub

## When to use it

- You want to see what agents do: a live execution tree, graph, token use and cost.
- You want to design configs with a visual builder, or start runs from a browser.
- You want to pause a run, set breakpoints or change parameters while it runs (a debugger).
- You want a long-lived server for chat sessions, monitors, MCP and A2A.

`rakitsu serve` is one process that does all of this.

## Start it

```bash
rakitsu serve                      # http://localhost:9100
rakitsu serve --port 8080
rakitsu serve --config-dir ./configs
```

Open `http://localhost:9100`. `serve` binds `localhost` by default. A non-loopback `--host` is refused unless `RAKITSU_API_TOKEN` is set (see [security](security.md)).

Flags are listed in [CLI](cli.md#rakitsu-serve). The main ones:

| Flag | Use |
|---|---|
| `--port`, `--host` | Where to listen. |
| `--config-dir DIR` | Extra folder of configs for the UI and `/api/configs`. |
| `--config FILE` | A config for MCP, A2A, `monitors` and `healthz` (see [MCP](mcp.md), [A2A](a2a.md), [monitors](monitors-healthz.md)). |
| `--mcp-port N` | Separate MCP listener. |
| `--sessions-dir DIR` | Where session files go. |

## Where configs come from

The UI and `/api/configs` list configs found in `.`, `./examples`, `./configs` (relative to the folder where you started `serve`) and in `--config-dir`. If the list is empty, start `serve` from your project folder or pass `--config-dir`. If you do not want other configs listed, start it from a clean folder.

## What the UI shows

| Area | Use it to |
|---|---|
| Visual Builder | Drag agents, tools, skills and orchestrators; export to YAML. |
| Agent Runner | Start a run of a listed config from the browser. |
| Run Inspector | Watch the execution tree, graph and token use live. |
| Debugger | Attach to a live run: breakpoints, pause and resume, parameter overrides. Detach to return to normal speed. |
| Session History | Browse and replay finished runs. |
| Chat | Open a chat session with an interactive config. |

The full manual is [docs/WEBUI.md](../WEBUI.md).

## Runs report to the hub

`rakitsu run` connects to a hub at `http://localhost:9100` on its own if one is running. Events are pushed in batches and show up as a card. Use `--hub URL` for another hub, `--no-hub` to turn it off, or `--debug-port N` for a stand-alone debug server of a single run.

```bash
# terminal 1
rakitsu serve
# terminal 2
rakitsu run examples/single/01-chat/config.yaml "Hello" 
```

If the hub has `RAKITSU_API_TOKEN` set, the `run` process needs the same token in its environment.

## HTTP endpoints

The ones that matter most. With a token set, every path under `/api/`, `/ws/`, `/events`, `/mcp` and `/a2a` needs `Authorization: Bearer <token>`.

| Endpoint | Purpose |
|---|---|
| `GET /health` | Liveness. Always public. |
| `GET /api/status` | Version and boot id. Public. |
| `GET /healthz` | Monitor health: 200 if all monitors are ok, else 503. Loopback callers only unless `healthz_allow_remote`. See [monitors](monitors-healthz.md). |
| `GET /events` | Server-sent event stream. |
| `GET /api/configs` | Configs found by the scan. Each entry has `id`, `name`, `path`, `agents`, `tools`, `strategy`, `interactive`. |
| `POST /api/configs/upload`, `/api/configs/inline`, `/api/configs/upload-zip`, `/api/configs/validate` | Add or check configs. |
| `POST /api/run`, `POST /api/run/stop` | Start or stop a run. |
| `GET /api/sessions`, `GET /api/sessions/{id}` | Saved sessions. |
| `/api/debug/*` | Debugger: `attach`, `detach`, `breakpoints`, `pause`, `resume`, `params`, `state`, `replay`, `rerun`, `export`, `user_input`. |
| `/api/hub/*` | CLI runs register, push events and poll for debug commands. |
| `/api/chat/*`, `/ws/chat/{id}` | Chat sessions. See [chat sessions](chat-sessions-messaging.md). |
| `POST /api/sessions/{id}/message`, `GET /api/sessions/live` | Cross-session messages. |
| `/a2a`, `/.well-known/agent-card.json` | On the main port, only when `--config` is given. See [A2A](a2a.md). |
| `/mcp` | On a separate listener, only when both `--config` and `--mcp-port` are given. It is not on the main port. See [MCP](mcp.md). |

## Limits to know

- While `RAKITSU_API_TOKEN` is set, the browser UI cannot load its data: the browser does not send the bearer token on page loads, event streams and WebSocket upgrades. Use the UI on loopback without a token, or reach a remote hub through an SSH tunnel and call the API with the token.
- Two running instances must not share one sessions directory.
- Uploads are capped: 1 MB for a config, 10 MB for a zip.

See [security](security.md) for the complete list.
