#!/usr/bin/env python3
"""Regression tests for scripts/post-review.py's retry behavior.

Reproduces the failure seen on real PRs (e.g. paupawsan/rakitsu#86,
2026-09-13): `gh api ... reviews` occasionally comes back from a GitHub
gateway hiccup with an empty/non-JSON body, and `gh` itself then fails
client-side with "unexpected end of JSON input" — a transient failure
distinct from the 422 (bad inline-comment anchor) case this script
already handled.

Also covers gaps the AI reviewer itself found across three rounds on
this same fix (2026-09-13), the last of which is why the duplicate-post
guard is marker-based rather than ID/count-diff based: diffing review
IDs or counts before/after a failed attempt can't tell "a review MY
retry created" apart from "a review some other, truly
concurrent process created for the same bot and commit in that window"
— a real TOCTOU race. A per-attempt UUID marker embedded in the review
body removes the ambiguity entirely: a match is unambiguous proof of
THIS specific attempt, regardless of what else happened concurrently.

Earlier, narrower findings this suite still guards against:
- The transient/non-transient split must not lump real, actionable
  rejections (401/403/404/429) in with genuine gateway hiccups
  (502/503/504, or gh's own empty-response parse failure).
- `gh api --paginate --jq` applies the jq filter per page, so a single
  aggregate jq expression silently miscounts across multiple pages —
  filtering must happen after collecting all pages.

post-review.py has a hyphen in its filename (matches the CLI-usage
convention of every other scripts/*.sh in this repo), so it can't be
`import`ed normally — load it via importlib, same trick anyone
extending this test file will need too.

Run: python3 -m unittest scripts.post_review_test
"""
import importlib.util
import json
import re
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

_SPEC = importlib.util.spec_from_file_location(
    "post_review", Path(__file__).parent / "post-review.py"
)
post_review = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(post_review)


def _proc(returncode, stdout="", stderr=""):
    return subprocess.CompletedProcess(args=["gh"], returncode=returncode, stdout=stdout, stderr=stderr)


def _reviews_stdout(*reviews):
    """Build the stdout `gh api --jq '.[]'` would produce: one compact
    JSON object per line, exactly as it looks across multiple pages."""
    return "\n".join(json.dumps(r) for r in reviews)


def _bot_review(review_id, commit="deadbeef", body=""):
    return {"id": review_id, "commit_id": commit, "body": body, "user": {"login": "github-actions[bot]"}}


OTHER_REVIEW = {"id": "other-1", "commit_id": "deadbeef", "body": "", "user": {"login": "someone-else"}}


class IsTransientFailureTest(unittest.TestCase):
    def test_gh_client_side_parse_failure_is_transient(self):
        self.assertTrue(post_review._is_transient_failure("unexpected end of JSON input"))

    def test_gateway_5xx_is_transient(self):
        for code in ("502", "503", "504"):
            with self.subTest(code=code):
                self.assertTrue(post_review._is_transient_failure(f"gh: Server Error (HTTP {code})"))

    def test_422_is_not_transient(self):
        self.assertFalse(post_review._is_transient_failure("gh: Validation Failed (HTTP 422)"))

    def test_auth_and_rate_limit_errors_are_not_transient(self):
        for code in ("401", "403", "404", "429"):
            with self.subTest(code=code):
                self.assertFalse(post_review._is_transient_failure(f"gh: Not Found (HTTP {code})"))

    def test_unrecognized_error_shape_is_not_treated_as_transient(self):
        # Erring toward NOT retrying an error we don't recognize is safer
        # than retrying something that might be a real, permanent rejection.
        self.assertFalse(post_review._is_transient_failure("gh: some new kind of error we've never seen"))


