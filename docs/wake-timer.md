# Native Wake-Up Timer for Serve Chat Sessions

## What it is

A server-owned clock runs cheap, deterministic checks on a schedule in a long-lived `rakitsu serve` chat session. It uses zero model calls while nothing changes, starts a normal agent turn in the same session only when a check reports a change or alarm, and keeps the session alive across the wait. The session's rolling summary, per-session JSONL and `resume_id` survive crashes and restarts.

## Config reference

Enable under `settings.wake`:

```yaml
settings:
  memory:
    enabled: true
    conversation: { enabled: true }
  wake:
    enabled: true
    interval_seconds: 60          # base tick (>= min_interval_seconds)
    backoff_factor: 1.5           # applied after each quiet tick
    min_interval_seconds: 30      # floor for any interval
    max_interval_seconds: 600     # max backoff, 10 min
    jitter_percent: 10
    max_turns_per_hour: 6         # hard cap on L2 turns
    turn_timeout_seconds: 180
    kill_switch_file: ~/.rakitsu/wake/STOP
    heartbeat_stale_seconds: 180  # contract for supervisors
    allow:                        # confinement for checks
      paths: [./logs, ./out]      # must sit within the workdir
      url_hosts: [localhost:8080, api.coingecko.com]
    secret_env: []                # env var NAMES only, resolved at restart
    checks:
      - name: log-fresh
        type: file_mtime          # alarm if not modified within max_age_seconds
        path: ./logs/app.log
        max_age_seconds: 300
      - name: done-marker
        type: file_contains
        path: ./out/status.txt
        contains: "FAILED"
      - name: api-up
        type: http_status
        url: http://localhost:8080/health
        expect: 200
        timeout_seconds: 5
      - name: btc-price
        type: http_json           # GET, read a number at a dotted path
        url: https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd
        field: bitcoin.usd
        alarm_if: { below: 55000 }
        timeout_seconds: 10
    judge:                        # optional L1 (config-only; no judge call in v1)
      enabled: false
      model: small-model-name
```

**Defaults table:**

| Parameter | Type | Default | Notes |
|-----------|------|---------|-------|
| `enabled` | bool | false | Opt-in; refused with cli/fs/mcp_server/a2a tools or if any agent lacks a system_prompt |
| `interval_seconds` | int | 60 | Base tick interval in seconds |
| `backoff_factor` | float | 1.5 | Multiplier after each quiet tick, capped at max_interval_seconds |
| `min_interval_seconds` | int | 30 | Minimum interval (must be ≥10) |
| `max_interval_seconds` | int | 600 | Maximum interval after backoff |
| `jitter_percent` | int | 10 | Random ±% variance per tick (0-50) |
| `max_turns_per_hour` | int | 6 | Hard cap; exceeding triggers hourly_cap suppression |
| `turn_timeout_seconds` | int | 180 | Timeout for L2 agent turns; exceeded turns are marked timeout |
| `kill_switch_file` | string | ~/.rakitsu/wake/STOP | Path to kill-switch file; tick stops loop if present |
| `heartbeat_stale_seconds` | int | 180 | Heartbeat older than this marks the session `stale` (status API and `/healthz` 503). The loop writes the heartbeat every `min(interval_seconds, heartbeat_stale_seconds/3)`, independent of backoff, so a healthy monitor never goes stale; a wedged loop does. Must be >= 15 |
| `allow.paths` | []string | [] | Allowed file paths (relative or absolute; resolved within workdir) |
| `allow.url_hosts` | []string | [] | Allowed URL hosts for http checks (validated at config load) |
| `secret_env` | []string | [] | Env var names (no values); verified at session start |
| `max_tasks_per_hour` | int | 10 | Only with `allow.configs`. Rolling hour, persisted in the state file; not reset on resume or restart |
| `max_concurrent_tasks` | int | 3 | Only with `allow.configs`. Overflow is refused, not queued |
| `task_timeout_seconds` | int | 300 | Only with `allow.configs`. Per started task; never unbounded |
| `cancel_on_stop` | bool | true | Stop route / kill-switch cancel running started tasks |
| `allow.configs` | []object | [] | Task configs `start_task` may launch (see below) |
| `judge.enabled` | bool | false | L1 judge call enable (v1: config-only, no call made) |
| `judge.model` | string | "" | Model for judge (required if judge.enabled) |

## Tiers (L0/L1/L2)

**L0: Free tick** (no model call)
- Runs all checks, updates state on disk
- Outcome: `quiet`, `changed`, `alarm`, or `unknown` (error/timeout)
- Emits `WAKE_TICK` event to session JSONL

