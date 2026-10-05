#!/usr/bin/env python3
"""Build the self-contained pages in docs/interactive/ from tools/src/*.html.

Each page source holds the markers
  /*@CSS@*/   -> contents of src/common.css
  /*@JS@*/    -> contents of src/common.js
  /*@CAST:x@*/ -> contents of casts/x.json (so the page works from file:// too)
The output has no external requests: everything is inline.
"""
import glob
import os
import re

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "src")
OUT = os.path.abspath(os.path.join(HERE, ".."))


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


css = read(os.path.join(SRC, "common.css"))
js = read(os.path.join(SRC, "common.js"))
for page in sorted(glob.glob(os.path.join(SRC, "*.html"))):
    html = read(page)
    html = html.replace("/*@CSS@*/", css).replace("/*@JS@*/", js)

    def cast(m):
        raw = read(os.path.join(OUT, "casts", m.group(1) + ".json")).strip()
        return raw.replace("</", "<\\/")

    html = re.sub(r"/\*@CAST:([a-z0-9-]+)@\*/", cast, html)
    dest = os.path.join(OUT, os.path.basename(page))
    with open(dest, "w", encoding="utf-8") as f:
        f.write(html)
    print("built", os.path.relpath(dest, OUT), len(html), "bytes")
