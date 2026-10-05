# Configuration reference

A rakitsu config is one YAML file (or a directory of small files). This page lists every root key and every `settings` key, with a short example for each. The keys come from `internal/config/config.go`, `monitors.go`, `wake_types.go` and `wake_alerts.go`.

For the long field tables (every tool, agent and orchestrator field) read [docs/configuration.md](../configuration.md). Feature pages in this folder explain how to use each key.

## Basics

- The file is YAML. Use `${VAR}` to read an environment variable and `${VAR:-default}` for a default.
- Put secrets in the environment, never as a literal in the YAML. See [security](security.md#secrets-in-configs-and-docs).
- Unknown keys are ignored without an error. A typo in a key name does not stop the load. Run [`rakitsu doctor`](doctor.md) and check the result.
- A `project_id` is generated when you leave it out.

### File references and discovery

Prompt fields (`system_prompt`, `prompt_template`, `prompt`) can load a file: write `file:prompts/reviewer.md` (relative to the config file). A single-line value that ends in `.md`, `.txt` or `.prompt` and names an existing file is loaded automatically. Multi-line values are always inline.

A config can also be split across directories `agents/`, `tools/`, `skills/`, `prompts/` next to the main file. They are discovered on load. See [examples/modular](../../examples/modular/).

`rakitsu serve` limits `file:` references in configs it loads to the served config directories. See [security](security.md).

## Complete minimal config

This file loads and runs (set `OPENAI_API_KEY` first):

```yaml
name: minimal
version: "1.0"
description: One agent, no tools.
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
agents:
  - name: Assistant
    role: worker
    system_prompt: Be brief.
```

## Root keys

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Name of the system. |
| `project_id` | string | Scopes memory. Generated if empty. |
| `version` | string | Your own version label. |
| `description` | string | Free text. |
| `interactive` | bool | Run as a chat instead of one shot. |
| `interactive_overlay` | bool | Wrap the root runner in a chat-host meta-agent. Default true for non-conversational configs. |
| `force_delegation` | bool | Chat host must call `invoke_config` every turn. |
| `settings` | object | Global settings (next section). |
| `tools` | list | Global tool definitions. See [tools](tools.md). |
| `skills` | list | Reusable tool sets with a prompt template. |
| `agents` | list | Agent definitions. See [agents](agents-orchestrators.md). |
| `orchestrator` | object | One orchestrator. |
| `orchestrators` | list | Several orchestrators (sub-orchestrators). |
| `workflows` | list | Parsed, and file references inside are resolved. No code outside the config loader reads it in this release, so it has no runtime effect. |
| `monitors` | list | `serve --config` only. Autostarted wake sessions. See [monitors](monitors-healthz.md). |
| `healthz` | object | `/healthz` tuning. See [monitors](monitors-healthz.md). |

### name, project_id, version, description

```yaml
name: support-bot
project_id: support-bot
version: "1.2"
description: Answers support questions.
```

### interactive, interactive_overlay, force_delegation

```yaml
interactive: true            # `rakitsu run config.yaml` opens a chat
interactive_overlay: false   # no chat-host wrapper; the agent answers directly
force_delegation: false      # true: the chat host must delegate every turn
```

`interactive: true` is also what makes a config a target for a chat session started by `serve` (see [chat sessions](chat-sessions-messaging.md)).

### skills

```yaml
skills:
  - name: security_audit
    description: Check a file for common problems.
    tools: [read_file]
    prompt_template: Check the file for hard-coded secrets.
```

An agent lists skill names in its `skills:` field.

### workflows

```yaml
workflows:
  - name: nightly
    description: Example only.
    steps:
      - agent: Assistant
        task: Summarize the day.
```

A workflow may also carry `final_synthesis: {agent, prompt}`; its `prompt` can be a file reference. This block is parsed but not run by the runtime in this release. Use a `Pipeline` orchestrator instead ([agents](agents-orchestrators.md)).

## settings keys

```yaml
settings:
  default_provider: openai
```

| Key | Type | Meaning |
|---|---|---|
| `default_provider` | string | Provider used when an agent names none. Fallback is `openai`. |
| `providers` | map | Named provider instances. See [providers](providers.md). |
| `api_keys` | map | Provider name to API key. |
| `base_urls` | map | Provider name to endpoint URL. |
| `credentials_files` | map | Provider name to credentials file path. |
| `locations` | map | Provider name to cloud region (Vertex). |
| `projects` | map | Provider name to cloud project (Vertex). |
| `allowed_commands` | list | Extra commands allowed for `cli` tools. |
| `defaults` | object | `model`, `temperature`, `max_tokens`. |
| `execution` | object | Limits for a run. |
| `pricing` | map | Per-model price for cost tracking. |
| `logging` | object | `level`, `file`. Parsed, not used by any code in this release. |
| `hub_url` | string | Hub URL for `rakitsu run` (the `--hub` flag wins). |
| `server` | object | `host`, `port`. Parsed, not used. `serve` reads `--host` and `--port`. |
| `retry` | object | LLM retry behavior. |
| `memory` | object | Native memory. See [memory](memory.md). |
| `spawn` | object | Runtime subagents. |
| `agent_chat` | object | Directed agent chat. |
| `session_msg` | object | Cross-session messaging gate. |
| `sessions_dir` | string | Session files directory. |
| `wake` | object | Wake timer. See [wake timer](wake-timer.md). |
| `redact_keywords` | list | Extra words that mark a key as secret in logs and events. |

### settings.default_provider, api_keys, base_urls, credentials_files, locations, projects

```yaml
settings:
  default_provider: gemini
  api_keys:
    anthropic: ${ANTHROPIC_API_KEY}
  base_urls:
    ollama: http://localhost:11434/v1
  credentials_files:
    gemini: ${GOOGLE_APPLICATION_CREDENTIALS}
  locations:
    gemini: us-central1
  projects:
    gemini: my-gcp-project
```

Values may be `${VAR}` references. The `providers:` map (named instances) is the newer, preferred form. See [providers](providers.md).

### settings.allowed_commands

```yaml
settings:
  allowed_commands: [terraform, jq]
```

Adds commands to the `cli` tool allowlist. This is a guard rail, not a sandbox. Read [security](security.md) first.

### settings.defaults

```yaml
settings:
  defaults:
    model: gpt-4o-mini
    temperature: 0.2
    max_tokens: 2048
```

`model` is the last fallback after `agent.model` and the provider `default_model`. `temperature` and `max_tokens` are passed to the provider as given when set.

### settings.execution

```yaml
settings:
  execution:
    max_iterations: 10
    timeout_seconds: 300
    idle_timeout_seconds: 60
    retry_attempts: 3
    max_total_tokens: 200000
    max_cost: 2.50
```

| Key | Meaning |
|---|---|
| `max_iterations` | Default step cap for agents without their own. `-1` is no cap. `0` is unset: no cap if the run has no timeout, otherwise 10. |
| `timeout_seconds` | Total time limit per run (per turn in chat). Negative or `0` is no limit. The `--timeout` flag wins when given. |
| `idle_timeout_seconds` | Cancel after this many seconds without streaming activity. `0` is off. |
| `retry_attempts` | Older retry setting. Prefer `settings.retry`. |
| `max_total_tokens` | Hard token budget for the whole run. `0` is unlimited. The result then reads `[budget exceeded: token_guard: token budget exceeded: N / LIMIT]`. Enforced in orchestrator runs and in `serve` chat sessions (the budget is for the whole session). Not enforced by a plain single-agent `rakitsu run` in this release. |
| `max_cost` | Hard cost budget in USD. `0` is unlimited. Same rules as `max_total_tokens`; cost is only counted for models that have a price (`settings.pricing`). |

### settings.pricing

```yaml
settings:
  pricing:
    my-local-model:
      input: 0.10    # USD per 1M input tokens
      output: 0.40   # USD per 1M output tokens
```

Used by cost tracking and `--dry-run`. A model with no known price shows zero cost.

### settings.retry

```yaml
settings:
  retry:
    max_attempts: 3
    base_delay: 2s
    max_delay: 30s
```

Without this block (and without `execution.retry_attempts`) the built-in default is 5 attempts.

### settings.hub_url, settings.logging, settings.server

```yaml
settings:
  hub_url: http://localhost:9100
```

`logging` and `server` are accepted and ignored in this release.

### settings.memory

```yaml
settings:
  memory:
    enabled: true
    dir: ~/.rakitsu/memory
    conversation:
      enabled: true
      keep_recent_turns: 2
      summary_max_chars: 2000
      disable_summary: false
    auto_recall:
      enabled: true
      top_k: 5
      scopes: [project, global]
      node_type: fact
```

See [memory](memory.md).

### settings.spawn

```yaml
settings:
  spawn:
    enabled: true
    max_concurrent: 4
    max_depth: 1
    timeout_seconds: 300
```

Gives every top-level agent a `spawn_agent` tool. One call builds a child agent at runtime and returns its result. Several calls in one response run in parallel. Defaults: 4 at once, depth 1 (children cannot spawn), 300 seconds per child. Example: [examples/single/10-spawn-fanout](../../examples/single/10-spawn-fanout/).

### settings.agent_chat

```yaml
settings:
  agent_chat:
    enabled: true
    max_retained: 20
    max_transcript_bytes: 262144
```

Lets a user talk to one agent in a chat (`/agent NAME`). It is on when the key is missing. `max_retained: -1` keeps nothing. See [chat sessions](chat-sessions-messaging.md).

### settings.session_msg

```yaml
settings:
  session_msg:
    enabled: true
```

Opt in to cross-session messaging. It does two things: the session accepts messages from other sessions, and its agents get the `send_message` and `list_sessions` tools. Default is off. See [chat sessions](chat-sessions-messaging.md).

### settings.sessions_dir

```yaml
settings:
  sessions_dir: ~/.rakitsu/instances/team-a/sessions
```

Where session files go. The `--sessions-dir` flag wins. Never let two running instances share one directory. See [sessions](sessions.md).

### settings.wake

```yaml
settings:
  wake:
    enabled: true
    interval_seconds: 60
    checks:
      - name: api-up
        type: http_status
        url: http://localhost:8080/health
        expect: 200
    allow:
      url_hosts: [localhost:8080]
```

All wake keys are on [wake timer](wake-timer.md), [start_task](start-task.md) and [alerts](alerts.md).

### settings.redact_keywords

```yaml
settings:
  redact_keywords: [project_code, internal_ticket_id]
```

Extra words (case-insensitive substrings) that make a key look secret, so its value is masked in session files and events. See [security](security.md).

## tools keys

```yaml
tools:
  - name: list_files
    type: cli
    description: List files
    command: ls -la
    sandbox:
      type: local_restricted
      allowed_paths: ["./"]
```

Tool types: `cli`, `fs`, `mcp_server`, `a2a`, and `jev` (a TypeSafe verification tool, see [examples/jev](../../examples/jev/)). The full field list is on [tools](tools.md).

## agents and orchestrator keys

See [agents, orchestrators and pipelines](agents-orchestrators.md).

## Validation

When a config loads, rakitsu checks it. Errors stop the load. Warnings are printed to stderr as `config warning: ...`. Examples of errors: a pipeline step that names an unknown agent, a wake setting out of range, a duplicate monitor id, an unknown `sandbox.type`.
