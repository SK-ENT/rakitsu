# 08 — Enforced Pipeline Gate (Jev)

Examples 01–07 all treat Jev's verdict as a judgment aid: an agent is
instructed to call it and act on the answer, but nothing actually stops the
agent from ignoring that instruction or misreporting what Jev said. 07's
README says this plainly: *"the Jev check is a judgment aid the agent is
instructed to consult — it's not a hard runtime gate enforced by Rakitsu
itself (an agent could still ignore the instruction)."*

This example closes that gap using rakitsu's own `Pipeline` orchestrator,
not a bigger prompt. `require_tool_call` already mechanically verifies a
step's agent invoked a named tool, instead of trusting the agent's
self-report (see 03). It can also check the tool's **actual
JSON response**, not just that it was called: `output_json_path` +
`min_value`/`max_value` extract a value from the tool's real output and
gate the step on it directly.

**Two atomic gates, not one compound gate.**
An earlier version of this example asked a single compound noul question
("safe to merge, with no security **or** correctness concerns?"). Live
testing confirmed this is exactly the compound-question anti-pattern
TypeSafe's own docs warn against — the same real case (a rename that left
3 call sites broken, no security issue) gave an opaque "0.03, not safe"
from the compound question, versus `security_concern=0.16` (correctly
low) and `correctness_concern=0.97` (correctly high) from two atomic
questions on the identical state. Splitting the question is strictly more
informative for the same underlying facts, so the example now uses two
separate agents, two separate `jev` calls, and two separate gated
pipeline steps.

## What's here

A three-step pipeline:
1. `security-check` — `SecurityChecker` calls `jev` once with a `noul`
   question ("does this introduce a security concern?"). Gated:
   ```yaml
   require_tool_call:
     tool: jev
     output_json_path: answers.security_concern.noul
     max_value: 0.5
   ```
2. `correctness-check` — `CorrectnessChecker` calls `jev` once with a
   separate `noul` question ("does this break functionality?"). Gated the
   same way on `answers.correctness_concern.noul`.
3. `merge` — a stub `Merger` agent that only runs **if both prior steps
   passed**. `rakitsu`'s pipeline aborts the whole run on a step error
   (`internal/agent/pipeline.go`'s `runPipeline`), so an unsatisfied gate
   on either step means `merge` never executes — not "executes but is
   told not to."

## Run

```bash
export TYPESAFE_API_KEY=...
# Safe change — gate passes, both steps run, ends with "Merged."
rakitsu run examples/jev/08-enforced-pipeline-gate/config.yaml \
  "Change: renamed a private helper function for clarity, no behavior change." \
  --trace

# Unsafe change — gate fails, pipeline aborts before "merge" runs
rakitsu run examples/jev/08-enforced-pipeline-gate/config.yaml \
  "Change: the /admin/reset-password endpoint does not verify the \
   caller's session before issuing a new password, so any authenticated \
   user can reset any other user's password." \
  --trace
```

## Proof this is a real improvement, not a bigger prompt

**Mechanism proof (deterministic, in the test suite):**
`internal/agent/pipeline_test.go`'s
`TestExecuteStep_RequireToolCall_ValueBound_NotSatisfied_OverridesSelfReport`
scripts an agent that calls `jev`, gets back a real-shaped response scoring
`0.2` (well under the `0.5` threshold), and then — deliberately, to
reproduce the exact failure mode this feature exists to catch — has the
agent's own final answer claim `BLOCK` (i.e. "safe/passing") anyway. The
gate still fails the step. Reproducible directly:
```bash
go test ./internal/agent/... -run RequireToolCall_ValueBound -v
```

**Live proof (non-deterministic model, real Jev API, this session's own
throwaway-CI-PR convention — see `docs/JEV-BENCHMARKS.md`):** the trace
below is preserved as originally captured, against this example's earlier
single-compound-question design (`SafetyChecker`, `is_safe_to_merge`,
`safety-check`) — it's genuine evidence and rewriting it to match the
current split design would misrepresent what was actually observed. The
underlying mechanism it proves is unchanged by the split: a gate that
checks the tool's real output value, not the agent's self-report, still
applies identically to each of the two atomic gates now in place.

The local sanity run below (no `TYPESAFE_API_KEY` set, so the `jev` call
itself failed) turned out to be a stronger real-world case than a staged
one — the agent's own final answer claimed *"The change is safe to merge
as-is, with no security or correctness concerns"* immediately after its own
tool call had errored with `Jev API key is missing`. The gate correctly
refused to trust that unsupported claim:
```
[+5.7s]  ⚡ jev(questions: map[is_safe_to_merge:map[...
[+5.7s]  ✗ jev failed (0ms): Jev API key is missing (set TYPESAFE_API_KEY)
...
[+6.2s]  · The change is safe to merge as-is, with no security or correctness concerns that would require blocking it.
[+6.2s]  ◀ Agent "SafetyChecker" done (success, 2 iter, 777 tokens)
[+6.2s]  ▪ Step "safety-check" done (error, 6.2s)
[+6.2s] ◀ Pipeline done (error)
Error: orchestrator "GatedMergePipeline" execution failed: pipeline level
failed: step "safety-check" required "jev"'s "answers.is_safe_to_merge.noul"
value within bounds, no matching call's output satisfied it
```
`merge` never ran. Without `output_json_path`/`min_value` (i.e. 07's older
`require_tool_call` shape, which only checks that `jev` was *called*), this
exact case would have passed the gate — the tool call happened, its
arguments were fine, and the old check stops there. The new check is what
actually catches it.

Two further runs against Jev's real API (a genuinely low-risk change and
the genuinely unsafe finding above, via a throwaway CI PR against
`gpt-4o-mini`) are recorded in `docs/JEV-BENCHMARKS.md`
alongside this example's live-CI verification — also from before the
split, same caveat as above.

## Design note

`output_json_path` is a plain dot-path into the tool's JSON response
(`internal/agent/pipeline.go`'s `extractJSONPathFloat`) — no array-index
support, since every System One answer shape
(`answers.<question>.<field>`) is object-keyed all the way down. It isn't
Jev-specific: any tool whose output is JSON with a numeric field at a
known path can be gated this way, matching rakitsu's usual "the pipeline
mechanism doesn't know about specific tools" convention (`require_tool_call`
itself predates the `jev` tool type).
