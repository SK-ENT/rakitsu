# Eval — Token-Split Comparison

Matched-pair eval for the hypothesis:
**"Splitting work across multiple specialist agents saves context-window tokens vs a single agent doing everything."**

Both arms use the **same task**, the **same tools**, the **same model**, and the **same sampling**. The only difference is orchestration.

- `single-agent/` — one `Auditor` agent does the whole Go + Vue audit in a single ReAct loop
- `multi-agent/` — `Pipeline` orchestrator: `BackendAuditor → FrontendAuditor → Synthesizer`

## Run

`source .env` loads `LITELLM_BASE_URL` and `LITELLM_API_KEY` (local vLLM
recommended):

```bash
cd <repo root>
source .env

Q='Audit this repository: summarize the Go backend (internal/) in one paragraph, then the Vue frontend (web/src/) in one paragraph.'

./bin/rakitsu run examples/eval/token-split/single-agent/config.yaml  "$Q" --no-hub --timeout 900
./bin/rakitsu run examples/eval/token-split/multi-agent/config.yaml  "$Q" --no-hub --timeout 900
```

Session JSONLs land in `~/.rakitsu/sessions/`. Parse `TOKEN_USAGE` events' `token_usage.input_tokens` / `output_tokens` fields to aggregate. The fields live at the **top level** of each event, not inside `payload`.

Extraction one-liner:

```bash
jq -r 'select(.event_type=="TOKEN_USAGE") | [.agent_name, .token_usage.input_tokens, .token_usage.output_tokens, .token_usage.total_tokens] | @tsv' \
  ~/.rakitsu/sessions/<id>.jsonl
```

## Result from 2026-04-19 run (your-model-alias, 16K context)

| arm | LLM calls | input tok | output tok | total tok | peak single-call input | wall-clock |
|---|---|---|---|---|---|---|
| single-agent | 3 | **4,976** | 3,757 | **8,733** | 3,359 | 1m45s |
| multi-agent  | 7 | 13,522 | 4,238 | 17,760 | 5,567 | 1m26s |
| delta        | +133% | +172% | +13% | +103% | +66% | -18% |

**For this task size, splitting cost ~2× more input tokens**, not fewer. The split-context hypothesis assumes each sub-agent's context is smaller than the monolithic one — but with `Pipeline` passing prior-step outputs forward, and each worker re-loading its own system prompt, the sum grows.

### When multi-agent WOULD win on tokens (not this task)

1. **Task exceeds single agent's context window.** Subdomain-split caps each worker at the subset relevant to it — true savings when a single agent would otherwise spill past the model's context limit.
2. **Task truly parallelizable.** Wall-clock savings possible even when token totals are worse (this eval's multi-agent arm already finished 18% faster despite more tokens, and it's sequential — parallel steps would widen that gap).
3. **Model heterogeneity.** Cheap model for bulk legwork, expensive model for synthesis; token counts then translate to different dollar costs.
4. **Specialized toolsets.** One agent can't carry security + formatting + doc tools all at once without prompt bloat.

For generic "summarize this codebase" on 16K context — **single agent is the cheaper choice**.

## Caveats

- **Synthesizer did not emit TOKEN_USAGE** in the 2026-04-19 multi-agent run. Either the step short-circuited or emission is not instrumented for that path. Multi-agent totals above are **without** Synthesizer cost; actual multi-agent cost is strictly higher than shown.
- **Single-agent run was small enough to avoid the reasoning-only-output fallback** (see [docs/providers/reasoning-models.md](../../../docs/providers/reasoning-models.md)) on nemotron. Larger-scope audits would likely push the single agent into that fallback long before hitting the 16K wall, at which point multi-agent wins by completing at all rather than by token economy.
- **Only one run per arm.** No variance data. For a rigorous result, run each arm 3-5× and report mean ± stdev.

## Next steps (if you want a rigorous publishable result)

1. Run each arm 3× for variance.
2. Add a large-scope task (e.g. "audit all 8 examples/ configs") to force the single agent past context and confirm multi-agent wins there.
3. Compare against a non-reasoning model (e.g. qwen3-8b or gpt-4o-mini) to separate the reasoning-only-output confound from orchestration efficiency.
4. Write up the results with charts.
