# Wake timer

## When to use it

You want an agent to watch something for hours or days, and you do not want to pay for a model call every minute.

The wake timer runs cheap checks on a schedule (a file, a web address). It calls the model only when a check changes or raises an alarm. Between those moments the cost is zero model calls. The chat session stays alive the whole time and keeps its context.

Examples: price watcher, "is the build still running", "did the nightly job write its marker file", "is my service answering".

Deep dive and file formats: [docs/wake-timer.md](../wake-timer.md). Runnable example: [examples/single/14-long-running-monitor](../../examples/single/14-long-running-monitor/).

## How it works

Each tick has three levels:

| Level | What happens | Model call |
|---|---|---|
| L0 tick | Run all checks. Result is `quiet`, `changed`, `alarm` or `unknown`. Written to disk. | No |
| L1 judge | Reserved. `judge.enabled` is accepted, and no judge call is made in this release. | No |
| L2 turn | On `changed` or `alarm`, a normal agent turn is injected into the session with the check results, marked as a timer turn. | Yes |

Quiet ticks make the interval grow (`backoff_factor`, up to `max_interval_seconds`). A turn already running blocks a new one (`single_flight`). A persistent alarm wakes the model once, when it starts. The same unchanged alarm does not wake it again until a quiet tick or a different alarm.

## Setup

