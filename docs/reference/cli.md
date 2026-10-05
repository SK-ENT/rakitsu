# CLI reference

Every command and flag below was taken from `rakitsu <command> --help` of a freshly built binary and from the cobra source in `cmd/rakitsu/`.

Run `rakitsu --help` to see the same list.

## Commands at a glance

| Command | What it does | Page |
|---|---|---|
| `run` | Run agents once, or as an interactive chat | [this page](#rakitsu-run) |
| `serve` | Start the hub: web UI, runner, debugger, MCP, A2A, monitors | [serve](serve-web-ui.md) |
| `ui` | Deprecated alias of `serve` | [this page](#rakitsu-ui-deprecated) |
| `sessions` | List saved sessions; `live` and `send` for cross-session messages | [sessions](sessions.md), [chat sessions](chat-sessions-messaging.md) |
| `export` | Convert a config to OpenClaw or NemoClaw files | [export](export.md) |
| `scaffold` | Generate a config from a preset | [scaffold and quickstart](scaffold-quickstart.md) |
| `quickstart` | Wizard that creates a first project | [scaffold and quickstart](scaffold-quickstart.md) |
| `doctor` | Check a config and its providers | [doctor](doctor.md) |
| `healthcheck` | Probe a running `serve` at `/healthz` | [monitors and health](monitors-healthz.md) |
| `acp` | Run as an ACP server over stdin/stdout | [ACP](acp.md) |
| `version` | Print the version | [this page](#rakitsu-version) |
| `completion` | Generate a shell completion script | [this page](#rakitsu-completion) |

Official licensed builds can have one more command, `license`. Source builds do not include it, so it is not described here.

## Global flags

These work on every command.

| Flag | Meaning |
|---|---|
| `-c, --config string` | Config file. Default is `./agent.yaml`. |
| `-v, --verbose` | Verbose output. |
| `--version` | Print the version (root command only). |
| `-h, --help` | Help for the command. |

Note: `rakitsu serve` defines its own `--config` flag (no short form) with a different meaning. See [serve](#rakitsu-serve).

## rakitsu run

```
rakitsu run [config.yaml] [query] [flags]
```

Runs the agents in a config. With a `query` it runs once and prints the answer. With `--interactive` it opens a chat.

If you give no config with `--interactive`, rakitsu creates a small keyless (Ollama) chat config once at `~/.rakitsu/default-agent.yaml` and reuses it. Edit that file to add tools or change the provider. One-shot runs always need a config.

By default `run` connects to a hub at `http://localhost:9100` if one is running. If no hub runs, the agent runs alone. See [serve](serve-web-ui.md).

| Flag | Meaning |
|---|---|
| `--attach <file>` | Attach a local image or audio file to the query. Repeatable (`--attach a.png --attach memo.wav`). The resolved agent must have `vision: true`. Audio is native on Gemini, transcribed on OpenAI-compatible providers, and rejected on Anthropic. See [attachments](attachments.md). |
| `--no-auto-attach` | Do not auto-attach image or audio files named in the query text. Files inside the workdir are otherwise picked up automatically. |
| `--debug-port int` | Start a stand-alone debug SSE server with the web UI on this port. Cannot be used together with hub mode. |
| `--dry-run` | Estimate tokens and cost. No LLM call is made. |
| `--embedding-provider string` | Provider for retrieval embeddings (for example `openai`, `gemini`, `ollama`). Overrides the config. |
| `--hub string` | Hub URL for event streaming. Default `http://localhost:9100`. |
| `--no-hub` | Do not connect to a hub. |
| `--idle-timeout int` | Cancel the run after N seconds with no streaming activity. `0` is off. A slow but progressing model never trips it. |
| `-i, --interactive` | Run as an interactive chat. Overrides the config `interactive` flag. |
| `--max-cost float` | Stop the run if the total cost goes over this USD amount. Overrides the config value. Known limits in this release: a plain single-agent `run` does not enforce any budget, and for orchestrator runs the flag only takes effect when the config also sets a budget under `settings.execution`. Cost needs a price: set `settings.pricing` for the model. |
| `--max-tokens int` | Override max output tokens for all agents and orchestrators. `0` keeps the config value. Raise it for reasoning models, whose hidden reasoning tokens share the same budget. |
| `--model string` | Override the default model for all agents. |
| `--provider string` | Override the default provider for all agents (for example `litellm`, `ollama`, `anthropic`). If the provider is not in the config, rakitsu builds it from `<NAME>_API_KEY` and `<NAME>_BASE_URL` environment variables. `nvidia` maps to the OpenAI type with the NVIDIA base URL. |
| `--resume string` | Resume a session by ID. A single agent replays the conversation. A pipeline resumes from its checkpoint. |
| `--sessions-dir string` | Directory for session files. Default is `settings.sessions_dir`, else `~/.rakitsu/sessions`. Give each running instance its own. |
| `-t, --timeout int` | Total time limit in seconds. Per turn in `--interactive`. Unset or `<=0` means no limit, and agents without `max_iterations` then have no iteration cap. Also set by `settings.execution.timeout_seconds`. |
| `--trace` | Show a live execution trace on stderr. |
| `-w, --workdir string` | Working directory for tools. Default is the current directory. |

Examples (all taken from the command help):

```bash
rakitsu run agent.yaml "What pods are running?"
rakitsu run --interactive
rakitsu run agent.yaml --interactive
rakitsu run agent.yaml "Analyze errors" --trace
rakitsu run agent.yaml "Query" --provider litellm --model gpt-4o
rakitsu run agent.yaml "hello" --dry-run
```

Rules to know:

- A non-interactive config needs a query. Without one, `run` stops with "query required for non-interactive runs".
- `--timeout` on the command line wins over `settings.execution.timeout_seconds`.

## rakitsu serve

```
rakitsu serve [flags]
```

Starts the hub: web UI, SSE event stream, agent runner, session history, debugger, chat sessions, MCP and A2A endpoints, monitors. Details: [serve and web UI](serve-web-ui.md).

| Flag | Meaning |
|---|---|
| `--config string` | YAML config for the MCP server, the A2A endpoint, and the `monitors` and `healthz` blocks. This is not the global `-c` flag. |
| `--config-dir string` | Extra directory scanned for agent configs. Configs found here show up in the UI and in `/api/configs`. |
| `--host string` | Host to bind. Default `localhost`. A non-loopback host needs `RAKITSU_API_TOKEN`; without it, serve refuses to start. |
| `--mcp-port int` | Start a separate MCP HTTP listener on this port. Needs `--config`; without it the flag is silently ignored (no listener, no error). |
| `-p, --port int` | Hub and UI port. Default `9100`. |
| `--sessions-dir string` | Directory for session files. Same rule as in `run`. |

Examples:

```bash
rakitsu serve
rakitsu serve --port 8080
rakitsu serve --config tools.yaml --mcp-port 9200
```

Serve also scans `.`, `./examples` and `./configs` (relative to where you start it) for configs. Start it from a clean directory if that matters. See [security](security.md).

## rakitsu ui (deprecated)

```
rakitsu ui [flags]
```

Alias of `rakitsu serve`. It prints a deprecation notice. Use `serve`.

| Flag | Meaning |
|---|---|
| `--config-dir string` | Extra directory scanned for agent configs. |
| `--host string` | Host to bind. Default `localhost`. |
| `-p, --port int` | Port. Default `9100`. |

## rakitsu sessions

```
rakitsu sessions [flags]
rakitsu sessions [command]
```

Lists recent sessions recorded by `run`. Details: [sessions](sessions.md).

| Flag | Meaning |
|---|---|
| `--limit int` | Maximum sessions to show. Default `20`. |
| `--resumable` | Only sessions with a resumable checkpoint. |
| `--sessions-dir string` | Session directory to read. |

Resume with `rakitsu run config.yaml "query" --resume <SESSION ID>`.

### rakitsu sessions live

```
rakitsu sessions live [--hub URL]
```

Lists live chat sessions registered with the hub. A session with `ACCEPTS=false` has not set `settings.session_msg.enabled` and rejects messages.

| Flag | Meaning |
|---|---|
| `--hub string` | Hub or server URL. Default `http://localhost:9100`. |

### rakitsu sessions send

```
rakitsu sessions send <session-id> <text> [flags]
```

Sends one message into a live session. The target agent runs a turn on it as if it were a user message, marked as coming from outside. It is fire and forget: success means the message was queued, not that the agent replied.

| Flag | Meaning |
|---|---|
| `--from-name string` | Sender name shown in the target session. Default `CLI`. |
| `--hub string` | Hub or server URL. Default `http://localhost:9100`. |

See [chat sessions and messaging](chat-sessions-messaging.md) for the token rules.

## rakitsu export

```
rakitsu export [config.yaml] [flags]
```

Converts a config into deployment files for OpenClaw or NemoClaw. Details: [export](export.md).

| Flag | Meaning |
|---|---|
| `-f, --format string` | `openclaw` or `nemoclaw`. Default `openclaw`. |
| `-o, --output string` | Output directory. Default `./<format>-export/`. |
| `--with-blueprint` | Also write `blueprint.yaml`. Only for contributions to NVIDIA's catalog. |

## rakitsu scaffold

```
rakitsu scaffold [use-case] [flags]
```

Generates a config from a built-in preset. Details: [scaffold and quickstart](scaffold-quickstart.md).

| Flag | Meaning |
|---|---|
| `--dir` | Write a modular directory instead of one YAML file. |
| `-f, --force` | Overwrite existing files at the output path. |
| `-l, --list` | List all use-cases. |
| `-m, --model string` | Model name. Default is the provider's recommended model. |
| `-o, --output string` | Output file or directory. Default `<use-case>.yaml` or `./<use-case>/`. |
| `-p, --provider string` | `openai`, `anthropic`, `gemini`, `ollama`, `litellm`. Default `openai`. |

## rakitsu quickstart

```
rakitsu quickstart
```

Interactive wizard. It has no flags except `-h`. It asks for a template, a provider and an API key (or detects one from the environment), writes the project into `./rakitsu-project/`, then starts the web UI.

## rakitsu doctor

```
rakitsu doctor [config.yaml] [flags]
```

Checks a config and each provider. Details: [doctor](doctor.md).

| Flag | Meaning |
|---|---|
| `--json` | Print findings as JSON. |

Exit codes: `0` all passed, `1` warnings, `2` errors. With no config argument, `./agent.yaml` is used.

## rakitsu healthcheck

```
rakitsu healthcheck [flags]
```

Calls `/healthz` on a running `serve`. Exit `0` if healthy, `1` otherwise. Details: [monitors and health](monitors-healthz.md).

| Flag | Meaning |
|---|---|
| `--timeout duration` | Request timeout. Default `5s`. |
| `--url string` | Health URL. Default `http://localhost:9100/healthz`. |

## rakitsu acp

```
rakitsu acp <config.yaml> [flags]
```

Starts an ACP (Agent Client Protocol) server over stdin and stdout, for editors such as Zed. Every session runs the config you pass. Details: [ACP](acp.md).

| Flag | Meaning |
|---|---|
| `--max-sessions int` | Sessions kept in memory. The least recently used idle session is dropped past this number. `<=0` is unlimited. Sessions with a running prompt are never dropped. Default `256`. |
| `--session-idle-timeout duration` | Drop a session unused for this long. `<=0` is never. A later prompt on it gets "session not found". Default `24h0m0s`. |
| `--timeout int` | Override `settings.execution.timeout_seconds` for each `session/prompt` turn. `<=0` turns it off. Unset uses the config, with a default of 300 seconds. |

## rakitsu version

```
rakitsu version
```

Prints the version and the licence line. Same version string as `rakitsu --version`. A plain `go build` prints `dev`. `make build` and release builds embed a version string.

## rakitsu completion

```
rakitsu completion bash|zsh|fish|powershell
```

Prints a shell completion script. See `rakitsu completion <shell> --help` for how to install it.

## Environment variables

These are read directly by rakitsu code. Provider keys such as `OPENAI_API_KEY` are read only through `${VAR}` references in your config (see [configuration](configuration.md)), or through the `--provider` shortcut.

| Variable | Used by | Meaning |
|---|---|---|
| `RAKITSU_API_TOKEN` | `serve`, `run`, `sessions` | Bearer token for the control plane. Required to bind a non-loopback host. Clients (`run --hub`, `sessions live/send`) send it to the hub. |
| `RAKITSU_SESSION_MSG_TOKEN` | `serve`, senders | Token only for cross-session messaging endpoints. `RAKITSU_API_TOKEN` is accepted there too. |
| `RAKITSU_IMAGE_SHRINK_BYTES` | image tools | Threshold in base64 bytes above which images are shrunk. `0` turns shrinking off. |
