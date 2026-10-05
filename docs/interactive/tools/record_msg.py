#!/usr/bin/env python3
"""Record docs/interactive/casts/msg.json: messaging between two live sessions.

Uses the real REST endpoints of a local serve: GET /api/sessions/live and
POST /api/sessions/{id}/message (wait, fire-and-forget, opt-in refusal, the
pairwise rate limit of 6 messages per 30 seconds).
"""
import json
import os
import shutil
import urllib.request

from castlib import HERE, Demo

d = Demo("session messaging between two live sessions")


def post(path, body):
    r = urllib.request.Request("http://127.0.0.1:9103" + path, data=json.dumps(body).encode(),
                               headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(r).read())


try:
    cfg = os.path.join(d.root, "cfg")
    os.makedirs(cfg)
    for n in ("bridge-reviewer.yaml", "closed-room.yaml"):
        shutil.copy(os.path.join(HERE, "demo", n), cfg)
    work = os.path.join(d.root, "work")
    os.makedirs(work)
    d.start_fake_llm(4010)
    d.start(["rakitsu", "serve", "--host", "127.0.0.1", "--port", "9103", "--config-dir", cfg,
             "--sessions-dir", os.path.join(d.root, "sessions")], "serve.log", cwd=work)
    d.wait_http("http://127.0.0.1:9103/healthz")

    ids = {c["name"]: c["id"] for c in json.loads(urllib.request.urlopen("http://127.0.0.1:9103/api/configs").read())}
    a = post("/api/chat/start", {"config_id": ids["bridge-reviewer"]})["id"]
    b = post("/api/chat/start", {"config_id": ids["closed-room"]})["id"]
    c = post("/api/chat/start", {"config_id": ids["bridge-reviewer"]})["id"]
    short = {a: "SESSION_A", b: "SESSION_B", c: "SESSION_C"}

    def unshort(s):
        for real, name in short.items():
            s = s.replace(name, real)
        return s

    def show(s):
        for real, name in short.items():
            s = s.replace(real, name)
        return s

    def run(cmd):
        d.sh(unshort(cmd), show=cmd)

    U = "http://127.0.0.1:9103"
    d.note("three live chat sessions. A and C opted in (session_msg.enabled), B did not")
    d.type_cmd("curl -s %s/api/sessions/live" % U)
    out = urllib.request.urlopen(U + "/api/sessions/live").read().decode()
    d.emit(show(out) + "\n", 0.4)

    d.note("C sends to A and waits for A's turn to finish")
    run('curl -s -X POST %s/api/sessions/SESSION_A/message -d \'{"text":"Hello from C","from_session_id":"SESSION_C","from_name":"session-c","wait":true}\'' % U)
    d.note("fire and forget: only the delivery status comes back")
    run('curl -s -X POST %s/api/sessions/SESSION_A/message -d \'{"text":"FYI only","from_session_id":"SESSION_C","from_name":"session-c"}\'' % U)
    d.note("B did not opt in: refused (403), and no turn is started")
    run('curl -s -w \' (HTTP %%{http_code})\\n\' -X POST %s/api/sessions/SESSION_B/message -d \'{"text":"let me in","from_session_id":"SESSION_C"}\'' % U)
    d.note("unknown target: 404")
    run('curl -s -w \' (HTTP %%{http_code})\\n\' -X POST %s/api/sessions/no-such-session/message -d \'{"text":"hi"}\'' % U)
    d.note("loop protection: at most 6 tries per 30 seconds per pair. A busy target answers 409, and those tries count too")
    run('for i in 1 2 3 4 5 6 7; do curl -s -o /dev/null -w "message $i -> HTTP %%{http_code}\\n" -X POST %s/api/sessions/SESSION_A/message -d \'{"text":"burst","from_session_id":"SESSION_C"}\'; done' % U)
    d.save("msg.json")
finally:
    d.close()
