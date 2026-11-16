"""Run one fixed-format Decision pipeline through the pinned Rakitsu CLI."""
import json
import os
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory

PIN = "v0.3.0-alpha.1-internal.1897"
BACKENDS = ("laya", "jev", "openai", "gemini")


def decide(config, state):
    binary = os.environ.get("RAKITSU_BIN", "rakitsu")
    version = subprocess.check_output([binary, "version"], text=True).splitlines()[0]
    if version != f"rakitsu version {PIN}":
        raise SystemExit(f"Use Rakitsu {PIN} for this recipe.")
    decision = config["orchestrator"]["pipeline"]["steps"][0]["decision"]
    with TemporaryDirectory() as tmp:
        path = Path(tmp) / "config.yaml"
        path.write_text(json.dumps(config))
        result = subprocess.run(
            [binary, "run", "--no-hub", "--timeout", "45", "--sessions-dir", tmp, str(path), state],
            cwd=tmp, capture_output=True, text=True, timeout=50,
        )
    if result.returncode:
        raise SystemExit(f'{decision["backend"]} did not return a valid decision.')
    answer = next((json.loads(line) for line in result.stdout.splitlines()
                   if line.startswith('{"backend"')), None)
    if answer is None or answer["backend"] != decision["backend"]:
        raise SystemExit("Expected a typed Decision response.")
    return answer["answers"]["result"]["choice"]
