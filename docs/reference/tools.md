# Tools: cli, fs, mcp_server, a2a

## When to use it

Give an agent tools when it must act on the world, not just talk.

| Type | Use it to |
|---|---|
| `cli` | Run one allowed command (`git`, `go`, `ls`, a linter). |
| `fs` | Read, write, list or search files inside folders you name. |
| `mcp_server` | Use the tools of an MCP server, started locally or reached by URL. |
| `a2a` | Delegate a task to an agent running in another rakitsu (or any A2A server). |

Read [security](security.md) before you give an agent `cli` or `fs` tools. A config with these tools can change your machine.

## How tools are wired

1. Define the tool once under the root `tools:` key (global), or inside one agent under `tools_inline:`.
2. List the tool name in the agent's `tools:` list.

Common fields of a tool definition:

| Field | Meaning |
|---|---|
| `name` | Unique tool name. The model sees it. |
| `type` | `cli`, `fs`, `mcp_server`, `a2a` (or `jev`). |
| `description` | Shown to the model. Write what the tool does. |
| `parameters` | Map of parameter name to `{type, description, required, default, enum, argv_split}`. |
| `timeout_seconds` | Time limit for one call. |
| `env` | Environment variables for the tool process. |
| `working_dir` | Working directory. |
| `allowed_paths` | Allowed folders (`fs`). |
| `allowed_exit_codes` | Exit codes that count as success (`cli`). |
| `sandbox` | Sandbox settings (below). |
| `executable` | Parsed from the config but not used by any tool in this release. |

## cli

Runs a command line built from a template. Use `{{name}}` for a parameter.

```yaml
name: cli-demo
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
tools:
  - name: list_dir
    type: cli
    description: List a directory
    command: "ls -la {{path}}"
    parameters:
      path: { type: string, description: Directory to list, required: true }
    sandbox:
      type: local_restricted
      allowed_paths: ["."]
      resource_limits:
        timeout_sec: 10
agents:
  - name: Assistant
    role: worker
    system_prompt: Use list_dir when asked about files.
    tools: [list_dir]
    settings: { max_iterations: 4 }
```

```bash
rakitsu run cli-demo.yaml "What is in the current folder?"
```

Rules (from `internal/tools/cli/tool.go`):

- The command runs directly, with no shell, unless your template is `sh -c '...'`.
- Only commands on the built-in allowlist run: `ls cat grep find head tail wc sort uniq diff tree git npm node go python3 pip3 kubectl helm docker sh bash`. Add more with `settings.allowed_commands`. A command that is not listed is refused with "command not in system whitelist" (for example `echo`).
- These names are always refused, even if you allow them: `rm rmdir sudo su chmod chown chgrp dd mkfs fdisk shutdown reboot halt init kill killall pkill mv cp`.
- The running rakitsu binary can never be called from a `cli` tool.
- A parameter with `argv_split: true` is split on spaces into several arguments when it is the whole part of the template (for example `command: "gh {{args}}"`). Without it, the value is one argument.
- `rakitsu` strips `RAKITSU_API_TOKEN` and `RAKITSU_SESSION_MSG_TOKEN` from the environment of the process it starts.

The allowlist is a guard rail. `python3`, `node`, `bash` and `find` can still do anything. For untrusted work use the docker sandbox.

### Sandbox

```yaml
sandbox:
  type: local_restricted      # default. Or: docker
  allowed_paths: ["./"]       # local_restricted: refuse arguments that name paths outside
  resource_limits:
    timeout_sec: 10
    max_output_bytes: 8192
```

With `type: docker`:

```yaml
sandbox:
  type: docker
  image: alpine:latest
  mount_workdir: true            # mounted read-only at /workspace
  mount_workdir_writable: false  # true to allow writes
  allow_network: false           # default: no network
  user: "65534:65534"
  resource_limits:
    cpu_limit: "0.5"
    memory_limit: 256m
    pids_limit: 128
    timeout_sec: 30
```

`network_isolated: true` is an old key kept for compatibility. It only means "no network", which is already the default. Use `allow_network` to turn the network on.