**L1: Optional Judge** (config-only in v1; judge call not implemented)
- Receives tick outcome and check results
- Returns a recommendation (e.g. "escalate", "defer", "suppress")
- Not currently used; reserved for future use

**L2: Agent Turn** (model call)
- Timer injects a [TIMER-SOURCED TURN] message with check results
- Agent runs a full ReAct loop in the same session
- Applies max_turns_per_hour cap and single_flight debounce
- Emits `WAKE_ESCALATE` event to session JSONL when turn completes

## Safety rules

**Opt-in with guardrails:**
- Enabled only when explicitly configured (`settings.wake.enabled: true`)
- Refused if config contains `cli`, `fs`, `mcp_server`, or `a2a` tools (v1 unattended-execution boundary)
- File checks are read-only; max 64 KiB per check
- HTTP checks are GET-only; max 64 KiB response; validated hosts only

**Throttling:**
- `max_turns_per_hour`: hard cap; excess turns suppressed with `hourly_cap` status
- `single_flight`: consecutive alarms without state change are suppressed
- `backoff`: quiet ticks increase interval up to max_interval_seconds
- Jitter ±% prevents thundering herd across multiple sessions

**Lifecycle:**
- Kill-switch file (`~/.rakitsu/wake/STOP`) stops the loop; session stays alive
- Explicit resume via `POST /api/chat/{id}/wake/resume`
- Restart with same `resume_id` resumes state (cap window survives)
- ACP cannot resume sessions (sessions in-memory only; no persistence in ACP mode)

**Visibility:**
- Silent on quiet ticks (no event emission)
- Non-quiet ticks, killed, degraded, and escalations emit events
- Audit log at `~/.rakitsu/wake/<session-id>.audit.jsonl` tracks all ticks with counts

## Files

| Path | Purpose |
|------|---------|
| `~/.rakitsu/wake/<session-id>.audit.jsonl` | Event log: ticks, outcomes, escalations, suppressions, cap window tracking |
| `~/.rakitsu/wake/<session-id>.state.json` | Session-local state: tick count, interval, cap window, last-known check state |
| `~/.rakitsu/wake/<session-id>.heartbeat` | Heartbeat marker; refreshed on a steady cadence (not just at ticks); stale if older than heartbeat_stale_seconds |
| `~/.rakitsu/wake/<session-id>.tasks/<task-id>.log` | Full result of a started task (the wake session never reads it) |
| `~/.rakitsu/wake/STOP` | Kill-switch file; presence stops the timer loop |

## Supervisor contract

Supervisors (monitoring a long-running session) can:
- Poll heartbeat mtime to detect staleness (> heartbeat_stale_seconds)
- Read audit log to track tick outcomes and escalations
- Call `POST /api/chat/sessions/<id>/wake/resume` after removing kill-switch file to restart

Secrets are specified by env var name only; values are resolved at session start, never stored. Missing names fail `Start`, preventing session registration.

## ACP limitation

`rakitsu acp` does not support wake-up timers. Sessions are in-memory only; no persistent state for restart. Use `rakitsu serve` for long-lived wake sessions.

## Starting tasks (`start_task`)

A woken session can launch independent task runs without getting `cli`, `fs`, `mcp_server` or `a2a`. Set `settings.wake.allow.configs`; the session then gets two native tools, `start_task` and `get_task_status`.

```yaml
settings:
  wake:
    max_tasks_per_hour: 2
    max_concurrent_tasks: 1
    task_timeout_seconds: 300
    allow:
      configs:
        - name: bearish-report            # default: file name without extension
          path: tasks/bearish-report.yaml # relative to the workdir
          task: Write a short report on a bearish flip.   # fixed instruction text
          params:                         # the only values the model may pass
            trend: { type: enum, enum: [down] }
            file:  { type: path }         # relative, under allow.paths
            count: { type: int, max: 20 } # 0..max
```

