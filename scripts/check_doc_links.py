#!/usr/bin/env python3
"""Relative-link and anchor checker. Usage: linkcheck.py <repo-root> <dir-under-root>"""
import os, re, sys
root, sub = sys.argv[1], sys.argv[2]
def slug(h):
    h = h.strip().lower()
    h = re.sub(r'[`*_]', '', h)
    h = re.sub(r'[^\w\- ]', '', h)
    return h.replace(' ', '-')
def anchors(path):
    out = set(); fence = False
    for l in open(path, encoding='utf-8'):
        if l.startswith('```'): fence = not fence
        if not fence and l.startswith('#'):
            out.add(slug(l.lstrip('#')))
    return out
total = bad = 0
for dp, _, fs in os.walk(os.path.join(root, sub)):
    for f in sorted(fs):
        if not f.endswith('.md'): continue
        p = os.path.join(dp, f)
        txt = open(p, encoding='utf-8').read()
        txt = re.sub(r'```.*?```', '', txt, flags=re.S)
        for m in re.finditer(r'\]\(([^)\s]+)\)', txt):
            tgt = m.group(1)
            if re.match(r'^(https?:|mailto:)', tgt): continue
            total += 1
            path, _, frag = tgt.partition('#')
            full = p if path == '' else os.path.normpath(os.path.join(dp, path))
            if not os.path.exists(full):
                print('MISSING', os.path.relpath(p, root), '->', tgt); bad += 1; continue
            if frag and full.endswith('.md') and slug(frag) not in anchors(full) and frag not in anchors(full):
                print('BAD ANCHOR', os.path.relpath(p, root), '->', tgt); bad += 1
print('links=%d bad=%d' % (total, bad))
sys.exit(1 if bad else 0)
