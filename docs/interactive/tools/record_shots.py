#!/usr/bin/env python3
"""Start the demo serve (fake model, one monitor, two chat configs), then run
shots.mjs against it to take web UI screenshots.

Needs Playwright for node (not a repo dependency):
  mkdir -p /tmp/pw && cd /tmp/pw && npm init -y && npm i playwright
  PW_NODE_PATH=/tmp/pw/node_modules python3 record_shots.py
"""
import json
import os
import shutil
import subprocess
import sys
import urllib.request

from castlib import HERE, Demo, OUT

d = Demo("web UI shots")
SHOTS = os.path.abspath(os.path.join(HERE, "..", "shots"))
try:
    mon = os.path.join(d.root, "mon")
    shutil.copytree(os.path.join(HERE, "demo"), mon)
    os.makedirs(os.path.join(mon, "monitors", "log-watch", "logs"))
    open(os.path.join(mon, "monitors", "log-watch", "logs", "app.log"), "w").write("service started\nall quiet\n")
    d.start_fake_llm(4010)
    d.start([sys.executable, os.path.join(HERE, "webhook_sink.py"), "4011", os.path.join(d.root, "hook.jsonl")], "webhook.log")
    d.start(["rakitsu", "serve", "--host", "127.0.0.1", "--port", "9104", "--config", "monitor.yaml",
             "--config-dir", mon, "--sessions-dir", os.path.join(d.root, "sessions")], "serve.log", cwd=mon,
            env={"ALERT_WEBHOOK_URL": "http://127.0.0.1:4011/hook", "ALERT_NTFY_URL": "http://127.0.0.1:4011/ntfy",
                 "ALERT_PAGER_URL": "http://127.0.0.1:4011/pager"})
    d.wait_http("http://127.0.0.1:9104/healthz")
    # Let the monitor see an alarm so the UI has a real timer-sourced turn to show.
    import time
    audit = os.path.join(d.root, ".rakitsu", "wake", "monitor-log-watch.audit.jsonl")
    time.sleep(12)
    open(os.path.join(mon, "monitors", "log-watch", "logs", "app.log"), "a").write("ERROR disk full\n")
    end = time.time() + 90
    while time.time() < end and not (os.path.exists(audit) and "task_end" in open(audit).read()):
        time.sleep(1)
    time.sleep(2)
    env = dict(os.environ)
    subprocess.check_call(["node", os.path.join(HERE, "shots.mjs"), "http://127.0.0.1:9104", SHOTS,
                           os.path.join(mon, "bridge-reviewer.yaml")], env=env)
finally:
    d.close()
