# 10 — Spawn Fan-out

A coordinator that fans work out to parallel runtime subagents.

## Run

```bash
rakitsu run examples/single/10-spawn-fanout/config.yaml "Research the current state of solid-state batteries and quantum computing, then summarize both"
```

## What's Here

- `Coordinator` agent that splits a task and calls `spawn_agent` once per
  part in the same response so subagents run in parallel
- `Researcher` template agent, spawned per topic
- `settings.spawn` — `enabled: true`, `max_concurrent: 3`
- Defaults to local Ollama (`llama3.1:8b`), keyless

## Demonstrates

- `spawn_agent` fan-out to parallel runtime subagents
- Coordinator/worker pattern without a full orchestrator strategy
- Synthesizing multiple subagent results into one answer
