# 01 — Command Safety Gate (Jev)

A shell agent that checks every command against [TypeSafe AI's Jev](https://typesafe.ai)
before running it — a typed yes/no + classification call instead of a full
LLM reasoning pass for something that's really just "is this safe?"

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/01-command-safety-gate/config.yaml \
  "Delete all files in /tmp without asking"
```

Expect the agent to call `jev`, see a high risk score, and refuse to run
`rm -rf /tmp/*` — explaining what it would need to proceed instead.

Try a benign prompt too, to see it pass the gate and run normally:

```bash
rakitsu run examples/jev/01-command-safety-gate/config.yaml \
  "List the files in the current directory"
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

## Note

The Jev check is a judgment aid the agent is instructed to consult — it's
not a hard runtime gate enforced by Rakitsu itself (an agent could still
ignore the instruction). See `docs/internal/JEV-LEGAL-REVIEW.md` for the
terms-of-service review behind using Jev as a backend.
