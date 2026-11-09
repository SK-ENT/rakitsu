# ACP Agent Config

A rakitsu config for running as an external agent inside an editor that speaks
the [Agent Client Protocol](https://agentclientprotocol.com) (ACP) — Zed, or
any other ACP client. Unlike the other examples, this isn't run directly with
`rakitsu run`; an ACP client launches `rakitsu acp <config>` itself and talks
to it over stdio (JSON-RPC).

## Built-in setup

Use `rakitsu acp` as the agent command (for example, set Zed's custom agent
`args` to `["acp"]`). On first launch, an ACP client with Terminal Auth
support offers **Log in**. Choose your provider and model in the setup
terminal. For Ollama, start Ollama and download a model first. For a cloud
provider, enter the provider’s API key when prompted. An agent waiting for
setup loads your selection on its next session request. You can also run
`rakitsu acp --login` directly.

The built-in agent is chat-only. Cloud keys are stored in Keychain on macOS,
Credential Manager on Windows, or a running Secret Service on Linux.
Ollama needs no key.

## Files

| File | Notes |
|------|-------|
| `dev-agent.yaml` | fs tools (`read-file`, `write-file`, `list-files`, `search-files`) + a sandboxed `run-command` shell tool, wired to a LiteLLM provider |

## Use an explicit YAML config

1. Build rakitsu (`make build`) or note the path to an existing binary.
2. Point your ACP client at it. For Zed, add to `settings.json`:

   ```json
   "agent_servers": {
     "rakitsu": {
       "type": "custom",
       "command": "/path/to/rakitsu",
       "args": ["acp", "/path/to/examples/acp/dev-agent.yaml"],
       "env": {
         "LITELLM_BASE_URL": "https://your-litellm-proxy/v1"
       }
     }
   }
   ```

3. Restart the client's agent panel. It should now be able to read, write,
   and search files in your project, and run allowlisted shell commands
   (`go`, `git`, `npm`, `make`, ...).

Swap `settings.providers.litellm` for `openai`/`anthropic`/`gemini`/`ollama`
(see `../providers/`) if you're not using a LiteLLM proxy.

## Demonstrates

- `rakitsu acp` as an ACP agent server (stdio JSON-RPC, not `rakitsu run`)
- A tool set that can actually edit files, not just chat — a chat-only config
  (no `tools:`) will connect fine but can't do anything useful in an editor
- Multi-turn sessions: `session/prompt` calls against the same session thread
  prior turns' history, so follow-up requests ("now append to that file")
  work the way they would in a normal chat
