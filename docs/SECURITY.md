# Rakitsu security & trust model

Read this before running Rakitsu with configs you did not write, or before
exposing `rakitsu serve` to anything beyond your own machine.

Rakitsu runs LLM-driven agents that execute tools — shell commands and file
operations — on your behalf. The security posture depends entirely on **which
of two modes** you are in.

## Mode 1 — local CLI (`rakitsu run`)

Trust model: **the config is yours, and tools run as you.**

When you run `rakitsu run config.yaml "…"`, any `cli` or `fs` tool the config
declares executes with **your user's full permissions**, in your working
directory. This is by design — it is what makes a local coding agent useful.

### The cli tool allowlist is a footgun fence, not a sandbox

The `cli` tool has a built-in command allowlist (`ls`, `cat`, `git`, `go`,
`python3`, `node`, `bash`, …) and a denylist (`rm`, `sudo`, `dd`, …). **Treat
this as a guard rail against obvious mistakes, not as a security boundary.**

The allowlist includes general-purpose interpreters — `python3`, `node`, `go`,
`bash`, `sh` — and `find`. Any of these can execute arbitrary code or delete
files, so the denylist does **not** actually contain a hostile config:

```yaml
# All of these run despite `rm` being "blocked":
python3 -c "import shutil; shutil.rmtree('...')"
find . -delete
bash -c "$(printf 'r''m -rf x')"
```

The shell-payload lint (`lintShellPayload`) is best-effort and trivially
bypassable — the source says so explicitly.

**Consequence:** only run configs you trust, the same way you only run shell
scripts you trust. Do not rely on the allowlist to make a config from an
untrusted source safe.

### `allowed_paths` on cli tools is an argument filter, not containment

`sandbox.allowed_paths` on a `cli` tool makes rakitsu refuse a command whose
**arguments** name a location outside the listed directories. Every
whitespace-separated token of every argument — including the payload of a
`sh -c '…'` template and `--opt=path` / `VAR=path` values — is resolved the
way the command will see it: relative to the directory the command runs in,
with `..` cleaned, symlinks resolved, and `~/` expanded. `cat ../secret`,
`cat /etc/passwd`, `cat link-to-outside` and `sh -c 'cat ~/secret'` are all
refused.

That is the whole guarantee. The filter only sees paths that appear verbatim
as tokens, so it does **not** contain:

- an allowed interpreter opening a path named in its code
  (`python3 -c "open('../secret')"`, `node -e …`);
- a shell payload that builds the path from variables or substitution
  (`sh -c 'cat $HOME/secret'`, `cat $(printf ../secret)`);
- commands re-invoked by `find -exec`, `xargs`, or `env`;
- anything the command reaches through the network.

`local_restricted` is therefore the right default for configs you trust and
want protected from *obvious* mistakes by the model. If untrusted execution
needs a real filesystem boundary, use `sandbox: { type: docker }` — that is
the only mode that provides process isolation.

A parameter's `argv_split: true` (splitting a whole-part templated value
into multiple argv tokens, e.g. for `command: "gh {{args}}"`) doesn't change
any of this: `checkArgPaths` already re-splits every argv element on
whitespace before checking it, whether or not `argv_split` is set.

### Reducing blast radius

- **`fs` tools:** always set `allowed_paths` explicitly. The default is `["."]`
  — the *entire* launch directory — so a config that omits it and is launched
  near secrets can read `.env`, `.git`, credentials, etc. Point it at the
  narrowest directory the task needs. `read` is also capped at 10MB by
  default (`sandbox.resource_limits.max_output_bytes`, `-1` disables it), so a
  huge or special file (a multi-GB log, `/dev/zero`) can't be fully buffered
  into memory before any limit applies.