Rules:
- **Allowlist only.** Anything not listed is refused. Paths must stay inside the workdir (symlinks resolved). At session start every listed file must exist and parse; otherwise the session does not start. The task configs may have their own tools (they are independent runs); the wake session itself still refuses `cli`/`fs`/`mcp_server`/`a2a`.
- **Fixed template.** The task prompt is your `task` text plus a fenced `key=value` block. Arguments must be exactly the declared params (extra or missing keys are refused). Values are enum members, safe relative paths, or bounded integers, so no free text, newline or fence marker can enter the prompt.
- **Independent run.** A task runs under its own context (not tied to the wake turn), loads its config fresh from disk with the server process environment only (it does not inherit the wake session's env), has no `user_input` and no session messaging, and cannot start tasks (the tools are not offered to it and a call from inside a task is refused).
- **Results.** The full result is written to `~/.rakitsu/wake/<session-id>.tasks/<task-id>.log` (mode 0600). The wake session only sees a status and a summary of at most 300 characters.
- **Caps.** `max_tasks_per_hour` is a rolling hour kept in the wake state file. `max_concurrent_tasks` refuses overflow. Both are separate from `max_turns_per_hour`. Refused calls do not use up the hourly allowance.
- **Stop.** `POST /api/chat/{session-id}/wake/stop`, the kill-switch file, and closing the session block new starts and, unless `cancel_on_stop: false`, cancel running tasks. Resume re-allows starts. Cancel one task with `POST /api/chat/{session-id}/wake/tasks/{task-id}/cancel`.
- **Memory and runners.** At most 100 finished task records stay in memory (oldest finished first; running tasks are never dropped). `get_task_status` for a dropped id returns `expired`; the on-disk result log is kept. A runner must honor its `ctx`: on timeout or cancel the task is marked finished at once, but closing the session waits up to 10 seconds for the runner to return, then writes a `task_runner_leaked` audit line and continues.
- **Audit.** Every start attempt (including refused ones) and every end is written to the audit log (`task_start`, `task_refused`, `task_end`) and emitted as `WAKE_TASK_START` / `WAKE_TASK_END`.

## Events

**WAKE_TICK** — `WakeTickPayload`
- Emitted: non-quiet ticks, killed, degraded results
- Fields:
  - `tick` (int): tick number
  - `interval_seconds` (int): interval at this tick
  - `results` ([]WakeCheckResult): per-check outcomes
  - `outcome` ('quiet'|'changed'|'alarm'|'unknown'): overall state
  - `result` ('killed'|'degraded'|''): condition flag
  - `note` (string): optional free-form context
  - `summary_chars`, `summary_cap` (int): rolling summary character counts

**WAKE_ESCALATE** — `WakeEscalatePayload`
- Emitted: escalation attempt, suppression, completion
- Fields:
  - `tick` (int): triggering tick
  - `reason` (string): why escalation was triggered
  - `deferred` (bool): escalation queued but not yet run
  - `suppressed` ('hourly_cap'|'single_flight'|'degraded'|''): suppression reason
  - `result` ('timeout'|'done'|'error'|''): turn outcome
  - `level` (string): 'L2' (tier level)
  - `summary_chars`, `summary_cap` (int): summary character counts at escalation

**WAKE_TASK_START** / **WAKE_TASK_END** — `WakeTaskPayload`
- Emitted: every `start_task` attempt (`status` `started` or `refused`) and when a task ends (`done`, `error`, `timeout`, `cancelled`)
- Fields: `task_id`, `config`, `status`, `reason`, `args` (validated values only), `summary` (at most 300 chars)

## Status and control surfaces

- `GET /api/chat/{id}/wake/status`; `POST /api/chat/{id}/wake/stop`, `.../wake/resume`, `.../wake/tasks/{task-id}/cancel`
- A2A skills `wake_status`, `wake_stop`, `wake_resume`
- `/wake status|stop|resume` slash command (chat TUI and serve chat)
- Web UI status card (`MonitorStatusCard.vue`); see [monitor-autostart.md](monitor-autostart.md)
- Alerts: `settings.wake.alerts` and the `send_alert` tool, see [monitor-autostart.md](monitor-autostart.md#alerts)

## How to cancel

To stop the wake timer in a running session, use one of these methods:

### Method 1: REST API stop endpoint (recommended)

```bash
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/stop
```

Response:
```json
{"status": "stopped"}
```

The stop is **idempotent** — calling again on an already-stopped session returns the same response. The session stays alive; you can continue using it manually or resume the timer later.

### Method 2: Manual kill-switch file

Create the kill-switch file directly:

```bash
touch ~/.rakitsu/wake/STOP
```

The timer stops on the next tick. To resume, remove the file and call:

```bash
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/resume
```

### Resuming after stop

After stopping (via either method), the session stays alive. To resume the timer:

```bash
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/resume
```

If you stopped using the kill-switch file, you **must** remove it first:

```bash
rm ~/.rakitsu/wake/STOP
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/resume
```

Response on success:
```json
{"status": "resumed"}
```

If the kill-switch file still exists, the resume fails with HTTP 409 Conflict.

### What happens to the session

Stopping the timer does **not** close the session or erase its history. The session remains open, the conversation summary persists, and all prior turns are retained. You can:
- Resume the timer with the same configuration
- Manually type messages and run the full agent loop
- Inspect the session's transcript and tree via the web UI
- Create a fork with `POST /api/chat/{session-id}/fork` and start fresh
