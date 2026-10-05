# Security model

Read this before you run a config you did not write, give an agent `cli` or `fs` tools, or let `serve` listen on anything but your own machine. The long version is [docs/SECURITY.md](../SECURITY.md). This page is the short map, plus the changes since alpha.19.

## Two modes

| Mode | Who is trusted | What protects you |
|---|---|---|
| `rakitsu run` (local CLI) | You and your config. Tools run as you, in your working directory. | Your own review of the config. |
| `rakitsu serve` (hub) | Loopback by default. Anyone who can reach it can upload a config and run code as the server user. | Loopback bind, API token, browser-attack guards. |

## Tools are powerful

- The `cli` allowlist and the blocklist are a guard rail against mistakes, not a security boundary. Interpreters on the allowlist (`python3`, `node`, `bash`, `sh`, `find`) can run any code. Only run configs you trust.
- `sandbox.allowed_paths` on a `cli` tool filters the **arguments** that name a path. It does not contain what an interpreter does inside its own code.
- For `fs` tools always set `allowed_paths`. The default `["."]` is the whole launch folder, including `.env` and `.git`.
- A `cli` tool can never call the running rakitsu binary, and it never receives `RAKITSU_API_TOKEN` or `RAKITSU_SESSION_MSG_TOKEN` in its environment.
- Configs with `cli` or `fs` tools must never be wired to anything an untrusted party can message (session messaging, a bridge, A2A, MCP). See [rakitsu-ask](rakitsu-ask.md#threat-model).

### Sandboxing

`sandbox.type` is `local_restricted` (default) or `docker`. Only `docker` isolates a process.

- Docker mode: read-only root filesystem, `--cap-drop=ALL`, `no-new-privileges`, non-root user (`65534:65534` unless `user` is set), a process limit (default 128), no network unless `allow_network: true`, and the working directory mounted read-only unless `mount_workdir_writable: true`.
- Docker fails closed. If `docker` is not on `PATH`, the call fails with `docker sandbox unavailable ... (host execution was not attempted)`. It never runs on the host instead.
- A wrong `sandbox.type` (such as `dokcer`) is refused when the config loads. Docker-only options on a `local_restricted` tool are refused too, and so are contradictory settings.

Docker is still a soft container, not a hardened jail.

## Hub: network exposure

- `--host` must be loopback unless `RAKITSU_API_TOKEN` is set. Otherwise `serve` stops with `refusing to bind non-loopback host ... without RAKITSU_API_TOKEN set`. An empty `--host ""` counts as non-loopback.
- With a token set, the control plane is default-deny: every request under `/api/`, `/ws/`, `/events`, `/mcp` and `/a2a` needs `Authorization: Bearer <token>`.
- Public even with a token: `/health`, `/api/status` (version and boot id), `/healthz` (loopback callers only unless `healthz_allow_remote`), `/.well-known/agent-card.json`, and the static UI files.
- One token covers everything. `RAKITSU_SESSION_MSG_TOKEN` is accepted only for the messaging endpoints.
- The browser UI does not work while a token is set. Use it on loopback, or tunnel and use the API.

Browser attacks (always on):

- A request with a `Host` header that is not `localhost`, `127.0.0.1` or `[::1]` on a loopback bind gets 403 (DNS rebinding).
- A `POST` or `DELETE` with an `Origin` that is not a localhost origin or the hub's own gets 403 (cross-site request).
- Provider keys for model-discovery use `X-Provider-Key`, so the control token is never sent to a `base_url` a caller chose.

### Configs sent to the hub

- `file:` prompt references in configs the hub loads must stay inside the served config directories.
- `${VAR}` in a submitted config resolves from the hub's environment. Treat anyone who may submit a config as able to run code and read the hub's environment.
- Uploads are capped (1 MB config, 10 MB zip, 1000 zip entries, 50 MB unpacked).

## Network rules

These apply to wake `http_status` and `http_json` checks, and to the `a2a` tool. They were tightened in v0.3.0-alpha.20 (see below).

- URLs must be `http` or `https`, with a host, and **no user name or password** in the URL.
- Environment proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`) are **not used**.
- The destination address is checked at connection time, and the checked address is the one that is dialed (this stops DNS rebinding). Loopback, private, link-local, CGNAT, multicast, unspecified and cloud-metadata addresses are refused unless you listed that destination (wake: `allow.url_hosts`; A2A: the host and port of the tool `url`). A listed host name never unlocks metadata or multicast addresses; list the IP.
- Wake checks **never follow redirects**. A redirect counts as a failed check.
- The A2A client follows **only same-origin redirects** (same scheme and host:port), at most 5, and drops the `Authorization` header on every redirect.
- Responses are size-limited and the whole request, body included, is bounded by a timeout.

Alert sinks have their own rules (HTTPS or loopback, exact host allow-list, same-host redirects only). See [alerts](alerts.md).

## Changes since v0.3.0-alpha.19

These are in v0.3.0-alpha.20. They can break a config that used to work.

| Change | What you may notice |
|---|---|
| Wake `http_*` checks no longer follow redirects. | A URL that answers 301, 302, 307 or 308 now gives a check error (`unknown`) instead of following to the final page. Point the check at the final URL. |
| The A2A client follows only same-origin redirects. | A peer that redirects to another host or port now fails with `cross-origin redirect refused`. |
| Neither client uses `HTTP_PROXY` or `HTTPS_PROXY`. | A setup that reached a peer or an API only through a proxy now fails to connect. List the real destination and allow direct access. |
| URLs with `user:password@` are rejected. | The config fails to load (wake checks), or the A2A tool is skipped with `init failed ... URL must not contain credentials`. Use the tool `api_key` or a header-less public URL. |
| Address checks happen at connection time on the address that is dialed, and every validated address is tried. | A host that resolves to a private address is refused unless listed. |
| Wake: the singleton lock is re-checked on every tick. | If the lock file is removed or replaced while a session runs, the loop stops (`lock_replaced`) and resume is refused (the resume call answers HTTP 500 in this release). Restart the session instead. |
| Wake `start_task`: the task config is hashed at session start and checked at every start. | A changed or replaced task config is refused with `task_config_changed`. |
| Wake stop creates the kill-switch file at the path the engine uses. Resume returns 409 while the file exists. | After a stop, remove the file before `resume`. |
| A persistent alarm wakes the model once, not on every tick. | You get one turn when the alarm starts. It repeats only after a quiet tick or a different alarm. |

## Secrets in configs and docs

- Never write a secret as a literal in YAML. Use `${VAR}` and set it in the environment. A literal secret is stored as written in session files.
- Never write `export KEY=literal` in a script, a README or your shell history. Load a secret for one command only, from a private file:

```bash
# create a private folder and file once; then add the line OPENAI_API_KEY=... with your editor
mkdir -p ~/.secrets && chmod 700 ~/.secrets
( umask 077; touch ~/.secrets/openai )

# use it for one command
OPENAI_API_KEY=$(grep '^OPENAI_API_KEY=' ~/.secrets/openai | cut -d= -f2-) \
  rakitsu serve
```

- A shared token for one local run (such as `RAKITSU_API_TOKEN`) is not a stored secret, but do not print it. Write it to a file with `umask 077` and read it back with `$(cat file)`:

```bash
( umask 077; openssl rand -hex 16 | tr -d '\n' > ~/.rakitsu/ask.token )
RAKITSU_API_TOKEN=$(cat ~/.rakitsu/ask.token) rakitsu serve
```

- Session files, events and the web UI mask values whose key looks secret (`token`, `api_key`, `password`, `secret`, `authorization`) and common secret shapes in tool output. This is pattern matching, not a guarantee. Extend it with `settings.redact_keywords`.
- Session files are `0600` in a `0700` directory.

## Reporting

This is pre-release software. Report a security problem as an issue labelled `security`.