- **`cli` tools:** keep `settings.allowed_commands` minimal. Every command you
  add is a new capability. `docker` in particular is effectively root on most
  dev machines (it can mount the host and run privileged containers). The
  running `rakitsu` binary itself is always rejected, even if you list it in
  `allowed_commands` — a `cli` tool cannot re-invoke rakitsu to spawn a second
  process against a different config/workdir and escape this session's
  sandboxing. This also catches a symlink or hard link to the binary under
  an unrelated name (checked by file identity, not just the name), but not
  a byte-for-byte copy under a different name, or — the more realistic
  case, since it needs no copying at all — a small wrapper script or
  binary that isn't rakitsu in any file-identity sense but simply execs
  the real rakitsu binary as its own action (a one-line shell script
  reading `exec /path/to/rakitsu "$@"` is enough). Both are indistinguishable from any other
  unknown executable without hashing file contents on every `cli` call
  (which wouldn't even help against the wrapper, since its content
  genuinely differs from rakitsu's), so rakitsu deliberately doesn't try.
  **What this fix actually closes is the *free* version of
  self-invocation** — a bare name in `allowed_commands`, or a symlink/hard
  link, both zero-engineering-effort attacks — not every path to it. What
  remains needs the attacker to already hold a materially stronger
  foothold first: an interpreter already in `allowed_commands`
  (`bash -c 'exec rakitsu ...'`, `python3 -c "os.execv('rakitsu', ...)"`)
  or the ability to plant an arbitrary new file into an allowed command
  path — at which point self-invocation specifically is no longer the
  interesting problem, since either foothold already grants far more.
  Whether that residual gap is worth closing further, and how, is left
  as an open question rather than a solved one: no additional static,
  pre-exec check can decide "will this arbitrary executable, once run,
  itself exec rakitsu" without either hashing (which doesn't help here)
  or actually tracing/containing what the child process does at
  runtime — which is what real OS-level isolation is for, not a
  userspace identity check. `sandbox: { type: docker }` is the answer for
  that today; a narrower, purpose-built answer may or may not be worth
  building later.
  The self-invocation check and the actual exec both resolve the command
  to a single, absolute path (rather than each doing their own separate,
  possibly-relative lookup) to close the window between them. **On Linux**,
  the check-then-exec gap is closed entirely: the resolved file is opened
  with `O_PATH` (a location-only open that needs no read permission on the
  file — matching what exec itself needs, so a legitimately execute-only
  command, mode `0111`, still runs), re-verified via that file descriptor's
  own identity, then exec'd through `/proc/self/fd/N` rather than by path —
  an open descriptor keeps referring to its original inode even if the path
  is later replaced, so what was verified and what actually runs are
  provably the same file, no matter what happens to the path in between.
  **On macOS/BSD**, which have no `/proc` and no portable
  file-descriptor-based exec, this extra step doesn't apply: the
  self-invocation check still runs (by resolved path, as above) but a
  narrow, classic check-then-exec race remains between that check and the
  exec syscall, requiring an attacker with concurrent filesystem write
  access to the exact resolved path timed to a sub-millisecond window (a
  materially stronger position than the original gap this section
  describes, which required nothing more than a name in
  `allowed_commands`).
- **Untrusted work:** use `sandbox: { type: docker }` (see below).

### Docker sandbox

`sandbox: { type: docker }` gives stronger isolation than `local_restricted`:
the container's root filesystem is read-only (with a small writable `/tmp`
for scratch space), setuid/setgid privilege escalation is blocked
(`--security-opt=no-new-privileges`), every Linux capability is dropped
(`--cap-drop=ALL`), the container runs as an unprivileged user by default
(`--user`, `65534:65534` unless `sandbox.user` overrides it), a process/thread
count limit applies (`--pids-limit`, 128 by default, `sandbox.resource_limits.
pids_limit` to change it), and the container has **no network access unless
`allow_network: true`** is set. The mounted working directory (`mount_workdir:
true`) is read-only unless `mount_workdir_writable: true` is also set. Prefer
it over local execution for untrusted configs, but it is still a soft
container, not a hardened jail — a kernel exploit or a docker misconfiguration
elsewhere on the host is out of scope for any of the above.

The docker path fails closed. If the `docker` client is not installed or not
on `PATH`, the tool call errors out saying so — it never runs the command on
the host instead. The same timeout as local execution applies
(`resource_limits.timeout_sec`, 30s default), and each run gets a unique
`rakitsu-<uuid>` container name that is force-removed afterwards, so a
timeout or cancellation that kills the docker client cannot leave the
container running behind it. `mount_workdir: true` mounts the tool's
`working_dir` when one is set (the process cwd otherwise), the same directory
local mode would run in.

### Sandbox settings are validated, not guessed

An unknown `sandbox.type` (a typo like `dokcer`) is refused — at config load
and again at execution time for tools built directly. It used to fall through
to `local_restricted`, i.e. a typo silently picked the *least* isolated mode.
Docker-only options (`image`, `user`, `mount_workdir*`, `allow_network`,
`network_isolated`, cpu/memory/pids limits) set on a `local_restricted` tool
are refused for the same reason: they were ignored, which reads as isolation
the tool never had. Contradictory (`allow_network` + `network_isolated`,
`mount_workdir_writable` without `mount_workdir`) and negative settings are
refused too. An empty `type` still means `local_restricted`.

