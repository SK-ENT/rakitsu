# 13 — Voice Orchestrator

A live, real-time voice conversation (streaming duplex, not push-to-talk)
that can dispatch a rakitsu worker agent to do real work in the background
while the conversation keeps going, then speak the result back once it's
ready.

## Cost warning

Realtime voice is far more expensive than text or text-to-speech: you're
billed for both the audio you send (mic input) and the audio the model
speaks back, the whole time the connection is open — not just while
someone is actually talking.

Per OpenAI's pricing page (`developers.openai.com/api/docs/pricing`), the
default model here is `gpt-realtime-2.1-mini`:

| | Audio input | Audio output |
|---|---|---|
| `gpt-realtime-2.1-mini` (default) | $10.00 / 1M tokens | $20.00 / 1M tokens |
| `gpt-realtime-2.1` | $32.00 / 1M tokens | $64.00 / 1M tokens |

As a rough guide (actual token rate varies by audio content), that's on
the order of **$0.02–$0.05 per minute of open conversation** on the mini
model — several times that on the full model. Each dispatched task
also runs a separate, much cheaper text-model rakitsu worker turn
(`worker-config.yaml` defaults to `gpt-4o-mini`) on top of that.

Close the bridge (Ctrl+C) when you're done — an open WebSocket connection
keeps consuming audio-input tokens for as long as it's live, whether or
not anyone is talking.

## Background

This example follows a simple design rule: a
realtime voice API — WebSocket protocol, audio codec, session config shape
— is exactly the kind of fast-moving external surface that should stay
outside rakitsu core. An external "voice bridge" owns the live audio and
the Realtime API connection; rakitsu supplies only what it already has for
async task dispatch.

OpenAI's Realtime API supports `async: true` tool calls: the model keeps
conversing after issuing a tool call, before the result comes back. That's
what gives "voice keeps going while a sub-agent works in the background" —
a capability of the realtime API itself, not something rakitsu needed to
build.

Four rakitsu-side dispatch mechanisms were surveyed before picking one:

- **A2A** (`internal/tools/a2a/tool.go`) — the wire protocol supports
  async submit/poll, but rakitsu's server handler is synchronous today, so
  it would block the bridge's HTTP call for the whole sub-agent run.
- **`chathost` tools** (`invoke_config` etc.) — in-process only, no HTTP
  surface an external bridge could reach.
- **Hierarchical orchestrator delegation** — fully blocking.
- **`internal/server/session_message.go` + `chat_manager.go` — the actual
  fit, with a correction found via live testing (see below).** `POST
  /api/chat/start` gets a session id immediately, then `POST
  /api/sessions/{id}/message {wait:true}` blocks server-side up to 120s
  (`sessionMsgWaitMaxMs`) and returns the worker's real answer directly.
  The original plan assumed `wait:false` + polling `GET
  /api/sessions/{id}/inbox` (a true fire-and-forget shape with no wait
  cap) — live testing found that path silently loses the answer for a
  local `rakitsu serve` chat session: the inbox/mailbox is only ever
  populated by a worker's own `send_message` tool call, which requires
  telling the worker to use it — and having the worker "reply" via a tool
  call, to a caller that isn't a real registered session, throws the
  actual answer away. `wait:true`, run in the bridge's background thread
  (not the WebSocket loop), gets the same non-blocking-to-the-user
  property with a proven mechanism instead. See "Live-tested" below.

**Result: zero new rakitsu core code.** This example is entirely a new
external bridge script + a worker config, reusing `serve`'s existing HTTP
surface as-is — the same "keep the volatile part external" pattern as
`12-media-generation`'s shell script, just dispatching to a full rakitsu
agent instead of a one-off `cli` tool call.

## What's here

