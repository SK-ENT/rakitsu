# A2A (agent to agent)

## When to use it

- Another system speaks the A2A protocol and should call your rakitsu agents.
- One rakitsu should delegate a self-contained task to an agent in another rakitsu (for example a heavy or private model on another machine).

A2A in rakitsu is one task per request. It does not keep a conversation. For a long conversation use [chat sessions](chat-sessions-messaging.md).

## Serve agents over A2A

Give `serve` a config with `--config`. Every agent in that config can be called.

```bash
rakitsu serve --config team.yaml
```

Endpoints on the main port:

| Endpoint | Meaning |
|---|---|
| `GET /.well-known/agent-card.json` | The Agent Card. One skill per agent. Always public. |
| `POST /a2a` | JSON-RPC 2.0. Needs the bearer token when `RAKITSU_API_TOKEN` is set. |

JSON-RPC methods (A2A v1.0.1 wire format, names in PascalCase):

| Method | Meaning |
|---|---|
| `SendMessage` | Start one agent run. Put the agent name in `params.tenant`. Text goes in `params.message.parts[0].text`. It returns a task at once (state `TASK_STATE_WORKING`); the run goes on in the background. |
| `GetTask` | Read a task by id. When the state is `TASK_STATE_COMPLETED`, the reply is in `result.artifacts[0].parts[0].text`. |
| `CancelTask` | Cancel a task that is not finished. A finished task gives error `-32002`. |

Example, with a config that has an agent named `Helper`:

```bash
curl -s http://127.0.0.1:9100/a2a \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"Helper","message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"Say hello"}]}}}'
```

The answer holds the task in `result.task`, with its id in `result.task.id`. Poll it until it is done. (The `GetTask` result is the task itself, so its fields are directly under `result`.)

```bash
curl -s http://127.0.0.1:9100/a2a \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"GetTask","params":{"id":"<task id>"}}'
```

When `result.status.state` is `TASK_STATE_COMPLETED`, the text of the reply is in `result.artifacts[0].parts[0].text`.

What is not supported, and what error you get (code `-32004`, "Unsupported operation"):

- More than one message part.
- `taskId` (continuing a task).
- `contextId` (continuing a context).

An unknown `tenant` gives `-32602`. An unknown method gives `-32601`.

The agent runs from a stripped config that holds only that agent and the global `tools:`. Interactive mode is ignored on purpose: an A2A call never opens a chat.

With a token set, the card advertises the bearer requirement.

## Call another rakitsu from an agent

Use an `a2a` tool. Details are in [tools](tools.md#a2a).

```yaml
tools:
  - name: helper
    type: a2a
    description: Ask the Helper agent on the other machine
    url: http://127.0.0.1:9100      # base URL of the other rakitsu; rakitsu adds /a2a
    agent: Helper
    api_key: ${PEER_TOKEN}          # only if the peer sets RAKITSU_API_TOKEN
```

The model calls the tool with one argument, `query`. The tool starts the task, polls until it is done, and returns the reply text. `timeout_seconds` limits the call (default 30).

Safety rules of the client (see [security](security.md#network-rules)):

- `http` or `https` only, with a host, and no `user:password@` in the URL.
- Proxy settings from the environment (`HTTP_PROXY` and similar) are ignored.
- The connection checks the resolved address when it connects. Private, loopback, link-local and metadata addresses are refused, except the host and port you wrote in `url`.
- Redirects are followed only when scheme and host:port stay the same (a cross-origin redirect fails with `cross-origin redirect refused`). The `Authorization` header is removed on every redirect, including same-origin ones.

## Runnable pair

Terminal 1:

```bash
rakitsu serve --config team.yaml --port 9100
```

Terminal 2: a config with the `a2a` tool above, then

```bash
rakitsu run caller.yaml "Ask the helper to say hello" --no-hub
```

## Wake and monitor control

The wake status, stop and resume controls are REST routes and a slash command. See [wake timer](wake-timer.md).
