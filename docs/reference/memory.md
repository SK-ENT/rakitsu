# Memory and knowledge graph

## When to use it

- An agent should remember facts, rules or procedures between runs.
- A long chat must stay cheap: keep a short summary plus the last few turns, not the whole history.
- You want recall to work even with a weak model that forgets to search: turn on auto-recall.

Memory is built into the single binary. There is no database to run. Knowledge is stored as JSON files.

## Turn it on

```yaml
name: memory-demo
interactive: true
project_id: memory-demo
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
  defaults:
    model: gpt-4o-mini
  memory:
    enabled: true
    conversation:
      enabled: true
      keep_recent_turns: 2
    auto_recall:
      enabled: true
      top_k: 5
agents:
  - name: Assistant
    role: worker
    system_prompt: Remember useful facts with memory_add. Look them up with memory_query.
    settings:
      max_iterations: 6
```

```bash
rakitsu run memory-demo.yaml --interactive
```

With `memory.enabled: true`, every agent gets six tools without any `tools:` entry:

| Tool | What it does |
|---|---|
| `memory_add` | Save knowledge. Arguments: `type` (`rule`, `pattern`, `fact`, `procedure`, `gotcha`, `note`), `title`, `content`, optional `id`, `tags`, `priority` (1 to 10), `source`, `supersedes`, `scope`. Re-using an `id` updates it. |
| `memory_query` | Free-text search (BM25 ranking). Arguments: `query`, optional `type`, `limit` (default 5), `as_of`, `include_expired`, `scope`. Returns an index of ids, titles and short snippets. |
| `memory_get` | Read the full content of one entry. |
| `memory_list` | List entries. Accepts `as_of` and `include_expired`. |
| `memory_link` | Link two entries. Relations: `applies_to`, `depends_on`, `part_of`, `related_to`, `supersedes`, `contradicts`. Optional `weight` 0.0 to 1.0. |
| `memory_retire` | End an entry with no replacement. It is hidden, not deleted. |

## Scopes and storage

Scopes are `session`, `project` (default) and `global`. The `project` scope uses the config `project_id`. Each scope is one JSON file under `settings.memory.dir` (default `~/.rakitsu/memory`), for example `global.json` and `project-memory-demo.json`.

Knowledge is never destroyed. When knowledge changes, add the new entry with `supersedes: <old-id>`. The old entry is kept as history but hidden from normal recall. You can look at the past with `as_of` (an RFC 3339 time, or `YYYY-MM-DD`, meaning the end of that day in UTC) or `include_expired`.

## Conversation memory

`memory.conversation.enabled: true` changes how chat history is fed to the model. Instead of the whole transcript, the model sees a rolling summary plus the last `keep_recent_turns` turns word for word. A model call after each turn updates the summary. If that call fails, the full history is kept, so nothing is lost.

| Key | Default | Meaning |
|---|---|---|
| `keep_recent_turns` | 2 | Turns kept word for word. |
| `summary_max_chars` | 2000 | Size cap of the summary. |
| `disable_summary` | false | Drop old turns with no summary call. Older context is then only reachable through the memory tools. Cheapest, but it can lose context. |

`rakitsu acp` uses conversation memory for single-agent configs only.

## Auto-recall

`memory.auto_recall.enabled: true` searches memory at the start of each run and puts the top matches into the model input as a fenced `<recalled_memory>` block. Keys: `top_k` (default 5), `scopes` (default: session, then project, then global; entries like `project:foo` are allowed), `node_type` (only one type). It needs `memory.enabled`.

## Isolation

Give every separate `serve` instance its own `settings.memory.dir` and its own sessions directory. See [sessions](sessions.md).

Runnable example: [examples/single/09-memory-chat](../../examples/single/09-memory-chat/). Events `MEMORY_WRITE` and `MEMORY_RECALL` show up with `--trace` and in the inspector.
