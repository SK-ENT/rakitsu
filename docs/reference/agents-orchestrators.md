# Agents, orchestrators and pipelines

## When to use it

- One agent: a chat helper, or a task that one prompt can do.
- Several agents with a **supervisor** (`ReAct`): the task needs different specialists and the supervisor decides who works next.
- A **pipeline**: the steps are known in advance (research, draft, edit). The flow is fixed by you, not by the model. It also works with models that cannot call tools.

## Choose an execution strategy

| Need | Use | What decides the next action |
|---|---|---|
| One model with optional tools | A single agent | The model runs its own ReAct loop. |
| A lead that chooses specialists as it learns more | `strategy: ReAct` | The supervisor model calls workers as tools. |
| Required stages, independent checks, or a bounded retry loop | `strategy: Pipeline` | The runtime schedules the YAML steps and dependencies. |

A Pipeline fixes the execution order and checks. Agent outputs can still vary between runs.

## Agents

An agent is a model plus a system prompt plus a tool list. Fields (from `agents:` in the config):

| Field | Meaning |
|---|---|
| `name` | Unique name. Other parts of the config refer to it. |
| `role` | `worker` or `supervisor`. |
| `provider` | Provider name (key under `settings.providers`). Falls back to `settings.default_provider`. |
| `providers` | Ordered fallback chain of `{name, model}`. Overrides `provider` when set. |
| `model` | Model name. Falls back to the provider `default_model`, then `settings.defaults.model`, then the v0.3.1 standard-provider fallback, `gpt-4o-mini`. |
| `model_config` | `temperature`, `max_tokens`, `top_p`, `frequency_penalty`, `presence_penalty`, `timeout_sec`, `no_stream_tools`, `max_thinking_tokens`, `thinking_offload`, `reasoning_effort`. |
| `system_prompt` | The instructions. A `file:` reference or a plain path to a prompt file is allowed (see [configuration](configuration.md#file-references-and-discovery)). |
| `tools` | Names of tools from the global `tools:` list. |
| `tools_inline` | Tool definitions that only this agent sees. |
| `skills` | Names of skills (a tool set plus a prompt template). |
| `vision` | `true`: always send images. `false`: never. Unset: auto. See [attachments](attachments.md). |
| `settings` | Per-agent limits and behavior (below). |

Per-agent `settings`:

| Key | Meaning |
|---|---|
| `max_iterations` | Maximum ReAct steps. |
| `max_total_tokens` | Token budget for this agent. `0` is unlimited. |
| `max_cost` | Cost budget in USD. `0` is unlimited. |
| `verbose` | Verbose output for this agent. |
| `timeout` | Time limit in seconds for this agent. |
| `reflection` | `enabled`, `mode` (`after_tool`, `before_answer`, `both`), `frequency` (`always`, `on_error`, `every_n`), `every_n`, `prompt`. The agent reviews its own step. |
| `ground_check` | `enabled`, `confidence_threshold` (0.0 to 1.0, default 0.7), `prompt`, `max_retries`. Checks the answer against the evidence. |
| `context` | How history is kept: `strategy` (`full`, `sliding_window`, `step_log`, `auto`), `window_size`, `keep_recent`, `max_tool_output`, `fence_outputs`, auto thresholds (`auto_full_threshold` default 30 messages, `auto_compress_threshold` default 60, `context_budget_threshold` default 0.75, `context_retrieval_threshold` default 0.90), and `retrieval` (`enabled`, `top_k` default 5, `embedding_provider` `ollama` or empty for BM25, `embedding_model`, `embedding_url` default `http://localhost:11434`, `error_bias` default 2.0). |
| `rollback` | `enabled`, `max_rollbacks` (default 3), `triggers`, `llm_self_judge`. Go back to an earlier step when a trigger fires. |

Minimal single agent (runs as given; `OPENAI_API_KEY` must be set):

```yaml
name: one-agent
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
      reasoning_effort: none
  defaults:
    model: gpt-5.6-luna
agents:
  - name: Assistant
    role: worker
    system_prompt: Answer in two short sentences.
    settings:
      max_iterations: 3
```

```bash
rakitsu run one-agent.yaml "What is a pipeline?"
```

## Orchestrators

An orchestrator coordinates agents. Use the `orchestrator:` key for one, or `orchestrators:` (a list) to define sub-orchestrators that a pipeline step can call.

| Field | Meaning |
|---|---|
| `name`, `role`, `provider`, `model`, `model_config`, `system_prompt` | Same meaning as for an agent. |
| `strategy` | `ReAct`, `PlanAndExecute`, `Hierarchical` or `Pipeline`. `PlanAndExecute` and `Hierarchical` currently run through the ReAct loop. |
| `agents` | Names of the agents (or sub-orchestrators) it manages. |
| `routing` | `auto_delegate_tools` (bool) and `rules` (a list of `condition` and `delegate_to`). |
| `handoff` | `include_context`, `max_context_length`, `allow_cross_agent_calls`. |
| `pipeline` | For `Pipeline` only (below). |

A config validation warning is printed if an agent has `role: supervisor` while a `Hierarchical` orchestrator exists, because Hierarchical builds its own supervisor.

### ReAct supervisor with workers

ReAct is a loop in which the model decides what to do, takes an action, and uses
the result to decide what to do next. It ends when the model answers or a run
limit stops it. An individual agent can use this loop to call tools. A ReAct
orchestrator uses the same loop with worker delegation tools.

1. The supervisor receives the prompt and its system instructions.
2. It calls a tool such as `delegate_to_Researcher` with a task for the worker.
3. The worker runs and returns its result as a tool response.
4. The supervisor can call another worker, call the same worker again, or answer.

Choose a supervisor model that supports tool calls. The example instructs the
supervisor to call Researcher before Writer. That instruction guides the model.
Use a Pipeline when both stages are required in that order.

Save this file as `react-team.yaml` and set `OPENAI_API_KEY` in the environment.

```yaml
name: react-team
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
      reasoning_effort: none
  defaults:
    model: gpt-5.6-luna
agents:
  - name: Researcher
    role: worker
    system_prompt: Collect the key facts about the topic. Be short.
    settings: { max_iterations: 2 }
  - name: Writer
    role: worker
    system_prompt: Turn the facts you are given into one clear paragraph.
    settings: { max_iterations: 2 }
orchestrator:
  name: Lead
  strategy: ReAct
  provider: openai
  model: gpt-5.6-luna
  system_prompt: Ask the Researcher first, then the Writer. Return the paragraph.
  agents: [Researcher, Writer]
  routing:
    auto_delegate_tools: true
```

```bash
rakitsu run react-team.yaml "Explain DNS caching"
```

## Pipelines

Declare the workers under `agents:`, set `orchestrator.strategy: Pipeline`,
and define `orchestrator.pipeline.steps`. Each leaf step names a worker or
sub-orchestrator and may give it a task. A step prompt can include the original
query, successful previous results, and the current task.

### Pipeline fields

| Field | Meaning |
|---|---|
| `steps` | The step list. |
| `synthesis` | `true`: one final model call that combines all step results. |
| `synthesis_prompt` | Custom prompt for that call. |

Fields of a step:

| Field | Meaning |
|---|---|
| `name` | Step name. Needed by `depends_on`. |
| `type` | `sequential` (default), `parallel` or `loop`. |
| `agent` | Sequential: the agent (or sub-orchestrator) to run. |
| `task` | The task text. |
| `steps` | Parallel and loop: the sub-steps. |
| `max_iterations` | Loop: maximum rounds. Default 5. |
| `condition_agent`, `condition_prompt` | Loop: the agent and prompt that judge pass or fail. |
| `timeout_sec` | Time limit for this step. |
| `depends_on` | Names of steps that must finish first. Makes a dependency graph (DAG). |
| `require_tool_call` | A mechanical check. The step fails if its agent never called the named tool, whatever the agent says. Keys: `tool`, `command_contains`, `arg_key`, `output_json_path`, `min_value`, `max_value`. |

Validation refuses a step that names an unknown agent, a `require_tool_call` without `tool`, and impossible `min_value`/`max_value` combinations.

### Create a sequential pipeline

Save this example as `write-and-check.yaml`. The edit step depends on the
draft step and receives its result as context. With synthesis disabled, the
last step's output is the pipeline answer.

```yaml
name: write-and-check
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
      reasoning_effort: none
  defaults:
    model: gpt-5.6-luna
agents:
  - name: Drafter
    role: worker
    system_prompt: Write a short draft on the topic.
    settings: { max_iterations: 2 }
  - name: Editor
    role: worker
    system_prompt: Improve the draft. Keep it short.
    settings: { max_iterations: 2 }
orchestrator:
  name: Flow
  strategy: Pipeline
  provider: openai
  model: gpt-5.6-luna
  agents: [Drafter, Editor]
  pipeline:
    synthesis: false
    steps:
      - name: draft
        agent: Drafter
        task: Write the first draft.
      - name: edit
        agent: Editor
        task: Edit the draft.
        depends_on: [draft]
```

```bash
rakitsu run write-and-check.yaml "Why backups matter"
rakitsu sessions --resumable          # a failed pipeline leaves a checkpoint
rakitsu run write-and-check.yaml "Why backups matter" --resume <session-id>
```

### Parallel work and dependencies

At the top level, omitting `depends_on` makes a step depend on the previous
step. Use `depends_on: []` to start an independent branch or name the required
steps explicitly. The runtime runs ready steps at the same dependency level
concurrently. A `type: parallel` step groups concurrent sub-steps under one name.

Use a distinct agent for each concurrent branch. Rakitsu rejects a parallel
group or level that would run the same agent instance concurrently. Reuse an
agent by scheduling those calls in sequence.

### Loops, gates, and failures

A loop runs its sub-steps in order, then calls `condition_agent` when configured.
A verdict that begins with the word `PASS` ends the loop early. Otherwise it
continues up to `max_iterations` and returns the last output. Reaching the cap
does not turn the last verdict into a run error.

A failed step or tool-call gate stops the pipeline before later levels. The
runtime saves a checkpoint after successful levels when session persistence
is available. Use `rakitsu sessions --resumable` to find a checkpoint before
attempting a resume. If optional synthesis fails, the run reports
`synthesis_failed`. Use `rakitsu run --trace` or the Debugger to inspect
`PIPELINE_STEP_END` and `PIPELINE_END`.

More examples: [examples/single/03-react-team](../../examples/single/03-react-team/), [04-pipeline](../../examples/single/04-pipeline/), [06-advanced-strategies](../../examples/single/06-advanced-strategies/) (hierarchical, plan-and-execute, loop, DAG), [07-nested-orchestrators](../../examples/single/07-nested-orchestrators/), and the modular layouts in [examples/modular](../../examples/modular/).

## Related

- Runtime fan-out without a fixed plan: `settings.spawn` (see [configuration](configuration.md#settingsspawn)).
- Talk to one agent inside a chat: see [chat sessions](chat-sessions-messaging.md).
