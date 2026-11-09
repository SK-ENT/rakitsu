# ACP (Agent Client Protocol)

## When to use it

Rakitsu works as an external agent in editors and other clients that speak ACP. The client starts `rakitsu acp` and communicates over standard input and output.

## Quick start in Zed

The Registry install becomes available once [Rakitsu’s ACP Registry pull request](https://github.com/agentclientprotocol/registry/pull/765) is merged and the listing is published.

1. In Zed, open the ACP Registry, install **Rakitsu**, and start a Rakitsu thread.
2. Choose **Log in**. In v0.3.2, choose a provider and model in the setup terminal. For Ollama, start Ollama and download a model before setup. For OpenAI, Anthropic, or Gemini, enter that provider’s API key when prompted.
3. Send your first prompt in the same thread.

Rakitsu saves your provider and model for future sessions and stores cloud API keys in your operating system’s credential store. Ollama setup needs no API key. On macOS, the system may ask for permission to use the login Keychain when saving a cloud key. If it asks for a Keychain password, use your existing Keychain password, usually your Mac login password. [Apple’s Keychain guidance](https://support.apple.com/en-ie/guide/keychain-access/kyca2612/mac).

The built-in agent is chat-only and needs no YAML. In v0.3.2, Gemini accepts model IDs with or without the `models/` prefix. In v0.3.1, **Log in** asks for an OpenAI API key and the built-in agent uses `gpt-4o-mini`.

### Install manually while the listing is pending

Install the [Rakitsu v0.3.2 binary](https://github.com/SK-ENT/rakitsu/releases/tag/v0.3.2). In Zed, open Agent Settings, select **External Agents → Add Agent → Add Custom Agent**, and use the absolute path to the binary. Add this entry under `agent_servers` in Zed’s settings.

```json
{
  "rakitsu": {
    "type": "custom",
    "command": "/path/to/rakitsu",
    "args": ["acp"]
  }
}
```

Start a Rakitsu thread and choose **Log in** to finish setup.

## Use another ACP client

Configure the client to launch the installed `rakitsu` executable with the argument `acp`. If the client supports Terminal Auth, use its login prompt. Otherwise, run `rakitsu acp --login` in a terminal, then start the agent in your client.

## Use a YAML config

Pass a config if you want your own agent instructions, tools, or a provider or endpoint outside the built-in setup.

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
