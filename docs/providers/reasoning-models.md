# Reasoning Models — Reliability Guide

Some LLMs (`your-model-alias`, DeepSeek-R1, QwQ, Qwen3-thinking) emit chain-of-thought as a separate `reasoning_content` stream and only commit the user-facing answer to the `content` stream after a final-answer stop condition. On long-form synthesis tasks with a 16K-context window, the model can consume its entire `max_tokens` budget on `reasoning_content` and never produce a `content` delta — leaving rakitsu with a multi-thousand-token chain-of-thought and no real answer.

Rakitsu handles the common case automatically. This guide explains the three production strategies for cases where the default isn't enough.

## How rakitsu handles reasoning models by default

`internal/llm/format/` selects an extraction adapter based on model name:

| Model pattern | Adapter | What it does |
|---|---|---|
| `nemotron`, `deepseek-r1`, `deepseek-reasoner`, `qwq` | `reasoning_content_field` (wrapped with inline-tag fallbacks) | Accumulates `reasoning_content` separately; on stream end, if `content` is empty, attempts to extract a final answer from reasoning. |
| `qwen3.*(thinking\|coder\|tool)` | `standard_openai` (wrapped with inline-tag fallbacks) | Strips `<think>` blocks and parses inline `<tool_call>` XML. |
| Other | Sniffing fallback | Auto-detects from first streaming delta. |

Extraction strategies (in `reasoning_content_field.go`), tried in order:

1. Explicit marker (`Final answer:`, `Thus:`, `Therefore:`, etc.) — first match wins, candidate length ≥ 20 chars.
2. Trailing markdown report (`#`/`##`/`###` headers + ≥ 2 header lines or ≥ 3 non-empty lines).
3. Fallback: prefix the raw reasoning with `[REASONING-ONLY OUTPUT — …]` so operators see what happened.

This works for repo-exploration tool loops (model reads files, then writes a report). It fails on supervisor-delegation and CSV-interpretation tasks where the answer data is embedded inline in CoT but never marked — extraction hardening for those cases is planned but not yet available.

## Strategy 1 — Default (nemotron + extraction)

Use this if you want one model end-to-end, accept occasional `[REASONING-ONLY OUTPUT]` markers, and don't want a cloud dependency.

```yaml
settings:
  default_provider: nemotron
  providers:
    nemotron:
      type: openai
      base_url: ${VLLM_BASE_URL}
      api_key: ${VLLM_API_KEY}
  defaults:
    model: your-model-alias
    max_tokens: 4096
```

**When this works**: tool-using exploration, code review, repo audits — anything where the model reads tool results and writes a final markdown report.

**When it doesn't**: long-form synthesis on small contexts, CSV interpretation, supervisor delegation under Hierarchical/Pipeline strategies, structured-output prompts (`OUTPUT_KEY: <value>` schemas).

**Failure signal**: `[REASONING-ONLY OUTPUT — …]` appears as the first ~250 chars of a worker's `final_answer`. Check session JSONL: `grep -l "REASONING-ONLY" ~/.rakitsu/sessions/*.jsonl`.

## Strategy 2 — Gemini synthesis fallback (escape hatch)

When Strategy 1 produces `[REASONING-ONLY]` consistently for your workload, swap to Gemini for the synthesis path. Gemini-3.1-flash-lite is non-reasoning, has a large context, and produces clean `content` streams. Tool calling and structured output both work.

Two ways to authenticate:

### Option A: Gemini API key (easiest)

```bash
export GOOGLE_API_KEY=AIza...
```

```yaml
settings:
  default_provider: gemini-direct
  providers:
    gemini-direct:
      type: gemini
      api_key: ${GOOGLE_API_KEY}
  defaults:
    model: gemini-3.1-flash-lite-preview
    temperature: 0.1
    max_tokens: 4096
```

### Option B: Vertex AI service account (production)

```yaml
settings:
  default_provider: vertex-gemini
  providers:
    vertex-gemini:
      type: gemini
      credentials_file: /path/to/service-account.json
      project: your-gcp-project
      location: global
  defaults:
    model: gemini-3.1-flash-lite-preview
```

Working reference: [examples/providers/gemini.yaml](../../examples/providers/gemini.yaml).

**Cost**: pay-per-token, billed via Google. Use Strategy 3 (hybrid) below if you want the cheap-local-worker pattern.

## Strategy 3 — Hybrid (local workers, cloud synthesis)

Run high-volume tool-using workers on the local nemotron model (free), then aggregate via a cloud non-reasoning model. This is the most reliable pattern for production-like reliability without paying for every token.

This configuration fragment omits tool definitions: add root `tools:` entries named `read_file`, `list_files` and `grep` before running it.

```yaml
settings:
  default_provider: nemotron-local   # ← required: DataGatherer has no explicit
                                      #   provider:, so it resolves via this field
  providers:
    nemotron-local:
      type: openai
      base_url: ${VLLM_BASE_URL}
      api_key: ${VLLM_API_KEY}
    gemini-cloud:
      type: gemini
      api_key: ${GOOGLE_API_KEY}
  defaults:
    model: your-model-alias

agents:
  - name: DataGatherer
    role: worker
    # Inherits provider+model from defaults — uses local nemotron.
    tools: [read_file, list_files, grep]

  - name: Synthesizer
    role: worker
    provider: gemini-cloud
    model: gemini-3.1-flash-lite-preview
    # No tools — pure synthesis.

orchestrator:
  name: HybridPipeline
  strategy: Pipeline
  agents: [DataGatherer, Synthesizer]
  pipeline:
    steps:
      - name: gather
        agent: DataGatherer
      - name: synthesize
        agent: Synthesizer
```

**Why it works**: worker tasks are tool-bounded — model reads, calls a tool, sees the result, calls the next tool. Reasoning models stop cleanly after each tool result. The synthesis step is the open-ended one that triggers reasoning loops, so use a non-reasoning model for that step.

**Cost ceiling**: only the synthesizer step's tokens hit Google. Workers stay local.

## Choosing between strategies

| You're seeing… | Try… |
|---|---|
| Strategy 1 `[REASONING-ONLY]` markers, willing to keep things local | Wait for extraction hardening (planned, not yet available). |
| Strategy 1 `[REASONING-ONLY]` markers, need reliability now | Strategy 2 (Gemini direct) — fastest swap. |
| Strategy 1 markers under high tool-call volume + you watch billing | Strategy 3 (hybrid) — local workers + cloud synthesis. |
| Strategy 1 working fine | Stay on Strategy 1; no action needed. |

## Verification

After switching strategies, verify by parsing session JSONL directly:

```bash
# Run a representative scenario
rakitsu run examples/providers/gemini.yaml \
  "Summarize this project's structure" \
  --trace --verbose --no-hub --timeout 300

# Check the session for reasoning-only markers
SESSION_ID=$(ls -t ~/.rakitsu/sessions/*.jsonl | head -1)
grep -c "REASONING-ONLY" "$SESSION_ID"
# Expected: 0

# Check final_answer non-empty
python3 -c "
import json
for line in open('$SESSION_ID'):
    e = json.loads(line)
    if e.get('event_type') == 'AGENT_END':
        print('final_answer chars:', len(e['payload']['final_answer']))
"
```

## See also

- [examples/providers/gemini.yaml](../../examples/providers/gemini.yaml) — minimal Gemini config, including the Vertex AI service-account fields (commented out).
- `internal/llm/format/format.go`'s package doc comment — how the format-adapter selection (config override > model-name pattern > fallback) actually works.