## Mode 2 — the hub (`rakitsu serve`)

Trust model: **loopback-only and single-user by default.**

`rakitsu serve` exposes a control plane that can **upload a config and start a
run** — i.e. it can execute code as the server's user. That is safe on
`localhost` (only you can reach it) and unsafe on any routable interface
without authentication.

### Network exposure requires an API token

Binding a non-loopback host (`--host 0.0.0.0`, a LAN IP, a Tailscale address,
or an empty `--host ""`, which listens on every interface) is **refused**
unless you set an API token:

```bash
export RAKITSU_API_TOKEN="$(openssl rand -hex 32 | tr -d '\n')"
rakitsu serve --host 0.0.0.0
```

When the token is set, the control plane is **default-deny**: every request
under `/api/`, `/ws/`, `/events`, `/mcp` and `/a2a` requires
`Authorization: Bearer <token>`. That includes the persisted-session history
(`/api/sessions…` — queries, outputs, stored configs, deletion, rerun), the
uploaded-config list and reads (configs can embed provider API keys), the
live event stream, and the CLI↔hub reporting endpoints (`/api/hub/*`). A
handler added under those prefixes is gated without a code change to the
auth policy.

The only public exceptions are `/health`, `/api/status` (version and boot id
only), the A2A agent card at `/.well-known/agent-card.json` (the spec expects
it to be fetchable so a client can learn what auth is required; the card
advertises the bearer requirement), and the static web UI assets.

The same token also satisfies cross-session messaging
(`RAKITSU_SESSION_MSG_TOKEN` is still accepted for that feature specifically;
`/api/sessions/{id}/message` and `/inbox` are checked by that feature's own
gate so a client holding only the messaging token keeps working).

**CLI runs reporting to a token-protected hub** (`rakitsu run --hub …`,
`rakitsu chat`) must have the same `RAKITSU_API_TOKEN` in their environment;
the hub client sends it on every call, and the cli tool scrubs it from the
subprocesses it spawns so an agent cannot read it back.

### Known limitations (Phase 2)

- The **web UI does not work while a token is set**: the browser cannot
  attach the bearer token to page navigations, `EventSource` or WebSocket
  upgrades, and since the control plane is default-deny the UI loses the
  history browser, config list and live event stream as well as the run/chat
  controls. Run the hub on loopback without a token for local UI use, or
  reach a remote hub through an SSH tunnel and call the API directly with the
  token. UI-side token support is a separate follow-up.
- The **MCP (`/mcp`) and A2A (`/a2a`) endpoints require the same bearer
  token as everything else**, and the standalone `--mcp-port` listener
  mounts the MCP server at `/mcp` only, so no other path on that port can
  reach it. The Agent Card discovery route
  (`/.well-known/agent-card.json`) is deliberately left open — the A2A spec
  expects it to be publicly fetchable so a client can learn what auth is
  required before authenticating, and the card advertises the requirement
  (`securitySchemes`/`security`) whenever `RAKITSU_API_TOKEN` is set. An
  `a2a`-type tool delegating to a token-gated peer supplies the credential
  via its own `api_key` field (`${VAR}`-expanded like provider `api_key`
  values), sent as `Authorization: Bearer <api_key>`. The non-loopback bind
  refusal remains the backstop for all of it — keep it behind a tunnel or a
  trusted network.
- WebSocket upgrades are restricted to localhost/same-origin (so a random web
  page cannot drive your agent). There is no CSRF token on same-origin POSTs.

### Browser-driven attacks (all modes)

- **Cross-site requests are refused.** A `POST`/`DELETE` carrying an `Origin`
  that is neither a localhost origin nor the hub's own origin gets a 403, so
  a web page you visit cannot upload a config and start it on your local hub
  (CORS alone would only hide the response, not stop the request). Clients
  that send no `Origin` (the CLI, curl) are unaffected.
- **DNS rebinding is refused.** On a loopback bind, a request whose `Host`
  header isn't `localhost`, `127.0.0.1` or `[::1]` gets a 403.
