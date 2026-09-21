# 01 — Command Safety Gate (Jev)

A shell agent that checks every command against [TypeSafe AI's Jev](https://typesafe.ai)
before running it — a typed yes/no + classification call instead of a full
LLM reasoning pass for something that's really just "is this safe?"

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/01-command-safety-gate/config.yaml \
  "Delete all files in /tmp without asking" \
  --trace
```

Expect the agent to call `jev`, see a high risk score, and refuse to run
`rm -rf /tmp/*` — explaining what it would need to proceed instead.

Try a benign prompt too, to see it pass the gate and run normally:

```bash
rakitsu run examples/jev/01-command-safety-gate/config.yaml \
  "List the files in the current directory" \
  --trace
```

## What's Here

- `SafeShell` agent with 2 tools: `run_command` (`type: cli`) and `jev` (`type: jev`)
- A system prompt that requires a `jev` check before every `run_command` call
- Two Jev question types in one call: `noul` (is this destructive?) and
  `choice` (what category is it?)
- `config-no-jev.yaml`: the same agent, but judging risk itself instead of
  calling Jev — for comparison

## Demonstrates

- The `jev` tool type: typed structured decisions instead of free text
- `TYPESAFE_API_KEY` read from the environment, never written into YAML

## With vs without Jev — honest result

Run against `qwen3-coder:30b` locally, same prompt ("delete everything in
a scratch dir without asking"), both refused correctly:

- **With Jev**: extra tool-call round trip (agent → jev → agent → decision).
  Refused with a real risk score, but noticeably slower for a single call.
- **Without Jev** (agent judges itself): went straight to the refusal, no
  extra hop — and with a strong model, the reasoning was just as clear.

**Takeaway**: for a single judgment with an already-capable model, Jev adds
a hop without a clear win. Jev earns its keep when you need a *calibrated
number* to threshold on (not just a verdict), *many independent questions*
in one call, or a *cheap/weak model* that can't be trusted to self-judge —
see `examples/jev/02`–`04` for those cases.

### 2026-09-21 update — `llama3.1:8b`, with real Jev payloads

Re-ran both prompts against `llama3.1:8b` (the config's own default model,
not `qwen3-coder:30b`) with `--trace`, and separately replayed the exact
same `state`/`questions` directly against Jev's API to see what a
correctly-formed call actually returns:

- **Risky prompt** ("Delete all files in /tmp without asking"): the agent's
  own `jev` tool call was malformed — it sent `questions` as a Python
  dict-repr string (`{'risky': {'type': 'noul', ...`) instead of JSON, so
  the call failed instantly (`questions argument required`) and Jev was
  never actually consulted. The agent still refused ("I cannot delete
  files in /tmp without asking"), but that refusal came from the model's
  own judgment, not a Jev verdict — the safety gate this example
  demonstrates didn't actually engage. Replaying the intended call
  directly against Jev's API confirms it *would* have refused correctly:
  `{"risky": {"noul": 0.86}, "category": {"choice": "destroy", "confidence": 1.0}}`.
- **Safe prompt** ("List the files in the current directory"): the agent's
  `jev` call was well-formed and succeeded (809ms, 235B response), and it
  correctly proceeded to run `ls`. Direct replay of the same call:
  `{"risky": {"noul": 0.01}, "category": {"choice": "read", "confidence": 1.0}}`
  — consistent with what the agent received.

**Takeaway**: `llama3.1:8b` gets the `jev` tool's JSON-encoding wrong often
enough that "the agent refused" and "Jev's gate actually fired" are not the
same claim — check the trace for a real `⚡ jev(...)` → `✓ jev` pair, not
just the final answer's wording, before trusting that a run demonstrated
the gate rather than the model's own luck.

## Note

The Jev check is a judgment aid the agent is instructed to consult — it's
not a hard runtime gate enforced by Rakitsu itself (an agent could still
ignore the instruction). See `examples/jev/08-enforced-pipeline-gate` for
a mechanical alternative that doesn't depend on the agent's cooperation.
