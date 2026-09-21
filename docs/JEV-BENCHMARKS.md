# Jev integration — reliability findings

Findings from testing the `jev` tool (`examples/jev/`) against real models and the real TypeSafe
API. This is a report on behavior — what actually happens when an agent calls Jev — not a pricing
or cost comparison.

## Method

Two kinds of runs, both against a locally-built `rakitsu` binary:

- A local batch against small/mid-size Ollama models (`llama3.1:8b`, `llama3.2:1b`), comparing a
  Jev-backed agent to the same agent judging for itself in free text.
- Live runs in CI against real cloud models and the real Jev API, for scenarios that need
  capabilities a local batch can't provide (a headless browser, real API keys).

## Tool-calling reliability, not judgment quality, was the real bottleneck

Every run where Jev was actually reached produced a correct typed answer. The gap wasn't in
Jev's judgment — it was in getting a well-formed tool call to Jev in the first place. Small local
models (`llama3.1:8b`, `llama3.2:1b`) frequently failed to dispatch a real, well-formed `jev`
call at all: malformed JSON, printing the intended call as prose instead of actually invoking the
tool, or swapping arguments into the wrong fields. This wasn't Jev-specific — the same models'
plain shell-command tool calls failed the same ways in side-by-side baseline runs.

A stronger model (tested via a cloud-model override on the same config) dispatched a clean,
well-formed call every time. The practical takeaway: Jev's typed-answer contract is only as
reliable as the calling model's own tool-calling ability — worth checking with a real run before
assuming a given model handles it cleanly, especially for a small local model.

## A real API contract gap, found by testing rather than assuming

Jev's `score` question type requires `criteria` as an ordered array of level descriptions — not
an object keyed by value, which is what a first attempt naturally reaches for: it's what Jev's
own `noul` and `choice` types use, and it reads as more natural YAML for "value → meaning."
Feeding a `score` question that shape instead produces a real `422` from Jev's API. rakitsu's own
config validation doesn't catch this — the mismatch only surfaces once the call actually reaches
Jev. Worth testing any new question shape live rather than trusting it compiles.

## A fact-grounded review example — honest result

`examples/jev/06-fact-checked-review` pairs a real web search with a Jev call, so a code-review
finding's factual claim gets checked against real reference material instead of the model's own
training-time memory. Iterating on this live surfaced a genuinely useful safety property: once
the agent is instructed to verify the fetched page actually mentions the specific term being
checked (not just a related one), it correctly reports "unsupported, low confidence" rather than
asserting a wrong verdict from an off-topic page. That safety property held across repeated runs.
A clean true-positive catch — the right page found, a real false claim caught outright — wasn't
achieved in this batch; that's an open gap in the example, not a hidden one.

## Enforcing a tool's real answer, not just trusting the agent's summary

By default, an agent that calls a typed-judgment tool has to be trusted to read the result
correctly and act on it — nothing stops it from misreading the number, or asserting success after
the tool call itself failed. `examples/jev/08-enforced-pipeline-gate` demonstrates a mechanical
alternative: a pipeline step can require that a named tool's actual JSON response contain a value
within given bounds, checked in code, before the next step is even allowed to run — independent
of what the calling agent claims. Caught live during testing: an agent whose own tool call had
failed outright still wrote "safe to merge" in its final answer; the mechanical check correctly
refused to trust that and blocked the next step.

## A methodology lesson worth passing on

An earlier version of the with/without comparison in these examples gave the Jev-backed agent a
larger iteration budget than the free-text baseline it was compared against — an unfair
comparison that made the baseline look worse than a fair one showed it to be. Equalizing the
budgets changed what one comparison actually demonstrated, without changing the others' outcomes.
Worth checking for in any similar with/without setup: an unequal resource budget will masquerade
as a capability difference.