- `worker-config.yaml` — a single-agent config, the same shape as
  `examples/single/02-single-agent`, hosted by `rakitsu serve` and
  dispatched to via A2A (`/a2a`, gated by `RAKITSU_API_TOKEN`). It adds a
  few things beyond an ordinary agent: `settings.session_msg.enabled:
  true` — kept for backward compatibility but **legacy and unused** by
  the current A2A dispatch path, which needs only `--config <file>` on
  `rakitsu serve` to activate `/a2a` — an explicit system-prompt
  instruction to **never** use the `send_message` tool that
  `session_msg.enabled` also grants the agent, and instead always answer
  directly in its own response text (found live, back when
  `session_msg` was the active dispatch path: without it, the worker
  tries to "reply" via `send_message` to the bridge's caller id, which
  isn't a real live session, and its real answer never reaches anyone —
  the instruction is kept since the same tool is still granted), and real
  capability tools (`list_dir`, `read_file`, `write_file`, and
  fixed-command `cli` tools for `git`/`go`/`ls`/`cat`), so a dispatched
  task can actually do development work, not just answer from the
  conversation. See "Development capability" below for the safety
  tradeoff that comes with that.
- `bridge/voice_bridge.py` — the external half. Connects to OpenAI's
  Realtime API over WebSocket (mic in via `sounddevice`, speaker out). The
  realtime model itself registers **no tools** and never triggers
  dispatch directly — instead, every completed user transcript (except
  obvious trivial chit-chat) is monitored: it runs the dispatch as a
  background `asyncio` task via a thread-pool executor — deliberately
  **not** awaited inline in the WebSocket receive loop — so mic audio
  keeps streaming and the conversation keeps going while the dispatch is
  in flight. Dispatch goes through rakitsu's A2A JSON-RPC endpoint
  (`POST /a2a`, shared across all three bridges via
  `bridge/rakitsu_dispatch.py`): `SendMessage` starts a fresh task and
  returns immediately, then `GetTask` is polled for its terminal status
  and result artifacts, bounded by this client's own
  `DISPATCH_MAX_WAIT_SECONDS` deadline (default 120s — not a server-side
  cap). The result is then posted back into the Realtime session as a
  system-role conversation item so the model reports it naturally;
  replies starting with "no action needed" stay silent. Model/endpoint/
  token names are all env vars, never hardcoded as the only option.
- `bridge/voice_bridge_local.py` — the local, turn-based variant. Not a
  realtime duplex connection like the two bridges above — still turn-based
  transcription, no word-by-word streaming — but turn boundaries are now
  found automatically with **Silero VAD** (ONNX variant) instead of a
  manual Enter key press: the mic is monitored continuously, a turn starts
  once speech is detected and ends after a trailing-silence window,
  transcribed with **faster-whisper** (local Whisper via CTranslate2),
  dispatched via `RakitsuDispatcher().dispatch_sync()` — called directly,
  no `asyncio` needed since there's no live audio event loop to protect —
  then spoken back with **Piper** (local neural TTS, run as a CLI
  subprocess). While Piper is speaking, VAD keeps running in the
  background: the instant it detects speech, playback stops immediately
  (barge-in) and that speech becomes the start of the next turn, not
  discarded audio. STT, TTS, and VAD all run entirely on this machine; no
  cloud voice API and no per-minute realtime audio billing. The dispatched
  `VoiceWorker` agent still uses its own separately configured LLM
  provider, unchanged — only the voice I/O layer is local here. A
  `VAD_ENABLED=0` fallback keeps the original manual push-to-talk
  loop available with no VAD and no barge-in.
- `bridge/gemini_tts_test.py` — a standalone probe for Gemini's *dedicated*
  TTS models (`gemini-3.8-flash-lite-tts` / `gemini-3.8-flash-tts`,
  shipped 2026-09-23), not a bridge and not wired to rakitsu at all. It's
  for exploring single-speaker style control and multi-speaker dialogue
  outside any realtime/conversational context. Uses the `:generateContent`
  request shape (not the newer `/v1beta/interactions` endpoint Google's
  docs now lead with), because that shape works against **both**
  `--auth api-key` (`GEMINI_API_KEY`, the Gemini Developer API) and
  `--auth vertex` (GCP project + `gcloud auth application-default login`,
  no API key needed) — different from the Live API's `BidiGenerateContent`
  used by `voice_bridge_gemini.py` below; see the script's own docstring
  before assuming either one's schema applies to the other, and note the
  Vertex path is unverified for the 3.8 models specifically (Google Cloud's
  own TTS docs, as of this writing, list only up to
  `gemini-3.1-flash-tts-preview` as confirmed-available on Vertex — try 3.8
  and fall back to 3.1-preview if Vertex rejects it). Run it directly:
  `python3 examples/single/13-voice-orchestrator/bridge/gemini_tts_test.py --help`

