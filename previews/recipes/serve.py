"""Serve the private catalog and keep only daily recipe counters."""
import argparse
from contextlib import closing
from datetime import datetime, timedelta, timezone
from functools import partial
from html import escape
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import sqlite3

EVENTS = ("view", "preview", "download")


class CatalogHandler(SimpleHTTPRequestHandler):
    def log_message(self, *_args):
        # The default handler logs the client IP and raw request URL.
        pass

    def end_headers(self):
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("X-Content-Type-Options", "nosniff")
        super().end_headers()

    def list_directory(self, _path):
        self.send_error(404, "Not found")
        return None

    def send_error(self, code, message=None, explain=None):
        # Some default error messages include the caller's input.
        super().send_error(code, self.responses.get(code, ("Error",))[0])

    def recipes(self):
        with (Path(self.directory) / "catalog.json").open() as source:
            return {row["id"]: row["title"] for row in json.load(source)}

    def counters(self, recipe=None, event=None):
        today = datetime.now(timezone.utc).date()
        with closing(sqlite3.connect(self.server.database)) as db, db:
            db.execute("""
                CREATE TABLE IF NOT EXISTS counts (
                    day TEXT, recipe TEXT, event TEXT, total INTEGER,
                    PRIMARY KEY (day, recipe, event)
                )
            """)
            db.execute("DELETE FROM counts WHERE day < ?", (str(today - timedelta(days=29)),))
            if recipe is not None:
                db.execute("""
                    INSERT INTO counts VALUES (?, ?, ?, 1)
                    ON CONFLICT (day, recipe, event) DO UPDATE SET total = total + 1
                """, (str(today), recipe, event))
                return None
            return {
                (recipe, event): total for recipe, event, total in db.execute(
                    "SELECT recipe, event, SUM(total) FROM counts GROUP BY recipe, event"
                )
            }

    def do_POST(self):
        if self.path != "/analytics/event":
            self.send_error(404)
            return
        if self.headers.get("Sec-Fetch-Site") != "same-origin":
            self.send_error(403)
            return
        length = self.headers.get("Content-Length", "")
        if self.headers.get("Content-Type") != "application/json" or length not in {
            str(size) for size in range(1, 257)
        }:
            self.send_error(400)
            return
        try:
            payload = json.loads(self.rfile.read(int(length)))
        except (ValueError, RecursionError):
            self.send_error(400)
            return
        if (not isinstance(payload, dict) or payload.keys() != {"recipe", "event"}
                or not isinstance(payload["recipe"], str) or payload["recipe"] not in self.recipes()
                or payload["event"] not in EVENTS):
            self.send_error(400)
            return
        self.counters(payload["recipe"], payload["event"])
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        if self.path != "/analytics":
            super().do_GET()
            return
        totals = self.counters()
        rows = []
        for recipe, title in self.recipes().items():
            cells = "".join(f"<td>{totals.get((recipe, event), 0)}</td>" for event in EVENTS)
            rows.append(f'<tr id="{escape(recipe)}"><th scope="row">{escape(title)}</th>{cells}</tr>')
        body = ("""<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Rakitsu Recipes analytics</title>
<style>
body{font:1rem/1.5 system-ui;max-width:58rem;margin:2rem auto;padding:0 1rem}
table{border-collapse:collapse;width:100%}th,td{border-bottom:1px solid #bbb;padding:.5rem;text-align:left}
td{text-align:right;font-variant-numeric:tabular-nums}
</style>
<a href="/">Back to recipes</a>
<h1>Rakitsu Recipes analytics</h1>
<p>Last 30 UTC days. Views count explicit opens. Preview clicks include workflows,
configuration selections and reports. Downloads count ZIP clicks.</p>
<p>Only daily totals by recipe are stored.</p>
<table><thead><tr><th>Recipe</th><th>Views</th><th>Preview clicks</th><th>Downloads</th></tr></thead>
<tbody>""" + "".join(rows) + "</tbody></table></html>").encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8818)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--database", type=Path, required=True)
    args = parser.parse_args()
    if args.database.resolve().is_relative_to(args.directory.resolve()):
        parser.error("The database must be outside the served directory")
    handler = partial(CatalogHandler, directory=args.directory)
    with ThreadingHTTPServer(("127.0.0.1", args.port), handler) as server:
        server.database = args.database
        server.serve_forever()
