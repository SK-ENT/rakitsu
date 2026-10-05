#!/usr/bin/env python3
"""Record docs/interactive/casts/monitor.json: /healthz, healthcheck, stop route, alerts.

Real binary + fake local model + a local sink that stands in for a webhook and
an ntfy server. Three alert sinks are configured: webhook (warn), ntfy (warn)
and a webhook that only takes critical alerts.
"""
import json
import os
import shutil
import sys
import time

from castlib import HERE, Demo

d = Demo("monitors: /healthz, healthcheck, stop route, alerts")


def until(fn, timeout=90):
    end = time.time() + timeout
    while time.time() < end:
        if fn():
            return True
        time.sleep(0.5)
    raise RuntimeError("timed out waiting")


def audit_lines():
    p = os.path.join(d.root, ".rakitsu", "wake", "monitor-log-watch.audit.jsonl")
    return [json.loads(x) for x in open(p) if x.strip()] if os.path.exists(p) else []


try:
    mon = os.path.join(d.root, "mon")
    shutil.copytree(os.path.join(HERE, "demo"), mon)
    os.makedirs(os.path.join(mon, "monitors", "log-watch", "logs"))
    open(os.path.join(mon, "monitors", "log-watch", "logs", "app.log"), "w").write("all quiet\n")
    d.start_fake_llm(4010)
    d.start([sys.executable, os.path.join(HERE, "webhook_sink.py"), "4011", os.path.join(d.root, "webhook.jsonl")], "webhook.log")
    d.start(["rakitsu", "serve", "--host", "127.0.0.1", "--port", "9105", "--config", "monitor.yaml",
             "--sessions-dir", os.path.join(d.root, "sessions")], "serve.log", cwd=mon,
            env={"ALERT_WEBHOOK_URL": "http://127.0.0.1:4011/hook", "ALERT_NTFY_URL": "http://127.0.0.1:4011/ntfy",
                 "ALERT_PAGER_URL": "http://127.0.0.1:4011/pager"})
    d.wait_http("http://127.0.0.1:9105/healthz")
    until(lambda: len(audit_lines()) >= 1)
    H = "http://127.0.0.1:9105"
    P = "monitor-log-watch"

    d.note("monitor.yaml lists env var NAMES for the alert URLs; the values come from the process environment")
    d.sh("grep -n 'env_refs' monitor.yaml", cwd=mon)
    d.note("/healthz: 200 while every monitor is ok")
    d.sh("curl -s -w ' (HTTP %%{http_code})\\n' %s/healthz" % H)
    d.note("rakitsu healthcheck is the Docker-style probe: exit 0 healthy, 1 otherwise")
    rc, _ = d.sh("rakitsu healthcheck --url %s/healthz" % H)
    d.exit_line(rc)
    d.note("stop the monitor on purpose. The stop route works by creating the kill-switch file")
    d.sh("curl -s -X POST %s/api/chat/%s/wake/stop" % (H, P))
    d.sh("ls ~/.rakitsu/wake/STOP")
    until(lambda: any(r.get("result") == "killed" for r in audit_lines()), 40)
    time.sleep(1)
    d.note("the loop saw the file at its next tick and stopped; the session stays open")
    d.sh("curl -s -w ' (HTTP %%{http_code})\\n' %s/healthz" % H)
    rc, _ = d.sh("rakitsu healthcheck --url %s/healthz" % H)
    d.exit_line(rc)
    d.note("resume is refused while the file exists")
    d.sh("curl -s -w ' (HTTP %%{http_code})\\n' -X POST %s/api/chat/%s/wake/resume" % (H, P))
    d.note("remove the file, then resume")
    d.sh("rm ~/.rakitsu/wake/STOP")
    d.sh("curl -s -X POST %s/api/chat/%s/wake/resume" % (H, P))
    k = len(audit_lines())
    until(lambda: len(audit_lines()) > k, 40)
    d.sh("curl -s -w ' (HTTP %%{http_code})\\n' %s/healthz" % H)

    d.note("now an alarm: the model calls send_alert (severity warn). Three sinks are configured")
    d.sh("grep -nE 'name:|type:|min_severity' monitors/log-watch.yaml | sed -n '/phone/,$p'", cwd=mon)
    d.sh("echo 'ERROR disk full' >> monitors/log-watch/logs/app.log", cwd=mon)
    until(lambda: any(r.get("event") == "task_end" for r in audit_lines()), 90)
    time.sleep(2)
    d.note("the webhook sink gets JSON; the ntfy sink gets the body as text plus Title and Priority headers")
    d.sh("cat webhook.jsonl", cwd=d.root)
    d.note("redaction: the home path became ~, the Bearer token and the sk- key became [redacted]")
    d.note("the pager sink only takes critical alerts, so it received nothing")
    d.sh("grep -c '/pager' webhook.jsonl", cwd=d.root)
    d.save("monitor.json")
finally:
    d.close()
