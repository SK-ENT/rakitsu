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

## Repeated-call variance — real numbers, not just a verdict

A community suggestion (on this project's own show-and-tell post) was to stop trusting a single
Jev call and instead run the same question several times, checking the spread across answers, not
just the average. Ran that against real cases already covered above, 5-8 repeated calls each,
real `TYPESAFE_API_KEY`:

| Case | Type | Values across repeated calls | Mean | Stdev |
|---|---|---|---|---|
| Clear-cut destructive shell command | noul | 0.89, 0.89, 0.90, 0.90, 0.89, 0.90, 0.89, 0.89 | 0.894 | 0.005 |
| Grounded fact-check, false claim | noul | 0.06, 0.06, 0.06, 0.06, 0.06, 0.06, 0.06, 0.05 | 0.059 | 0.004 |
| Critical severity finding | score | 1.99, 1.99, 1.99, 1.99, 1.99, 1.99, 1.98, 1.99 | 1.989 | 0.004 |
| Cosmetic severity finding | score | 0.05, 0.05, 0.03, 0.03, 0.04 | 0.040 | 0.010 |
| Moderate severity finding | score | 0.98, 0.98, 0.98, 0.98, 0.97 | 0.978 | 0.005 |

Every case was extremely stable — repeated identical calls essentially agree with each other
(stdev ≤ 0.01 in every case tested). The concern this was meant to check (a noisy average hiding
real disagreement) didn't materialize anywhere here.

## What actually breaks — probed deliberately, not found by accident

Once reliability itself looked solid, the more useful question became: what's Jev's real
capability boundary? A second round of testing, on cases chosen specifically to probe edge
behavior rather than re-confirm the examples above, found two genuine limits:

**Doesn't hedge on contested claims.** Asked whether "TDD is the correct default, teams that skip
it are cutting corners" is a widely-agreed best practice — a claim reasonable people actually
disagree about — Jev gave a confident 0.23-0.24 across 6 repeated calls, not a value near 0.5.
It commits to a side rather than signaling genuine debate.

**Doesn't reliably notice when the evidence it's given doesn't address the question.** Given the
correct grounding text, a noul question about a Go build-tag claim answered confidently and
correctly (0.05-0.06). Given the exact same claim but completely unrelated reference material (web
framework documentation, nothing to do with the claim), it did not hedge toward 0.5 — it returned
0.34-0.42, a noisier but still opinion-shaped answer, closer to what looks like a training-data
prior leaking through than a genuine "I can't determine this." An explicit "no reference material
found" statement, by contrast, correctly produced 0.47-0.50 — real hedging. The difference matters:
Jev will not catch a bad search result on its own; the calling code has to verify relevance before
trusting the answer.

On the positive side: it stayed stable even with the real fact buried inside a realistic, noisy
page-scrape (nav links, unrelated release notes, footer text) rather than a hand-cleaned paragraph
— 0.04 across 5 calls, stdev 0.0000, same direction and even tighter than the clean-text version.

## Two real example bugs, found by this testing

**A wrong assumption about `score`'s range.** One example's action-band thresholds assumed
Jev's `score` type always returns 0.0-1.0. It doesn't — the range scales with how many criteria
levels you give it (`0` to `len(criteria)-1`). With 3 levels, a genuinely moderate case scored
~0.98 raw and silently cleared a `>=0.7` "critical" threshold meant only for the top of the scale.
The first fix attempt asked the agent to normalize the value itself before comparing — live-tested
against a small local model, that didn't hold up: the model reported the raw ~0.98 as if it were
already normalized, skipping the actual division. The fix that stuck: pre-scale the thresholds
to match the raw range, so the model only ever compares the number it got back, with no arithmetic
step to get wrong.

**A compound question hiding real information.** A different example asked a single yes/no
question bundling two separate concerns together ("safe to merge, with no security *or*
correctness concerns?"). On a real test case (a rename that left call sites broken, with no actual
security issue), the compound question gave an opaque "0.03, not safe" — correct, but silent on
*why*. Splitting it into two separate questions on the identical input gave
`security_concern = 0.16` (correctly low) and `correctness_concern = 0.97` (correctly high) —
strictly more useful for the same underlying facts, and it now identifies specifically which
concern failed instead of just that something did.

## A methodology lesson worth passing on

An earlier version of the with/without comparison in these examples gave the Jev-backed agent a
larger iteration budget than the free-text baseline it was compared against — an unfair
comparison that made the baseline look worse than a fair one showed it to be. Equalizing the
budgets changed what one comparison actually demonstrated, without changing the others' outcomes.
Worth checking for in any similar with/without setup: an unequal resource budget will masquerade
as a capability difference.
