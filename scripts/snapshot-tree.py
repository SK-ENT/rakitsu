#!/usr/bin/env python3
"""Write every regular file of a git commit into a directory, byte for byte.

Usage: scripts/snapshot-tree.py <commit> <dest-dir>   (run inside the repo)

Used by scripts/ci-review.sh to give the full AI review a read-only copy of
the PR head. Reads blobs straight from the object database
(`git ls-tree` + `git cat-file --batch`) on purpose: `git archive` and
`git checkout-index` apply the commit's own .gitattributes (export-ignore,
export-subst, eol conversion, filters), so a PR could hide files from the
reviewer or change what it reads. Nothing here runs anything from the
commit.

Skipped: symlinks (mode 120000) so no path can lead outside dest,
submodules, and any path that is absolute or has an empty, ".", "..", or
".git" component.
"""

import os
import subprocess
import sys

REGULAR_MODES = (b"100644", b"100755")
UNSAFE_PARTS = (b"", b".", b"..", b".git")


def list_blobs(commit):
    out = subprocess.run(
        ["git", "ls-tree", "-r", "-z", "--full-tree", commit],
        check=True, capture_output=True,
    ).stdout
    for rec in out.split(b"\0"):
        if not rec:
            continue
        meta, path = rec.split(b"\t", 1)
        mode, kind, sha = meta.split(b" ")
        if kind != b"blob" or mode not in REGULAR_MODES:
            continue
        if path.startswith(b"/") or any(p in UNSAFE_PARTS for p in path.split(b"/")):
            continue
        yield sha, path


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: snapshot-tree.py <commit> <dest-dir>")
    commit, dest = sys.argv[1], sys.argv[2].encode()
    cat = subprocess.Popen(["git", "cat-file", "--batch"],
                           stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        for sha, path in list_blobs(commit):
            cat.stdin.write(sha + b"\n")
            cat.stdin.flush()
            header = cat.stdout.readline().split()
            if len(header) != 3 or header[1] != b"blob":
                sys.exit(f"snapshot-tree: unexpected cat-file header {header!r}")
            data = cat.stdout.read(int(header[2]))
            cat.stdout.read(1)  # trailing newline after each object

            target = os.path.join(dest, path)
            os.makedirs(os.path.dirname(target), exist_ok=True)
            # O_EXCL | O_NOFOLLOW: never write through anything already there.
            fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
            with os.fdopen(fd, "wb") as f:
                f.write(data)
    finally:
        cat.stdin.close()
        cat.wait()


if __name__ == "__main__":
    main()
