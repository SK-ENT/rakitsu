# Running the rakitsu monitor 24/7

`rakitsu serve --config monitor.yaml` is the only process. A supervisor only restarts it.
See [docs/monitor-autostart.md](../../docs/monitor-autostart.md).

## Secrets
Put `KEY=value` lines in an env file with mode 0600 (`monitor.env`). Never put values in YAML,
in the compose file, or on the command line. YAML lists env var names only.

## Docker
The container binds `0.0.0.0`, so put a generated `RAKITSU_API_TOKEN` in the private
`monitor.env` as well as the provider and alert variables. Publishing on host loopback
does not remove the token requirement. Token-protected control routes require
`Authorization: Bearer <token>`; `/healthz` does not.

    mkdir -p monitors            # add btc-price.yaml etc.
    ( umask 077; touch monitor.env )   # then edit it: OPENAI_API_KEY=..., ALERT_WEBHOOK_URL=...
    docker compose up -d
    docker compose ps            # health column

## Paths and prerequisites
Monitor `config` and `workdir` paths resolve from the server process working directory;
`--config` does not change it. Edit your local copy of `monitor.example.yaml` before use:

- Compose uses `/workspace` as cwd but mounts configs under `/config`. Use
  `config: /config/monitors/btc-price.yaml`. Mount and create a suitable monitor
  workdir, then set `workdir` to its absolute container path; the read-only config
  mount is not a writable task workspace.
- The systemd unit uses `/var/lib/rakitsu` as cwd. For example, use
  `config: /etc/rakitsu/monitors/btc-price.yaml` and
  `workdir: /var/lib/rakitsu/monitors/btc-price`, with those files and directories
  provisioned for the `rakitsu` service user.
- For launchd, use absolute paths in `monitor.yaml`; do not depend on the launch
  directory. Install the executable wrapper and create log directories writable
  by the launching user before loading the plist.

Each monitor config must enable wake and should set its own absolute
`settings.wake.kill_switch_file`; the default STOP file is shared by all sessions.
The deployment files are templates and do not provision monitor configs or workdirs.

## systemd (Linux)
Create the `rakitsu` user and group, install the binary, and provision the paths above
before enabling the unit.

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
monitors are healthy, 503 otherwise. Loopback only by default. `starting` is healthy;
with no monitors the response is also 200, including after a rejected monitor block.
Verify that the response contains all expected monitor IDs. Docker records an
unhealthy status, but `restart: unless-stopped` does not itself restart a running
unhealthy container; arrange external remediation if required. CI and heartbeat checks do not establish continuous 24/7 operation.

## Stop on purpose
`POST /api/chat/monitor-<id>/wake/stop`, or `/wake stop` in chat. Both create the
configured kill-switch file. Remove that file before
`POST /api/chat/monitor-<id>/wake/resume`; resume returns 409 while it exists.
A stopped monitor fails `/healthz` unless `healthz_fail_on_stopped: false`.
To stop the whole service: `docker compose down`, `systemctl stop rakitsu-monitor`, or `launchctl unload`.
