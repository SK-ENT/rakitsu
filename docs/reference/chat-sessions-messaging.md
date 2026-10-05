# Chat sessions and session messaging

## When to use it

- You want a conversation that lives for hours or days and keeps its context: a chat session.
- You want another program, another agent, or you from a script to talk to that conversation: session messaging.

Typical cases: a long-running monitor that a person can ask questions, a reviewer agent that Claude Code consults, two agents that hand work to each other.

## Chat sessions

A chat session is a config with `interactive: true` that stays alive. You can open one in three ways:

| Way | How |
|---|---|
| Terminal | `rakitsu run config.yaml --interactive` (or `-i`). Slash commands work in the chat. |
| Web UI | Start `rakitsu serve`, open a listed interactive config. |
| HTTP | `POST /api/chat/start` on a running `serve`. |

Minimal interactive config:

```yaml
name: chat-demo
interactive: true
interactive_overlay: false
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
    system_prompt: Be brief.
    settings:
      max_iterations: 3
```

`interactive_overlay: false` makes the agent answer directly. If you leave it out, a non-conversational config gets a chat-host agent in front that delegates to your agents.

### Slash commands

| Command | Meaning |
|---|---|
| `/help` | Show the list. |
| `/clear` | Clear the conversation. |
| `/usage` | Token, cost and budget use. |
| `/context` | What is loaded in each agent's context. |
| `/agent`, `/agent NAME` | List agents, or open a direct thread with one agent (`settings.agent_chat`). |
| `/push` | Push the thread's last reply to the main chat. |
| `/copy` | Copy the selected reply. |
| `/retry` | Re-run the last query. |
| `/model`, `/model AGENT`, `/model AGENT MODEL[@PROVIDER]` | Show or change an agent's model for this session. |
| `/wake status`, `/wake stop`, `/wake resume` | Control the wake timer ([wake timer](wake-timer.md)). |
| `/exit`, `/quit` | Leave. |

### Chat over HTTP

All paths need `Authorization: Bearer <token>` when `RAKITSU_API_TOKEN` is set on the server.

| Request | Meaning |
|---|---|
| `POST /api/chat/start` body `{"config_id": "<id>"}` | Start a session. Optional `workdir`, `env_vars`, `resume_id`. Returns `id`, `config_id`, `name`, `agent_name`, `model`, `interactive`, `created`, `generating`. Body limit 4 KiB. |
| `GET /api/chat` | List active sessions. |
| `GET /api/chat/{id}` | One session. |
| `POST /api/chat/{id}/stop` | Stop it. |
| `POST /api/chat/{id}/fork` | Fork from a past turn. |
| `GET /api/chat/{id}/tree`, `GET /api/chat/{id}/turn/{turn}` | Turn tree and one turn. |
| `/ws/chat/{id}` (WebSocket) | The live conversation. Client frames: `turn`, `interrupt`, `user_input_response`, `edit_turn`, `regenerate`, `switch_branch`, `set_active_path`, `request_tree`. |
| `POST /api/chat/{id}/wake/stop`, `/wake/resume`, `GET /api/chat/{id}/wake/status` | Wake control. |

Get `config_id` from `GET /api/configs` (the `id` of the entry whose `name` you want). Sessions are saved in the sessions directory ([sessions](sessions.md)), and `resume_id` starts a chat from a saved one.

## Session messaging

Session messaging lets one live session inject a message into another. The target agent runs a full turn on it, as if a user typed it. The message is marked as coming from outside.

### Opt in first

Receiving is off by default. The target config must say:

```yaml
settings:
  session_msg:
    enabled: true
```

This flag has two effects: the session accepts messages, and its agents get the tools `send_message` and `list_sessions`. Think before you enable it on a config that has `cli` or `fs` tools: any message then drives those tools. See [security](security.md).

### From the command line

```bash
rakitsu sessions live                       # id, name, kind, location, accepts
rakitsu sessions send chat-1234 "status update: build is green" --from-name ci
```

`sessions send` is fire and forget. Both commands take `--hub URL` (default `http://localhost:9100`) and send `RAKITSU_API_TOKEN` or `RAKITSU_SESSION_MSG_TOKEN` from your environment when set.

### From HTTP

```
POST /api/sessions/{id}/message
{"text": "...", "from_name": "ci", "from_session_id": "", "wait": true, "timeout_ms": 30000}
```

| Field | Meaning |
|---|---|
| `text` | Required. At most 16 KiB. |
| `from_name`, `from_session_id` | Who is sending. Free text, not checked. The target must not treat it as proof of identity. |
| `wait` | `true`: block until the target's turn ends and return the reply. Default is fire and forget. |
| `timeout_ms` | Wait limit. Default 30000, at most 120000. |

Answers (JSON with a `status`):

| HTTP | `status` | Meaning |
|---|---|---|
| 200 | `delivered` | Accepted. With `wait`, the body also has `waited`, `reply`, `interrupted`, and `turn_error` when the turn failed. If the wait ran out, `waited` is `false` and the message was still delivered. |
| 200 | `mailboxed` | The target is not live; the message went to its hub mailbox. |
| 401 | | Bad or missing token. |
| 403 | `rejected` | The target does not accept messages (`session_msg.enabled` is false) or is not a messaging target. |
| 404 | `not_found` | No such session. |
| 409 | `busy` | The target has a turn running and one already queued (`session busy: a turn is already queued`). One message can wait in the queue. For a one-shot run target, a wait from the same sender is already in flight. |
| 413 | | Text over 16 KiB. |
| 429 | `rate_limited` | More than 6 messages in 30 seconds between the same two sessions. No automatic retry. |

`GET /api/sessions/live` lists live targets. `GET /api/sessions/{id}/inbox` drains the mailbox of an external sender (the sender id you used in `from_session_id`).

### From an agent

With `session_msg.enabled`, agents have:

- `list_sessions`: shows `id | name | kind | location | accepts`.
- `send_message`: arguments `session_id`, `text`, optional `wait` and `timeout_seconds` (default 30, at most 120).

A reply to a fire-and-forget message arrives later as a new message in the sender's conversation.

### Tokens

One token covers the whole control plane: `RAKITSU_API_TOKEN`. The messaging endpoints also accept `RAKITSU_SESSION_MSG_TOKEN`, so you can give a sender access to messaging only. With neither set, loopback trust applies and no token is needed.

### Limits that protect you

- Pair rate limit: 6 messages per 30 seconds per pair of sessions. It reduces ping-pong loops, but identities are not verified, so it is not a per-user quota.
- Message size 16 KiB.
- A wait is capped at 120 seconds.

## Use it from Claude Code

A native Claude Code subagent can talk to a chat session through the `rakitsu-ask` helper. See [rakitsu-ask](rakitsu-ask.md).
