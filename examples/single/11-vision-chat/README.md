# 11 — Vision Chat

Minimal single-agent config with vision enabled, for testing `--attach`
(M1.5 phase 1: image attachments). Same shape as `01-chat`, plus `vision: true`.

## Run

```bash
rakitsu run examples/single/11-vision-chat/config.yaml "what is in this image?" \
  --attach /path/to/photo.png
```

## What's Here

- Single `VisionAssistant` agent, no tools, no orchestrator
- `vision: true` on the agent
- Defaults to local Ollama (`llama3.1:8b`) — **not vision-capable**, swap in
  a VL model or uncomment a cloud provider before using `--attach` for real

## Demonstrates

- Vision-capable agent config (`vision: true`)
- `--attach` for passing image input to a run

`--attach` requires a vision-capable model — not every provider/model
combination supports images. Pick one that does (e.g. `gpt-4o`, a Claude 3+
model, `gemini-*`, or a vLLM-served VL model via litellm/ollama).
