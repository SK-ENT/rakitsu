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
  "Fix the nil-pointer bug in the profile update handler and verify the regression."
```

Review/QA-only request against an existing diff:

```sh
go run ./cmd/rakitsu run examples/jev/05-product-dev-team/config.yaml \
  "Review the existing diff for the profile update handler and run the relevant QA checks."
```

Full feature request:

```sh
go run ./cmd/rakitsu run examples/jev/05-product-dev-team/config.yaml \
  "Add rate limiting to the public search endpoint, including tests and documentation."
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
  it blocked any multi-iteration tool-calling agent on `ollama`. This has since been fixed upstream
  in Rakitsu, independent of this example's design.
- The `jev` tool itself and the `JevGate`/`Triage` typed-question pattern are unaffected — they're
  proven working in `examples/jev/02` and `03`. This example's own Coordinator/spawn wiring and
  the config-only parts (schema, routing prompts, agent/tool declarations) are verified structurally
  sound.

## With vs without Jev

Use `config-no-jev.yaml` for the comparison. It keeps the same agents and routing, but replaces
JevGate with LLMGate and Triage with LLMTriage.
