# ACP (Agent Client Protocol)

## When to use it

You want rakitsu to work as a coding agent inside an editor that speaks ACP, such as Zed. The editor starts `rakitsu acp` itself and talks to it over stdin and stdout.

## Run it

```bash
rakitsu acp examples/acp/dev-agent.yaml
```

Every session the editor opens runs the config you pass on the command line. The editor does not choose the config.

Flags: `--max-sessions` (default 256), `--session-idle-timeout` (default 24h), `--timeout` (seconds per turn; default is the config value, else 300). See [CLI](cli.md#rakitsu-acp).

## Editor setup (Zed)

```json
"agent_servers": {
  "rakitsu": {
    "type": "custom",
    "command": "/path/to/rakitsu",
    "args": ["acp", "/path/to/examples/acp/dev-agent.yaml"]
  }
}
```

Give the config the tools an editor agent needs (`fs` read, write, list, search and a sandboxed `cli` tool). A chat-only config connects but cannot do much. Keys such as `LITELLM_API_KEY` go in the `env` of the editor entry or in your environment, never in the YAML. A full guide with a ready config is in [examples/acp](../../examples/acp/).

## What the server speaks

JSON-RPC 2.0, one message per line.

| Message | Direction | Meaning |
|---|---|---|
| `initialize` | editor to rakitsu | Returns protocol version 1 and empty capabilities (no `loadSession`, no image or audio input, no MCP forwarding). |
| `session/new` | editor to rakitsu | Creates a session. `cwd` and `mcpServers` are accepted and ignored. File access follows the config, not the session. A note goes to stderr. |
| `session/prompt` | editor to rakitsu | Runs a turn. Updates stream back as `session/update`. The reply has `stopReason`. A second prompt on a busy session gets error `-32000` "session busy". |
| `session/cancel` | editor to rakitsu | Notification. Cancels the running turn. |

Try it from a shell (the answer needs a working provider):

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | rakitsu acp examples/acp/dev-agent.yaml
```

## Limits

- Sessions are held in memory. There is no resume after a restart, and the wake timer is not supported. Use [`serve`](serve-web-ui.md) for that.
- A line over 16 MiB is rejected.
- Conversation memory (`settings.memory.conversation`) applies to single-agent configs only.
- A session unused longer than `--session-idle-timeout` is dropped; a later prompt gets "session not found".
