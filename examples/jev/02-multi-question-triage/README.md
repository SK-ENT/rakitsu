# 02 — Multi-Question Triage (Jev)

An agent that reviews a code diff or PR description supplied as the user query.
It asks Jev four independent typed questions in one call: security/authentication
impact, secret leakage, breaking API impact, and overall risk category.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/02-multi-question-triage/config.yaml \
  "PR: remove the legacy /v1/users endpoint and add a bearer-token parser" \
  --trace
```

Run the same query against the baseline:

```bash
rakitsu run examples/jev/02-multi-question-triage/config-no-jev.yaml \
  "PR: remove the legacy /v1/users endpoint and add a bearer-token parser" \
  --trace
```

## What's Here

- `ChangeTriage` calls `jev` once with four typed questions.
- The three binary questions use `noul`; `overall_risk` uses `choice` with
  `safe`, `needs-review`, and `blocking` criteria.
- `config-no-jev.yaml` asks the main LLM the same four questions in one
  free-text prompt.

## With vs without Jev

Run against `qwen3-coder:30b` locally with a PR description that logs a raw
bearer token (a real security issue):

- **With Jev**: correctly flagged "blocking" — called out the token-logging
  vulnerability specifically. Took 9.4s.
- **Without Jev** (one free-text prompt asking the same 4 questions):
  also correctly identified all four points, including the same blocking
  verdict, walking through each question explicitly in prose. Took 2.9s —
  faster, since it skipped the extra Jev round trip.

**Takeaway**: with a strong model, both approaches reached the same correct
answer; the free-text version was faster here. Jev's real edge in this
scenario isn't raw judgment quality — it's a *guaranteed* parseable answer
per question (four separate typed fields you can act on directly in code)
versus prose you'd need to parse or trust the model to structure
correctly every time. For a human-facing summary, free text is fine; for
code that branches on "is overall_risk == blocking", Jev's structured
answer is safer to depend on.

### 2026-09-21 update — `llama3.1:8b`, neither path actually fired

Re-ran with the endpoint-removal PR prompt from this README against
`llama3.1:8b` (the config's own default model). Checked the `--trace`
output for real tool-call markers (`⚡`/`✓`/`✗`) in both variants: neither
made one. Both variants printed a well-formed tool-call JSON block (correct
schema, correct field names) as their *final text answer* instead of it
being dispatched as an actual function call — `jev`'s in the with-Jev run,
`run_command`'s (a `git diff --stat`) in the no-Jev run. Same failure
shape in both, so this run didn't actually compare Jev's judgment against
the model's own — it compared two different ways the same model failed to
invoke a tool at all.

Replaying the exact `state`/`questions` from this example directly against
Jev's API shows what the gate would have said, had it fired:
`touches_security: 0.94`, `leaks_secret: 0.09`, `breaking_api: 0.85`,
`overall_risk: needs-review (0.94)` — a reasonable "flag for human review,
don't auto-block" verdict for an endpoint removal plus a new auth parser.

**Takeaway**: `llama3.1:8b`'s tool-calling reliability, not Jev's judgment
quality, was the bottleneck on this run. Don't read "the run finished
quickly with rc=0" as "the comparison happened" — check for real `⚡ jev`
execution markers first.

### 2026-09-21 correction — the baseline had 1/3 the iteration budget

`config-no-jev.yaml`'s `ChangeTriage` was configured with `max_iterations: 1` versus
`config.yaml`'s `max_iterations: 3` for the same agent role — an unfair comparison, not a Jev
finding. In one run of this pair (a second re-run, not the one above), the no-jev version spent
its single iteration on a `run_command` call, had zero budget left to answer, and Rakitsu's
forced-synthesis fallback then itself failed — producing nothing at all. That looked like "Jev
wins," but the real cause was the budget mismatch. Fixed (both now `max_iterations: 3`) — see
[docs/JEV-BENCHMARKS.md](../../../docs/JEV-BENCHMARKS.md)'s
"methodology lesson" section for the corrected result: with matched budgets, `ChangeTriage`
(no-jev) still didn't produce the requested four-point review — it described a `run_command` call
it never made instead — while the with-jev version produced a real, on-topic review despite its
own `jev` call also failing. That gap is a genuine, budget-independent finding for this model and
prompt, unlike the original asymmetric-budget result above.
