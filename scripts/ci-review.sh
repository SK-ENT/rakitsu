#!/usr/bin/env bash
# scripts/ci-review.sh — whole-diff PR review via rakitsu against direct
# OpenAI, for .github/workflows/ai-review.yml.
#
# Writes a JSON review result to <output-file>:
#   {"body": "<preamble + summary>", "comments": [{"path","line","body"}, ...]}
# scripts/post-review.py turns this into a GitHub PR review — one inline
# comment per finding, falling back to a body-only review if GitHub
# rejects an inline anchor. This script never posts anything itself, and
# nothing in the diff/findings text (untrusted PR content) is ever
# shell-interpolated — it only ever flows through files into
# scripts/parse-findings.py's own string/JSON handling.
#
# Usage: scripts/ci-review.sh <full|incremental> <base-ref> <head-ref> <output-file> <pr-base-ref>
set -euo pipefail

MAX_DIFF_BYTES=120000  # kept under Linux's per-argv MAX_ARG_STRLEN (128 KiB):
# the whole diff becomes one argv element to `rakitsu run` below, and
# Linux's execve() rejects any single argument over 131072 bytes
# regardless of ulimit/ARG_MAX — a higher cap here would let some diffs
# through that then fail with a bare "Argument list too long".

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RAKITSU_BIN="${RAKITSU_BIN:-rakitsu}"

if [ $# -ne 5 ]; then
  echo "Usage: $0 <full|incremental> <base-ref> <head-ref> <output-file> <pr-base-ref>" >&2
  exit 1
fi
MODE="$1"; BASE="$2"; HEAD="$3"; OUTPUT="$4"; PR_BASE="$5"
# PR_BASE is the PR's CURRENT resolved base (its target branch's tip right
# now — what full mode's own $BASE already is). Only incremental mode uses
# it, as a file-scope filter — see below.

# Full reviews read the whole repo at the PR head (read-only tools);
# incremental follow-ups stay diff-only.
case "$MODE" in
  full)
    SCOPE_TEXT="the full PR diff, with read-only access to the whole repo at the PR head"
    CONFIG="$SCRIPT_DIR/review-config-openai-full.yaml" ;;
  incremental)
    SCOPE_TEXT="the diff since the last commit only (follow-up review)"
    CONFIG="$SCRIPT_DIR/review-config-openai.yaml" ;;
  *) echo "FATAL: mode must be 'full' or 'incremental', got: $MODE" >&2; exit 1 ;;
esac
# The preamble names the model; read it from the config so the label can't
# drift from what actually ran (it once said gpt-5.6-luna after the switch).
REVIEW_MODEL=$(sed -n 's/^[[:space:]]*model:[[:space:]]*\([^[:space:]#]*\).*/\1/p' "$CONFIG" | head -1)

# `^{commit}` matters: a bare 40-hex string always "verifies" under
# `git rev-parse --verify` whether or not the object actually exists
# locally (git accepts it as a well-formed name without checking the
# object database) — the `^{commit}` dereference is what actually forces
# the lookup, so a genuinely missing object fails here with a clear
# message instead of a bare `git diff` crash three lines down.
git rev-parse --verify --quiet "$BASE^{commit}" >/dev/null || { echo "FATAL: bad ref (not fetched locally?): $BASE" >&2; exit 1; }
git rev-parse --verify --quiet "$HEAD^{commit}" >/dev/null || { echo "FATAL: bad ref (not fetched locally?): $HEAD" >&2; exit 1; }
git rev-parse --verify --quiet "$PR_BASE^{commit}" >/dev/null || { echo "FATAL: bad ref (not fetched locally?): $PR_BASE" >&2; exit 1; }
: "${OPENAI_API_KEY:?OPENAI_API_KEY not set}"

if [ "$MODE" = "full" ]; then
  diff_content=$(git diff "$BASE...$HEAD")
else
  # A plain two-dot diff between the last-reviewed commit and the new head
  # can include content that ISN'T actually part of this PR: pushing a
  # merge of the target branch into the PR branch (bringing in commits
  # merged elsewhere since this PR opened) makes files identical to the
  # target branch show up as "changed since last push", even though
  # they're unchanged relative to the PR's own (now-updated) base — GitHub
  # agrees with the latter, and rejects an inline comment anchored to a
  # line it doesn't consider part of the PR's diff (HTTP 422). Restrict to
  # paths that are ALSO part of the PR's actual current diff (three-dot
  # against PR_BASE) so a finding can only be anchored where GitHub will
  # actually accept it.
  pr_files=()
  while IFS= read -r f; do pr_files+=("$f"); done < <(git diff --name-only "$PR_BASE...$HEAD")
  if [ "${#pr_files[@]}" -eq 0 ]; then
    diff_content=""
  else
    diff_content=$(git diff "$BASE" "$HEAD" -- "${pr_files[@]}")
  fi
