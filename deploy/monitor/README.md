# Running the rakitsu monitor 24/7

`rakitsu serve --config monitor.yaml` is the only process. A supervisor only restarts it.
See [docs/monitor-autostart.md](../../docs/monitor-autostart.md).

## Secrets
Put `KEY=value` lines in an env file with mode 0600 (`monitor.env`). Never put values in YAML,
in the compose file, or on the command line. YAML lists env var names only.

## Docker
    mkdir -p monitors            # add btc-price.yaml etc.
    ( umask 077; touch monitor.env )   # then edit it: OPENAI_API_KEY=..., ALERT_WEBHOOK_URL=...
    docker compose up -d
    docker compose ps            # health column

## systemd (Linux)
    sudo install -m 0644 rakitsu-monitor.service /etc/systemd/system/
    sudo install -d -m 0750 /etc/rakitsu   # place monitor.yaml and monitor.env (0600) here
    sudo systemctl daemon-reload && sudo systemctl enable --now rakitsu-monitor
    systemd-analyze verify /etc/systemd/system/rakitsu-monitor.service

## launchd (macOS)
launchd has no env file. Write a small wrapper that loads the env file and execs rakitsu:

    #!/bin/sh
    set -a; . "$HOME/.rakitsu/monitor.env"; set +a
    exec /usr/local/bin/rakitsu serve --config "$HOME/.rakitsu/monitor.yaml"

Then: `cp com.rakitsu.monitor.plist ~/Library/LaunchAgents/`, `plutil -lint` it, and
`launchctl load ~/Library/LaunchAgents/com.rakitsu.monitor.plist`.

## Health
`curl -s http://localhost:9100/healthz` returns `{"status":"ok","monitors":{...}}`, 200 when all
monitors are ok, 503 otherwise. Loopback only by default.

## Stop on purpose
`POST /api/chat/monitor-<id>/wake/stop` (resume with `/wake/resume`), or `/wake stop` in chat.
A stopped monitor fails `/healthz` unless `healthz_fail_on_stopped: false`.
To stop the whole service: `docker compose down`, `systemctl stop rakitsu-monitor`, or `launchctl unload`.
