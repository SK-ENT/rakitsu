# doctor

## When to use it

Run it before you run a config, and when a run fails in a confusing way. It loads the config the same way `rakitsu run` does and checks the provider, so you see a clear message instead of an error in the middle of a run.

## Usage

```bash
rakitsu doctor config.yaml
rakitsu doctor                     # uses ./agent.yaml
rakitsu doctor --json config.yaml  # machine-readable
```

## What it checks

| Check label | Meaning |
|---|---|
| `Config file` | The file can be read. |
| `Env var refs` | Every `${VAR}` in the config has a value. An unset variable is an error. |
| `Config parse` | The YAML loads and passes validation (the same rules as `run`). |
| `Provider reachability` | For a provider with a `base_url`, calls its `/models` endpoint and checks auth. Providers without a `base_url` are skipped with a warning. |
| `Agent model` | Each agent's model is in the provider's model list. If no model is set anywhere you get a warning. |

Each finding has a severity: `OK`, `INFO`, `WARN` or `ERR`. With `--json` you get a list of objects with `severity`, `label`, `subject` (when there is one) and `message`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | All checks passed. |
| 1 | One or more warnings. |
| 2 | One or more errors. |

Example output (a model that is not in the catalog):

```
[OK]   Config parse               d2.yaml: 1 agents, 0 tools, default_provider="fake"
[OK]   Provider reachability      fake: http://127.0.0.1:9188/v1/models OK, 1 models
[ERR]  Agent model                Assistant: "no-such-model" NOT in fake catalog -> available: fake-model
```
