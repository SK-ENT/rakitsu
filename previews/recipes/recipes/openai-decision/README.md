# OpenAI Decisions support routing

Rakitsu tested build: `v0.3.0-alpha.1-internal.1897` (source `b7ea2bfc`).
Use this exact internal build for the example. Native Decision support targets
Rakitsu 0.4.0. The stable v0.3.2 release lacks this pipeline step.
Verified against a loopback fixture and the live OpenAI Decisions API.
On 2026-11-15 the bundled synthetic input returned the expected
`billing` queue.

Classify a duplicate-payment report into a support queue.

## Try without credentials

Run from the extracted folder with Python 3 and the tested Rakitsu binary.

```sh
python3 smoke.py
```

The fixture returns `billing` only for the bundled synthetic input. It
checks the outgoing request and verifies Rakitsu decodes the answer.
This checks the connection format and the workflow’s output, not model quality.

## Prerequisites for a live run

1. Obtain your own API key from a project with access to OpenAI Decisions.
2. Use your secret manager or shell profile to set the `OPENAI_API_KEY` environment
   variable in the terminal that runs Rakitsu. Keep the value out of YAML,
   shell history, and shared files.

The check below confirms that the variable is set without displaying it.
A live run sends the bundled sample to the configured decision endpoint.

## Run against the provider

```sh
(
  ( [ -n "${OPENAI_API_KEY:-}" ] ) || { printf '%s\n' 'Set OPENAI_API_KEY before running.' >&2; exit 1; }
  rakitsu run --no-hub --timeout 45 config.yaml "$(cat input.txt)"
)
```

[Provider reference](https://developers.openai.com/api/docs/guides/decisions)
