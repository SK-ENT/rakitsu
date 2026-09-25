# 07 — Severity Score Triage (Jev)

Examples 01–06 all use Jev's `noul` (yes/no) and `choice` (category)
question types. None use `score` — a continuous rating along an ordered
spectrum, the right type when "yes/no" is too coarse and a fixed category
list is too rigid. This example asks Jev a single `score` question to rate
how severe a review finding or incident report is, then maps that number
to a threshold-based action band (`BLOCK` / `WARN` / `INFO`) itself.

**Important, confirmed live: `score`'s range is `0` to
`len(criteria) - 1`, not a fixed 0.0–1.0.** With this example's 3-level
`criteria`, the raw value ranges 0 to 2.0. An earlier version of this
example ignored that and applied 0.0–1.0 thresholds directly to the raw
value, which let a genuinely moderate finding (raw ~0.98) incorrectly
clear a `>= 0.7` BLOCK threshold meant only for the critical end of the
scale.

The fix is **not** "ask the agent to normalize the score before
comparing" — live-tested and that didn't hold up with a small local model
(`llama3.1:8b`): asked to compute `raw / 2` and then compare, it instead
reported the raw ~0.98 value as if it already were the normalized number,
landing in BLOCK anyway. Asking a model to do arithmetic correctly before
using the result is one more thing that can silently go wrong. Instead,
the agent's thresholds are pre-scaled to the raw 0–2 range (`>= 1.4`,
`0.6-1.39`, `< 0.6`) — a plain magnitude comparison against the number
Jev actually returned, no division required.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/07-severity-score-triage/config.yaml \
  "Finding: the /admin/reset-password endpoint does not verify the \
   caller's session before issuing a new password, so any authenticated \
   user can reset any other user's password." \
  --trace
```

Try a low-severity finding too, to see it land in a different band:

```bash
rakitsu run examples/jev/07-severity-score-triage/config.yaml \
  "Finding: a log message uses inconsistent capitalization for the word \
   'Error'." \
  --trace
```

## What's Here

- `SeverityTriage` agent with one tool: `jev` (`type: jev`)
- A single `score`-type question, `severity`, with `criteria` anchors at
  levels 0/1/2 (cosmetic/moderate/critical) and action-band thresholds
  pre-scaled to that raw 0–2.0 range, so the agent only has to compare
  the number it got back, not compute anything from it first
- `config-no-jev.yaml`: the same agent, but scoring severity itself
  instead of calling Jev — for comparison

## Demonstrates

- The `score` question type — Jev's only continuous-output type, not used
  by any other example in this directory
- A real, non-obvious API requirement found only by live-testing: Jev's
  `score` questions require `criteria` as an **ordered list**, not a dict.
  A dict shape (`{ "0.0": "...", "1.0": "..." }`) — which reads as the more
  natural YAML for "value → meaning" and is what this example's `config.yaml`
  first shipped with locally — gets rejected by Jev's API with a `422
  list_type` error. Levels are identified by their **position** in the
  list, not by any field inside each element:
  ```yaml
  criteria: [ "cosmetic, no real consequence",
              "moderate — real but contained impact",
              "critical — data loss, security breach, or outage" ]
  ```
  An earlier version of this example used a list of `{score, description}`
  objects instead of plain strings — that also works (Jev tolerates
  arbitrary fields per element), but it's not TypeSafe's documented
  convention and wrongly implies the `score` field inside each element is
  what sets the level's numeric value, when the actual mapping is by array
  position. Corrected after cross-checking `docs.typesafe.ai/primitives/score.md`.

  Rakitsu's own `jev` tool schema previously declared `criteria` as a bare,
  untyped field for every question type, so this only surfaced at Jev's API
  layer, not at rakitsu's own config-validation or `rakitsu doctor` step.
  `internal/tools/jev/tool.go`'s schema now spells out each
  type's expected shape in the tool's own parameter description, so a
  model building a new `score` question gets this guidance upfront instead
  of discovering it via a live 422.

- A second, separate real gotcha, found later: this
  example originally assumed `score` always returns 0.0–1.0 and applied
  its action-band thresholds directly to the raw value. It doesn't —
  `score`'s range is `0` to `len(criteria) - 1`. Confirmed via live
  testing (a repeated-call check, 5+ calls per
  case): a cosmetic finding scored ~0.04 raw, a genuinely moderate finding
  scored ~0.98 raw, and a critical finding scored ~1.99 raw — all against
  this same 3-level `criteria` list. Un-normalized, the moderate case
  would incorrectly clear a `>= 0.7` BLOCK threshold. A first fix attempt
  (ask the agent to normalize before comparing) was itself live-tested and
  found unreliable with a small local model — see below. The shipped fix
  pre-scales the thresholds instead, so the agent never has to do
  arithmetic on Jev's answer at all.

## With vs without Jev — honest result

Four live runs against `gpt-5.6-luna` via a throwaway CI PR (real API
keys, real GitHub Actions), 2026-09-21:

1. **First run — real bug caught live**: the dict-shaped `criteria` above
   got a genuine `422 Unprocessable Entity` from Jev's API. The model's
   own retry attempt recovered by improvising a list shape that happened
   to work, but relying on an LLM to recover from a malformed call every
   run isn't something to ship — fixed by baking the correct list shape
   into the config directly.
2. **Second run, after the fix**: clean first-try success — `jev` called
   once, `state`/`questions` well-formed, real 414-byte response, correctly
   landing in the `BLOCK` band for a genuine account-takeover finding.
   *Correction: this run's report originally recorded
   "Score: 1.0," which is not consistent with `score`'s actual 0–2 raw
   range for this 3-level criteria list. Direct, repeated re-testing of
   an equivalent critical-severity finding (8 repeated live calls) found a raw score of 1.98–1.99
   (normalized ≈ 0.99), not 1.0 — the original number was likely
   misreported by the calling model rather than a genuine API response.
   The BLOCK verdict itself was still correct; only the specific number
   recorded here was wrong.*

Baseline (`config-no-jev.yaml`, local `llama3.1:8b` via Ollama, same
finding): scored `0.9`, also `BLOCK`, with a correct one-line reason.

**Normalization fix, live-tested twice:** the first
fix attempt asked the agent to normalize the raw score (`raw / 2`) before
comparing to 0.0–1.0 thresholds. Live run against the moderate-VPN-only
finding above, local `llama3.1:8b`: the agent reported "Normalized
severity: 0.98" and `BLOCK` — 0.98 matches the *raw* score independently
measured for this exact finding with repeated live calls
(0.97–0.98), meaning the model most likely never actually divided,
just relabeled the raw number. The second fix (pre-scaled thresholds,
no division asked of the model) is the one shipped in `config.yaml`.

**Takeaway**: for a single, unambiguous finding like this one, both paths
land on the same correct action band — consistent with 01's finding that
Jev's edge isn't raw judgment quality with a capable model, it's a
*guaranteed parseable number* your code can threshold on directly, instead
of parsing prose or trusting a free-text response to include one. The
`criteria`-shape gotcha above is the more interesting result of this
round of testing: even a `type: jev` tool that validates cleanly against
rakitsu's own schema can still get a runtime 422 from Jev's actual API if
the config's shape doesn't match what the backend expects — worth
live-testing any new question shape before trusting `rakitsu doctor`
alone.

## Note

The Jev check is a judgment aid the agent is instructed to consult — it's
not a hard runtime gate enforced by Rakitsu itself (an agent could still
ignore the instruction).