class ReviewWithMarkerExistsTest(unittest.TestCase):
    def test_true_when_a_bot_review_on_this_commit_contains_the_marker(self):
        stdout = _reviews_stdout(OTHER_REVIEW, _bot_review("r1", body="hello <!-- post-review:abc123 --> world"))
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertTrue(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_false_when_no_review_has_the_marker(self):
        stdout = _reviews_stdout(_bot_review("r1", body="unrelated review"))
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertFalse(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_false_when_marker_belongs_to_a_different_commit(self):
        stdout = _reviews_stdout(_bot_review("r1", commit="other-sha", body="<!-- post-review:abc123 -->"))
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertFalse(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_false_when_marker_belongs_to_a_different_user(self):
        stdout = _reviews_stdout({"id": "r1", "commit_id": "deadbeef", "body": "<!-- post-review:abc123 -->", "user": {"login": "someone-else"}})
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertFalse(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_none_when_the_check_itself_fails(self):
        with patch.object(subprocess, "run", return_value=_proc(1, stderr="network error")):
            self.assertIsNone(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_finds_the_marker_across_multiple_pages(self):
        # Simulates gh's actual --paginate --jq '.[]' output: every
        # review object on its own line, regardless of which page it
        # came from — no per-page aggregation to get wrong.
        stdout = _reviews_stdout(
            OTHER_REVIEW,
            _bot_review("r1", body="no marker here"),
            _bot_review("r2", body="<!-- post-review:abc123 -->"),
        )
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertTrue(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))

    def test_ignores_unparseable_lines(self):
        stdout = "not json\n" + _reviews_stdout(_bot_review("r1", body="<!-- post-review:abc123 -->"))
        with patch.object(subprocess, "run", return_value=_proc(0, stdout=stdout)):
            self.assertTrue(post_review._review_with_marker_exists("o/r", "1", "deadbeef", "<!-- post-review:abc123 -->"))


class TagBodyTest(unittest.TestCase):
    def test_appends_marker_to_a_short_body_unchanged(self):
        result = post_review._tag_body("some findings", "<!-- post-review:abc -->")
        self.assertEqual(result, "some findings\n\n<!-- post-review:abc -->")

    def test_truncates_an_oversized_body_but_keeps_the_marker_intact(self):
        marker = "<!-- post-review:abc -->"
        oversized = "x" * (post_review.MAX_BODY_CHARS + 5000)
        result = post_review._tag_body(oversized, marker)
        self.assertLessEqual(len(result), post_review.MAX_BODY_CHARS)
        # The marker must survive verbatim — retry detection depends on it.
        self.assertTrue(result.endswith(f"\n\n{marker}"))
        self.assertIn(post_review._TRUNCATION_NOTE, result)

    def test_a_body_exactly_at_the_original_cap_still_leaves_room_for_the_marker(self):
        # Reproduces the reviewer's finding: appending the marker with no
        # length check could push an already-near-the-limit body over.
        marker = "<!-- post-review:abc -->"
        body = "x" * post_review.MAX_BODY_CHARS
        result = post_review._tag_body(body, marker)
        self.assertLessEqual(len(result), post_review.MAX_BODY_CHARS)
        self.assertTrue(result.endswith(f"\n\n{marker}"))


class PostWithRetryTest(unittest.TestCase):
    def _marker_from(self, mock_post):
        """Extract the marker post_with_retry tagged onto the payload it
        actually sent, so a test can assert on it without hardcoding a uuid."""
        body = mock_post.call_args_list[0].args[2]["body"]
        match = re.search(r"<!-- post-review:[0-9a-f]{32} -->", body)
        self.assertIsNotNone(match, f"no marker found in posted body: {body!r}")
        return match.group(0)

    def test_succeeds_immediately_without_checking_for_a_marker(self):
        with patch.object(post_review, "post", return_value=_proc(0, stdout="ok")) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists") as mock_check:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 0)
        mock_post.assert_called_once()
        mock_check.assert_not_called()

    def test_tags_the_payload_body_with_a_marker_without_losing_the_original_text(self):
        with patch.object(post_review, "post", return_value=_proc(0, stdout="ok")) as mock_post:
            post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "original findings text"})
        sent_body = mock_post.call_args_list[0].args[2]["body"]
        self.assertIn("original findings text", sent_body)
        self.assertRegex(sent_body, r"<!-- post-review:[0-9a-f]{32} -->")

    def test_does_not_retry_on_422_leaves_it_to_the_existing_fallback(self):
        with patch.object(post_review, "post", return_value=_proc(1, stderr="gh: Validation Failed (HTTP 422)")) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists") as mock_check:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 1)
        mock_post.assert_called_once()
        mock_check.assert_not_called()

    def test_does_not_retry_a_real_rejection(self):
        with patch.object(post_review, "post", return_value=_proc(1, stderr="gh: Bad credentials (HTTP 401)")) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists") as mock_check:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 1)
        mock_post.assert_called_once()
        mock_check.assert_not_called()

    def test_retries_with_the_same_marker_and_recovers(self):
        responses = [
            _proc(1, stderr="unexpected end of JSON input"),
            _proc(0, stdout="posted"),
        ]
        with patch.object(post_review, "post", side_effect=responses) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists", return_value=False) as mock_check, \
                patch.object(post_review.time, "sleep") as mock_sleep:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(mock_post.call_count, 2)
        mock_sleep.assert_called_once()
        mock_check.assert_called_once()
        # Both attempts must carry the exact same marker.
        first_body = mock_post.call_args_list[0].args[2]["body"]
        second_body = mock_post.call_args_list[1].args[2]["body"]
        self.assertEqual(first_body, second_body)

    def test_stops_retrying_once_this_attempts_own_marker_is_found(self):
        # The exact scenario the marker exists for: the attempt's
        # response was lost, but the review actually landed — and a
        # marker match is unambiguous even if some unrelated review also
        # exists on this commit (a stale one, or a truly concurrent one).
        with patch.object(post_review, "post", return_value=_proc(1, stderr="unexpected end of JSON input")) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists", return_value=True), \
                patch.object(post_review.time, "sleep") as mock_sleep:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 0)
        mock_post.assert_called_once()
        # The delay is still taken before checking — giving GitHub's
        # eventual consistency the whole window to catch up — even
        # though the check then finds the marker and stops.
        mock_sleep.assert_called_once()

    def test_checks_for_the_marker_only_after_the_retry_delay_not_before(self):
        # Reproduces the reviewer's finding: checking immediately after
        # the failure (before the delay) leaves too short a window for
        # GitHub's own eventual consistency to catch up. Verified here by
        # call order, not just call counts.
        calls = []
        with patch.object(post_review, "post", side_effect=[
                    _proc(1, stderr="unexpected end of JSON input"),
                    _proc(0, stdout="posted"),
                ]) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists",
                             side_effect=lambda *a: calls.append("check") or False) as mock_check, \
                patch.object(post_review.time, "sleep", side_effect=lambda *a: calls.append("sleep")) as mock_sleep:
            post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(calls, ["sleep", "check"])

    def test_gives_up_after_exhausting_retries(self):
        failure = _proc(1, stderr="unexpected end of JSON input")
        with patch.object(post_review, "post", return_value=failure) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists", return_value=False), \
                patch.object(post_review.time, "sleep"):
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(mock_post.call_count, 1 + len(post_review.RETRY_DELAYS))

    def test_unknown_marker_check_result_does_not_block_retrying(self):
        # If the check itself can't run (None), we can't prove a
        # duplicate would happen — favor retrying over silently dropping
        # the review, matching the rest of this module's error handling.
        responses = [
            _proc(1, stderr="unexpected end of JSON input"),
            _proc(0, stdout="posted"),
        ]
        with patch.object(post_review, "post", side_effect=responses) as mock_post, \
                patch.object(post_review, "_review_with_marker_exists", return_value=None), \
                patch.object(post_review.time, "sleep") as mock_sleep:
            proc = post_review.post_with_retry("o/r", "1", "deadbeef", {"body": "hi"})
        self.assertEqual(proc.returncode, 0)
        self.assertEqual(mock_post.call_count, 2)
        mock_sleep.assert_called_once()


if __name__ == "__main__":
    sys.exit(unittest.main())