fi
diff_bytes=$(LC_ALL=C printf '%s' "$diff_content" | wc -c)

preamble() {
  cat <<EOF
## Automated review

This review was generated automatically by this repo's automated review
pipeline, not a human — it ran rakitsu against openai/${REVIEW_MODEL:-unknown-model} over
$SCOPE_TEXT, reading code as text only, no code
execution. Treat findings as a starting point to verify, not a final word.

---
EOF
}

# Body-only JSON result, no inline comments. $1 here is built only from
# trusted local text (SCOPE_TEXT/BASE/HEAD, which are validated git SHAs,
# never raw diff/PR content), so passing it through argv is safe.
write_body_only() {
  local msg
  msg="$(preamble)

$1"
  python3 -c '
import json, sys
json.dump({"body": sys.argv[1], "comments": []}, sys.stdout)
' "$msg"
}

if [ "$diff_bytes" -eq 0 ]; then
  write_body_only "No changes found between $BASE and $HEAD." > "$OUTPUT"
  exit 0
fi

if [ "$diff_bytes" -gt "$MAX_DIFF_BYTES" ]; then
  write_body_only "Diff is $diff_bytes bytes, over the $MAX_DIFF_BYTES-byte auto-review cap — skipped. Review this one by hand, or split it into smaller PRs." > "$OUTPUT"
  exit 0
fi

query="DIFF ($SCOPE_TEXT, base=$BASE, head=$HEAD):

$diff_content"

TMP_STDERR=$(mktemp)
FINDINGS_TXT=$(mktemp)
PREAMBLE_TXT=$(mktemp)
PARSED_JSON=$(mktemp)
DIFF_TXT=$(mktemp)
SNAPSHOT=""
trap 'rm -f "$TMP_STDERR" "$FINDINGS_TXT" "$PREAMBLE_TXT" "$PARSED_JSON" "$DIFF_TXT"; if [ -n "$SNAPSHOT" ]; then rm -rf "$SNAPSHOT"; fi' EXIT

# Full mode: the PR head as plain files in a fresh temp dir, for the
# reviewer's read-only fs tools (fenced to it via --workdir). snapshot-tree.py
# copies raw blobs — not `git archive`, which applies the PR's own
# .gitattributes and would let a PR hide or rewrite files the reviewer
# reads. It writes no symlinks, so no path in the snapshot leads outside it
# (the fs tool's fence also resolves symlinks). Nothing from the PR is ever
# executed or checked out next to these scripts.
run_args=(--no-hub)
if [ "$MODE" = "full" ]; then
  SNAPSHOT=$(mktemp -d)
  python3 "$SCRIPT_DIR/snapshot-tree.py" "$HEAD" "$SNAPSHOT"
  run_args+=(--workdir "$SNAPSHOT")
fi

set +e
raw_output=$("$RAKITSU_BIN" run "$CONFIG" "$query" "${run_args[@]}" 2>"$TMP_STDERR")
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  echo "FATAL: rakitsu run failed (exit $rc):" >&2
  cat "$TMP_STDERR" >&2
  exit 1
fi

# rakitsu run's stdout wraps the actual answer between a "Result (took...)"
# header and a closing ruler of only U+2501 (━) characters — everything
# else (config path, session ID, "Executing agent...") is CLI scaffolding
# that must not reach a public PR comment.
findings=$(printf '%s\n' "$raw_output" | awk '
  /^Result \(took/ { in_result=1; next }
  in_result && /^━+$/ { if (started==1) exit; started=1; next }
  in_result && started==1 { print }
')

if [ -z "$findings" ]; then
  echo "FATAL: could not extract Result content from rakitsu output:" >&2
  echo "$raw_output" >&2
  exit 1
fi

preamble > "$PREAMBLE_TXT"
printf '%s\n' "$findings" > "$FINDINGS_TXT"
python3 "$SCRIPT_DIR/parse-findings.py" "$PREAMBLE_TXT" "$FINDINGS_TXT" > "$PARSED_JSON"

# Per-finding Jev verification pass. Fails open on its own —
# see scripts/jev-verify-findings.py's docstring — no TYPESAFE_API_KEY set,
# or any Jev call error, reprints $PARSED_JSON unchanged. Never blocks the
# review on Jev being reachable.
printf '%s' "$diff_content" > "$DIFF_TXT"
python3 "$SCRIPT_DIR/jev-verify-findings.py" "$PARSED_JSON" "$DIFF_TXT" > "$OUTPUT"
