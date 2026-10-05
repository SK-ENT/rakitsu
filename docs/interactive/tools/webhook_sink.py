#!/usr/bin/env python3
"""Local stand-in for an alert webhook/ntfy endpoint (demo only).

Appends each POST (headers of interest + body) as one JSON line to the file
given as argv[2]. Listens on 127.0.0.1:argv[1] only.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n).decode("utf-8", "replace")
        rec = {"path": self.path, "content_type": self.headers.get("Content-Type"), "body": body}
        for h in ("Title", "Priority"):
            if self.headers.get(h):
                rec[h.lower()] = self.headers.get(h)
        with open(sys.argv[2], "a") as f:
            f.write(json.dumps(rec) + "\n")
        self.send_response(204)
        self.end_headers()


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
