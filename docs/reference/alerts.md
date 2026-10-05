# Alerts

## When to use it

A wake session or monitor should tell a person when something is wrong: a phone push, a chat webhook. The agent only **queues** a message. The server sends it, to hosts you allowed, with secrets removed and a rate cap.

## Setup

Sinks live under `settings.wake.alerts`. They work in sessions that have `settings.wake.enabled: true`.

```yaml
settings:
  wake:
    enabled: true
    alerts:
      rate_per_hour: 20
      retry:
        max_attempts: 3
        backoff_seconds: 5
      sinks:
        - name: phone
          type: ntfy                  # webhook | ntfy
          url_env: ALERT_WEBHOOK_URL  # NAME of the env var that holds the URL
          allow_hosts: [ntfy.sh]
          min_severity: warn          # info | warn | critical (default warn)
```

The URL itself is never in the YAML. Set it in the environment of the server, for example in an env file ([monitors](monitors-healthz.md)) and list the name in the monitor's `env_refs`.

| Key | Default | Rule |
|---|---|---|
| `rate_per_hour` | 20 | Per sink. 1 to 600. Extra alerts are dropped, and a summary "Alerts suppressed" goes out in the next hour. |
| `retry.max_attempts` | 3 | 1 to 5. |
| `retry.backoff_seconds` | 5 | 1 to 60. |
| `sinks[].name` | | Required, unique. |
| `sinks[].type` | | `webhook` sends JSON `{severity, title, body, time}`. `ntfy` sends the body as text with a `Title` and `Priority` header. |
| `sinks[].url_env` | | Required. An environment variable name. |
| `sinks[].allow_hosts` | | Required. Bare lowercase host names, no scheme, port, path or wildcard. |
| `sinks[].min_severity` | `warn` | Lower severities are not sent to this sink. |

## The send_alert tool

With a working sink, the session's agents get one tool.

| Tool | Arguments |
|---|---|
| `send_alert` | `severity` (`info`, `warn`, `critical`), `title`, `body` |

It returns `alert queued` at once and never waits for delivery. Title is cut at 200 characters, body at 2000. A payload over 8 KB is dropped. The queue holds 128 messages and drops the oldest when full.

## Safety rules

- The sink URL must be `https`. `http` is allowed only for `localhost` and `127.0.0.1`. The host must be in `allow_hosts`. No `user:password@` in the URL.
- Redirects are followed only to the same host, and only if that host is allowed.
- A sink with a missing environment variable, a bad URL or a bad type is disabled, and the reason (never the URL) is reported as an error event.
- Each sink has its own rate limit and retries up to `max_attempts` within 60 seconds.

## Limits

- Alerts need `settings.wake.enabled: true`. Without it, no notifier is created.
- If any sink is invalid, the whole notifier is not created in a serve chat session, so the other, valid sinks are not used either. Fix every sink, or remove the broken one.
