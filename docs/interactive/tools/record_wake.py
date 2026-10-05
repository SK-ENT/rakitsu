#!/usr/bin/env python3
"""Record docs/interactive/casts/wake.json: wake timer, healthz, alert, start_task.

Real binary + fake local model (fake_llm.py) + local webhook sink. The demo
config uses 10 second ticks so the whole run takes about two minutes; waits in
the cast are shortened, the output is not.
"""
import json
import os
import shutil
import sys
import time
import urllib.request

from castlib import HERE, Demo

d = Demo("wake timer: free ticks, one turn on an alarm, caps, kill switch")


def until(fn, timeout=90):
    end = time.time() + timeout
    while time.time() < end:
        if fn():
            return True
        time.sleep(0.5)
    raise RuntimeError("timed out waiting")


def audit_lines():
    p = os.path.join(d.root, ".rakitsu", "wake", "monitor-log-watch.audit.jsonl")
    if not os.path.exists(p):
        return []
    return [json.loads(x) for x in open(p) if x.strip()]


def skip(real_s, cast_s):
    """Wait real time, show only cast_s of it."""
    time.sleep(real_s)
    d.pause(cast_s)


try:
    mon = os.path.join(d.root, "mon")
    shutil.copytree(os.path.join(HERE, "demo"), mon)
    os.makedirs(os.path.join(mon, "monitors", "log-watch", "logs"))
    log = os.path.join(mon, "monitors", "log-watch", "logs", "app.log")
    open(log, "w").write("service started\nall quiet\n")
    hook_log = os.path.join(d.root, "webhook.jsonl")

    d.start_fake_llm(4010)
    d.start([sys.executable, os.path.join(HERE, "webhook_sink.py"), "4011", hook_log], "webhook.log")
    d.start(["rakitsu", "serve", "--host", "127.0.0.1", "--port", "9102", "--config", "monitor.yaml",
             "--sessions-dir", os.path.join(d.root, "sessions")], "serve.log", cwd=mon,
            env={"ALERT_WEBHOOK_URL": "http://127.0.0.1:4011/hook", "ALERT_NTFY_URL": "http://127.0.0.1:4011/ntfy",
                 "ALERT_PAGER_URL": "http://127.0.0.1:4011/pager"})
    d.wait_http("http://127.0.0.1:9102/healthz")
    until(lambda: len(audit_lines()) >= 1)

    W = os.path.join(mon)
    calls = "curl -s http://127.0.0.1:9101/count"  # placeholder, replaced below
    CNT = "curl -s http://127.0.0.1:4010/count"
    AUD = "cat ~/.rakitsu/wake/monitor-log-watch.audit.jsonl"
    SHORT = ("python3 -c \"import json,sys\n"
             "for l in sys.stdin:\n"
             " r=json.loads(l)\n"
             " print({k:v for k,v in r.items() if k in ('event','tick','outcome','suppressed','reason','level','result','status')})\"")

    def show_audit(cmd_label="rakitsu-audit"):
        # Print the audit file as short one-liners (the same fields, compact).
        d.type_cmd("cat ~/.rakitsu/wake/monitor-log-watch.audit.jsonl")
        for r in audit_lines():
            keep = {k: v for k, v in r.items() if k in
                    ("event", "tick", "outcome", "suppressed", "reason", "level", "result", "status", "config", "task_id")}
            d.emit(json.dumps(keep, separators=(",", ":")) + "\n", 0.05)

    def calls_now():
        d.type_cmd(CNT)
        d.emit("model calls so far: %d\n" % d.llm_calls(4010), 0.3)

    d.note("serve autostarts the monitor from monitor.yaml: session monitor-log-watch")
    d.sh("curl -s -w ' (HTTP %{http_code})\\n' http://127.0.0.1:9102/healthz")
    d.note("the log is quiet. Ticks run free checks only; watch the model call count")
    until(lambda: len([r for r in audit_lines() if r.get("event") == "tick"]) >= 3, 60)
    show_audit()
    calls_now()
    d.note("quiet ticks back off: next_tick_in_seconds is 20 although the base interval is 10 (x1.5 per quiet tick, max 20 here)")
    d.sh("curl -s http://127.0.0.1:9102/api/chat/monitor-log-watch/wake/status")

    d.note("now the log gets an ERROR line")
    d.sh("echo 'ERROR disk full' >> monitors/log-watch/logs/app.log", cwd=mon)
    n0 = len(audit_lines())
    until(lambda: any(r.get("event") == "escalate" for r in audit_lines()))
    until(lambda: any(r.get("event") == "task_end" for r in audit_lines()), 60)
    skip(3, 1)
    show_audit()
    calls_now()
    d.note("the alert reached the webhook (path redaction: /Users/... became ~)")
    d.sh("cat %s" % hook_log, show="cat webhook.jsonl")

    d.note("the alarm is still there on the next tick: same alarm, no new turn")
    k = len([r for r in audit_lines() if r.get("event") == "tick"])
    until(lambda: len([r for r in audit_lines() if r.get("event") == "tick"]) > k, 40)
    show_audit()
    calls_now()

    d.note("status after the alarm: interval is back to the base, 1 of 2 turns used this hour")
    d.sh("curl -s http://127.0.0.1:9102/api/chat/monitor-log-watch/wake/status")

    def ticks():
        return len([r for r in audit_lines() if r.get("event") == "tick"])

    def escalations():
        return len([r for r in audit_lines() if r.get("event") == "escalate"])

    d.note("clear the log: one quiet tick re-arms escalation")
    d.sh(": > monitors/log-watch/logs/app.log", cwd=mon)
    k = ticks()
    until(lambda: ticks() > k + 0 and audit_lines()[-1].get("outcome") == "quiet", 40)
    d.note("a NEW alarm: second turn of the hour")
    d.sh("echo 'ERROR timeout' >> monitors/log-watch/logs/app.log", cwd=mon)
    e = escalations()
    until(lambda: escalations() > e, 40)
    until(lambda: len([r for r in audit_lines() if r.get("event") == "task_end"]) >= 2, 60)
    d.note("clear again, then a third alarm: the hourly cap (2) blocks it, no model call")
    d.sh(": > monitors/log-watch/logs/app.log", cwd=mon)
    until(lambda: audit_lines()[-1].get("outcome") == "quiet", 40)
    d.sh("echo 'ERROR again' >> monitors/log-watch/logs/app.log", cwd=mon)
    until(lambda: any(r.get("suppressed") == "hourly_cap" for r in audit_lines()), 40)
    show_audit()
    calls_now()

    d.note("kill switch: create the STOP file; the loop stops at its next tick, the session stays")
    d.sh("touch ~/.rakitsu/wake/STOP")
    until(lambda: any(r.get("result") == "killed" for r in audit_lines()), 40)
    skip(1, 1)
    d.sh("tail -n 1 ~/.rakitsu/wake/monitor-log-watch.audit.jsonl")
    d.sh("curl -s -w ' (HTTP %{http_code})\\n' http://127.0.0.1:9102/healthz")
    d.note("resume is refused (409) while the file exists")
    d.sh("curl -s -o /dev/null -w 'HTTP %{http_code}\\n' -X POST http://127.0.0.1:9102/api/chat/monitor-log-watch/wake/resume")
    d.sh("rm ~/.rakitsu/wake/STOP")
    d.sh("curl -s -X POST http://127.0.0.1:9102/api/chat/monitor-log-watch/wake/resume")
    d.save("wake.json")
finally:
    d.close()
