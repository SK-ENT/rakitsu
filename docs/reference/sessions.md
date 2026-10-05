# Sessions

## When to use it

- Read what an agent did after the fact: every tool call, error and token count is recorded.
- Continue a run that failed or stopped (`--resume`).
- Give each server its own history (`--sessions-dir`).

## What is saved

Every run is written to a session directory. The default is `~/.rakitsu/sessions`. The directory has mode `0700` and files have mode `0600`.

| File | Content |
|---|---|
| `<uuid>.jsonl` | One JSON object per line. The first line is the session record (`id`, `name`, `query`, `config_path`, a copy of the config, `agents`, `start_time`, `status`, token and event counts). The next lines are events: `id`, `timestamp`, `agent_name`, `event_type` (for example `AGENT_START`, `THOUGHT_START`, `TOKEN_CHUNK`, `TOKEN_USAGE`, `THOUGHT_END`), and `payload`. |
| `<uuid>.chat.json` | For chat sessions: the turn tree, so you can resume or fork from a past turn. |
| `sessions.json` | Index of all sessions. It is not safe for two processes to write at once. |

The copy of the config in the first line masks `api_key` as `[REDACTED]`. Fields written as `${VAR}` stay as the reference, not the value. A secret typed as a literal in the YAML is stored as written. Tool call arguments whose key looks like a secret (`token`, `api_key`, `password`, `secret`, `authorization`) are masked. Add your own words with `settings.redact_keywords`. See [security](security.md).

Choose the directory with `--sessions-dir DIR` (on `run`, `serve` and `sessions`), or `settings.sessions_dir`. The flag wins. Never let two running instances share one directory.

## List sessions

```bash
rakitsu sessions                     # last 20, newest first
rakitsu sessions --limit 5
rakitsu sessions --resumable         # only runs with a checkpoint
rakitsu sessions --sessions-dir ~/.rakitsu/instances/team-a/sessions
```

The columns are `SESSION ID`, `NAME`, `STATUS`, `DURATION`, `RESUMABLE`, `QUERY`.

## Resume

```bash
rakitsu run config.yaml "next question" --resume <session-id>
```

A single agent replays the earlier conversation into the model context. A pipeline continues from its checkpoint, so finished steps are not run again. A chat session can be restarted from the web UI, or with `resume_id` on `POST /api/chat/start` ([chat sessions](chat-sessions-messaging.md)).

## Read a session with your tools

The JSONL format is plain. For example, to print the streamed text of a run:

```bash
jq -r 'select(.event_type=="TOKEN_CHUNK") | .payload.text' ~/.rakitsu/sessions/<id>.jsonl
```

## In the web UI

Session History lists runs. You can inspect the tree and graph, replay a run, or rerun from a step. See [serve and web UI](serve-web-ui.md).
