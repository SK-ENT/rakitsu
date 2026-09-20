<p align="center">
  <img src="docs/assets/brand/rakitsu-logo.svg" width="240" alt="Rakitsu logo">
</p>

<h1 align="center">Rakitsu</h1>

[![CI](https://github.com/SK-ENT/rakitsu/actions/workflows/ci.yml/badge.svg)](https://github.com/SK-ENT/rakitsu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SK-ENT/rakitsu?include_prereleases)](https://github.com/SK-ENT/rakitsu/releases)
[![Status: Alpha](https://img.shields.io/badge/status-alpha-orange.svg)](#disclaimer)
[![License: BSL 1.1](https://img.shields.io/badge/License-BSL%201.1-blue.svg)](LICENSE)
[![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8.svg)](https://go.dev)

**The Agent IDE** — Build, debug, and ship AI agents visually.

> *rakit* (Indonesian: craft) + *jiritsu* (Japanese: autonomous) — "autonomous assembly"

Design multi-agent systems on a drag-and-drop canvas, debug with breakpoints and time-travel, deploy as a single zero-dependency binary. YAML-configured, multi-provider, open source.

<!-- TODO: hero GIF — the visual builder in motion: drag two agents onto the
     canvas, wire a tool between them, hit Run, watch the debugger stream the
     execution tree live. ~10s loop, ~1200px wide, matches the app's theme. -->
<!-- Alt text when the asset lands: "Rakitsu visual builder — two agents wired
     on a drag-and-drop canvas with the live execution tree streaming below." -->

## Quick Start

```bash
# Install (Linux / macOS)
curl -fsSL https://raw.githubusercontent.com/SK-ENT/rakitsu/main/install.sh | sh

# Create your first project (interactive wizard)
rakitsu quickstart

# Or jump straight into the web UI
rakitsu serve
```

Open **http://localhost:9100** — drag agents onto the canvas, wire up tools, hit Run.

`rakitsu serve` runs in the foreground and keeps that terminal busy — that's
expected, it's the server. Leave it running and open a **new terminal tab**
for anything else (editing files, running `rakitsu run`, etc). Ctrl+C stops it.

There are two ways to run an agent once `serve` is up:
- **Click Run in the browser** — the whole run happens inside the `serve`
  process itself. No other terminal needed.
- **`rakitsu run config.yaml "query"`** from a second terminal — runs as its
  own CLI process and, if `serve` is already running, shows up live as a
  card at http://localhost:9100 so you can watch it in the debugger.

Rakitsu doesn't run inference itself — you need access to a model, either
your own [Ollama](https://ollama.com) running locally or an API key for
OpenAI, Anthropic, Gemini, or a LiteLLM proxy.

No API key? Click **Run the Demo** on the welcome screen and pick Ollama —
still needs a local Ollama install with a model pulled (`ollama pull
llama3.2`), just no cloud key. It's a pre-wired run with breakpoints
already set, so you can see the visual builder and live debugger in action
before writing a config.

> **macOS:** release binaries aren't notarized yet, so Gatekeeper can silently
> kill a freshly downloaded `rakitsu` on first run (no dialog, just "killed").
> If that happens: `xattr -cr $(command -v rakitsu)`, and if it's still
> killed, `sudo spctl --add $(command -v rakitsu)` or System Settings →
> Privacy & Security → **Open Anyway**.

## Why Rakitsu?

- **Visual-first**: Design agents on a drag-and-drop canvas, not in code
- **Debug like software**: Breakpoints, pause, inspect reasoning mid-execution
- **Single binary**: One file, zero dependencies — download and run
- **YAML-configured**: Declarative configs, no framework code to write
- **Multi-provider**: Mix OpenAI, Anthropic, Gemini, Ollama per-agent in one config

## Features

**Visual Builder**
- Vue Flow canvas: drag agents, wire tools, group into pipelines
- Block programming: pipeline, parallel, and team groups
- Inherit/override: group defines defaults, agents override, with badges
- Canvas search, auto-arrange, hierarchy panel

**Runtime Debugger**
- Set breakpoints on any agent or tool node
- Pause mid-execution, inspect reasoning, override parameters, resume
- 4 visualization modes: Tree, Block Diagram, Timeline, Mind Map
- Playback scrubbing on completed runs

**Agent Engine**
- ReAct loop with reflection and ground-checking
- Orchestration strategies: Sequential, Parallel, Hierarchical, DAG, Plan-and-Execute
- Pipeline checkpoint and resume
- Token budget enforcement, cost tracking, context auto-compression

**Providers**
- OpenAI, Anthropic Claude, Google Gemini, Ollama, LiteLLM
- Mix providers per-agent in a single config
- Auto-discover models from any OpenAI-compatible endpoint

**Runtime portability (optional)**
- Primary runtime is the Rakitsu binary; for teams shipping to alternative runtimes, configs export via the `RuntimeTarget` interface
- Built-in formats: NemoClaw, OpenClaw — see [Export Verification](#export-verification)

**Operations**
- Session history with replay and rerun
- CLI and web UI modes
- Secure tools: CLI whitelist/blocklist, file path restrictions, Docker sandbox
- Cross-platform: Linux, macOS, Windows (amd64 + arm64)

**Protocol interop**
- `rakitsu acp` — run as an ACP (Agent Client Protocol) server so editors like Zed can talk to rakitsu agents directly
- `rakitsu serve` exposes an MCP server at `/mcp` (legacy era, ≤2025-11-25 revision) so external MCP clients can discover and call rakitsu's tools
- `rakitsu serve` exposes an A2A endpoint at `/a2a` (real A2A v1.0.1) plus agent-card discovery, so a rakitsu agent can delegate to a named agent in a different rakitsu process
- Tool types `mcp_server`, `a2a`, and `jev` let a rakitsu agent call *out* to other MCP servers, A2A agents, or TypeSafe AI's Jev model for typed yes/no, pick-one, or scored questions

## CLI

```bash
rakitsu quickstart                    # Interactive project wizard
rakitsu serve                         # Web UI + agent runner (also serves MCP at /mcp, A2A at /a2a)
rakitsu run config.yaml "query"       # Run agents from CLI
rakitsu acp                           # Run as an ACP server (editor integration, e.g. Zed)
rakitsu scaffold code-review          # Generate config from template
rakitsu sessions                      # Browse past runs
rakitsu doctor config.yaml            # Diagnose config + provider health before running

# Override provider/model at runtime (no config edits needed):
rakitsu run config.yaml "query" --provider litellm --model gpt-4o
rakitsu run config.yaml "query" --provider ollama --model llama3
```

## Example Config

```yaml
name: Code Reviewer
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini

tools:
  - name: read_file
    type: fs
    operation: read

agents:
  - name: Reviewer
    role: worker
    provider: openai
    model: gpt-4o
    system_prompt: Review code for bugs, security issues, and best practices.
    tools: [read_file]

orchestrator:
  name: Lead
  strategy: Pipeline
  agents: [Reviewer]
```

Optional settings blocks:

```yaml
settings:
  spawn:              # runtime subagent fan-out (spawn_agent tool)
    enabled: true
    max_concurrent: 3 # run-global cap on concurrent children (default 4)
    max_depth: 1      # children cannot spawn further (default)
    timeout_seconds: 300
```

With `settings.spawn.enabled`, every agent (and the interactive chat host) gets a `spawn_agent` tool: it can spawn parallel subagents at runtime — ad-hoc workers or clones of config-defined agents — each with its own timeout, streamed into the same trace/debugger with explicit parent links. See [examples/single/10-spawn-fanout/](examples/single/10-spawn-fanout/).

See [examples/](examples/) for chat bots, dev teams, pipelines, RAG assistants, and more.

## Export Verification

Rakitsu's `RuntimeTarget` exports are verified end-to-end against real infrastructure — not claims, real HTTP traces. The current implementations cover OpenClaw and NVIDIA NemoClaw; the table below shows what each was tested against.

### What's Verified

| Component | Runtime Tested Against | Result |
|-----------|-----------------------|--------|
| `openclaw.json` | `ghcr.io/openclaw/openclaw:latest` (Docker) | Gateway started with Rakitsu-generated config |
| `sandbox-policy.yaml` | NVIDIA OpenShell on DGX Spark (aarch64) | `Policy version 2 loaded (active)` |
| Full NemoClaw sandbox | NemoClaw CLI in docker-in-docker, sandbox created with Landlock+seccomp+netns | `Policy version 6 loaded (active)` |
| End-to-end inference | NemoClaw sandbox → egress proxy → DGX Spark Nemotron-30B | **HTTP 200 chat completion** |

### E2E Test Setup

```
docker:dind (local host)
  └── NemoClaw CLI + OpenShell gateway (k3s-in-Docker)
       └── Sandbox "my-assistant" (Landlock + seccomp + netns)
            └── OpenClaw agent
                 └── Rakitsu-exported openclaw.json  ←── our export
                 └── Rakitsu-exported sandbox-policy  ←── our export
                 └── HTTP request to DGX Spark (via egress proxy)
                      └── LiteLLM → vLLM → Nemotron-30B
                           └── HTTP 200 chat completion ✓
```

All files destroyed with `docker rm -f` after verification — no residual state. The heavy E2E test is **manual only**; unit regression tests guard the export schema (binary allowlist, port extraction, env var expansion) and run on every CI build.

See [docs/EXPORT-VERIFICATION.md](docs/EXPORT-VERIFICATION.md) for the full audit trail.

## Build from Source

```bash
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
make build-embedded    # frontend + Go binary → bin/rakitsu
./bin/rakitsu serve
```

Requires: Go 1.25+, Node.js 20+

## Project Structure

```
cmd/rakitsu/        CLI entry points (Cobra)
internal/agent/     ReAct loop, orchestrator, pipeline executor
internal/llm/       LLM provider interface + implementations
internal/tools/     Tool interface: cli/, fs/, mcp/, a2a/, memory/
internal/server/    SSE hub, agent runner, config store
internal/debug/     Debug controller, replay, breakpoints
internal/store/     Session persistence (JSONL, one file per session)
web/src/            Vue 3 + Vue Flow frontend
examples/           Ready-to-run YAML configs
```

## Troubleshooting

**"openai API key not set"** — Export your key before running:
```bash
export OPENAI_API_KEY=sk-...
```

**"cannot load config: no such file or directory"** — Check the config path. Try `ls examples/single/` to see available configs.

**"cannot create provider for agent …"** — An agent references a provider that isn't defined. Add it under `settings.providers` in your config, and make sure the agent's `provider:` field matches the provider name exactly.

**"is port 9100 already in use?"** — Another `rakitsu serve` may be running. Kill it or use a different port:
```bash
rakitsu serve --port 8080
```

**Agent runs but produces no output** — Check `--trace` for execution details:
```bash
rakitsu run config.yaml "query" --trace --verbose
```

**Multi-agent run times out** — Default is 300s. Increase with `--timeout`:
```bash
rakitsu run config.yaml "query" --timeout 600
```

**Output starts with `[REASONING-ONLY OUTPUT — …]`** — The model (typically nemotron, DeepSeek-R1, QwQ) consumed its `max_tokens` budget on chain-of-thought before emitting any user-facing answer. Three production-ready alternatives are documented in [docs/providers/reasoning-models.md](docs/providers/reasoning-models.md): keep the default and rely on extraction, swap synthesis to Gemini, or run hybrid (local workers + cloud synthesis).

## Origin

Rakitsu began in spring 2026 as a small project to support my son. It was
supposed to stay small — it didn't. The problem turned out to be genuinely
interesting, and it grew into serious, long-term work. It remains a family
project at heart.

Rakitsu is built openly with AI: it is co-developed by a human and AI coding
assistants. Design decisions, code review, and final verification are human;
a large share of the implementation is AI-assisted. See
[`AI_USE_NOTICE.md`](AI_USE_NOTICE.md) for how AI tooling relates to the
license.

## Disclaimer

> **This software is alpha and not production-ready.** Interfaces, APIs, and behavior may change without notice. Rakitsu executes LLM-generated tool calls (shell commands, file operations) which carry inherent risk. Use at your own risk. The authors assume no liability for any losses, damages, or consequences arising from use of this software. See [LICENSE](LICENSE) for full terms.

## License

BSL 1.1 — See [LICENSE](LICENSE) for details. Each release converts to Apache 2.0 four years after *that version's* first publication, not on one fixed date for the whole project — see [RELEASE-NOTES.md](RELEASE-NOTES.md) for per-version dates.

**AI tooling**: see [`AI_USE_NOTICE.md`](AI_USE_NOTICE.md). Reading and personal study with AI tools is fine; AI-accelerated competitive reimplementation is subject to the BSL non-compete clause.

## Links

- [Examples](examples/)
- [Contributing](CONTRIBUTING.md)
- [Development Guide](docs/DEVELOPMENT.md)
- [System Spec](docs/SYSTEM-SPEC.md)
- [Changelog](docs/CHANGELOG-P0-P1.md)
