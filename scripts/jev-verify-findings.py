#!/usr/bin/env python3
"""scripts/jev-verify-findings.py — check each finding from
scripts/ci-review.sh's gpt-5.6-luna pass is GROUNDED in the diff before
scripts/post-review.py posts it to GitHub.

Reads the {"body", "comments": [{"path","line","body"}, ...]} JSON that
scripts/parse-findings.py already produced. For each comment, asks Jev one
typed noul question — "does the diff text shown actually contain the
file/line/code construct this finding is about?" — against TypeSafe AI's
System One API directly (no LLM agent in between; the question is already
fully determined, so there's nothing for an agent loop to decide).

Scope, deliberately narrow: this checks GROUNDING, not TRUTH. It catches a
reviewer citing code that doesn't actually exist in the diff (a pure
extraction/lookup task, well inside what a System One classifier can do
from the state it's given). It does NOT catch a finding whose cited code
is real but whose factual claim about that code is wrong — that requires
knowledge outside the diff (e.g. language/toolchain semantics), which
Jev has no way to check from state alone. A prior version of this script
asked the broader "is this claim true" question and, in a live test
(issue #634), missed exactly that kind of false positive — the `unix`
build-tag claim on #625/#93, which cited real code but drew a wrong
conclusion from it. Don't widen this back to a truth-check without first
finding a way to give Jev the actual missing fact (e.g. reference docs)
as part of its state, not just a broader-sounding question.

A low grounding score doesn't delete the finding — this is not a place to
silently lose a possibly-real bug — it prefixes the comment body with a
visible flag so a human reviewer knows to double check it.

Fails open: no TYPESAFE_API_KEY, no findings, or any Jev call error
(network, timeout, bad response) reprints the input JSON unchanged rather
than blocking the review pipeline on Jev being reachable. Never touches
GitHub itself — same convention as parse-findings.py and ci-review.sh.

Usage: jev-verify-findings.py <findings-json-file> <diff-file>
"""
import json
import os
import sys
import urllib.request

ENDPOINT = "https://api.typesafe.ai/v1/systemone"
API_KEY_ENV = "TYPESAFE_API_KEY"

# Below this, Jev thinks the finding is probably citing code that isn't
# actually in the diff. Starting point, not a calibrated cutoff — see
# issue #634's test-case step before trusting this in prod CI unsupervised.
CONFIDENCE_THRESHOLD = 0.35

REQUEST_TIMEOUT_SECONDS = 15

# Serial, one call per finding, up to REQUEST_TIMEOUT_SECONDS each — an
# unusually large review (or an unresponsive Jev endpoint) could otherwise
# eat most of the CI job's own timeout budget just on this verification
# pass. Findings beyond this cap are posted unverified rather than making
# the whole job wait on them — consistent with this script's fail-open
# design: unverified is the same outcome as "no TYPESAFE_API_KEY set" for
# those, not a new failure mode.
MAX_FINDINGS_TO_VERIFY = 15


def verify_one(path: str, line: int, body: str, diff_text: str, api_key: str) -> float:
    state = f"Code review finding on {path}:{line}\n\n{body}\n\nDiff under review:\n{diff_text}"
    payload = json.dumps(
        {
            "state": state,
            "model": "jev-latest",
            "questions": {
                "grounded": {
                    "type": "noul",
                    "instructions": (
                        "Does the diff text shown actually contain the specific "
                        "file, line, or code construct that this finding's claim "
                        "is about? Answer only about whether the cited code is "
                        "really present in the diff — not about whether the "
                        "finding's conclusion about that code is correct."
                    ),
                }
            },
        }
    ).encode("utf-8")
    req = urllib.request.Request(
        ENDPOINT,
        data=payload,
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT_SECONDS) as resp:
        data = json.load(resp)
    return float(data["answers"]["grounded"]["noul"])


def main() -> int:
    findings_path, diff_path = sys.argv[1], sys.argv[2]
    result = json.load(open(findings_path, encoding="utf-8"))

    api_key = os.environ.get(API_KEY_ENV, "")
    comments = result.get("comments") or []
    if not api_key or not comments:
        json.dump(result, sys.stdout)
        return 0

    diff_text = open(diff_path, encoding="utf-8").read()

    for comment in comments[:MAX_FINDINGS_TO_VERIFY]:
        try:
            score = verify_one(
                comment["path"], comment["line"], comment["body"], diff_text, api_key
            )
        except Exception as e:
            # Broad on purpose: this call reaches a third-party API whose
            # response shape isn't fully controlled (e.g. a `noul: null`
            # answer raises TypeError on the float() conversion, not caught
            # by a narrower tuple) — any failure here must degrade to
            # "pass this finding through unverified", never abort the
            # whole script. ci-review.sh runs this under `set -euo
            # pipefail` with no `set +e` wrapper, so an uncaught exception
            # here would fail the entire CI review step, not just this one
            # finding — exactly what "fails open" is supposed to prevent.
            print(
                f"WARN: Jev grounding check failed for {comment.get('path')}:{comment.get('line')}: {e}",
                file=sys.stderr,
            )
            continue
        if score < CONFIDENCE_THRESHOLD:
            comment["body"] = (
                f"_(possibly ungrounded per Jev, score={score:.2f} — the cited "
                f"code may not actually be present in this diff, double-check)_"
                f"\n\n{comment['body']}"
            )

    json.dump(result, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
