# 02 — Multi-Question Triage (Jev)

An agent that reviews a code diff or PR description supplied as the user query.
It asks Jev four independent typed questions in one call: security/authentication
impact, secret leakage, breaking API impact, and overall risk category.

## Run

```bash
export TYPESAFE_API_KEY=...
rakitsu run examples/jev/02-multi-question-triage/config.yaml \
  "PR: remove the legacy /v1/users endpoint and add a bearer-token parser"
```

Run the same query against the baseline:

```bash
rakitsu run examples/jev/02-multi-question-triage/config-no-jev.yaml \
  "PR: remove the legacy /v1/users endpoint and add a bearer-token parser"
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
