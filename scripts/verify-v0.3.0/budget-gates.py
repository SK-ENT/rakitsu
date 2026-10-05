#!/usr/bin/env python3
"""Verify v0.3.0 budget and serve-flag fixes with a local fake provider.

Usage: python3 scripts/verify-v0.3.0/budget-gates.py --bin bin/rakitsu

The only network listener is an ephemeral loopback OpenAI-compatible server.
No provider credentials or external services are used.
"""

from __future__ import annotations

import argparse
import http.server
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import threading


def sse(payload: dict) -> bytes:
    return b"data: " + json.dumps(payload).encode() + b"\n\n"


def free_loopback_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class FakeProvider(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length))
        model = request.get("model", "fake-model")
        chunks = [
            {
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": "done"}, "finish_reason": None}],
            },
            {
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            },
            {
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [],
                "usage": {"prompt_tokens": 100, "completion_tokens": 0, "total_tokens": 100},
            },
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for chunk in chunks:
            self.wfile.write(sse(chunk))
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def log_message(self, *_args: object) -> None:
        pass


def config_text(base_url: str, *, token_limit: int = 0, pipeline: bool = False) -> str:
    execution = f"  execution:\n    max_total_tokens: {token_limit}\n" if token_limit else ""
    orchestrator = """orchestrator:
  name: Root
  strategy: Pipeline
  provider: fake
  model: fake-model
  agents: [Worker]
  pipeline:
    synthesis: false
    steps:
      - name: run-worker
        agent: Worker
        task: Do the requested task.
""" if pipeline else ""
    return f"""settings:
  default_provider: fake
  providers:
    fake:
      type: openai
      api_key: test-only
      base_url: {base_url}
      default_model: fake-model
{execution}  pricing:
    fake-model:
      input: 1000000
      output: 1000000
agents:
  - name: Worker
    role: worker
    system_prompt: Answer briefly.
    settings:
      max_iterations: 1
{orchestrator}"""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", required=True, type=pathlib.Path, help="path to the built rakitsu binary")
    args = parser.parse_args()
    binary = args.bin.resolve()
    if not binary.is_file():
        parser.error(f"binary does not exist: {binary}")

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeProvider)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base_url = f"http://127.0.0.1:{server.server_port}/v1"
    failures = 0
    try:
        with tempfile.TemporaryDirectory(prefix="rakitsu-v030-budget-") as temp:
            tempdir = pathlib.Path(temp)
            home = tempdir / "home"
            home.mkdir()
            env = {"PATH": os.environ.get("PATH", ""), "HOME": str(home), "TMPDIR": temp}

            try:
                serve = subprocess.run(
                    [str(binary), "serve", "--port", "0", "--mcp-port", str(free_loopback_port())],
                    cwd=tempdir,
                    env=env,
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    timeout=5,
                )
                serve_output = serve.stdout
                serve_ok = serve.returncode != 0 and "--mcp-port requires --config" in serve_output
            except subprocess.TimeoutExpired as exc:
                serve_output = (exc.stdout or b"").decode(errors="replace") if isinstance(exc.stdout, bytes) else (exc.stdout or "")
                serve_ok = False
            print(f"serve requires config for --mcp-port: {'PASS' if serve_ok else 'FAIL'}")
            failures += not serve_ok

            cases = [
                ("single-agent --max-cost without YAML cost limit", 0, False, ["--max-cost", "0.0001"], "cost budget exceeded"),
                ("single-agent YAML global token limit", 5, False, [], "token budget exceeded"),
                ("pipeline --max-cost without YAML cost limit", 0, True, ["--max-cost", "0.0001"], "cost budget exceeded"),
            ]
            for index, (name, token_limit, pipeline, flags, expected) in enumerate(cases):
                config = tempdir / f"case-{index}.yaml"
                config.write_text(config_text(base_url, token_limit=token_limit, pipeline=pipeline))
                result = subprocess.run(
                    [str(binary), "run", str(config), "hello", "--no-hub", *flags],
                    cwd=tempdir,
                    env=env,
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT,
                    timeout=20,
                )
                output = result.stdout.lower()
                passed = expected in output and "budget exceeded" in output
                print(f"{name}: {'PASS' if passed else 'FAIL'}")
                if not passed:
                    print("  expected budget result was not present")
                failures += not passed
    finally:
        server.shutdown()
        server.server_close()
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
