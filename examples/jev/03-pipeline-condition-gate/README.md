# 03 — Pipeline Condition Gate (Jev)

A small `Pipeline` loop where the condition agent is `JevGate`. After each
iteration, it calls Jev once to classify whether the work is complete and
acceptable, then emits exactly `PASS` or `FAIL`. Rakitsu exits a loop only
when a condition result starts with `PASS`.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/03-pipeline-condition-gate/config.yaml \
  "Write a Go function that returns the larger of two integers."
```

Run the same query against the free-text baseline:

```bash
rakitsu run examples/jev/03-pipeline-condition-gate/config-no-jev.yaml \
  "Write a Go function that returns the larger of two integers."
```

## What's Here

- A `Worker` loop step creates or revises the requested work.
- `JevGate` has only the `jev` tool and makes one typed `choice` decision.
- `JevGate` maps Jev's `pass`/`fail` choice to exactly `PASS`/`FAIL`.
- `config-no-jev.yaml` keeps the same pipeline, but asks `LLMGate` to judge
  through its own reasoning while obeying the same single-word output rule.

## With vs without Jev

Run against `qwen3-coder:30b` locally with "write a haiku about autumn":

- **With Jev**: `JevGate` called Jev, got a `pass` choice, emitted `PASS`,
  loop exited after 1 iteration. Took 11.7s.
- **Without Jev**: `LLMGate` judged its own output, emitted `PASS`, loop
  exited after 1 iteration. Took 589ms — much faster, no extra tool hop.

**Takeaway**: with a strong model instructed to output exactly one word,
both worked correctly and reliably on this task. Jev's structural
guarantee (it literally cannot answer anything but `pass`/`fail` from its
schema) matters most when the condition agent's own model is weaker or the
task is more ambiguous — a strong model told "output exactly PASS or FAIL"
mostly follows that instruction fine. This example proves the *plumbing*
works (a real Jev call driving a real Pipeline loop's exit condition), but
didn't surface a case where free-text parsing actually broke. Worth
re-testing with a weaker condition-agent model, or a genuinely ambiguous
completion judgment, to see the two diverge.
