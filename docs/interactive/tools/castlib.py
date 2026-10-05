"""Shared helpers for the demo recorders (standard library only).

A cast is a small JSON file:
  {"version": 1, "title": "...", "width": 100, "height": 28,
   "events": [[seconds, "o", "text to print"], ...]}
Events are the real output of real commands. Only the pauses are shaped:
typing is simulated at a steady speed and long waits are shortened.
"""
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
OUT = os.path.abspath(os.path.join(HERE, "..", "casts"))

DIM = "\x1b[2m"
GREEN = "\x1b[32m"
CYAN = "\x1b[36m"
RESET = "\x1b[0m"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class Demo:
    """A scratch directory, a built binary and a cast recorder."""

    def __init__(self, title):
        self.root = os.path.realpath(tempfile.mkdtemp(prefix="rk-demo-"))
        self.bin = os.path.join(self.root, "bin")
        os.makedirs(self.bin)
        self.events = []
        self.t = 0.0
        self.title = title
        self.procs = []
        exe = os.environ.get("RAKITSU_BIN")
        if not exe:
            exe = os.path.join(self.root, "rakitsu-real")
            subprocess.check_call(["go", "build", "-o", exe, "./cmd/rakitsu"], cwd=REPO)
        os.symlink(exe, os.path.join(self.bin, "rakitsu"))
        ask = os.path.join(REPO, "scripts", "rakitsu-ask")
        if os.path.exists(ask):
            os.symlink(ask, os.path.join(self.bin, "rakitsu-ask"))
        self.env = dict(os.environ)
        self.env["PATH"] = self.bin + os.pathsep + self.env.get("PATH", "")
        self.env["HOME"] = self.root  # keep ~/.rakitsu inside the scratch dir
        for k in list(self.env):
            if k.endswith("_API_KEY") or k.endswith("_TOKEN"):
                del self.env[k]

    # --- sanitising -------------------------------------------------
    def clean(self, s):
        s = s.replace(self.root, "~")
        s = re.sub(r"/Users/[^/\s]+", "~", s)
        s = re.sub(r"/private/var/folders/\S+", "<tmp>", s)
        return s

    # --- cast writing -----------------------------------------------
    def emit(self, text, dt=0.0):
        self.t += dt
        self.events.append([round(self.t, 3), "o", text.replace("\r\n", "\n").replace("\n", "\r\n")])

    def note(self, text):
        self.emit(DIM + "# " + text + RESET + "\n", 0.5)

    def type_cmd(self, cmd):
        self.emit(GREEN + "$ " + RESET, 0.4)
        for ch in cmd:
            self.emit(ch, 0.025)
        self.emit("\n", 0.3)

    def pause(self, seconds):
        self.t += seconds

    def sh(self, cmd, show=None, expect=None, env=None, cwd=None):
        """Type `show or cmd`, run `cmd` for real, print its real output."""
        self.type_cmd(show or cmd)
        e = dict(self.env)
        e.update(env or {})
        p = subprocess.run(cmd, shell=True, env=e, cwd=cwd or self.root,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        out = self.clean(p.stdout)
        first = True
        for line in out.splitlines():
            self.emit(line + "\n", 0.45 if first else 0.03)
            first = False
        return p.returncode, out

    def exit_line(self, code):
        self.emit(DIM + "[exit %d]" % code + RESET + "\n", 0.1)

    def save(self, name):
        os.makedirs(OUT, exist_ok=True)
        path = os.path.join(OUT, name)
        with open(path, "w") as f:
            json.dump({"version": 1, "title": self.title, "width": 100, "height": 28,
                       "events": self.events}, f, separators=(",", ":"))
        print("wrote", path, os.path.getsize(path), "bytes")

    # --- processes --------------------------------------------------
    def start(self, argv, log, env=None, cwd=None):
        e = dict(self.env)
        e.update(env or {})
        lf = open(os.path.join(self.root, log), "w")
        p = subprocess.Popen(argv, env=e, cwd=cwd or self.root, stdout=lf, stderr=subprocess.STDOUT)
        self.procs.append(p)
        return p

    def wait_http(self, url, timeout=30):
        end = time.time() + timeout
        while time.time() < end:
            try:
                urllib.request.urlopen(url, timeout=2).read()
                return
            except Exception as ex:
                if hasattr(ex, "code"):  # an HTTP error still means it is up
                    return
                time.sleep(0.3)
        raise RuntimeError("not up: " + url)

    def start_fake_llm(self, port):
        self.start([sys.executable, os.path.join(HERE, "fake_llm.py"), str(port)], "fake-llm.log")
        self.wait_http("http://127.0.0.1:%d/count" % port)

    def llm_calls(self, port):
        return json.loads(urllib.request.urlopen("http://127.0.0.1:%d/count" % port).read())["calls"]

    def close(self):
        for p in self.procs:
            p.terminate()
        for p in self.procs:
            try:
                p.wait(5)
            except Exception:
                p.kill()
        if not os.environ.get("KEEP_DEMO"):
            shutil.rmtree(self.root, ignore_errors=True)