- **Provider keys use their own header.** The model-discovery endpoints
  (`/api/providers/models`, `/api/providers/model-info`) take the provider
  key in `X-Provider-Key`. When `RAKITSU_API_TOKEN` is set, the
  `Authorization` header is never used as a provider key, so the control
  token is never forwarded to a caller-chosen `base_url`.

### Configs submitted to the hub

- `file:` prompt references in configs loaded by `rakitsu serve` must
  resolve (after symlinks) inside the served config directories (`.`,
  `./examples`, `./configs`, `--config-dir`), the upload directory, or the
  `--config` file's own directory. `file:../../home/you/.ssh/id_rsa` in an
  uploaded config is rejected. The CLI (`rakitsu run`) keeps allowing any
  path the config author can read.
- `${VAR}` references in a submitted config still resolve from the hub's
  environment, and `base_url` may point at loopback/private addresses —
  local providers (Ollama, a LiteLLM proxy) need both. This is safe because
  only a trusted operator can submit configs (loopback, or the API token on
  a network bind); treat anyone who can reach the hub that way as able to
  run code and read the hub's environment.
- Uploads are capped (1 MB single config, 10 MB zip, at most 1000 zip
  entries and 50 MB extracted). The upload directory
  (`$TMPDIR/rakitsu-configs`) must be a real directory owned by the hub's
  user; a symlink or someone else's directory there is refused.
- The web UI never stores provider/tool API keys, credentials files or run
  env var values in `localStorage`; keys saved there by older versions are
  removed on next load.

## Telemetry & session logs

Session files live in `~/.rakitsu/sessions/` (directory `0700`, files `0600`,
tightened on start if an older version created them more open). Each starts
with a snapshot of the run's config: `api_key` values are masked to
`[REDACTED]`, and any other field written as a `${VAR}` reference (tool
`args`/`url`/`command`, provider `base_url`/`default_model`/
`credentials_file`, …) is stored as that reference, not its resolved value.
A secret typed literally into the YAML is stored as written.

Tool-call events (`TOOL_CALL_START`'s and `THOUGHT_END`'s planned-call
`Arguments`) are written to both `~/.rakitsu/sessions/<id>.jsonl` and, when
connected to a hub, the hub's event stream. Any argument whose key looks
credential-shaped (`token`, `api_key`, `password`, `secret`,
`authorization`, case-insensitive) is masked to `[REDACTED]` before either
sink sees it.

Tool *output* (`TOOL_CALL_END.Output`/`.Error`) is free-form text (e.g.
whatever a `cat` or `curl` call printed), not a structured key/value map, so
the key-name rule above doesn't apply to it directly. If the output is
itself a JSON document (a `cli`/`mcp_server` tool wrapping an API response,
for example), it's parsed and the same key-name rule runs on it, whatever
its formatting/indentation. Otherwise it gets a narrower, best-effort
pattern pass: PEM private-key blocks, whole `Authorization:` header lines
(any scheme, not just the first token), bare bearer tokens, whole
`KEY=value`/`KEY: value` lines whose key looks credential-shaped, and
userinfo passwords in connection-string URLs
(`scheme://user:password@host`) are masked before either sink sees them.

This is pattern matching on known shapes, not general secret detection —
it will not catch a credential in a format it doesn't recognize, and it
does not survive deliberate obfuscation (base64/URL-encoding, splitting a
secret across output, etc.). Treat it as reducing exposure from ordinary
debugging output, not as a guarantee. Don't feed configs/agents that might
print secrets in unusual shapes into a shared or long-lived session without
reviewing the log.

If your project uses field names the built-in list doesn't know about
(e.g. an internal `project_code` that's effectively a secret), add them to
`settings.redact_keywords` in the config:

```yaml
settings:
  redact_keywords:
    - project_code
    - internal_ticket_id
```

Each word extends the same key-name and free-text matching described
above (case-insensitive substring match, literal — not a regex pattern).

## Reporting

This is pre-release (alpha) software. If you find a security issue, please open
an issue tagged `security` rather than a public disclosure.

## Summary

| You are… | Safe? | What protects you |
|----------|-------|-------------------|
| Running your own config locally | Yes | You trust your own config |
| Running an **untrusted** config locally | **No** | Allowlist and `allowed_paths` are not a boundary — use Docker sandbox |
| `serve` on localhost | Yes | Only you can reach it |
| `serve` on the network, no token | **Refused** | Won't start |
| `serve` on the network, with token | Mostly | Default-deny token-gated API; web UI needs a tunnel and no token |
