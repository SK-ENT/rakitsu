#!/usr/bin/env python3
"""Post a review built from scripts/ci-review.sh's JSON output to a PR,
attaching one inline comment per finding where possible.

GitHub's Reviews API rejects the WHOLE request if even one inline
comment's file/line doesn't match a line that's actually part of the
diff (a real risk since the model can misjudge a line number) — so on
that specific failure (HTTP 422) this retries once as a body-only
review, folding every finding into the body as a fallback rather than
silently dropping them.

A separate, transient failure class (observed live 2026-09-13, PR
SK-ENT/rakitsu#86): a GitHub gateway hiccup returns an empty or
non-JSON body, and `gh` itself fails client-side with "unexpected end
of JSON input" before any HTTP status is even visible, or GitHub itself
returns a bare 502/503/504. This is worth a couple of short retries —
see `_is_transient_failure` for exactly which failures qualify (a real
rejection — 401/403/404/422/429 — is never retried, see
`post_with_retry`).

A blind retry risks posting the same review twice if the original
request actually landed server-side despite us seeing an error. Two
earlier versions of this fix tried to detect that by diffing review
IDs or counts before/after a failed attempt — both are fundamentally
unable to tell "a review MY retry created" apart from "a review some
other, truly concurrent process created for the same bot and commit in
that window" (a real TOCTOU race, not just a theoretical one, per
review on this same PR). The actual fix: `post_with_retry` tags every
attempt's body with a unique, invisible marker (an HTML comment,
`<!-- post-review:<uuid> -->` — rendered as nothing by GitHub) before
the first POST, and before each retry checks whether a review
containing THAT EXACT marker already exists. That match is
unambiguous regardless of what else is happening concurrently — no
other process can produce the same uuid — so there is no race left to
bound by the workflow's concurrency settings or anything else.

Three follow-up findings on the marker fix itself: (1) the marker check
ran immediately after a failure, before the retry delay — if GitHub's
review actually lands a moment later than that check (any latency
between "the request was accepted" and "it's visible on a GET"), the
window it needs to appear in was too short, so the check now runs
AFTER the delay, giving eventual consistency the whole delay to catch
up, right before the next POST would otherwise fire; (2) the marker
was appended to the body with no length check, so a body already at or
near GitHub's review-body limit could tip over it purely from the
marker's own bytes — `_tag_body` now applies the length cap (and
truncates the ORIGINAL text, never the marker) for every post, so
`main()` no longer needs its own separate truncation logic for the 422
fallback path either; (3) even after the delay, a SINGLE point-in-time
check can still land during a genuine eventual-consistency lag on
GitHub's own side (a write can be accepted while a subsequent read
briefly still misses it on a lagging replica) — `_wait_for_marker` now
polls a few times with a short gap between checks instead of checking
once, meaningfully narrowing (not claiming to eliminate) that window.
GitHub's Reviews API has no idempotency-key mechanism, so nothing
short of that from GitHub's side can make this provably exact; this is
the practical mitigation available without one.

The review itself always posts as event "COMMENT" — it never requests
changes as a GitHub review state. Instead, once the review is posted,
this exits non-zero whenever the result carried at least one real
finding (result["comments"] is non-empty), which fails the Action's own
check (a red X in the PR's checks list) without touching GitHub's
review-approval mechanics or requiring branch protection to be
reconfigured. A skipped review (diff too large/empty) or a clean
NO FINDINGS both parse to an empty comments list (see
parse-findings.py) and exit 0, same as always.

Usage: post-review.py <result-json> <repo> <pr-number> <commit-sha>
"""
import json
import re
import subprocess
import sys
import time
import uuid

# GitHub's review body cap; the 422 fallback can inline every finding
# into one body, which a large finding set could exceed — and even the
# initial (non-fallback) body is uncapped coming out of
# parse-findings.py, which embeds the model's raw output verbatim on a
# parsing miss. Enforced centrally in `_tag_body`, applied to every post.
MAX_BODY_CHARS = 60000
_TRUNCATION_NOTE = "\n\n… (truncated, over GitHub's review body limit)"

# Seconds to wait before each retry of a transient POST failure. Short —
# this is a gateway hiccup, not an outage worth minutes of backoff — but
# more than one, since the first retry can land during the same blip.
RETRY_DELAYS = (3, 6)

# After a retry delay, how many times (and how far apart) to poll for the
# marker before concluding it isn't there. A single check can still lose
# a race against GitHub's own read-after-write lag; polling narrows that
# window further without pretending to close it entirely.
_MARKER_CHECK_POLLS = 3
_MARKER_CHECK_POLL_INTERVAL = 2


def post(repo: str, pr_number: str, payload: dict) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["gh", "api", "--method", "POST", f"repos/{repo}/pulls/{pr_number}/reviews", "--input", "-"],
        input=json.dumps(payload),
        text=True,
        capture_output=True,
    )


# HTTP statuses that are real, actionable rejections — never worth a
# retry, regardless of what shape the error message takes.
_NON_TRANSIENT_STATUSES = ("401", "403", "404", "422", "429")
# Statuses that ARE a gateway-style hiccup, worth a short retry.
_TRANSIENT_STATUSES = ("502", "503", "504")


def _is_transient_failure(stderr: str) -> bool:
    """True for a failure worth retrying: gh's own client-side JSON-parse
    error on an empty/garbled response (no HTTP status visible at all),
    or an explicit 502/503/504 gateway error. False for a real rejection
    (401/403/404/422/429) — a retry wouldn't fix any of those — and
    false for anything else unrecognized, erring toward NOT retrying an
    error shape we haven't seen rather than assuming it's transient."""
    if any(re.search(rf"\b{code}\b", stderr) for code in _NON_TRANSIENT_STATUSES):
        return False
    if "unexpected end of JSON input" in stderr:
        return True
    return any(re.search(rf"\b{code}\b", stderr) for code in _TRANSIENT_STATUSES)


