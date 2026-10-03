# Monitor autostart

Deployment files: `deploy/monitor/`.

Status: merged on main. The native 24h run is tested with a fake clock; a live run is in progress and not yet proven.

## Idea
`rakitsu serve --config monitor.yaml` starts every monitor in `monitors:` itself. Session ids are
stable (`monitor-<id>`), so a restart resumes the same session. A supervisor (Docker, systemd, launchd)
only restarts the process.

## Config
See `deploy/monitor/monitor.example.yaml` (BTC price example). YAML holds env var names only.

    monitors:
      - id: btc-price                  # session id: monitor-btc-price
        config: ./monitors/btc-price.yaml   # must set settings.wake.enabled: true
        workdir: ./monitors/btc-price
        env_refs: [OPENAI_API_KEY, ALERT_WEBHOOK_URL]
    healthz:
      monitors_start_grace_seconds: 120   # default 120
      healthz_fail_on_stopped: true       # default true
      healthz_allow_remote: false         # default false: loopback callers only

## Health
`/healthz`: 200 when every monitor is `ok`, else 503. States: ok, stale (heartbeat older than `heartbeat_stale_seconds`, same rule as the status API), stopped, failed, starting. A running loop with no readable heartbeat file is `starting` for `heartbeat_stale_seconds` after the loop starts (200 on `/healthz`), then `stale` (503).
Docker uses `rakitsu healthcheck [--url URL] [--timeout 5s]` (exit 0 healthy, 1 otherwise); systemd uses `WatchdogSec` with sd_notify (`NOTIFY_SOCKET`).

## Alerts
The agent calls the `send_alert` tool, which only queues a message. The server sends it to allow-listed hosts,
with redaction and a rate cap. Sinks live under `settings.wake.alerts` (code: `internal/alert/`):

    settings:
      wake:
        alerts:
          rate_per_hour: 20          # per sink, default 20
          retry: {max_attempts: 3, backoff_seconds: 5}   # defaults; max 5 / 60
          sinks:
            - name: phone
              type: ntfy             # webhook | ntfy
              url_env: ALERT_WEBHOOK_URL   # env var NAME holding the URL
              allow_hosts: [ntfy.sh]
              min_severity: warn     # info | warn | critical, default warn

## Status and control
`GET /api/chat/{id}/wake/status`, `POST .../wake/stop`, `POST .../wake/resume`, `POST .../wake/tasks/{task-id}/cancel`.
The same three controls exist as A2A skills (`wake_status`, `wake_stop`, `wake_resume`) and as the `/wake status|stop|resume`
slash command in chat. The web UI card
(`MonitorStatusCard.vue`, props: `sessionId`) polls status every 5 s and has Stop and Resume buttons.
Mount it with `<MonitorStatusCard session-id="monitor-btc-price" />`.

## Secrets
Use an env file (0600). Never `export KEY=literal` in docs or shell history.