## Run

**Use headphones/earbuds, not open speakers.** This script has no
acoustic echo cancellation — with speakers, the mic picks up the
assistant's own voice and it ends up talking to itself. See "Live-tested"
below for what that looked like when it happened.

**If `pip install` fails with "No module named pip"**, your `python3` is
likely a `uv`-managed venv (check `python3 -c "import sys;
print(sys.executable)"` — a `uv` venv has no `pip` by default). Use `uv pip
install ...` instead of plain `pip install ...` in the commands below.

**Loading secrets without ever exposing the literal value — MANDATORY,
not a suggestion.** None of the commands below use `export KEY=value` with
a real value typed in. `export` followed by a literal secret leaves it
sitting in your shell history and terminal scrollback (both readable by
anything with disk/screen access — a session recorder, a shared terminal,
a crash-report tool) for as long as that history file exists. Instead:

- Secrets load via a `secret()` helper function reading a local
  `KEY=value` file (never committed, `chmod 600`) — exact-match on the key
  name, and strips only the first `=` so a value that itself contains `=`
  (common in base64/JWT-shaped keys) comes through intact.
- `RAKITSU_API_TOKEN` (a one-session password shared between `serve` and
  a bridge, not a stored provider secret) is generated with `mktemp` into
  a file instead of printed — a fixed path like `/tmp/foo` would let
  another local user pre-create it as a symlink to a file they can read,
  since shell redirection (`>`) follows symlinks; `mktemp` creates a
  private file atomically (`O_EXCL`, mode 0600, unpredictable name),
  closing that off.
- Every command assigns each variable **inline** (`VAR=$(...) command`),
  not via a separate `export` line — inline scopes the value to that one
  process only, while `export` also leaks it into every later command in
  that shell session. Functionally identical to the program either way
  (confirmed against `internal/server/auth.go`'s and
  `internal/config/config.go`'s plain `os.Getenv` reads) — this is about
  exposure surface, not behavior.

**Each block below is self-contained — copy the whole block for the
terminal you're setting up, not just the last command in it.** Shell
functions and unexported variables are per-process: `serve` and each
bridge run in separate terminals, so `secret()` must be (re)defined, and
`RAKITSU_TOKEN_FILE` (re)set, in every terminal — which is why each block
repeats them rather than pointing back at an earlier one.

**1. Build the binary once (from the repo root — produces `./bin/rakitsu`):**

```bash
make build-embedded
```

**2. Terminal 1 — start the rakitsu side.** Fill in your own
`SECRETS_FILE` path, then run the whole block as one paste:

```bash
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=$(mktemp)
openssl rand -hex 16 > "$RAKITSU_TOKEN_FILE"
echo "$RAKITSU_TOKEN_FILE"
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
OPENAI_API_KEY=$(secret OPENAI_API_KEY) \
./bin/rakitsu serve --config-dir examples/single/13-voice-orchestrator \
  --config examples/single/13-voice-orchestrator/worker-config.yaml
```

The `echo` prints `$RAKITSU_TOKEN_FILE`'s *path* (not the secret itself —
just where it lives, safe to see on screen) before `serve` takes over the
terminal. **Copy that path — you'll paste it into every other terminal
below**, replacing the `/tmp/tmp.XXXXXXXXXX` placeholder each one shows.

`RAKITSU_API_TOKEN` is the server's control-plane API token required for
`/a2a` auth (per `internal/server/auth.go`); the bridge must use the same
value or dispatch calls get refused. The legacy `session_msg.enabled: true`
setting is not required by the bridge's A2A dispatch path.

**Both flags are required, not just `--config-dir`.** `--config <file>` is
what actually registers the `/a2a` endpoint (`cmd/rakitsu/serve.go`'s
`mcpConfig != ""` gate); without it, every request to `/a2a` — from any of
the three bridges below — silently falls through to the web UI's SPA
catch-all and comes back as an HTML page instead of a JSON-RPC response,
which surfaces client-side as a confusing `JSONDecodeError`, not a clear
"wrong flag" error. Use `./bin/rakitsu` (the binary you just built), not a
bare `rakitsu` — a different, possibly stale, globally-installed binary
may be first on your `PATH`; run `which rakitsu` to check, or just always
call it via the explicit `./bin/rakitsu` path shown above.

