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

## What's here

A two-step pipeline:
1. `safety-check` — `SafetyChecker` calls `jev` once with a `noul` question
   ("is this change safe to merge?"). The step is gated:
   ```yaml
   require_tool_call:
     tool: jev
     output_json_path: answers.is_safe_to_merge.noul
     min_value: 0.5
   ```
2. `merge` — a stub `Merger` agent that only runs **if step 1 passed**.
   `rakitsu`'s pipeline aborts the whole run on a step error
   (`internal/agent/pipeline.go`'s `runPipeline`), so an unsatisfied gate
   means `merge` never executes — not "executes but is told not to."

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

**Live proof (non-deterministic model, real Jev API — see
`docs/JEV-BENCHMARKS.md`):** the local sanity run below (no `TYPESAFE_API_KEY` set, so the `jev` call
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
the genuinely unsafe finding above, against `gpt-4o-mini`) are summarized
in `docs/JEV-BENCHMARKS.md` alongside this example's live-CI verification.

## Design note

`output_json_path` is a plain dot-path into the tool's JSON response
(`internal/agent/pipeline.go`'s `extractJSONPathFloat`) — no array-index
support, since every System One answer shape
(`answers.<question>.<field>`) is object-keyed all the way down. It isn't
Jev-specific: any tool whose output is JSON with a numeric field at a
known path can be gated this way, matching rakitsu's usual "the pipeline
mechanism doesn't know about specific tools" convention (`require_tool_call`
itself predates the `jev` tool type).
