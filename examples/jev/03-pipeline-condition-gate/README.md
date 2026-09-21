# 03 — Pipeline Condition Gate (Jev)

A small `Pipeline` loop where the condition agent is `JevGate`. After each
iteration, it calls Jev once to classify whether the work is complete and
acceptable, then emits exactly `PASS` or `FAIL`. Rakitsu exits a loop only
when a condition result starts with `PASS`.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/03-pipeline-condition-gate/config.yaml \
  "Write a Go function that returns the larger of two integers." \
  --trace
```

Run the same query against the free-text baseline:

```bash
rakitsu run examples/jev/03-pipeline-condition-gate/config-no-jev.yaml \
  "Write a Go function that returns the larger of two integers." \
  --trace
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

### 2026-09-21 update — weaker model re-test, as suggested above

Re-ran with `llama3.1:8b` (the config's own default) on "write a Go
function that returns the larger of two integers" — this is the divergence
case the note above asked for. Result: **`JevGate`'s own tool call failed**,
not on formatting this time but on an argument swap — it sent the
`questions` object's content into the `state` field and the plain-English
instruction into `questions`, so the call errored with
`questions argument required` before Jev ever saw it (trace:
`⚡ jev(questions: Is the work complete and acceptable for..., state: {'completion': {'type': 'choice'...`
— fields visibly reversed). The pipeline still finished with working Go
code, meaning the loop exited despite its own condition agent's tool call
failing outright — worth checking separately whether Pipeline's
`condition_agent` failure path defaults to "proceed" rather than "retry" or
"fail loud," since that's a distinct question from Jev's own reliability.

Replaying the intended call directly against Jev's API (correct field
order) returns `completion: {"choice": "pass", "confidence": 0.95}` for
the actual finished work — so Jev's judgment itself was right, again for a
call it never got asked correctly.

**Takeaway**: this confirms the divergence the previous test predicted, but
not in the way expected — the weaker model's tool-calling reliability
broke before Jev's typed schema even mattered, so the "genuinely ambiguous
completion" scenario this note asked for still hasn't been tested. The
condition-agent-call-fails-silently behavior is arguably the more
actionable finding here and may be worth its own issue.

### 2026-09-21 correction — budget was also unequal here, fixed

`LLMGate` (no-jev) had `max_iterations: 1` versus `JevGate`'s `max_iterations: 2` — same class of
unfair-comparison bug as example 02's, fixed to match (both now 2). Re-ran with equal budgets:
no change in outcome — both variants still produce correct Go code. Unlike example 02, this
example's result doesn't depend on the budget fix.
