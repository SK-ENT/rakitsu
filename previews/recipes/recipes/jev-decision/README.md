# Jev code review routing

Rakitsu tested build: `v0.3.0-alpha.1-internal.1897` (source `b7ea2bfc`).
Use this exact internal build for the example. Native Decision support targets
Rakitsu 0.4.0. The stable v0.3.2 release lacks this pipeline step.
Verified against a loopback fixture and the live Jev API.
On 2026-11-15 the bundled synthetic input returned the expected
`security` queue.

A synthetic change removes login from an account export. A typed review label feeds a fixed checklist selector.

The smoke test also applies a fixed review checklist to the returned route. The checklist is ordinary code and does not approve or merge changes.

## Try without credentials

Run from the extracted folder with Python 3 and the tested Rakitsu binary.

```sh
python3 smoke.py
```

The fixture returns `security` only for the bundled synthetic input. It
checks the outgoing request and verifies Rakitsu decodes the answer.
This checks the connection format and the workflow’s output, not model quality.

## Prerequisites for a live run

1. Obtain your own API key from a project with access to TypeSafe Jev.
2. Use your secret manager or shell profile to set the `TYPESAFE_API_KEY` environment
   variable in the terminal that runs Rakitsu. Keep the value out of YAML,
   shell history, and shared files.

The check below confirms that the variable is set without displaying it.
A live run sends the bundled sample to the configured decision endpoint.

## Run against the provider

```sh
(
  ( [ -n "${TYPESAFE_API_KEY:-}" ] ) || { printf '%s\n' 'Set TYPESAFE_API_KEY before running.' >&2; exit 1; }
  rakitsu run --no-hub --timeout 45 config.yaml "$(cat input.txt)" | python3 review_queue.py
)
```

[Provider reference](https://api.typesafe.ai/redoc)
