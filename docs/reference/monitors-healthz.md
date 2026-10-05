# Monitors, /healthz and healthcheck

## When to use it

You want a watcher ([wake timer](wake-timer.md)) that starts by itself when the server starts, survives restarts, and can be supervised by Docker, systemd or launchd. This is the 24/7 setup.

## How it works

`rakitsu serve --config monitor.yaml` reads the `monitors:` list and starts each one as a chat session with a stable id `monitor-<id>`. After a restart the same session id is resumed, so the wake state and the hourly cap are kept. A supervisor only has to restart the process.

Secrets are never written in YAML. A monitor lists the **names** of the environment variables it needs (`env_refs`). The values are read from the server process environment when the monitor starts.

## Setup

Serve config (`monitor.yaml`):

```yaml
name: monitor
monitors:
  - id: file-watch                   # session id: monitor-file-watch
    config: ./monitors/file-watch.yaml
    workdir: ./monitors/file-watch
    env_refs: [OPENAI_API_KEY]       # names only
healthz:
  monitors_start_grace_seconds: 120
  healthz_fail_on_stopped: true
  healthz_allow_remote: false
```

The monitor config (`monitors/file-watch.yaml`) is a normal wake config. It must set `settings.wake.enabled: true`. See [wake timer](wake-timer.md#setup).

Start it:

```bash
OPENAI_API_KEY=$(grep '^OPENAI_API_KEY=' ~/.secrets/openai | cut -d= -f2-) \
  rakitsu serve --config monitor.yaml
```

Keys:

| Key | Rule |
|---|---|
| `monitors[].id` | Required. Lowercase letters, digits and `-`, 1 to 40 characters, unique. |
| `monitors[].config` | Required. Path to the monitor config. |
| `monitors[].workdir` | Working directory for the monitor. File checks and task paths are relative to it. |
| `monitors[].env_refs` | Environment variable **names** (no values). If one is missing or empty the monitor is marked `failed`. |
| `healthz.monitors_start_grace_seconds` | Must be 0 or more. Accepted, and no code reads it in this release. |
| `healthz.healthz_fail_on_stopped` | Default `true`: a stopped monitor makes `/healthz` fail. |
| `healthz.healthz_allow_remote` | Default `false`: only loopback callers may read `/healthz`. |

If the `monitors` block has any error, the whole block is rejected. The server still starts, with no monitors, and logs the reason.

## /healthz

`GET /healthz` returns JSON and a status code.

```bash
curl -s http://localhost:9100/healthz
# {"status":"ok","monitors":{"file-watch":"ok"}}
```

| State | Meaning | Counts as healthy |
|---|---|---|
| `ok` | Running, heartbeat fresh. | Yes |
| `starting` | Autostart has not finished. | Yes |
| `stale` | Heartbeat older than `heartbeat_stale_seconds`. | No |
| `stopped` | Stopped by the stop route or the kill-switch file. | No, unless `healthz_fail_on_stopped: false` |
| `failed` | Could not start (missing env name, bad config, session locked by another process). | No |

The answer is `200` when all monitors are healthy, `503` otherwise. With no monitors it is `200`. A non-loopback caller gets `403` unless `healthz_allow_remote: true`. The route uses the real connection address and never trusts `X-Forwarded-For`.

## healthcheck command

```bash
rakitsu healthcheck                                  # http://localhost:9100/healthz
rakitsu healthcheck --url http://localhost:9100/healthz --timeout 5s
echo $?                                              # 0 healthy, 1 otherwise
```

Use it as the Docker health check. Exit code `0` means healthy.

## Run it under a supervisor

Files are in [deploy/monitor](../../deploy/monitor/): a Docker Compose file, a systemd unit and a launchd plist. Key points:

- Put `KEY=value` lines in an env file with mode `0600` (`monitor.env`). Never put values in YAML, in the compose file or on the command line.
- The compose file binds `0.0.0.0` inside the container. A non-loopback bind is refused unless `RAKITSU_API_TOKEN` is set, so add a token line to `monitor.env`. The compose file publishes the port on loopback only.
- Docker uses `rakitsu healthcheck`. systemd can use `WatchdogSec` (rakitsu sends `sd_notify` when `NOTIFY_SOCKET` is set).
- Stop on purpose with `POST /api/chat/monitor-<id>/wake/stop`, or `docker compose down`, `systemctl stop rakitsu-monitor`, `launchctl unload`.

## Control

The wake controls work on a monitor session id: `GET /api/chat/monitor-<id>/wake/status`, `POST .../wake/stop`, `POST .../wake/resume`. A stop also creates the kill-switch file, which you must remove before resume. See [wake timer](wake-timer.md#control). The web UI has a status card with Stop and Resume buttons.

Give each monitor its own `settings.wake.kill_switch_file`. The default path `~/.rakitsu/wake/STOP` is the same for every session. A stop writes that file, and every session that uses the same path stops at its next tick. With two monitors on the default path, stopping one stopped both.

Details: [docs/monitor-autostart.md](../monitor-autostart.md).
