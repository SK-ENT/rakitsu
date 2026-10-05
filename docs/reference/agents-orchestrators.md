# Agents, orchestrators and pipelines

## When to use it

- One agent: a chat helper, or a task that one prompt can do.
- Several agents with a **supervisor** (`ReAct`): the task needs different specialists and the supervisor decides who works next.
- A **pipeline**: the steps are known in advance (research, draft, edit). The flow is fixed by you, not by the model. It also works with models that cannot call tools.

## Agents

An agent is a model plus a system prompt plus a tool list. Fields (from `agents:` in the config):

| Field | Meaning |
|---|---|
| `name` | Unique name. Other parts of the config refer to it. |
| `role` | `worker` or `supervisor`. |
| `provider` | Provider name (key under `settings.providers`). Falls back to `settings.default_provider`. |
| `providers` | Ordered fallback chain of `{name, model}`. Overrides `provider` when set. |
| `model` | Model name. Falls back to the provider `default_model`, then `settings.defaults.model`, then `gpt-4o-mini`. |
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
  defaults:
    model: gpt-4o-mini
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

```yaml
name: react-team
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
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
  model: gpt-4o-mini
  system_prompt: Ask the Researcher first, then the Writer. Return the paragraph.
  agents: [Researcher, Writer]
  routing:
    auto_delegate_tools: true
```

```bash
rakitsu run react-team.yaml "Explain DNS caching"
```

## Pipelines

`strategy: Pipeline` runs steps in a fixed order. Fields under `orchestrator.pipeline`:

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

```yaml
name: write-and-check
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
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
  model: gpt-4o-mini
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

More examples: [examples/single/03-react-team](../../examples/single/03-react-team/), [04-pipeline](../../examples/single/04-pipeline/), [06-advanced-strategies](../../examples/single/06-advanced-strategies/) (hierarchical, plan-and-execute, loop, DAG), [07-nested-orchestrators](../../examples/single/07-nested-orchestrators/), and the modular layouts in [examples/modular](../../examples/modular/).

## Related

- Runtime fan-out without a fixed plan: `settings.spawn` (see [configuration](configuration.md#settingsspawn)).
- Talk to one agent inside a chat: see [chat sessions](chat-sessions-messaging.md).
