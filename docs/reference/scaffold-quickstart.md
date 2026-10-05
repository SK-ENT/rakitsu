# scaffold and quickstart

## When to use it

- `quickstart`: you are new and want a working project in a minute, by answering a few questions.
- `scaffold`: you know what you want and need a config file now, from a script or a terminal, without questions.

## scaffold

```bash
rakitsu scaffold --list
rakitsu scaffold llm-chat
rakitsu scaffold code-review --provider anthropic --model claude-sonnet-4-6
rakitsu scaffold dev-team --dir -o ./my-dev-team
```

Use-cases (from `rakitsu scaffold --list`):

| Use-case | Type | What it is |
|---|---|---|
| `llm-chat` | single agent | Conversational assistant. |
| `code-review` | single agent | Code reviewer with file read tools. |
| `data-analysis` | single agent | Analyst with file and cli tools. |
| `web-research` | single agent | Reasoning agent with no tools. |
| `dev-team` | multi-agent | Pipeline: planner, developer, reviewer. |
| `qa-pipeline` | multi-agent | Pipeline: test generator, validator. |
| `rag-assistant` | single agent | Searches files, then answers. |
| `react-team` | multi-agent | ReAct supervisor with specialist workers. |
| `hierarchical` | multi-agent | Hierarchical supervisor with delegation. |

Flags: `--provider` (`openai` default, `anthropic`, `gemini`, `ollama`, `litellm`), `--model`, `-o/--output`, `--dir` (a modular folder with `agents/` and `tools/` instead of one file), `-f/--force`, `-l/--list`. An existing output path is refused unless you pass `--force`. An unknown use-case prints the valid ones.

The default output is `<use-case>.yaml`, or `./<use-case>/` with `--dir`. The default `llm-chat` file uses `api_key: "${OPENAI_API_KEY}"`. Check it with [`rakitsu doctor`](doctor.md), then run it:

```bash
rakitsu doctor llm-chat.yaml
rakitsu run llm-chat.yaml "Hello"
```

Note: `rakitsu scaffold --help` lists seven use-cases. `--list` shows all nine, and is the list to trust.

## quickstart

```bash
rakitsu quickstart
```

It needs an interactive terminal. It asks, in order:

1. A template: Chat Bot, Code Reviewer, Research Team, Dev Team, Data Analysis, QA Pipeline, RAG Assistant.
2. A provider: OpenAI, Anthropic, Google Gemini, Ollama (local), LiteLLM (proxy), Codex (ChatGPT subscription, needs `codex login`).
3. For providers that need it: an API key or a base URL. If you give no key, it warns you to set the environment variable before running.
4. The project directory (default `rakitsu-project`).
5. A modular layout (`agents/`, `tools/` folders) or one YAML file.
6. Whether to start the web UI now. If not, it prints `cd rakitsu-project && rakitsu serve`.

Without a terminal it stops with an error that points to `rakitsu scaffold` for non-interactive use.
