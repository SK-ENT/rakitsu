# Rakitsu reference

Rakitsu is a single program that runs teams of AI agents from a YAML file. It has a command line, a web UI with a visual builder and a debugger, and servers for A2A, MCP and ACP.

Use the reference below when you want to customize a project, choose a model, or look up a command.

If you are learning the execution model, start with [ReAct delegation](agents-orchestrators.md#react-supervisor-with-workers). If you need a predictable workflow, jump to [how to create a Pipeline](agents-orchestrators.md#pipelines).

## Start without editing a config

1. Install Rakitsu using the [README instructions](../../README.md#quick-start).
2. Run `rakitsu quickstart`. Choose **Chat Bot**, then pick your provider. Enter an API key if asked.
3. Start the web UI when prompted. Open **http://localhost:9100** and send your first message.

For Zed, follow the [ACP quick start](acp.md#quick-start-in-zed). For terminal runs or a custom config, see [scaffold and quickstart](scaffold-quickstart.md).

## Map

### Start here

| Page | What is in it |
|---|---|
| [CLI](cli.md) | Every command and flag, exit codes, environment variables. |
| [Configuration](configuration.md) | Every root key and every `settings` key, with an example each. |
| [Use cases](use-cases.md) | "I want to..." table that links to the examples. |
| [scaffold and quickstart](scaffold-quickstart.md) | Make a config or a whole project. |
| [doctor](doctor.md) | Check a config and its providers. |

### Build agents

| Page | What is in it |
|---|---|
| [Agents, orchestrators, pipelines](agents-orchestrators.md) | Single agents, ReAct teams, fixed pipelines, loops and DAGs. |
| [Tools](tools.md) | `cli`, `fs`, `mcp_server`, `a2a`, and the sandbox. |
| [Providers](providers.md) | OpenAI, Anthropic, Gemini, Ollama, LiteLLM, NVIDIA, Codex. |
| [Memory and knowledge graph](memory.md) | Persistent facts, summarized chat, auto-recall. |
| [Attachments](attachments.md) | Images, vision and audio. |

### Run and watch

| Page | What is in it |
|---|---|
| [serve, web UI, hub](serve-web-ui.md) | Builder, inspector, debugger, endpoints. |
| [Sessions](sessions.md) | Saved runs, resume, the JSONL format. |
| [Chat sessions and messaging](chat-sessions-messaging.md) | Long conversations, messages between sessions. |

### Run unattended

| Page | What is in it |
|---|---|
| [Wake timer](wake-timer.md) | Cheap checks on a timer; the model wakes only on a change. |
| [start_task](start-task.md) | Pre-approved jobs started from a wake session. |
| [Monitors, /healthz, healthcheck](monitors-healthz.md) | 24/7 setup under a supervisor. |
| [Alerts](alerts.md) | Phone and webhook pushes. |

### Connect to other systems

| Page | What is in it |
|---|---|
| [rakitsu-ask](rakitsu-ask.md) | **Claude Code subagent to a rakitsu peer.** Setup, safety, use cases. |
| [A2A](a2a.md) | Agent-to-agent calls, server and client. |
| [MCP](mcp.md) | Serve your tools over MCP, or use an MCP server's tools. |
| [ACP](acp.md) | Use rakitsu as an editor agent (Zed). |
| [export](export.md) | Convert a config to OpenClaw or NemoClaw. |

### Safety

| Page | What is in it |
|---|---|
| [Security model](security.md) | Modes, tokens, sandbox, network rules, changes since alpha.19. |

## Other docs in this repository

- [docs/configuration.md](../configuration.md): long tables of every field.
- [docs/SECURITY.md](../SECURITY.md), [docs/wake-timer.md](../wake-timer.md), [docs/monitor-autostart.md](../monitor-autostart.md), [docs/WEBUI.md](../WEBUI.md): the older, longer guides.
- [examples/](../../examples/): runnable configs, from a one-line chat to a 24/7 monitor.

## Conventions on these pages

- Commands start with `rakitsu` and assume it is on your `PATH`.
- Secrets are loaded with `VAR=$(...) command`, one command at a time. These pages never show `export KEY=value`.
- "Loopback" means your own machine (`localhost`, `127.0.0.1`, `::1`).
- File paths in examples are relative to the folder where you run the command.