An unknown `type`, docker-only options on a `local_restricted` tool, and contradictory settings are refused when the config loads. Docker fails closed: if `docker` is missing, the call fails and the command is not run on the host. Details: [security](security.md#sandboxing).

## fs

File operations with a folder fence.

| `operation` | Meaning |
|---|---|
| `read` | Read a text file. Argument: `path`. Capped at 10 MB by default (`resource_limits.max_output_bytes`, `-1` turns the cap off). |
| `write` | Write a file. Arguments: `path`, `content`, and optional `mode` (`append` appends). |
| `list` | List a directory. Arguments: `path`, optional `pattern` (default `*`). |
| `search` | Search text with a regex. Arguments: `path`, `pattern`, optional `file_pattern`. |
| `read_image` | Load a png, jpeg, gif or webp (max 20 MB) for a `vision: true` agent. |
| `read_audio`, `read_media` | Load audio, or image or audio, for a capable agent. See [attachments](attachments.md). |

```yaml
tools:
  - name: read_file
    type: fs
    operation: read
    allowed_paths: ["./docs"]
    description: Read a file in docs
    parameters:
      path: { type: string, description: File path, required: true }
```

Always set `allowed_paths`. The default is `["."]`, the whole launch folder, which can include `.env` and `.git`. A path outside the list is refused with "is not in allowed paths".

## mcp_server

Uses another program's tools through the Model Context Protocol. The transport is `http` when `url` is set, otherwise `stdio`.

```yaml
tools:
  - name: files            # prefix for the tool names this server brings
    type: mcp_server
    description: Local MCP server over stdio
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "."]
    env:
      EXAMPLE_FLAG: "1"
  - name: remote
    type: mcp_server
    description: MCP server reached over HTTP
    transport: http
    url: http://localhost:9200/mcp
    max_response_bytes: 1048576    # default 1 MiB per response
    timeout_seconds: 30
```

Each tool the server lists becomes a rakitsu tool named `<name>_<server tool name>` (non-alphanumeric characters become `_`). For `name: remote` and a server tool `say`, the tool is `remote_say`. In the agent's `tools:` list write the **server name** (`remote`). That gives the agent all of the server's tools. The individual names such as `remote_say` are what the model sees and calls; they cannot be listed in `tools:` because they do not exist when the config is validated.

Limits:

- The HTTP client sends no `Authorization` header and has no `api_key` field. It cannot talk to an MCP endpoint that needs a bearer token, including a rakitsu `serve` that has `RAKITSU_API_TOKEN` set.
- `stdio` starts `command` as a subprocess. Only configure commands you trust.
- If the server fails to start, rakitsu prints `warn: MCP server "<name>" init failed` and continues without those tools.
- Responses larger than `max_response_bytes` fail only that call.

To serve your own tools over MCP, see [MCP](mcp.md).

## a2a

Delegates one task to an agent on another rakitsu instance. The tool takes a text task and returns the agent's answer.

```yaml
tools:
  - name: researcher
    type: a2a
    description: Ask the Helper agent on the other rakitsu
    url: http://127.0.0.1:9100  # base URL of the other rakitsu, without /a2a
    agent: Helper
    api_key: ${PEER_TOKEN}      # only if the peer sets RAKITSU_API_TOKEN
    timeout_seconds: 30         # default 30
```

The model calls the tool with one argument, `query`. The URL is the base address of the other rakitsu. Rakitsu adds `/a2a` itself, so do not write it. The URL must be `http` or `https`, with a host and no user name or password. The tool uses a network client that checks the resolved address at connection time, ignores `HTTP_PROXY` settings, and follows only same-origin redirects. The peer you configure (its host and port) is allowed even if it is on a private address. The key is sent as `Authorization: Bearer <api_key>`. Details: [A2A](a2a.md), [security](security.md#network-rules).

## Skills

A skill bundles tool names and a prompt template, and agents list skills in `skills:`:

```yaml
skills:
  - name: file_helper
    description: Read files carefully
    tools: [read_file]
    prompt_template: Read the file before you answer.
```

## Where to look next

- Runnable configs with tools: [examples/single/02-single-agent](../../examples/single/02-single-agent/), [08-full-featured](../../examples/single/08-full-featured/).
- Tools that rakitsu adds by itself (memory, messaging, spawn, alerts, start_task): [memory](memory.md), [chat sessions](chat-sessions-messaging.md), [alerts](alerts.md), [start_task](start-task.md).
