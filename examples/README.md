# Rakitsu Examples

Progressive examples from simple to complex. Each has two variants:
- **`single/`** — everything in one YAML file
- **`modular/`** — split into `agents/`, `tools/`, `skills/`, `orchestrators/` subdirectories (auto-discovered)

## Quick Start

```bash
# Set your API key (all examples default to OpenAI)
export OPENAI_API_KEY=sk-...

# Single-file variant
rakitsu run examples/single/01-chat/config.yaml "Hello!"

# Modular variant (same behavior, different layout)
rakitsu run examples/modular/01-chat/config.yaml "Hello!"

# Multi-agent pipeline
rakitsu run examples/single/04-pipeline/config.yaml "Write a blog post about WebAssembly"
```

To use a different provider (Anthropic, Ollama, LiteLLM), see `providers/` for reference configs.

To run rakitsu as an agent inside an editor (Zed or another ACP client) instead of via `rakitsu run`, see `acp/`.

Other example categories: `eval/` (evaluation harness configs), `jev/` (TypeSafe Jev verification patterns) — see each directory's own README(s).

## Examples

| # | Name | Strategy | Description |
|---|------|----------|-------------|
| [01](single/01-chat/) | Chat | Single agent | Minimal config — one agent, no tools |
| [02](single/02-single-agent/) | Single Agent | Single agent | CLI + filesystem tools, sandbox |
| [03](single/03-react-team/) | ReAct Team | ReAct | Supervisor delegates to 3 specialists |
| [04](single/04-pipeline/) | Pipeline | Pipeline | Sequential + parallel steps, synthesis |
| [05](single/05-dev-team/) | Dev Team | Pipeline | Plan → implement → verify → review |
| [06](single/06-advanced-strategies/) | Advanced | Mixed | Hierarchical, PlanAndExecute, Loop, DAG |
| [07](single/07-nested-orchestrators/) | Nested | Pipeline | Sub-orchestrators, 3-level hierarchy |
| [08](single/08-full-featured/) | Full Featured | Pipeline | Every feature combined |
| [09](single/09-memory-chat/) | Memory Chat | Single agent | Native memory/KG + summarized-context conversation |
| [10](single/10-spawn-fanout/) | Spawn Fan-out | Single agent | Coordinator spawns parallel runtime subagents |
| [11](single/11-vision-chat/) | Vision Chat | Single agent | Minimal single-agent config with image attachments |

`09`-`11` are standalone feature demos, not further steps in the `01`-`08`
strategy progression — each isolates one capability (memory/KG, spawn
fan-out, vision) rather than building on the previous example. They're
single-file only; no modular variant exists for them yet.

`06` has no single `config.yaml` — it ships four strategy-variant files
(`hierarchical.yaml`, `plan-and-execute.yaml`, `pipeline-loop.yaml`,
`pipeline-dag.yaml`) instead. See its own README.

## Feature Matrix

| Feature | 01 | 02 | 03 | 04 | 05 | 06 | 07 | 08 | 09 | 10 | 11 |
|---------|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Named providers | x | x | x | x | x | x | x | x | x | x | x |
| CLI tools | | x | x | | x | x | x | x | | | |
| FS tools | | x | x | x | x | x | x | x | | | |
| Skills | | | x | | | | | x | | | |
| Inline tools | | | | | | | | x | | | |
| Docker sandbox | | | | | | | | x | | | |
| Reflection | | | x | | | | | x | | | |
| Ground check | | | | | | | | x | | | |
| Budget limits | | | | | | | | x | | | |
| Context management | | | | | | | | x | | | |
| Vision agent | | | | | | | | x | | | x |
| Provider fallback | | | | | | | | x | | | |
| Native memory/KG | | | | | | | | | x | | |
| Spawn fan-out (subagents) | | | | | | | | | | x | |
| Pipeline sequential | | | | x | x | x | x | x | | | |
| Pipeline parallel | | | | x | | | | | | | |
| Pipeline loop | | | | | | x | | | | | |
| Pipeline DAG | | | | | | x | | | | | |
| Pipeline synthesis | | | | x | | | | x | | | |
| Nested orchestrators | | | | | | | x | | | | |
| Modular layout | | x | x | x | x | x | x | x | | | |

## Directory Structure

```
examples/
  single/                      # Single YAML file per example
    01-chat/config.yaml
    02-single-agent/config.yaml
    ...
    08-full-featured/config.yaml
    09-memory-chat/config.yaml    # standalone: native memory/KG demo
    10-spawn-fanout/config.yaml   # standalone: spawn_agent fan-out demo
    11-vision-chat/config.yaml    # standalone: image-attachment demo
  modular/                     # Auto-discovery layout per example
    01-chat/
      config.yaml              # Settings only (no inline agents)
      agents/assistant.md      # Auto-discovered
    02-single-agent/
      config.yaml
      agents/explorer.md
      tools/read_file.yaml
      tools/...
    ...
  providers/                   # Provider configuration reference
    openai.yaml
    anthropic.yaml
    gemini.yaml
    codex.yaml
    ollama.yaml
    litellm.yaml
    nvidia.yaml
    multi-provider.yaml
  acp/                          # rakitsu as an editor agent (ACP protocol)
    dev-agent.yaml
  eval/                         # Evaluation harness configs
  jev/                          # TypeSafe Jev verification patterns
```

## Strategy Guide

| Strategy | Tool Calling Required? | Best For |
|----------|:----------------------:|----------|
| **Pipeline** | No | Fixed workflows, weak models, deterministic execution |
| **ReAct** | Yes | Flexible investigation, strong models (GPT-4o, Claude) |
| **Hierarchical** | Yes | Large teams, dynamic task assignment |
| **PlanAndExecute** | Yes | Complex tasks requiring upfront planning |
