# ACP schema pin: @zed-industries/agent-client-protocol 0.4.5

Test data for `internal/acp/schema_pin_test.go`. The test asserts that the field
names and enum values the permission flow depends on are still present, so a
future re-pin that renames something fails loudly.

- Source: npm package `@zed-industries/agent-client-protocol@0.4.5`, file `dist/schema.d.ts`
- Upstream repository tag: `v0.4.5` of `zed-industries/agent-client-protocol`
- Tarball sha512 (hex, `shasum -a 512` of the `npm pack` output):
  `3b01ca7eee4088aba5fc64898a51e0dfafa439257d78e9767989e347ab5e9c040376b6929eddb65859344831537ae000373d46c64bfe0d37fb1eadf6aef5fb15`
- npm integrity (`dist.integrity`):
  `sha512-OwHKfu5AiKul/GSJilHg36+kOSV9eOl2eYnjR6tenAQDdraSnt22WFk0SDFTeuAANz1Gxkv+DTf7Hq32rvX7FQ==`
- Retrieved: 2026-10-04
- Licence: Apache-2.0 (package.json `license`, file `LICENSE-APACHE`, "Copyright 2025 Zed Industries, Inc.").
  Apache-2.0 permits redistribution with the licence text, so the excerpt is
  committed verbatim and `LICENSE-APACHE` is copied next to it. The excerpt
  keeps its own licence; it is test data and is not linked into the binary.
- Cross-check: `schema/schema.json` in the package is byte-identical to
  `schema/schema.json` at the upstream tag `v0.4.5` (checked with `cmp`). The
  protocol doc `docs/protocol/tool-calls.mdx` at that tag agrees with the facts below.

## Declarations excerpted (verbatim, `schema-excerpt.d.ts`)

`AGENT_METHODS`, `CLIENT_METHODS`, `ToolCallContent`, `Role`,
`EmbeddedResourceResource`, `ToolKind`, `ToolCallStatus`,
`RequestPermissionRequest`, `PermissionOption`, `ToolCallUpdate`,
`ToolCallLocation`, `Annotations`, `TextResourceContents`,
`BlobResourceContents`, `RequestPermissionResponse`, `CancelNotification`.
(`PermissionOptionKind` and `RequestPermissionOutcome` are inline types in this
version, inside `PermissionOption.kind` and `RequestPermissionResponse.outcome`.)

## Wire facts confirmed from the schema

1. Method `session/request_permission`: agent to client, a request with an id (`CLIENT_METHODS.session_request_permission`).
2. Params: `sessionId` (string, required), `toolCall` (`ToolCallUpdate`, required), `options` (`PermissionOption[]`, required), optional `_meta`.
   `PermissionOption` = `{ optionId: string, name: string, kind }`, all required.
3. `kind` values: `allow_once`, `allow_always`, `reject_once`, `reject_always`.
4. Result: `{ outcome: { outcome: "selected", optionId } }` or `{ outcome: { outcome: "cancelled" } }`.
5. `ToolCallUpdate`: only `toolCallId` is required. Optional: `content` (array or null), `kind`, `locations`, `rawInput`, `rawOutput`, `status`, `title`.
   The schema does not require `toolCallId` to refer to an earlier announced `tool_call`; it is described as "the tool call being updated", and the protocol doc examples reuse an announced id.
   `ToolCallContent` variants: `{type:"content", content: ContentBlock}` (text, image, audio, resource_link, resource), `{type:"diff", path, oldText?, newText}`, `{type:"terminal", terminalId}`.
6. Cancellation: the schema description of the cancelled outcome and the protocol doc say that when the client sends `session/cancel` it MUST answer all pending `session/request_permission` requests with `cancelled`. The doc also says clients MAY auto-allow or auto-reject per user settings.
