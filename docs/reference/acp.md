# ACP (Agent Client Protocol)

## When to use it

Rakitsu works as an external agent in editors and other clients that speak ACP. The client starts `rakitsu acp` and communicates over standard input and output.

## Quick start in Zed

The Registry install becomes available once [Rakitsu’s ACP Registry pull request](https://github.com/agentclientprotocol/registry/pull/765) is merged and the listing is published.

1. In Zed, open the ACP Registry, install **Rakitsu**, and start a Rakitsu thread.
2. Choose **Log in** and enter your OpenAI API key. Rakitsu saves it in your operating system’s credential store for future sessions.
3. Send your first prompt in the same thread.

On macOS, the system may ask for permission to use the login Keychain. If it asks for the Keychain password, use your existing Keychain password, usually your Mac login password. Enter your OpenAI API key at the `OpenAI API key:` prompt. [Apple’s Keychain guidance](https://support.apple.com/en-ie/guide/keychain-access/kyca2612/mac).

The built-in agent is chat-only and needs no YAML. It uses `gpt-5.6-luna` in v0.3.2 and `gpt-4o-mini` in v0.3.1. While the listing is pending, you can install the Rakitsu binary yourself and use the custom agent setup below with `"args": ["acp"]`.

## Use another ACP client

Configure the client to launch the installed `rakitsu` executable with the argument `acp`. If the client supports Terminal Auth, use its login prompt. Otherwise, run `rakitsu acp --login` in a terminal, then start the agent in your client.

## Use a YAML config

Pass a config if you want a different provider, your own agent instructions, or tools.

```bash
rakitsu acp examples/acp/dev-agent.yaml
```

Every session uses the config passed at launch. For a custom agent in Zed, add this entry to `settings.json` with the paths to your binary and config.

```json
{
  "agent_servers": {
    "rakitsu": {
      "type": "custom",
      "command": "/path/to/rakitsu",
      "args": ["acp", "/path/to/examples/acp/dev-agent.yaml"]
    }
  }
}
```

Supply provider credentials through the environment used to launch the agent. The [ACP example](../../examples/acp/) includes a LiteLLM configuration with file tools and an allowlisted shell tool.

See [CLI](cli.md#rakitsu-acp) for `--max-sessions`, `--session-idle-timeout`, and `--timeout`.

## What the server speaks

JSON-RPC 2.0, one message per line.

| Message | Direction | Meaning |
|---|---|---|
| `initialize` | editor to rakitsu | Returns protocol version 1. The built-in agent advertises Terminal Auth to compatible clients. |
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
