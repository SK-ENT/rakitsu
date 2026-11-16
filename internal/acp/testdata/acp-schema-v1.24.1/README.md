# ACP v1 schema pin: v1.24.1

This folder contains a bounded excerpt from the official [ACP schema-v1.24.1 release](https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.24.1), captured on 2026-10-05. The artifact declares ACP protocol version `1`; it is not the experimental v2 schema.

The excerpt pins the session config option methods and types used by Rakitsu: `session/new` config options, client config-option capability, select options, `session/set_config_option`, `config_option_update`, and the corresponding response fields. The complete source artifact SHA-256 is `3c17bd6385d90cf672d8a661fddc359d73422cf8b8ce6865213d25cfd4c0eca7`; the source `meta.json` SHA-256 is `061edb6efa8fb2aa2792459a86ec7268de5fe665bba48b2ffe7939df01481f88`.

The excerpt keeps the upstream JSON definitions intact for the listed names; unrelated ACP definitions are omitted. The existing `acp-schema-0.4.5` pin remains for the older permission-flow shape. Both source releases are Apache-2.0; the license text is copied alongside this excerpt.
