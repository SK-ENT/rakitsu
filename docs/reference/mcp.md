# MCP (Model Context Protocol)

## When to use it

- Another tool or client (for example an editor or an agent runtime) should use the tools you defined in a rakitsu config: serve them over MCP.
- Your agent should use tools from an MCP server: add an `mcp_server` tool. See [tools](tools.md#mcp_server).

## Serve your tools over MCP

The MCP server exposes the global `tools:` of a config. It runs on its own port, not on the main port.

```yaml
name: mcp-tools
settings:
  default_provider: openai
  providers:
    openai:
      type: openai
      api_key: ${OPENAI_API_KEY}
tools:
  - name: count_lines
    type: cli
    description: Count the lines in a file
    command: "wc -l {{path}}"
    parameters:
      path: { type: string, description: File to count, required: true }
agents:
  - name: Placeholder
    role: worker
    system_prompt: Not used by MCP.
```

```bash
rakitsu serve --config mcp-tools.yaml --mcp-port 9200
```

The server prints `MCP server running at http://localhost:9200/mcp`.

Facts about the server (from `internal/server/mcp.go`):

- Path: `/mcp` on the `--mcp-port` listener only. No other path on that port reaches it. `--mcp-port` needs `--config`.
- Transport: HTTP `POST` with JSON-RPC. `GET` returns 405 (there is no server-initiated stream). `DELETE` ends a session.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`.
- Protocol versions accepted: `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05`. A client that only speaks a newer revision gets a 400 and should fall back to `initialize`.
- Every tool in the global `tools:` list is listed and callable. An MCP caller runs those tools with the permissions of the server process, so apply the same care as for `cli` and `fs` tools ([security](security.md)).
- A browser `Origin` that is not a localhost origin is refused (403).
- With `RAKITSU_API_TOKEN` set, requests need `Authorization: Bearer <token>`.

- `initialize` answers with an `Mcp-Session-Id` response header. Every later request must send that header back. Without it the server answers 400 (`Mcp-Session-Id header is required`).
- Without `--config`, `--mcp-port` is silently ignored: `serve` starts, no MCP listener opens and no error is printed.

Quick check with curl (no token set). The first call prints the response headers, so you can read the session id:

```bash
curl -s -i http://127.0.0.1:9200/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'

# copy the Mcp-Session-Id value into the next call
curl -s http://127.0.0.1:9200/mcp \
  -H 'Content-Type: application/json' -H 'Mcp-Session-Id: <id from above>' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

## Use MCP tools in an agent

```yaml
tools:
  - name: remote
    type: mcp_server
    description: Tools served by another rakitsu
    transport: http
    url: http://127.0.0.1:9200/mcp
```

Tools appear to the model as `remote_count_lines`. In the agent, write `tools: [remote]` (the server name). Note the limit: the client cannot send a bearer token, so it cannot use an MCP endpoint that has `RAKITSU_API_TOKEN` set.

## Not the same as session messaging

`/mcp` exposes global tools. It does not give access to a chat session. To talk to a chat session use [session messaging](chat-sessions-messaging.md) or [rakitsu-ask](rakitsu-ask.md).
