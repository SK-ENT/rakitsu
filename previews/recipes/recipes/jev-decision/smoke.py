"""Test the bundled Decision recipe against a local protocol fixture."""
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import json
import os
import subprocess
from tempfile import TemporaryDirectory
from threading import Thread

BACKEND = 'jev'
PROMPT = Path("input.txt").read_text().strip()
MODEL = 'jev-latest'
KEY_ENV = 'TYPESAFE_API_KEY'
ENDPOINT_ENV = 'RAKITSU_JEV_DECISION_URL'
CHOICE = 'security'
OPTIONS = ['security', 'database', 'general']
from review_queue import checklist

requests = []


class Fixture(BaseHTTPRequestHandler):
    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if BACKEND == "openai":
            state = data["input"]
            question, = data["questions"]
            auth = self.headers.get("Authorization")
            options = [item["value"] for item in question["choices"]]
            reply = {"answers": [{"name": "queue", "type": "choice", "choice": CHOICE}]}
        else:
            state = data["state"] if BACKEND == "jev" else data["input"][0]["text"]
            question = data["questions"]["queue"]
            auth = self.headers.get("Authorization") if BACKEND == "jev" else self.headers.get("x-goog-api-key")
            options = list(question["criteria"])
            reply = {"answers": {"queue": {"type": "choice", "choice": CHOICE}}}
        expected_auth = "fixture-key" if BACKEND == "gemini" else "Bearer fixture-key"
        if (self.path != "/decision" or data["model"] != MODEL or state != PROMPT or
                question["type"] != "choice" or set(options) != set(OPTIONS) or auth != expected_auth):
            self.send_error(400)
            return
        requests.append(1)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(reply).encode())


binary = os.environ.get("RAKITSU_BIN", "rakitsu")
version = subprocess.check_output([binary, "version"], text=True).splitlines()[0]
if version != "rakitsu version v0.3.0-alpha.1-internal.1897":
    raise SystemExit("This recipe pins Rakitsu v0.3.0-alpha.1-internal.1897. Set RAKITSU_BIN to that build.")

with HTTPServer(("127.0.0.1", 0), Fixture) as server, TemporaryDirectory() as work:
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        env = {**os.environ, KEY_ENV: "fixture-key", ENDPOINT_ENV: f"http://127.0.0.1:{server.server_port}/decision"}
        result = subprocess.run([binary, "run", "--no-hub", "--timeout", "45", "--sessions-dir", work,
                                 "config.yaml", PROMPT], env=env, capture_output=True, text=True)
        if result.returncode:
            raise SystemExit(result.stderr or result.stdout)
    finally:
        server.shutdown()
        thread.join()
answer = next(json.loads(line) for line in result.stdout.splitlines() if line.startswith('{"backend"'))
assert requests == [1] and answer["backend"] == BACKEND and answer["answers"]["queue"]["choice"] == CHOICE
print(json.dumps(answer, ensure_ascii=False))
print("Review checklist:", checklist(answer))