def _review_with_marker_exists(repo: str, pr_number: str, commit_sha: str, marker: str) -> "bool | None":
    """True if some review from github-actions[bot] on this exact commit
    contains `marker` in its body. `marker` is a per-attempt UUID no
    other process could produce, so a match is unambiguous proof THIS
    specific attempt already landed — not a stale review, not one a
    concurrent process created. Fetches every review object
    one-per-line (`--jq '.[]'`) rather than filtering with a single
    aggregate `--jq` expression — `gh api --paginate` applies `--jq`
    separately to EACH page, so an aggregate expression like `length`
    silently miscounts across more than one page. Returns None if the
    check itself can't be run — the caller treats that as "can't
    confirm either way" rather than failing outright."""
    proc = subprocess.run(
        ["gh", "api", f"repos/{repo}/pulls/{pr_number}/reviews", "--paginate", "--jq", ".[]"],
        text=True,
        capture_output=True,
    )
    if proc.returncode != 0:
        return None
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            review = json.loads(line)
        except json.JSONDecodeError:
            continue
        if (
            review.get("commit_id") == commit_sha
            and review.get("user", {}).get("login") == "github-actions[bot]"
            and marker in (review.get("body") or "")
        ):
            return True
    return False


def _wait_for_marker(repo: str, pr_number: str, commit_sha: str, marker: str) -> "bool | None":
    """Poll `_review_with_marker_exists` a few times, with a short gap
    between checks, instead of checking once. A single check right after
    the retry delay can still lose a race against GitHub's own
    read-after-write lag (a write accepted while a subsequent read
    briefly still misses it) — polling narrows that window further.
    Stops early on a confirmed True, or on None (the check itself
    failing isn't something more polling fixes)."""
    for attempt in range(_MARKER_CHECK_POLLS):
        found = _review_with_marker_exists(repo, pr_number, commit_sha, marker)
        if found or found is None:
            return found
        if attempt < _MARKER_CHECK_POLLS - 1:
            time.sleep(_MARKER_CHECK_POLL_INTERVAL)
    return False


def _tag_body(body: str, marker: str) -> str:
    """Append `marker` to `body`, truncating the ORIGINAL text first if
    the combined result would exceed GitHub's review body limit. The
    marker must always survive intact — retry detection depends on
    finding it verbatim — so truncation only ever shortens `body`."""
    suffix = f"\n\n{marker}"
    budget = MAX_BODY_CHARS - len(suffix)
    if len(body) > budget:
        body = body[: budget - len(_TRUNCATION_NOTE)] + _TRUNCATION_NOTE
    return body + suffix


def post_with_retry(repo: str, pr_number: str, commit_sha: str, payload: dict) -> subprocess.CompletedProcess:
    """post(), retrying a transient failure (see `_is_transient_failure`)
    a couple of times. See the module docstring for why every attempt's
    body carries a unique marker, polled for (after the retry delay, not
    before it) via `_wait_for_marker` rather than diffing review
    IDs/counts or checking just once."""
    marker = f"<!-- post-review:{uuid.uuid4().hex} -->"
    tagged_payload = {**payload, "body": _tag_body(payload["body"], marker)}
    proc = post(repo, pr_number, tagged_payload)
    for delay in RETRY_DELAYS:
        if proc.returncode == 0 or not _is_transient_failure(proc.stderr):
            return proc
        sys.stderr.write(
            f"Review POST failed transiently ({proc.stderr.strip() or 'no error output'}); "
            f"waiting {delay}s before checking/retrying...\n"
        )
        time.sleep(delay)
        if _wait_for_marker(repo, pr_number, commit_sha, marker):
            sys.stderr.write(
                "This exact attempt's review is now present (marker match) — not retrying.\n"
            )
            return subprocess.CompletedProcess(proc.args, 0, stdout=proc.stdout, stderr=proc.stderr)
        proc = post(repo, pr_number, tagged_payload)
    return proc


def main() -> int:
    result_path, repo, pr_number, commit_sha = sys.argv[1:5]
    result = json.load(open(result_path, encoding="utf-8"))
    comments = result.get("comments", [])

    payload = {
        "commit_id": commit_sha,
        "event": "COMMENT",
        "body": result["body"],
        "comments": [{"path": c["path"], "line": c["line"], "body": c["body"]} for c in comments],
    }
    proc = post_with_retry(repo, pr_number, commit_sha, payload)

    if proc.returncode != 0 and comments and "422" in proc.stderr:
        sys.stderr.write(
            "Inline comment post rejected (HTTP 422 — likely a file/line "
            f"the model named that isn't actually part of the diff): "
            f"retrying as a body-only review:\n{proc.stderr}\n"
        )
        fallback_body = result["body"]
        for c in comments:
            fallback_body += f"\n\n**{c['path']}:{c['line']}** — {c['body']}"
        # No length cap here — post_with_retry's _tag_body enforces
        # MAX_BODY_CHARS (marker included) for every post, this one too.
        proc = post_with_retry(repo, pr_number, commit_sha, {"commit_id": commit_sha, "event": "COMMENT", "body": fallback_body})

    sys.stdout.write(proc.stdout)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        return proc.returncode

    if comments:
        plural = "" if len(comments) == 1 else "s"
        sys.stderr.write(
            f"Review posted with {len(comments)} finding{plural} — "
            "failing this check so they get a look before merge.\n"
        )
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
