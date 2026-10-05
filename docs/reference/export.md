# export

## When to use it

You designed and tested an agent in rakitsu and want to deploy the same config to another runtime: OpenClaw, or NVIDIA NemoClaw (an OpenClaw config plus a sandbox policy).

## Usage

```bash
rakitsu export --format openclaw config.yaml
rakitsu export --format nemoclaw config.yaml --output ./deploy/
rakitsu export --format nemoclaw config.yaml --with-blueprint
```

| Flag | Meaning |
|---|---|
| `-f, --format` | `openclaw` (default) or `nemoclaw`. Any other value gives `unknown format ... (supported: openclaw, nemoclaw)`. |
| `-o, --output` | Output directory. Default `./<format>-export/`. |
| `--with-blueprint` | Also write `blueprint.yaml`. Only for contributions to NVIDIA's blueprint catalog. Normal users do not need it. Every provider `api_key` must be a `${VAR}` reference; a literal key makes the export fail with `api_key must be an ${VAR} reference to include it in blueprint.yaml`. |

## What you get

| Format | Files |
|---|---|
| `openclaw` | `openclaw.json` |
| `nemoclaw` | `openclaw.json`, `sandbox-policy.yaml`, `AGENT.md`, `README.md` |

For a multi-agent config, one subdirectory per agent is written, each with its own sandbox settings.

## NemoClaw deployment steps

1. On the target host, run `nemoclaw onboard` to create a sandbox.
2. Copy the exported `openclaw.json` to `~/.openclaw/openclaw.json`.
3. Apply the policy: `openshell policy set <sandbox> --policy sandbox-policy.yaml --wait`.

Verification notes: [docs/EXPORT-VERIFICATION.md](../EXPORT-VERIFICATION.md).
