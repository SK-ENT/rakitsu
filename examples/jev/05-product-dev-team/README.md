# Product Development Team

This example defines a seven-agent product-development team coordinated through
dynamic `spawn_agent` calls. The Coordinator is the only top-level agent; there is
no forced pipeline or `orchestrator:` block. It chooses the smallest useful set of
specialists for the user's request.

The agents are:

- Planner — writes a short implementation plan for a new feature.
- Developer — implements, fixes, or revises work.
- Verifier — runs tests and commands and reports concrete pass/fail evidence.
- JevGate — uses one typed Jev choice to emit exactly `PASS` or `FAIL`.
- Triage — uses three typed Jev questions in one call to classify a change.
- Reviewer — reviews the change and emits `APPROVE` or `REQUEST_CHANGES`.
- Coordinator — decides which specialists to spawn and passes their reports onward.

The Coordinator's routing is request-driven. Bug fixes use Developer → Verifier →
JevGate, retrying Developer with the gate feedback for at most three rounds. A
review or QA-only request uses Triage → Reviewer. A full feature request uses
Planner first, then the implementation/verification/gate loop, followed by Triage
and Reviewer.

## Example runs

Bug-fix-only request:

```sh
go run ./cmd/rakitsu run examples/jev/05-product-dev-team/config.yaml \
  "Fix the nil-pointer bug in the profile update handler and verify the regression." \
  --trace
```

Review/QA-only request against an existing diff:

```sh
go run ./cmd/rakitsu run examples/jev/05-product-dev-team/config.yaml \
  "Review the existing diff for the profile update handler and run the relevant QA checks." \
  --trace
```

Full feature request:

```sh
go run ./cmd/rakitsu run examples/jev/05-product-dev-team/config.yaml \
  "Add rate limiting to the public search endpoint, including tests and documentation." \
  --trace
```

## Verification status

- `go build ./cmd/rakitsu/...` — passes.
- `rakitsu doctor` on both `config.yaml` and `config-no-jev.yaml` — passes (originally shipped with
  `model: llama3.1:8b`, which wasn't pulled locally; switched to `qwen3-coder:30b`, the model used
  throughout `examples/jev/01`-`04`).
- **Full end-to-end live run originally hit a pre-existing, unrelated bug**, not caused by anything
  in this config: the `ollama` provider 400d with `invalid message content type: <nil>` as soon as
  the Coordinator's spawned `Developer`/`Planner` agent had a tool-call-only turn (no text) followed
  by a second tool-calling turn — `go-openai`'s `omitempty` on `Content` dropped the field entirely,
  which Ollama's OpenAI-compat endpoint rejected instead of treating as empty text. Not Jev-specific;
  it blocked any multi-iteration tool-calling agent on `ollama`.
  - This was claimed fixed upstream as of the note above, but that fix only covered the
    *assistant* tool-call-only-turn shape. **2026-09-21: re-running this example live reproduced the
    identical error** via a second, unpatched instance of the same gap — a *tool-response* message
    with a legitimately empty result (a shell command producing 0 bytes of stdout) hit the same
    `omitempty`-drops-the-content-key issue one branch earlier in `convertMessage`, before the
    earlier fix would apply. Filed and fixed; re-verified live afterward — the same 0-byte tool
    result now occurs without breaking the next turn. Both branches of this gap are fixed now, not
    just the one originally covered.
- The `jev` tool itself and the `JevGate`/`Triage` typed-question pattern are unaffected — they're
  proven working in `examples/jev/02` and `03`. This example's own Coordinator/spawn wiring and
  the config-only parts (schema, routing prompts, agent/tool declarations) are verified structurally
  sound.

## With vs without Jev

Use `config-no-jev.yaml` for the comparison. It keeps the same agents and routing, but replaces
JevGate with LLMGate and Triage with LLMTriage.

### 2026-09-21 update

Ran the bug-fix request ("Fix the nil-pointer bug in the profile update handler and verify the
regression.") against both `config.yaml` and `config-no-jev.yaml` with `llama3.1:8b` and
`--timeout 600`. Both variants hit `max_iterations` before finishing and returned a partial
result — `with-jev` stopped mid-exploration ("Let me look for handler or controller files..."),
`no-jev` similarly ("Let me try a different approach to explore the project structure..."). This
example's default iteration budget is too low for `llama3.1:8b` to complete the full
plan→implement→verify→gate loop this scenario is built to exercise; neither variant reached the
point where `JevGate`/`Triage` or their `LLMGate`/`LLMTriage` counterparts would actually run, so
this didn't produce a with/without comparison at all — just confirmation that the iteration
budget, not Jev, was the limiting factor here. Worth a follow-up run with a higher
`max_iterations` or a stronger model before drawing any conclusion about this example's own
Jev-vs-no-Jev behavior.

### 2026-09-21 correction — a second unfair budget, plus a re-verification

`LLMGate` and `LLMTriage` (no-jev) each had `max_iterations: 1` versus `JevGate`'s 2 and
`Triage`'s 3 — same class of bug as examples 02/03, fixed to match. Re-ran both variants with the
also-just-fixed omitempty bug (see "Verification status" above) resolved and the budgets
equalized: both still hit `max_iterations` on the `Planner` agent, unchanged from before. The
budget fix had no effect on
05's outcome, because 05's actual bottleneck sits upstream of the agents it touched — `Planner`
never gets far enough to reach `JevGate`/`LLMGate` or `Triage`/`LLMTriage` either way.
