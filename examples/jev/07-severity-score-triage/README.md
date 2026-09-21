# 07 — Severity Score Triage (Jev)

Examples 01–06 all use Jev's `noul` (yes/no) and `choice` (category)
question types. None use `score` — a continuous 0.0–1.0 rating, the right
type when "yes/no" is too coarse and a fixed category list is too rigid.
This example asks Jev a single `score` question to rate how severe a
review finding or incident report is, then maps that number to a
threshold-based action band (`BLOCK` / `WARN` / `INFO`) itself.

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
  0.0/0.5/1.0 to calibrate what the number means
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
  Fixed: `internal/tools/jev/tool.go`'s schema now spells out each
  type's expected shape in the tool's own parameter description, so a
  model building a new `score` question gets this guidance upfront instead
  of discovering it via a live 422.

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
   once, `state`/`questions` well-formed, real 414-byte response. Score:
   `1.0`. Action band: `BLOCK`. Correct verdict for a genuine
   account-takeover finding.

Baseline (`config-no-jev.yaml`, local `llama3.1:8b` via Ollama, same
finding): scored `0.9`, also `BLOCK`, with a correct one-line reason.

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
ignore the instruction). See `examples/jev/08-enforced-pipeline-gate` for
a mechanical alternative that doesn't depend on the agent's cooperation.
