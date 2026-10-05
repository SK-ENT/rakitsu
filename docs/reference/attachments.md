# Attachments: images, vision and audio

## When to use it

- Ask a question about a screenshot, a photo or a chart.
- Give an agent a voice memo and let it summarize it.
- Let a tool (for example `fs` with `read_image`) hand an image to the model during a run.

## Turn vision on

An agent only accepts images when its config says so:

```yaml
name: vision-demo
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
tools:
  - name: show_image
    type: fs
    operation: read_image
    allowed_paths: ["."]
    description: Load an image file
    parameters:
      path: { type: string, description: Image path, required: true }
agents:
  - name: Looker
    role: worker
    vision: true
    system_prompt: Describe what you see in two sentences.
    tools: [show_image]
    settings:
      max_iterations: 3
```

`vision` values: `true` always sends images. `false` never does. If unset, images in tool results are sent when the model allows it; a model that rejects images falls back to a text note. For `--attach` the agent must have `vision: true`. Without it you get: `agent "Looker" does not accept image input (set vision: true in its config) - cannot use --attach`.

The model itself must be able to read images. Pick a vision model.

## Attach files to a run

```bash
rakitsu run vision-demo.yaml "What is in this image?" --attach shot.png
rakitsu run vision-demo.yaml "Summarize this memo" --attach memo.wav
rakitsu run vision-demo.yaml "Compare these" --attach a.png --attach b.png
```

| Rule | Detail |
|---|---|
| Image types | jpeg, png, gif, webp. |
| Audio types | mp3, wav, ogg, flac, m4a, webm. |
| Size | At most 20 MB per file. |
| Type check | Rakitsu reads the first bytes of the file. A text file gives `unsupported content type ...`. |
| Missing file | `cannot attach "x.png": cannot stat ...`. |

### Auto-attach

If your query names an image or audio file that exists inside the working directory, it is attached automatically.

```bash
rakitsu run vision-demo.yaml "Describe shot.png please"
rakitsu run vision-demo.yaml "Describe shot.png please" --no-auto-attach   # do not attach
```

URLs in the query are ignored. Files outside the workdir are not picked up.

### Audio by provider

| Provider | What happens to audio |
|---|---|
| Gemini | Sent natively. |
| OpenAI and OpenAI-compatible (`openai`, `litellm`, `ollama` types) | Transcribed first with the transcription endpoint, then the text is added to the query as `[transcript of memo.wav]: ...`. The model name comes from `transcription_model` (default `whisper-1`). |
| Anthropic | Refused: `provider (anthropic) does not accept audio input - Claude has no audio input support`. |

## Images from tools

The `fs` tool has three media operations:

| `operation` | Use |
|---|---|
| `read_image` | Load an image. |
| `read_audio` | Load an audio file. |
| `read_media` | Look at the file and load it as an image or audio. |

The model calls the tool with `path`. The image goes into the conversation of a `vision: true` agent. MCP tools that return images are handled the same way.

Large images (more than 1,000,000 bytes in base64, about 750 KB of file) are shrunk to 1568 px and sent as JPEG. Change the limit with the `RAKITSU_IMAGE_SHRINK_BYTES` environment variable, or set it to `0` to turn shrinking off.

Example config: [examples/single/11-vision-chat](../../examples/single/11-vision-chat/).
