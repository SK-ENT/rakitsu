#!/usr/bin/env python3
"""Tiny OpenAI-compatible fake model server for the interactive demos.

No network, no keys. It answers /v1/chat/completions (streaming or not) with
short canned text and counts every call, so a demo can show "model calls: N".

  GET /count   -> {"calls": N}
  GET /v1/models

If the latest user message contains "TIMER-SOURCED", the model answers with a
scripted run of tool calls (send_alert, then start_task twice) when those
tools are offered, and then plain text. Used by the wake/alerts demos.
"""
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
CALLS = 0


def last_user(messages):
    for m in reversed(messages):
        if m.get("role") == "user":
            c = m.get("content")
            if isinstance(c, list):
                c = " ".join(p.get("text", "") for p in c if isinstance(p, dict))
            return c or ""
    return ""


def answer(req):
    msgs = req.get("messages", [])
    text = last_user(msgs)
    n_user = sum(1 for m in msgs if m.get("role") == "user")
    tools = [t.get("function", {}).get("name") for t in req.get("tools") or []]
    if "TIMER-SOURCED" in text:
        # Scripted tool use inside one timer turn: count tool results after the
        # last user message and walk through alert -> start_task -> refused task.
        done = 0
        for m in reversed(msgs):
            if m.get("role") == "user":
                break
            if m.get("role") == "tool":
                done += 1
        if done == 0 and "send_alert" in tools:
            return None, ("send_alert", {"severity": "warn", "title": "log-watch alarm",
                                         "body": "The timer saw ERROR in the log. Home is /Users/someone. Header was Authorization: Bearer demo-not-a-real-token-123 and key sk-" "demoFAKEkey123."})
        if done <= 1 and "start_task" in tools:
            return None, ("start_task", {"config_name": "summary-note", "arguments": {"topic": "log"}})
        if done <= 2 and "start_task" in tools:
            return None, ("start_task", {"config_name": "not-on-the-list", "arguments": {}})
        return "The timer reported an alarm. I raised an alert, started one allowed task and tried one that is not allowed.", None
    snippet = " ".join(text.split())[:60]
    return "Fake model reply. This conversation now has %d user message(s). You said: %s" % (n_user, snippet), None


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path == "/count":
            return self._json({"calls": CALLS})
        if self.path.endswith("/models"):
            return self._json({"object": "list", "data": [{"id": "fake-model", "object": "model"}]})
        self._json({"error": "not found"}, 404)

    def do_POST(self):
        global CALLS
        n = int(self.headers.get("Content-Length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        if "chat/completions" not in self.path:
            return self._json({"error": "not found"}, 404)
        with LOCK:
            CALLS += 1
        text, call = answer(req)
        usage = {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
        msg = {"role": "assistant", "content": text}
        finish = "stop"
        if call is not None:
            with LOCK:
                cid = "call_demo%d" % CALLS
            msg = {"role": "assistant", "content": None, "tool_calls": [{
                "id": cid, "type": "function",
                "function": {"name": call[0], "arguments": json.dumps(call[1])}}]}
            finish = "tool_calls"
        if not req.get("stream"):
            return self._json({"id": "fake", "object": "chat.completion", "model": "fake-model",
                               "choices": [{"index": 0, "message": msg, "finish_reason": finish}], "usage": usage})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()

        def send(delta, fin=None, u=None):
            d = {"id": "fake", "object": "chat.completion.chunk", "model": "fake-model",
                 "choices": [{"index": 0, "delta": delta, "finish_reason": fin}]}
            if u:
                d["usage"] = u
            self.wfile.write(b"data: " + json.dumps(d).encode() + b"\n\n")
            self.wfile.flush()

        if call is not None:
            tc = msg["tool_calls"][0]
            send({"role": "assistant", "tool_calls": [{"index": 0, "id": tc["id"], "type": "function",
                  "function": {"name": call[0], "arguments": tc["function"]["arguments"]}}]})
        else:
            send({"role": "assistant", "content": text})
        send({}, finish, usage)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 4010
    ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