**3. Terminal 2 — start the bridge.** New terminal, new shell process:
fill in the same `SECRETS_FILE` and replace `RAKITSU_TOKEN_FILE`'s
placeholder with the path terminal 1 printed, then run the whole block:

```bash
pip install websockets sounddevice numpy requests
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=/tmp/tmp.XXXXXXXXXX
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
OPENAI_API_KEY=$(secret OPENAI_API_KEY) \
python3 examples/single/13-voice-orchestrator/bridge/voice_bridge.py
```

Talk to it. Ask for something that needs real work (a lookup, a
calculation, a multi-step task) rather than something answerable from
conversation alone — the completed transcript is monitored automatically
(the model doesn't decide whether to dispatch), the conversation keeps
going while the worker runs, and the actual result gets reported once it
comes back.

See `voice_bridge.py`'s module docstring for the full env var list
(`OPENAI_REALTIME_MODEL`, `OPENAI_REALTIME_VOICE`, `RAKITSU_SERVE_URL`,
`RAKITSU_WORKER_CONFIG_NAME`, `RAKITSU_WORKER_WORKDIR`,
`DISPATCH_MAX_WAIT_SECONDS`, `VERBOSE`).

A dispatched task is bounded by `DISPATCH_MAX_WAIT_SECONDS` (default
120s) — a client-side deadline the bridge owns, not a server-side cap;
the server keeps running the task even after the bridge stops polling.
120s is fine for lookups, quick calculations, short multi-step tasks; for
anything genuinely long-running, raise the env var rather than assuming
it's a hard rakitsu-side limit.

### Run (Gemini variant)

**UNTESTED / first draft** — unlike the OpenAI bridge's live testing below,
this variant has only been checked offline. Use headphones here too.

**Terminal 1 — build once, then start the rakitsu side** (same as the
OpenAI variant above — if that's already running, skip straight to
terminal 2 below):

```bash
make build-embedded
```

```bash
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=$(mktemp)
openssl rand -hex 16 > "$RAKITSU_TOKEN_FILE"
echo "$RAKITSU_TOKEN_FILE"
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
OPENAI_API_KEY=$(secret OPENAI_API_KEY) \
./bin/rakitsu serve --config-dir examples/single/13-voice-orchestrator \
  --config examples/single/13-voice-orchestrator/worker-config.yaml
```

Copy the printed token path — you'll need it in terminal 2. (`--config
<file>` is required, not just `--config-dir` — without it `/a2a` silently
falls through to the web UI and every dispatch call fails with a
confusing `JSONDecodeError` instead of a clear error. Use `./bin/rakitsu`,
not a bare `rakitsu`, in case a different, possibly stale, globally
installed binary is first on your `PATH`.)

**Terminal 2 — the Gemini bridge.** New terminal, new shell process: fill
in the same `SECRETS_FILE` and replace `RAKITSU_TOKEN_FILE`'s placeholder
with the path terminal 1 printed:

```bash
pip install websockets sounddevice numpy requests
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=/tmp/tmp.XXXXXXXXXX
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
GEMINI_API_KEY=$(secret GEMINI_API_KEY) \
GEMINI_LIVE_MODEL=gemini-2.5-flash-native-audio-preview-09-2025 \
GEMINI_LIVE_VOICE=Puck \
RAKITSU_SERVE_URL=http://localhost:9100 \
RAKITSU_WORKER_AGENT_NAME=VoiceWorker \
python3 examples/single/13-voice-orchestrator/bridge/voice_bridge_gemini.py
```

The same `RAKITSU_*` variables apply (see the bridge's module docstring),
including `RAKITSU_WORKER_CONFIG_NAME` and the legacy, display-only
`RAKITSU_WORKER_WORKDIR`. A2A dispatch does **not** send a workdir override;
configure the worker's tool scope on the server. Both bridges now share
`bridge/rakitsu_dispatch.py`: transcript-triggered A2A `SendMessage` /
`GetTask`, with a client-side `DISPATCH_MAX_WAIT_SECONDS` deadline and
`DISPATCH_POLL_INTERVAL_SECONDS` polling. Neither registers realtime tools;
results starting with "no action needed" stay silent. Older session-message
and tool-calling descriptions below record the original live-tested design.

The dispatch target is independent of the bridge provider: the Gemini
bridge uses the **same `worker-config.yaml`**, whose `default_provider:
openai` and `gpt-4o-mini` model still require `OPENAI_API_KEY` on the
**serve/worker side**. It is not a Gemini worker config. You may separately
reconfigure the worker to use Gemini, but the bridge transport and worker
LLM are two independent provider choices and do not need to match.

**Biggest unverified assumption — transcript completion:** Gemini supplies
`inputTranscription.text` chunks, but current docs show no discrete "input
transcription completed" event like OpenAI's. This draft accumulates those
chunks and treats `generationComplete` or `turnComplete` (the **model's**
completion signals) as the boundary for dispatching the user transcript.
That is an inference, not a documented guarantee: late chunks and barge-in
need live verification before trusting dispatch-triggering behavior.
Background results are injected as framed `clientContent` user turns (no
mid-session system role); their conversational handling also needs live
verification. Gemini VAD triggers replies automatically, unlike the OpenAI
bridge's manual response gating.

### Run (local turn-based variant)

Headphones/earbuds are still recommended: playback barge-in means the mic
stays hot while Piper is speaking, so open speakers make it easier for the
assistant's own voice to trigger an accidental interrupt (this bridge still
has no acoustic echo cancellation).

**Terminal 1 — build once, then start the rakitsu side** (same as the
OpenAI variant above — if that's already running, skip straight to
terminal 2 below):

```bash
make build-embedded
```

```bash
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=$(mktemp)
openssl rand -hex 16 > "$RAKITSU_TOKEN_FILE"
echo "$RAKITSU_TOKEN_FILE"
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
OPENAI_API_KEY=$(secret OPENAI_API_KEY) \
./bin/rakitsu serve --config-dir examples/single/13-voice-orchestrator \
  --config examples/single/13-voice-orchestrator/worker-config.yaml
```

Copy the printed token path — you'll need it in terminal 2. (`--config
<file>` is required, not just `--config-dir` — without it `/a2a` silently
falls through to the web UI and every dispatch call fails with a
confusing `JSONDecodeError` instead of a clear error. Use `./bin/rakitsu`,
not a bare `rakitsu`, in case a different, possibly stale, globally
installed binary is first on your `PATH`.)

**Terminal 2 — the local bridge:**

```bash
pip install faster-whisper sounddevice numpy requests silero-vad
```

Piper is used as a CLI subprocess, not a Python import — install a
prebuilt binary, or `pip install piper-tts` (also installs a `piper`
console script), then download a voice model (`.onnx` + `.onnx.json` pair)
from https://github.com/rhasspy/piper/blob/master/VOICES.md. Then, in that
same terminal — filling in the same `SECRETS_FILE` and the token path
terminal 1 printed:

```bash
SECRETS_FILE="/path/to/your/secrets.env"
secret() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); print; exit}' "$SECRETS_FILE"; }
RAKITSU_TOKEN_FILE=/tmp/tmp.XXXXXXXXXX
RAKITSU_API_TOKEN=$(cat "$RAKITSU_TOKEN_FILE") \
PIPER_MODEL_PATH=/path/to/en_US-lessac-medium.onnx \
python3 examples/single/13-voice-orchestrator/bridge/voice_bridge_local.py
```

No key press needed — just start talking. The mic is monitored
continuously with Silero VAD (ONNX): a turn starts once speech is
detected and ends after `VAD_SILENCE_MS` (default 800ms) of trailing
silence, then it's transcribed locally (faster-whisper), dispatched to
`VoiceWorker` via the same A2A `SendMessage`/`GetTask` path the other two
bridges use, and the result is spoken back locally (Piper) — no realtime
WebSocket, no cloud voice API, no per-minute audio billing. While Piper is
speaking, VAD keeps listening in the background: speak at any point and
playback stops immediately, with that speech carried over as the start of
the next turn instead of being dropped. Set `VAD_ENABLED=0` to fall back
to the original manual push-to-talk loop (Enter to start a turn,
Enter to stop, no VAD, no barge-in) if VAD misbehaves on your mic/room.
`--list-voices` lists any `.onnx` voice files found next to
`PIPER_MODEL_PATH`; Piper itself has no built-in voice catalog, so see the
VOICES.md link above to find and download new ones.

See `voice_bridge_local.py`'s module docstring for the full env var list
(`WHISPER_MODEL_SIZE`, `WHISPER_DEVICE`, `WHISPER_COMPUTE_TYPE`,
`WHISPER_LANGUAGE`, `PIPER_MODEL_PATH`, `PIPER_BINARY`, `VAD_ENABLED`,
`VAD_THRESHOLD`, `VAD_SILENCE_MS`, `VAD_MIN_SPEECH_MS`, plus the shared
`RAKITSU_*`/`DISPATCH_*` variables from `rakitsu_dispatch.py`).

**VAD timing/barge-in feel not live-tested with a real microphone/speaker
as of this writing** — the underlying turn-based push-to-talk path
was live-tested 2026-09-28/29 and works end-to-end; what's new here (the
auto-VAD turn detection and playback-interrupt behavior)
has only been verified via `py_compile`, `--help`/`--list-voices` running
cleanly without faster-whisper/silero-vad/Piper installed, and a read-through
of the VAD/threading/interrupt logic for the usual bug classes (races
between the playback-cancel signal and the audio callback thread, unsafe
stream teardown, dropped barge-in audio). Whether `VAD_THRESHOLD` and
`VAD_SILENCE_MS`'s defaults actually feel right, and whether the
barge-in interrupt is fast enough in practice, needs a human with a
working mic and speakers to confirm — same caveat pattern as the untested
Gemini realtime bridge below.

## Development capability

`worker-config.yaml` gives `VoiceWorker` real write/exec tools:
`write_file` (fs), plus these fixed-command `cli` tools with no parameters:

| Tool | Fixed command |
| --- | --- |
| `git_status` | `git status` |
| `git_log` | `git log --oneline -20` |
| `git_diff` | `git diff` |
| `git_diff_last_commit` | `git diff HEAD~1` |
| `git_show_head` | `git show HEAD` |
| `git_branch` | `git branch -a` |
| `go_build` | `go build ./...` |
| `go_test` | `go test ./...` |
| `go_vet` | `go vet ./...` |

Git and Go receive only config-authored argv, with no model-controlled
placeholders or parameters. Direct execution without a shell is not enough:
free-form arguments could supply Git's `-c core.sshCommand` or Go's
`-toolexec`/`-exec` flags to invoke arbitrary programs. The CLI binary
allowlist does not restrict those flags; fixed commands remove that injection
slot. `settings.allowed_commands` still lists `git` and `go` because these
binaries are still invoked.

`run_ls` and `run_cat` remain direct-exec tools accepting arguments with
`argv_split: true`; neither binary documents a flag for executing an external
command or configuration hook. No CLI tool here uses a shell wrapper.
A dispatched task can read code, edit files, and run the fixed Git/Go
operations — not just answer questions. This closes argument injection, not
all execution risks: repository configuration and Go source/tests must still
be trusted, and the worker can write files.

That's meaningfully more dangerous than a read-only demo, because a
**voice command is a much less deliberate trigger** than a typed one:
background noise, a mis-transcription, or the model over-interpreting a
vague request can trigger a dispatch with no confirmation step in
between (transcript monitoring dispatches automatically, with no model
decision or user confirmation in the loop). Two mitigations, both partial:

- **Scope the worker's tool access server-side** — A2A dispatch does not
  accept a per-call workdir override (`RAKITSU_WORKER_WORKDIR` is legacy,
  display-only), so the directory the worker's file/command tools can
  touch is whatever `rakitsu serve`'s own working directory resolves
  `allowed_paths: ["."]` against. Start `serve` from the directory you're
  willing to have read, written, and `git`/`go`-command'd against —
  never something broad like `$HOME`.
- **Prompted, not enforced, caution** — both the realtime session
  instructions and the worker's own system prompt tell the model to state
  what it's about to do before dispatching, keep changes scoped exactly
  to what was asked, and report plainly what actually happened. This is a
  prompt-level mitigation, not a technical gate — there is no hard
  confirmation step before a write or command actually runs. If you want
  one, that's a real design change (e.g. the worker proposing a diff/
  command and waiting for an explicit separate "yes" turn), not built
  here.

## Live-tested

Live-tested end to end, including real spoken conversations — and every
round of live testing so far has caught a real bug no amount of code
reading would have found. Rounds 1-3 below predate the switch to A2A
dispatch (see "A2A note" above) and were run against the original
`session_message` design; rounds 3-4's audio-layer findings (echo,
barge-in) are about the WebSocket/audio path, unchanged by that switch:

1. **Rakitsu-side dispatch path** (original `session_message` design) —
   `rakitsu serve` hosting `worker-config.yaml`, `POST /api/chat/start`,
   `POST /api/sessions/{id}/message {wait:true}`, and the worker's real
   answer coming back correctly. Caught: the original `wait:false` +
   poll-inbox design never actually delivers a reply for a local
   `rakitsu serve` session, and `session_msg.enabled: true` grants the
   worker a `send_message` tool it would otherwise use to (uselessly)
   "reply" instead of answering inline. This dispatch mechanism was later
   replaced by asynchronous A2A `SendMessage`/`GetTask`; the bugs it
   caught (and the "always answer inline, never send_message" system
   prompt rule they led to) remain valid history.
2. **WebSocket connection and session setup** against OpenAI's real
   Realtime API. Caught: the original code targeted the Beta API shape,
   which OpenAI removed 2026-05-12 — first live connection attempt failed
   with `invalid_request_error.beta_api_shape_disabled`. Fixed to the
   current GA shape (`session.type: "realtime"`, nested
   `session.audio.input`/`.output`, `response.output_audio.delta`).
3. **A real spoken conversation** surfaced two more bugs the first two
   rounds couldn't have caught:
   - **Acoustic echo**: with speakers (not headphones) and no echo
     cancellation between the `sounddevice` input/output streams, the mic
     picked up the assistant's own voice and the model ended up
     responding to itself — confirmed via a session log where the
     `[user]` transcript was a verbatim copy of the prior `[assistant]`
     reply. First fix attempt muted the mic while the assistant's audio
     played, which does stop the echo — but it also silences the exact
     window a real mid-sentence interruption would need the mic listening
     during, killing that capability. Reverted: **this script requires
     headphones/earbuds** instead. Headphones remove the speaker-to-mic
     path entirely, so there's no echo to guard against and no tradeoff
     against interruption to make. There's no software fix here for open
     speakers without real acoustic echo cancellation (AEC) — a genuinely
     bigger addition, not built here.
   - **The model never triggered a dispatch** (back when dispatch was a
     realtime function-call tool the model itself had to invoke) — asked
     "which folder are we in," it answered "I don't have access" directly
     instead of delegating, and the worker had no tools to answer that
     even if it had been dispatched. This unreliable-function-calling
     behavior is exactly why the design moved to transcript monitoring
     (the current mechanism): dispatch no longer depends on the model
     deciding to call a tool at all. The worker was also given real tools
     (see "Development capability" above).
4. **Interrupting mid-response did nothing** — switching to headphones
   fixed the echo but not this: the assistant kept talking right through
   a real barge-in. Root cause: with a WebSocket connection, the *client*
   owns audio playback, not the server. `turn_detection.interrupt_response:
   true` only stops the model from generating further audio once the
   server detects new speech — it does nothing about audio this script
   already wrote to the local speaker stream. Fixed by calling
   `RawOutputStream.abort()` (discard what's queued, don't wait for it to
   finish) the moment an `input_audio_buffer.speech_started` event arrives
   — see `VoiceBridge._interrupt_playback`.
   **Known remaining gap**: this doesn't send `conversation.item.truncate`,
   so the model's own conversation history still records the full text it
   intended to say, not the shorter bit actually spoken before being cut
   off — it may reference "what I just said" inaccurately in a later turn.
   Not built here; the truncate call needs tracking how many ms of audio
   were actually played vs. generated, which this script doesn't do yet.

If something still comes up in a different environment/setup, please
report it back as an issue.

**A2A note**: this example originally dispatched via `session_message`
(`wait:true`), not A2A, even though A2A's wire protocol is the more
natural fit for this kind of async dispatch. That was a deliberate call
at first — rakitsu's A2A server handler was synchronous, so switching to
it would've bought nothing (see "Background" above). That gap was fixed
at the rakitsu core level:
`SendMessage` runs asynchronously and `CancelTask` genuinely stops an
in-flight run — and all three bridges here now dispatch via A2A
(`POST /a2a` `SendMessage` + poll `GetTask`, through the shared
`bridge/rakitsu_dispatch.py`) instead of `session_message`. The
`settings.session_msg` config block is legacy and unused by the current
dispatch path (still present in `worker-config.yaml` for backward
compatibility, but not required). `--config <file>` (not just
`--config-dir`) is still required to activate `/a2a` on `rakitsu serve`.

## Extending to other bridges

Same dispatch pattern, no new rakitsu concept needed, just a different
external script:

- **Gemini Live** — built as `bridge/voice_bridge_gemini.py`, but
  **UNTESTED / first draft**, not live-tested like the OpenAI bridge. Reuses
  the same `RakitsuDispatcher` / A2A implementation through the shared
  `bridge/rakitsu_dispatch.py` module, with the same zero-tools,
  realtime-model-only-converses design. The transport is Gemini's
  `BidiGenerateContent` WebSocket: 16kHz PCM input / 24kHz PCM output,
  versus OpenAI's 24kHz in both directions. See "Run (Gemini variant)"
  for setup and the unverified transcript-completion assumption.
- **Cheaper turn-based STT/TTS bridge** — built as `bridge/voice_bridge_local.py`:
  auto-VAD-turn STT → dispatch → TTS (still no live WebSocket duplex, still
  turn-based, not streaming/incremental ASR), using the exact same
  rakitsu-side A2A dispatch as the other two bridges, just triggered
  per-completed-turn instead of per-tool-call/per-completed-transcript.
  Uses **faster-whisper** (local STT), **Piper** (local TTS), and
  **Silero VAD** ONNX (local turn detection + playback barge-in) instead
  of a cloud realtime voice API — see "Run (local turn-based variant)"
  above, and "Testing status summary" below for what's live-tested here
  and what isn't yet.

## Demonstrates

- Dispatch-as-orchestration: rakitsu contributes only its existing A2A
  (`/a2a`) HTTP API; the volatile realtime-voice surface (WebSocket
  protocol, audio codec, session config, transcript-driven monitoring)
  lives entirely in an external script, same pattern as
  `12-media-generation`'s external `cli` tool.
- Transcript monitoring, not model-driven tool calling, as the dispatch
  trigger: the realtime model has zero tools and never decides whether to
  dispatch — every non-trivial completed user transcript is checked
  independently, which sidesteps the unreliable-function-calling failure
  mode found in early live testing (see "Live-tested" above).
- A2A `SendMessage` + poll `GetTask`, run off the event loop in a
  background thread, as a simple, already-supported pattern for "let a
  live conversation keep going while a background agent works" — no new
  rakitsu async primitive needed, and no server-side wait cap (the client
  owns its own `DISPATCH_MAX_WAIT_SECONDS` deadline).

## Testing status summary

- **OpenAI bridge (`voice_bridge.py`)** — live-tested end to end with
  real spoken conversations, including a fixed interrupt/
  barge-in path. One known remaining gap: no `conversation.item.truncate`
  call, so the model's own history can misremember what it actually said
  after being cut off mid-sentence (see "Live-tested" above).
- **Gemini bridge (`voice_bridge_gemini.py`)** — **UNTESTED / first
  draft**, never live-tested with real audio; checked offline only
  (`py_compile`, code review). The transcript-completion boundary it
  relies on (`generationComplete`/`turnComplete`) is an inference from
  docs, not a documented guarantee — see "Run (Gemini variant)".
- **Local bridge (`voice_bridge_local.py`)** — split status: the
  push-to-talk mode was live-tested successfully end to
  end, including a real multi-speaker Gemini TTS bug and a real `/a2a`
  routing bug (missing `--config`) found and fixed along the way. The
  VAD auto-detect + playback barge-in additions were built
  and passed code review but have **not** been live-tested for timing/
  feel — `VAD_THRESHOLD`, `VAD_SILENCE_MS` tuning and barge-in
  responsiveness still need a human with a working mic/speakers to
  confirm (see "Run (local turn-based variant)" above).