Wake runs in a **chat session that `serve` starts**, not in `rakitsu run`. Use an interactive config (`interactive: true`). The config must have **no** `cli`, `fs`, `mcp_server` or `a2a` tools, because unattended turns have no approval step. Rakitsu refuses the config at load time if any such tool is present (global `tools:` or an agent's `tools_inline:`), with an error such as `tool type "cli" is refused while settings.wake.enabled is true`. Give every agent a system prompt that says what to do on a timer turn.

```yaml
name: file-watcher
interactive: true
interactive_overlay: false
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
  memory:
    enabled: true
    conversation:
      enabled: true
  wake:
    enabled: true
    interval_seconds: 30
    min_interval_seconds: 10
    max_interval_seconds: 120
    jitter_percent: 0
    max_turns_per_hour: 6
    allow:
      paths: [./watched]
    checks:
      - name: marker
        type: file_contains
        path: ./watched/status.txt
        contains: FAILED
agents:
  - name: Watcher
    role: worker
    system_prompt: |
      You are woken by a timer when a check fails. Explain in two short lines
      what the check results say. Do not guess beyond them.
    settings:
      max_iterations: 3
```

Start it from the folder that holds `watched/`:

```bash
rakitsu serve --config-dir .
# then start the "file-watcher" config in the web UI, or:
curl -s -X POST http://127.0.0.1:9100/api/chat/start \
  -H 'Content-Type: application/json' \
  -d '{"config_id":"<id from /api/configs>"}'
```

When `watched/status.txt` contains `FAILED`, the next tick raises an alarm and the Watcher answers. Check state with `GET /api/chat/{id}/wake/status`.

For a fully unattended setup with automatic start, use [monitors](monitors-healthz.md).

## Keys (`settings.wake`)

| Key | Default | Rule |
|---|---|---|
| `enabled` | false | Opt in. |
| `interval_seconds` | 60 | Must be at least `min_interval_seconds`. |
| `backoff_factor` | 1.5 | Between 1.0 and 4.0. Applied after each quiet tick. |
| `min_interval_seconds` | 30 | At least 10. |
| `max_interval_seconds` | 600 | At least `interval_seconds`. |
| `jitter_percent` | 10 | 0 to 50. Random variance per tick. |
| `max_turns_per_hour` | 6 | 1 to 60. Hard cap for L2 turns. |
| `turn_timeout_seconds` | 180 | Time limit of a timer turn. |
| `kill_switch_file` | `~/.rakitsu/wake/STOP` | While the file exists, the loop stops. A relative path is relative to the folder where `serve` was started, not to the session workdir. Use an absolute path to avoid surprises. |
| `heartbeat_stale_seconds` | 180 | At least 15. A heartbeat older than this marks the session `stale`. |
| `consecutive_alarms` | 2 | HTTP checks only. An alarm or unknown must repeat this many times before it is raised. |
| `allow.paths` | none | Folders that file checks may read. Must not contain `..`. A check `path` must equal an entry or sit below it after cleaning (`./watched/status.txt` is under `./watched`). The entry `.` does not cover `./status.txt`, so put checked files in a subfolder. |
| `allow.url_hosts` | none | Hosts that HTTP checks may call, written as `host` or `host:port` exactly as in the URL. |
| `allow.configs` | none | Task configs for `start_task`. See [start_task](start-task.md). |
| `secret_env` | none | Environment variable names (no values) that must exist at session start. |
| `alerts` | none | Alert sinks. See [alerts](alerts.md). |
| `checks` | required | At least one. |
| `judge` | off | `enabled`, `model`. No call is made yet. |
| `max_tasks_per_hour`, `max_concurrent_tasks`, `task_timeout_seconds`, `cancel_on_stop` | 10, 3, 300, true | Only with `allow.configs`. |

### Check types

| `type` | Alarm when | Keys |
|---|---|---|
| `file_mtime` | The file is missing or older than `max_age_seconds`. | `path`, `max_age_seconds` |
| `file_contains` | The file **contains** the text `contains`. (First 64 KiB is read.) | `path`, `contains` |
| `http_status` | The status code is not `expect` (default 200). | `url`, `expect`, `timeout_seconds` |
| `http_json` | The number at dotted path `field` meets `alarm_if`: `below`, `above`, or `change_percent` over `window_seconds`. A non-number is `unknown`. | `url`, `field`, `alarm_if`, `label_band`, `timeout_seconds` |

`label_band` (`upper`, `lower`, `exit`) turns a number into a label `up`, `down` or `neutral` with hysteresis. A label flip counts as `changed` and wakes the model.

Check `timeout_seconds` defaults to 10. File checks stay inside the working directory (symlinks are resolved first).

## Network rules for HTTP checks

HTTP checks are read-only `GET` requests with strict rules:

- Only hosts listed in `allow.url_hosts` are called.
- The address is checked when connecting. Loopback, private, link-local, CGNAT, multicast, unspecified and cloud-metadata addresses are refused unless the URL host is listed. A listed host name never unlocks metadata or multicast addresses.
- **Redirects are never followed.** A redirect answer counts as a failed check (`unknown`), not as success.
- A URL with `user:password@` is rejected.
- Proxy environment variables (`HTTP_PROXY`, `HTTPS_PROXY`) are ignored.
- The whole request, including reading the body, is bounded by the check timeout. The body is limited to 64 KiB.

## Control

| Way | Use |
|---|---|
| `GET /api/chat/{id}/wake/status` | State, tick counters, check results. |
| `POST /api/chat/{id}/wake/stop` | Stop the loop. The session stays alive. Safe to repeat. It creates the kill-switch file. The loop stops at its next tick, so status and `/healthz` show `stopped` after up to one interval. |
| `POST /api/chat/{id}/wake/resume` | Start again. Returns 409 while the kill-switch file exists, so after a stop you must remove the file first (`rm ~/.rakitsu/wake/STOP`). |
| `POST /api/chat/{id}/wake/tasks/{task-id}/cancel` | Cancel one started task. |
| `/wake status`, `/wake stop`, `/wake resume` | Same, as a slash command in chat. |
| Kill-switch file | `touch ~/.rakitsu/wake/STOP`. The loop stops at the next tick. Remove it, then resume. The default path is shared by all sessions, so one file stops them all. Set `kill_switch_file` per session to stop them one by one. |

Files are written under `~/.rakitsu/wake/`: `<session-id>.audit.jsonl`, `.state.json`, `.heartbeat`, `.tasks/<task-id>.log`. A single process holds `<session-id>.lock`. If that lock file is removed or replaced while the session runs, the loop stops (audit line `lock_replaced`) and resume is refused.

## Limits

- Not available in `rakitsu acp` (sessions are in memory only).
- State and the hourly cap survive a restart when you restart with the same session id (`resume_id`).
- Events: `WAKE_TICK`, `WAKE_ESCALATE`, `WAKE_TASK_START`, `WAKE_TASK_END`.
