# Rakitsu — Before / After: P0 + P1 Phase Completion

> This document captures what changed across the P0 and P1 implementation phases,
> shown as before/after comparisons with flow diagrams.
> Retry examples below are historical phase snapshots. In v0.3.0-beta.4,
> runtime defaults are 5 total attempts, 1s base delay and 60s maximum delay
> (10s base delay for rate limits); see [current retry settings](configuration.md#retrysettings).

---

## Table of Contents

1. [P0 — Panic Recovery + LLM Retry](#1-p0--panic-recovery--llm-retry)
2. [P0 — Per-Step Timeout](#2-p0--per-step-timeout)
3. [P1 — Provider Fallback Chains](#3-p1--provider-fallback-chains)
4. [P1 — Pipeline DAG (depends_on)](#4-p1--pipeline-dag-depends_on)
5. [P1 — Token/Cost Budget Enforcement](#5-p1--tokencost-budget-enforcement)
6. [P1 — Thinking Token Offload (1B)](#6-p1--thinking-token-offload-1b)
7. [P1 — Context Auto-Strategy (1A)](#7-p1--context-auto-strategy-1a)
8. [P1 — Pipeline Checkpoint + --resume](#8-p1--pipeline-checkpoint----resume)

---

## 1. P0 — Panic Recovery + LLM Retry

### Before

```
Tool goroutine panics
        │
        ▼
  Process crashes ◄─── silent, no log, no recovery
```

```
LLM API returns 429 / 503
        │
        ▼
  Run fails immediately ◄─── no retry, user must restart
```

### After

```
Tool goroutine panics
        │
        ▼
  recover() catches panic
        │
        ▼
  Log error + emit TOOL_ERROR event
        │
        ▼
  Agent continues next iteration ◄─── no process crash
```

```
LLM API returns 429 / 503
        │
        ▼
  Attempt 1 fails
        │  wait: base×2^0 + jitter
        ▼
  Attempt 2 fails
        │  wait: base×2^1 + jitter
        ▼
  Attempt 3 succeeds ──► continue run
        │
        (or)
        ▼
  All 3 fail ──► RETRY_ATTEMPT event emitted, clean error returned
```

### YAML (no config needed — automatic)

```yaml
# Retry is built-in. Optional tuning via settings:
settings:
  retry:
    max_attempts: 3    # default
    base_delay: "1s"   # default — initial backoff
    max_delay: "16s"   # default — backoff ceiling
```

---

## 2. P0 — Per-Step Timeout

### Before

```
Pipeline Step N starts
        │
        ▼
  Agent calls slow/hung tool
        │
        ▼
  Blocks forever ◄─── no timeout, entire pipeline hangs
```

### After

```
Pipeline Step N starts
        │
        ▼  timeout_sec: 30
  Agent calls slow tool ──────────────────────┐
        │                                     │ 30s elapsed
        │ (tool returns)                      ▼
        ▼                              context.DeadlineExceeded
  Step completes ──► checkpoint        │
                                       ▼
                               Step fails cleanly
                                       │
                                       ▼
                               Pipeline reports timeout,
                               remaining steps skipped
```

### YAML

```yaml
orchestrator:
  pipeline:
    steps:
      - name: slow_analysis
        agent: analyzer
        task: "Analyze the codebase"
        timeout_sec: 60    # ← new field; 0 = inherit global timeout
```

---

## 3. P1 — Provider Fallback Chains

### Before

```
Agent request
      │
      ▼
  Provider A fails (API down)
      │
      ▼
  Run fails ◄─── no fallback
```

### After

```
Agent request
      │
      ▼
  Provider A (primary) fails
      │
      ▼
  Provider B (fallback 1) fails
      │
      ▼
  Provider C (fallback 2) succeeds ──► response returned
```

### YAML

```yaml
settings:
  providers:
    primary:
      type: anthropic
      api_key: ${ANTHROPIC_API_KEY}
    fallback_openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
    fallback_local:
      type: ollama
      base_url: http://localhost:11434

agents:
  - name: ResilientAgent
    providers:                     # ← ordered fallback chain, tried in order
      - name: primary
        model: claude-sonnet-4-6
      - name: fallback_openai
        model: gpt-4o
      - name: fallback_local
        model: llama3
```

---

## 4. P1 — Pipeline DAG (depends_on)

### Before

```
Steps executed strictly in YAML order, sequentially:

  step1 ──► step2 ──► step3 ──► step4
  (each waits for previous regardless of dependency)
```

### After

```
Steps form a DAG; independent steps run in parallel per level:

  Level 0:        step1
                    │
            ┌───────┴───────┐
  Level 1:  step2          step3      ◄── parallel
            │               │
            └───────┬───────┘
  Level 2:        step4              ◄── waits for both
```

### YAML

```yaml
orchestrator:
  pipeline:
    steps:
      - name: plan
        agent: planner
        task: "Create a plan"

      - name: implement
        agent: developer
        task: "Write the code"
        depends_on: [plan]

      - name: test
        agent: tester
        task: "Test the code"
        depends_on: [plan]          # ← both independent of each other

      - name: review
        agent: reviewer
        task: "Review everything"
        depends_on: [implement, test]  # ← waits for both
```

### Execution timeline (wall-clock)

```
Before (sequential):
  plan(10s) ──► implement(20s) ──► test(15s) ──► review(5s)  =  50s total

After (DAG parallel):
  plan(10s) ──► implement(20s) ──┐
              └──► test(15s)  ───┘──► review(5s)  =  35s total  (-30%)
```

---

## 5. P1 — Token/Cost Budget Enforcement

### Before

```
Multi-agent pipeline with shared budget:

  Root TokenGuard (max: 100k tokens)
       │
  ┌────┴────┐
  │         │
Agent A   Agent B
  │         │
  Add()    Add()
  │         │
  ▼         ▼
Per-agent guards updated ✓
Root guard stays at 0 ✗  ◄── bug: shared budget never enforced
```

### After

```
  Root TokenGuard (max: 100k tokens)
       │
  CompositeGuard
  ┌────┴────┐
  │         │
Agent A   Agent B
  │         │
AddTokens() AddTokens()
  │         │
  ▼         ▼
Per-agent guards updated ✓
        │
        ▼
  Root guard propagated ✓  ◄── fix: AddTokens() fans out through chain
        │
        ▼
  Budget check fires correctly when 100k reached
```

### YAML

```yaml
settings:
  max_total_tokens: 100000   # shared across all agents in pipeline
  max_cost: 2.50             # shared USD cap

agents:
  - name: planner
    model_config:
      max_total_tokens: 30000  # per-agent sub-budget
      max_cost: 0.50
```

---

## 6. P1 — Thinking Token Offload (1B)

### Before

```
Iteration 1:
  history: [user: "build scheduler"]
  LLM response: <think>long reasoning 2000 tokens</think>\nAnswer...
  history: [user, assistant(think+answer)]  ← 2000 thinking tokens stay

Iteration 2:
  history: [user, assistant(think+answer), tool_result, ...]
  LLM response: <think>more reasoning 2000 tokens</think>\n...
  history grows: 4000 thinking tokens accumulated

Iteration 4:
  history: 8000+ thinking tokens ──► context window exhausted ──► run fails
```

### After

```
Iteration 1:
  LLM response: <think>long reasoning 2000 tokens</think>\nAnswer...
           │
           ▼
  extractOpenAIThinking() / Anthropic block extraction
           │
    ┌──────┴──────┐
    │             │
    ▼             ▼
  Save to disk  Inject placeholder
  ~/.rakitsu/     [Thought 1: decided to
  thinking/...   use write_file...]  ← 15 tokens
           │
           ▼
  history: [user, assistant(answer), user([Thought 1:...])]
           ← only 15 tokens from thinking remain in context

Iteration 4:
  history: 60 tokens of placeholders vs 8000 before ──► run succeeds
```

### Anthropic round-trip (special case)

```
Anthropic API requires thinking blocks (with signature) to be
re-sent in subsequent requests or the API rejects them.

Solution:
  thinking block ──► stored in Message.Metadata["anthropic_thinking_blocks"]
                          │
                          ▼
                  buildMessages() re-injects blocks before text
                          │
                          ▼
                  API call succeeds ✓
                  Visible history shows only [Thought N: ...] ✓
```

### YAML

```yaml
agents:
  - name: reasoner
    provider: anthropic
    model: claude-sonnet-4-6
    model_config:
      max_thinking_tokens: 4096    # Anthropic extended thinking budget cap
      thinking_offload: true       # strip from history, save to disk

  - name: coder
    provider: vllm
    model: qwen3-30b
    model_config:
      thinking_offload: true       # strips <think>...</think> from responses
```

---

## 7. P1 — Context Auto-Strategy (1A)

### Before

```
auto strategy: escalates on message COUNT only

  Messages 1–29:   full history sent ──► fine
  Messages 30–59:  sliding window    ──► fine
  Messages 60+:    step-log mode     ──► fine

Problem: 10 messages with huge tool outputs can exhaust 32K context
         before ever hitting the message threshold (30).
```

### After

```
auto strategy: token PRESSURE takes priority over message count

  Set max_tokens: 32768 in YAML ──► enables pressure mode

  After each LLM call:
    pressure = inputTokens / maxContextTokens

  buildAuto():
    ┌─ pressure known (> 0)? ─────────────────────────────────┐
    │                                                          │
    │  pressure ≥ 0.90  ──► step_log (heavy compression)      │
    │  pressure ≥ 0.75  ──► sliding_window                    │
    │  pressure < 0.75  ──► full (skip message count check)   │
    │                                                          │
    └─ pressure unknown (max_tokens not set) ─────────────────┘
         message count thresholds used (existing behaviour)
```

### Context escalation flow

```
                        ┌──────────────────────────────────┐
                        │          Agent ReAct Loop         │
                        └──────────────┬───────────────────┘
                                       │ each iteration
                                       ▼
                               LLM call returns
                               TokenUsage.InputTokens
                                       │
                                       ▼
                         SetTokenPressure(in, maxCtx)
                                       │
                         ┌─────────────┼─────────────┐
                         │             │             │
                       < 75%        75–90%         ≥ 90%
                         │             │             │
                       full      sliding_window  step_log
                                       │
                                       ▼
                          (if strategy changed)
                          emit CONTEXT_COMPRESSED event
                          RunInspector shows: "78% ctx"
```

### YAML

```yaml
model_config:
  max_tokens: 32768          # enables token-pressure mode

settings:
  context:
    strategy: auto
    context_budget_threshold: 0.75     # optional; default 0.75
    context_retrieval_threshold: 0.90  # optional; default 0.90
    window_size: 20                    # sliding_window turns
    keep_recent: 3                     # step_log recent steps
```

---

## 8. P1 — Pipeline Checkpoint + --resume

### Before

```
20-step pipeline
        │
  Steps 1–17 complete (45 minutes elapsed)
        │
        ▼
  Step 18 fails (API timeout)
        │
        ▼
  Run exits ──► all 45 minutes of work lost
                Must restart from step 1
```

```
User wants to resume:
  "What was my session ID?" ──► no way to find out
```

### After

```
20-step pipeline
        │
  Step 1 completes ──► checkpoint written
  Step 2 completes ──► checkpoint written
  ...
  Step 17 completes ──► checkpoint written
        │
        ▼
  Step 18 fails ──► run exits, checkpoint preserved
        │
        ▼
  User runs: rakitsu sessions --resumable

  SESSION ID        NAME    STATUS  DURATION  RESUMABLE  QUERY
  abc123-...        myapp   error   45.2m     yes        build REST API

  rakitsu run pipeline.yaml "build REST API" --resume abc123-...
  Resuming from checkpoint: 17 step(s) already completed — [plan, implement, ...]
        │
        ▼
  Steps 1–17: skipped (outputs injected from checkpoint)
  Step 18: retried ──► completes
  ...
  Step 20: completes ──► pipeline done
```

### Checkpoint file

```
~/.rakitsu/sessions/
├── abc123.jsonl                  ← event log
├── abc123.checkpoint.json        ← resumable checkpoint
└── sessions.json                 ← index
```

```json
{
  "session_id": "abc123-...",
  "query": "build REST API",
  "completed_steps": ["plan", "implement", "test"],
  "results": {
    "plan": { "name": "plan", "output": "1. Create models...", "duration_ns": 12400000000 },
    "implement": { "name": "implement", "output": "// main.go\n...", "duration_ns": 34200000000 },
    "test": { "name": "test", "output": "PASS: 12/12 tests", "duration_ns": 8100000000 }
  }
}
```

### Resume flow

```
rakitsu run config.yaml "query" --resume <session-id>
        │
        ▼
  LoadCheckpoint(sessionID)
        │
        ▼
  Convert StoreCheckpointData ──► agent.CheckpointData
        │
        ▼
  orch.SetCheckpoint(cp)
        │
        ▼
  runPipeline():
    for each step:
      if step.name in checkpoint.CompletedSteps:
        inject saved output into PipelineContext ──► skip execution
      else:
        execute normally ──► checkpoint updated on success
```

---

## Summary Table

| Feature | Before | After | Key Metric |
|---|---|---|---|
| Tool panic | Process crash | Caught, logged, run continues | 0 crashes |
| LLM transient error | Immediate failure | 3 retries + backoff | ~99% recovery on 429/503 |
| Hung tool | Blocks forever | Timeout per step | Configurable per step |
| Provider outage | Run fails | Automatic fallback chain | N providers available |
| Pipeline steps | Sequential only | DAG parallel levels | Up to -30% wall-clock time |
| Shared token budget | Never enforced | Correct propagation | Budget actually stops runs |
| Thinking tokens | Accumulate → OOM | Offloaded, placeholder injected | 32K models run 10+ iterations |
| Context overflow | Silent degradation | Pressure-based compression | Auto at 75%/90% token usage |
| Failed pipeline | Restart from step 1 | Resume from checkpoint | Preserve hours of work |
| Find session to resume | Manual file inspection | `rakitsu sessions --resumable` | One command |
