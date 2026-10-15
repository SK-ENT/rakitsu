# Rakitsu Use Cases

Where Rakitsu earns its place in your workflow. This document is written for the
2026+ landscape, where agentic coding editors can read logs and propose fixes
on their own. We focus on what Rakitsu does that those tools *can't*
replicate — not on a feature inventory.

---

## 1. Run multi-agent systems with a clean execution contract

**Who it's for**: Anyone building agent systems that need to be inspected, replayed, or audited.

Every Rakitsu run produces a complete, structured event stream at
`~/.rakitsu/sessions/<uuid>.jsonl`. Tool calls, agent boundaries, errors,
delegations, retries — all recorded with stable schemas.

Why this matters in 2026: your agentic editor *is* your debugger. Claude Code
can `cat` the JSONL, correlate across runs, propose YAML changes, and verify
the fix — in seconds, with no UI clicks. Rakitsu's job is to make the data
clean enough that the agent doesn't have to guess.

**Durable advantage**: a stable, well-documented JSONL contract is more
valuable than any specific frontend. The frontend can be regenerated; the data
schema is what your tooling depends on.

---

## 2. Intervene in a running agent (live runtime control)

**Who it's for**: Developers iterating on prompts, tool wiring, or supervisor
behavior in long-running multi-agent runs — *and* for agentic editors that
want to drive the runtime programmatically.

Rakitsu exposes a complete debug HTTP API:

| Endpoint | Purpose |
|---|---|
| `POST /api/debug/attach`, `/detach` | Begin/end a debug session against a live run |
| `GET/POST /api/debug/breakpoints` | List or set breakpoints at agent / step boundaries |
| `POST /api/debug/pause`, `/resume` | Halt and continue a running session |
| `POST/DELETE /api/debug/params` | Override prompt, model settings, iteration limit, or enabled tools mid-flight |
| `GET /api/debug/state` | Inspect current execution state |
| `POST /api/debug/replay`, `/rerun` | Reconstruct history; re-run from a named step |
| `POST /api/debug/export` | Return the requested agent's parameter overrides as JSON |
| `POST /api/debug/user_input` | Answer a pending `user_input` request in a chat session |

The Vue UI is one client of this API. Your agentic editor is another. Claude
Code can subscribe to the SSE event stream, notice a supervisor about to
loop, `POST /api/debug/pause`, swap the system prompt via `/params`, and
`POST /resume` — all without a human clicking anything.

**Durable advantage**: the API contract, not the UI. Rakitsu is one of the
few agent runtimes that exposes runtime control as HTTP — most frameworks
expose only logs and a config file.

**Use it when**: a 5-minute multi-agent run is about to fail, and you (or
your editor) want to swap the supervisor's prompt mid-flight instead of
restarting from scratch.

---

## 3. See your agents think (onboarding & comprehension)

**Who it's for**: Newcomers learning how YAML config maps to runtime behavior;
demos; teaching multi-agent patterns.

Rakitsu's visual debugger renders the live execution as a tree, graph, or
timeline. Watching a `Hierarchical` orchestrator delegate to workers, or a
`Pipeline` fan out and converge, builds intuition that reading 200 JSONL
events does not.

We are honest about the trajectory: with cheap tokens, agentic editors can
*generate* visualizations on demand from the JSONL — mermaid graphs, animated
SVG, whatever the user asks for. The built-in visualizer's role narrows over
time toward onboarding and demos rather than power-user analysis.

**Use it when**: showing Rakitsu to someone for the first time, or building
intuition about a new orchestrator pattern.

---

## What Rakitsu is *not* trying to be

- **Not a log viewer competing with `jq` + an LLM.** Post-mortem bug-hunting
  through a UI tree is slower and lossier than agentic JSONL analysis. Use
  the JSONL directly with your editor of choice.
- **Not a closed runtime.** The session format is stable, documented, and
  designed to be consumed by external tooling. We treat your agentic editor
  as a first-class debugger client, not a competitor.

---

## Positioning summary

> Rakitsu is the agent runtime your agentic editor debugs through.

Two contracts carry the value:

1. **Session JSONL** — clean, stable, agent-readable post-mortem data.
2. **Debug HTTP API** — live attach / pause / params / resume, callable by
   humans (via the Vue UI) or by agents (via HTTP).

The visual debugger is one client of those contracts. Claude Code is another.
Both are first-class. The protocol is the product; the UI is a bonus for the
30-second demo and the first-week learning curve.

---

## Appendix: pluggable runtime targets (optional extension)

Rakitsu's primary runtime is the Rakitsu binary itself — the one all
three use cases above run on. For teams that want to ship the same
agent config to a different runtime, the `RuntimeTarget` interface
plugs alternative export formats in:

```bash
rakitsu export --format nemoclaw    # NVIDIA NemoClaw sandbox + agent config
rakitsu export --format openclaw    # Raw OpenClaw JSON5
```

These are downstream extensions, not the product. Rakitsu builds and
debugs the agent; where the agent eventually ships is a separate
concern. NemoClaw and OpenClaw happen to be the two formats currently
implemented — adding another is a `RuntimeTarget` implementation, not
a fork. See [Export Verification](../README.md#export-verification)
for the audit trail on the existing exports.
