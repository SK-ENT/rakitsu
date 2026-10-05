# LLM providers

## When to use it

Pick a provider by where your model runs.

| Provider type | Use it for |
|---|---|
| `openai` | OpenAI, and any server that speaks the OpenAI chat API (set `base_url`). |
| `litellm` | A LiteLLM proxy in front of many models. Same wire format as `openai`; no API key needed if the proxy has none. |
| `ollama` | Local models. No API key. Default `base_url` is `http://localhost:11434/v1`. |
| `anthropic` | Claude models. |
| `gemini` | Google Gemini, direct or on Vertex AI. |
| `codex` | The ChatGPT subscription, reusing the login that Codex CLI saved. No API key. |

NVIDIA NIM is OpenAI-compatible. Use `type: openai` with its `base_url`, or run with `--provider nvidia`.

## Define a provider

Providers live under `settings.providers`. The key is the instance name that agents refer to.

```yaml
name: provider-demo
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
    local:
      type: ollama
      base_url: http://localhost:11434/v1
  defaults:
    model: gpt-4o-mini
agents:
  - name: Assistant
    role: worker
    system_prompt: Be brief.
  - name: LocalHelper
    role: worker
    provider: local
    model: llama3.1:8b
    system_prompt: Be brief.
```

Provider fields:

| Field | Meaning |
|---|---|
| `type` | `openai`, `anthropic`, `gemini`, `codex`, `ollama`, `litellm`. |
| `api_key` | API key. Use `${VAR}`. Not used by `codex`. Required for `openai` and `anthropic`. |
| `base_url` | Custom endpoint. |
| `credentials_file` | Gemini: service-account JSON. Codex: path to `auth.json`. |
| `location`, `project` | Vertex AI region and project. |
| `default_model` | Model for this provider when the agent names none. |
| `response_format` | Adapter override: `standard_openai` or `reasoning_content_field`. Empty means auto-detect from the model name. |
| `rate_limit` | Maximum requests per minute to this provider. `0` is unlimited. |
| `reasoning_effort` | OpenAI `reasoning_effort` on every request: `none`, `minimal`, `low`, `medium`, `high`. GPT-5.6 models need it when tools are used. An agent can override it in `model_config.reasoning_effort`. |
| `transcription_model` | Audio transcription model used by `--attach` of audio on OpenAI-compatible providers. Default `whisper-1`. |
| `client_version` | Codex only. Version sent on model-list requests. The backend lists newer models only for newer versions. Default `1.0.0`. |

The older flat maps `settings.api_keys`, `base_urls`, `credentials_files`, `locations` and `projects` still work. See [configuration](configuration.md).

### How a provider and a model are chosen

- Provider: the agent `provider`, then `settings.default_provider`, then `openai`.
- Model: the agent `model`, then the provider `default_model`, then `settings.defaults.model`, then `gpt-4o-mini`.
- Override both from the command line for one run: `rakitsu run config.yaml "query" --provider litellm --model gpt-4o`. If the provider is not in the config, rakitsu builds it from `<NAME>_API_KEY` and `<NAME>_BASE_URL`.

If a key is missing you see an error such as `openai API key not set - export OPENAI_API_KEY or add api_key to settings.providers.openai`. The key must reach the provider block (usually through `api_key: ${OPENAI_API_KEY}`), not only the environment.

### Fallback chain

An agent can list several providers. They are tried in order.

```yaml
agents:
  - name: Resilient
    role: worker
    system_prompt: Be brief.
    providers:
      - name: openai
        model: gpt-4o-mini
      - name: local
        model: llama3.1:8b
```

## Codex (ChatGPT subscription)

The `codex` provider speaks the Responses API of the ChatGPT backend and reuses the login of the Codex CLI. You log in once with `codex login`. Rakitsu reads `~/.codex/auth.json` and refreshes the token when it is about to expire.

```yaml
name: codex-demo
settings:
  default_provider: codex
  providers:
    codex:
      type: codex
      default_model: gpt-6-astra     # optional
agents:
  - name: Assistant
    role: worker
    system_prompt: Be brief.
```

Facts from the code:

- If `default_model` is not set, the `model` line of `config.toml` next to `auth.json` is used. If neither exists, startup fails with "no model set".
- `model_reasoning_effort` from that `config.toml` is used as the reasoning effort.
- The endpoint defaults to `https://chatgpt.com/backend-api/codex`. `base_url` overrides it.
- It is streaming only. `defaults.max_tokens` is not sent to the backend.
- `credentials_file` changes the path of `auth.json`.
- The login is your own ChatGPT account. OpenAI's terms for that account apply.

A ready file is in [examples/providers/codex.yaml](../../examples/providers/codex.yaml). Use it as a cost-aware writer behind the bridge: [rakitsu-ask](rakitsu-ask.md#use-cases).

## Reasoning models

Models that stream their thinking in a separate field are handled automatically. See [docs/providers/reasoning-models.md](../providers/reasoning-models.md). Raise `--max-tokens` for these models, because hidden reasoning tokens use the same budget.

## Check a provider

`rakitsu doctor config.yaml` probes each provider with a `base_url` and checks that the model is in the catalog. See [doctor](doctor.md).

More samples: [examples/providers](../../examples/providers/).
