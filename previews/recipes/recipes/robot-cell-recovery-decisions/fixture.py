"""Local, deterministic responses for the three cloud adapters."""
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from threading import Thread


@contextmanager
def cloud_fixtures(stages, event):
    cloud = {stage["backend"]: stage for stage in stages if stage["backend"] != "laya"}
    headers = {"jev": ("Authorization", "Bearer fixture-key"),
               "openai": ("Authorization", "Bearer fixture-key"),
               "gemini": ("x-goog-api-key", "fixture-key")}
    models = {"jev": "jev-latest", "openai": "gpt-6-luna", "gemini": "gemini-decision-1"}

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            backend = self.path.removeprefix("/")
            stage = cloud[backend]
            data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            header, token = headers[backend]
            if backend == "openai":
                question, = data["questions"]
                state = data["input"]
                choices = [entry["value"] for entry in question["choices"]]
                response = {"answers": [{"name": "result", "type": "choice", "choice": stage["fixture_answer"]}]}
            else:
                question = data["questions"]["result"]
                state = data["state"] if backend == "jev" else data["input"][0]["text"]
                choices = question["criteria"]
                response = {"answers": {"result": {"type": "choice", "choice": stage["fixture_answer"]}}}
            if (self.headers.get(header) != token or data["model"] != models[backend] or
                    question["type"] != "choice" or stage["fixture_answer"] not in choices or state != event[stage["input"]]):
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())

    with HTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = Thread(target=server.serve_forever, daemon=True)
        values = {f"RAKITSU_{backend.upper()}_DECISION_URL": f"http://127.0.0.1:{server.server_port}/{backend}" for backend in cloud}
        values.update(dict.fromkeys(("TYPESAFE_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"), "fixture-key"))
        previous = {name: os.environ.get(name) for name in values}
        os.environ.update(values)
        thread.start()
        try:
            yield
        finally:
            server.shutdown()
            thread.join()
            for name, value in previous.items():
                if value is None:
                    os.environ.pop(name, None)
                else:
                    os.environ[name] = value
