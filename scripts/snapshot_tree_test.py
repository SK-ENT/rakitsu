#!/usr/bin/env python3
"""Regression tests for scripts/snapshot-tree.py.

The full AI review reads the PR head from a snapshot directory. It first
used `git archive`, which honors the PR's own .gitattributes: a PR could
mark files `export-ignore` to hide them from the reviewer, or
`export-subst` to change what it reads.
The snapshot must hold every regular file exactly as committed, and never
a symlink.

Run: python3 -m unittest scripts.snapshot_tree_test
"""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("snapshot-tree.py")


def git(repo, *args):
    subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True)


class SnapshotTreeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.repo = root / "repo"
        self.snap = root / "snap"
        self.secret = root / "secret.env"
        self.secret.write_text("SECRET=do-not-leak\n")
        self.repo.mkdir()
        self.snap.mkdir()
        git(self.repo, "init", "-q")

        (self.repo / ".gitattributes").write_text(
            "hidden.go export-ignore\n"
            "subst.txt export-subst\n"
            "crlf.txt text eol=crlf\n"
        )
        (self.repo / "hidden.go").write_text("package hidden\n")
        (self.repo / "subst.txt").write_text("commit: $Format:%H$\n")
        (self.repo / "crlf.txt").write_text("one\ntwo\n")
        (self.repo / "sub").mkdir()
        (self.repo / "sub" / "run.sh").write_text("#!/bin/sh\necho hi\n")
        os.chmod(self.repo / "sub" / "run.sh", 0o755)
        os.symlink(self.secret, self.repo / "leak.txt")
        os.symlink(self.secret.parent, self.repo / "sub" / "dirlink")
        git(self.repo, "add", "-A")
        git(self.repo, "-c", "user.email=t@t", "-c", "user.name=t",
            "-c", "commit.gpgsign=false", "commit", "-qm", "t")

    def tearDown(self):
        self.tmp.cleanup()

    def snapshot(self):
        subprocess.run(["python3", str(SCRIPT), "HEAD", str(self.snap)],
                       cwd=self.repo, check=True, capture_output=True)

    def test_attributes_do_not_hide_or_alter_files(self):
        self.snapshot()
        self.assertEqual((self.snap / "hidden.go").read_text(), "package hidden\n")
        self.assertEqual((self.snap / "subst.txt").read_text(), "commit: $Format:%H$\n")
        self.assertEqual((self.snap / "crlf.txt").read_bytes(), b"one\ntwo\n")
        self.assertTrue((self.snap / ".gitattributes").is_file())

    def test_no_symlinks_and_no_secret(self):
        self.snapshot()
        links = [p for p in self.snap.rglob("*") if p.is_symlink()]
        self.assertEqual(links, [])
        self.assertFalse((self.snap / "leak.txt").exists())
        for p in self.snap.rglob("*"):
            if p.is_file():
                self.assertNotIn("do-not-leak", p.read_text(errors="replace"))

    def test_nested_and_executable_files_kept(self):
        self.snapshot()
        run = self.snap / "sub" / "run.sh"
        self.assertEqual(run.read_text(), "#!/bin/sh\necho hi\n")

    def test_file_set_matches_committed_regular_files(self):
        self.snapshot()
        out = subprocess.run(["git", "-C", self.repo, "ls-tree", "-r", "--name-only", "HEAD"],
                             check=True, capture_output=True, text=True).stdout.split()
        want = {p for p in out if p not in ("leak.txt", "sub/dirlink")}
        got = {str(p.relative_to(self.snap)) for p in self.snap.rglob("*") if p.is_file()}
        self.assertEqual(got, want)


if __name__ == "__main__":
    unittest.main()
