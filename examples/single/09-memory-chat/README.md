# 09 — Memory Chat

Conversational assistant with native persistent memory (memory/KG tools) and
summarized-context conversation, for chats that outgrow the raw transcript.

## Run

```bash
rakitsu run examples/single/09-memory-chat/config.yaml -i
```

## What's Here

- Single `Assistant` agent, memory discipline built into the system prompt
- `settings.memory.enabled` — six native tools (`memory_add`, `memory_query`,
  `memory_get`, `memory_list`, `memory_link`, `memory_retire`) backed by JSON
  files under `~/.rakitsu/memory/`, no external DB
- `settings.memory.conversation` — rolling summary + last `keep_recent_turns`
  turns verbatim, instead of full-history re-feed
- `settings.memory.auto_recall` — top-k relevant memories injected as a
  `<recalled_memory>` block at the start of each turn

## Demonstrates

- Native memory/KG persistence across runs (`project_id`-scoped)
- Superseding/retiring memory entries instead of destroying them
- Bounded per-turn context via conversation summarization
- Auto-recall for weak models that never think to call `memory_query`

Try: tell it facts in early turns, chat past `keep_recent_turns`, then ask it
to recall — the answer comes from the rolling summary / memory store, not the
transcript. Watch `MEMORY_WRITE` / `MEMORY_RECALL` events with `--trace` or in
the inspector.
