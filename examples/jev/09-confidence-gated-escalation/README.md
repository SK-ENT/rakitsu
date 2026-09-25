# 09 — Confidence-Gated Escalation (Jev)

Examples 01-08 use a Jev verdict as either advice (act on it) or a hard
pass/fail gate (03, 08). None of them use Jev's own **confidence** as a
routing signal, and none exercise rakitsu's `user_input` tool — an agent
pausing mid-run to ask a real person, a capability the codebase already
has but no other jev example touches.

This example combines both, following [TypeSafe's confidence-routing
pattern](https://docs.typesafe.ai/patterns/confidence-routing.md):
stakes-scaled thresholds decide whether the agent acts automatically or
asks a human, and anything genuinely uncertain escalates no matter what
category it falls in.

## Why this needs a live terminal, unlike 01-08

`user_input` only registers when the agent runs in interactive chat mode
(`cmd/rakitsu/runtime.go`), not for a plain one-shot `rakitsu run <config>
"<query>"`. Every other jev example runs one-shot and can be scripted
against a throwaway CI PR; this one can't, the same way.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/09-confidence-gated-escalation/config.yaml -i
> Delete all files matching *.log in the current directory
```

Expect: `jev` gets called with a `choice` question (`read`/`write`/`destroy`),
and because this is a destructive command, the policy calls `user_input` to
confirm before running it — regardless of how confident Jev was.

Try a clearly low-stakes prompt too:

```bash
> List the files in the current directory
```

Expect: high-confidence `read` classification, no escalation, runs directly.

## What's here

- `SafeShell` agent with `run_command` (`type: cli`) and `jev` (`type: jev`)
- `interactive: true` — required for `user_input` to register at all
- A stakes-scaled policy in the system prompt: `read` needs ≥0.6 confidence
  to auto-run, `write` needs ≥0.75, `destroy` always escalates regardless
  of confidence, and anything below the 0.6 floor escalates regardless of
  category — mirroring the cookbook's own low-stakes/high-stakes split
- An explicit instruction for what to do if the `jev` call itself fails
  (treat it as maximum uncertainty, escalate, never guess or claim a
  category you don't have) — the same class of gap [08's enforced
  pipeline gate](../08-enforced-pipeline-gate) exists to catch mechanically,
  handled here in policy instead since there's no pipeline step boundary
  to gate on
- `config-no-jev.yaml`: same policy, but the model reports its own
  category and confidence instead of calling Jev — see below for why that,
  not "no escalation at all," is the fair comparison

## With vs without Jev — what's actually being compared

`user_input` is available on both sides of this comparison. The variable
under test is *where the confidence number comes from* — a typed API call
with a real probability distribution behind it, vs. a model self-reporting
a number in free text — not whether escalation exists. An earlier version
of this project's own benchmark work found that giving one side of a
comparison a different capability than the other (not just a different
confidence source) produces a misleading result; see `docs/JEV-BENCHMARKS.md`.

## Design note — the confidence floor applies before the category split

The 0.6 floor check runs first, before the category-specific thresholds.
A `read`-classified command with 0.55 confidence escalates on the floor
check, not because 0.55 < 0.6's own `read` threshold (they're the same
number here, but they don't have to be — the floor is meant to catch "the
model isn't sure what this even is," which is a different failure mode
than "the model is sure, but the stakes need more certainty than that").

## Live verification — what was actually tested, honestly

Verifying this required scripting the interactive TUI headlessly (a pty
harness, `llama3.1:8b`, no `TYPESAFE_API_KEY` set), not a throwaway CI PR
like 06-08 — see the note above on why this example can't run one-shot.

**Confirmed live**: the `jev` call failure path. With no API key set, the
agent's own `jev` call correctly failed (`✗ Jev API key is missing`), and
the agent followed the policy exactly as written — it did not guess a
category or invent a confidence number. It triggered `user_input` with:
*"Jev call failed. Jev's category was: None, confidence was: None. What
do you want to do about the command 'Delete all files matching *.log in
the current directory'?"* That's the same class of honesty this project's
[08 example](../08-enforced-pipeline-gate) caught an agent failing at —
here it held, in policy rather than a mechanical gate.

**Not yet verified live in this session**: the auto-run path (real Jev
API response, `read`/`write` above its confidence threshold, running
without asking), the full escalation round-trip (a human actually
answering `user_input` and the agent then acting on that answer), and the
`destroy`-always-escalates path with a real (non-failed) Jev response.
These need a real `TYPESAFE_API_KEY` and either a longer scripted
harness or a manual interactive session — flagged here rather than
assumed, the same way example 05's own gap was documented rather than
glossed over.

## Note

Like every other jev example, this is a judgment aid the agent is
instructed to follow — not a hard runtime gate enforced by rakitsu itself.
See [08-enforced-pipeline-gate](../08-enforced-pipeline-gate) for a
mechanical alternative in contexts that have a pipeline step boundary to
gate on; a single-agent interactive session like this one doesn't.
