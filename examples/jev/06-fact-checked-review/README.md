# 06 — Fact-Checked Review (Jev + Playwright)

This example closes a real gap found earlier in this project's own Jev integration
work: asking Jev whether a code-review finding's claim is *true*, with nothing
but the diff as context, misses false claims that depend on knowledge the diff
doesn't contain.

**The evidence.** A live API test on the exact claim that slipped through in
production ("the `unix` build constraint is not a standard Go build tag" — this
is false; `unix` has been an automatic Go build tag since Go 1.19):

| Input to Jev | Score | Correct? |
|---|---|---|
| Diff + finding only | **0.71** ("true") | ❌ Wrong |
| + one paragraph of the actual Go build-constraints doc | **0.17** ("false") | ✅ Correct |
| + same fact, more narrowly-scoped question | **0.92** ("yes, unix is automatic") | ✅ Correct |

Adding the missing fact flipped Jev from confidently wrong to confidently
right. An earlier fix narrowed the question to "is this grounded in
the diff" — safe, but it explicitly can't catch this class of miss. This
example automates *finding* the missing fact via a real web search, then asks
Jev a fact-grounded question.

## What's here

- `FactChecker` — given a code-review finding, searches the web for grounding
  material via a real headless browser, then calls `jev` once with the
  retrieved reference text as part of its `state`.
- A Playwright-based `mcp_server` tool (`pw`) for the actual browsing — a
  plain HTTP scrape of DuckDuckGo's search page gets bot-blocked after a
  couple of requests (confirmed live, 2026-09-21); a real browser doesn't.

## Install

You need Node.js/npx (for the Playwright MCP server) and a Chromium-based
browser. If you already have Google Chrome installed, `mcp/browser-server.sh`
finds and reuses it automatically — no extra download. If not, either:

```sh
npx -y @playwright/mcp@latest --help   # first run installs the package
npx playwright install chromium        # only needed if no local Chrome/Chromium found
```

Read `mcp/browser-server.sh`'s header comments before running this anywhere
but your own machine — it explains what this does and does not restrict
(it is an `mcp_server` tool, not a sandboxed `cli` tool), and the added risk
of a real JS-executing browser versus a plain fetch.

## Run

```sh
export TYPESAFE_API_KEY=...
export OPENAI_API_KEY=...
rakitsu run examples/jev/06-fact-checked-review/config.yaml \
  "Code review finding on internal/store/filelock_unix.go:1: The \`unix\`
   build constraint is not a standard Go-supplied build tag, so this file
   is excluded on normal Unix builds unless the caller explicitly passes
   -tags unix." --trace
```

Expect: a search for the Go build-constraints documentation, a fetch of the
top result (ideally `pkg.go.dev/go/build/constraint` or `go.dev/doc/go1.19`),
and a final `jev` call reporting a low score (claim is false) with the
reference source cited.

## Status

**Pipeline mechanics fixed and live-tested; the demo's own search step is
still unreliable.** Four live throwaway-CI runs on 2026-09-21 (real
`rakitsu run` on a real GitHub Actions runner, real API keys, closed
without merging):

1. First run found rakitsu's `mcp_server` tool registration was silently
   dropping `tools_inline` on this config's code path — a real core bug,
   unrelated to this example, filed and fixed. Confirmed fixed:
   the `pw_*` tools now register correctly.
2. Second run: the browser itself failed to launch — `--no-sandbox` was
   gated on `id -u = 0`, but GitHub Actions' non-root runner still needs it
   (unprivileged user namespaces are restricted on that image). Fixed by
   making `--no-sandbox` an explicit opt-in (`PLAYWRIGHT_NO_SANDBOX=1`)
   instead of trying to auto-detect the need.
3. Third run: the browser launched and searched, but DuckDuckGo
   bot-blocked the request from CI's shared IP (a challenge page, not
   results). Switched the search target to Bing.
4. Fourth+ runs: Bing search works and the browser fetches a real page,
   but the page picked has twice been the wrong one (once off-topic,
   once 404) — not a rakitsu bug, a weak page-selection heuristic in this
   example's own prompt. Tightened the prompt to require checking the
   fetched page's text actually contains the term being checked before
   trusting it as grounding.

**The safety property this tightening was meant to prove is now proven,
3 different live runs in a row**: when the fetched page doesn't actually
cover the claim, the agent honestly reports "unsupported/inconclusive"
with a low-confidence score instead of asserting a wrong verdict with
false confidence — including one run that would otherwise have gotten a
real false claim backwards (0.75 "likely true" on a claim that is
actually false, before the relevance-check fix).

**Not yet proven**: a full successful catch — real search, right page,
Jev correctly flags a fabricated claim as false with genuine supporting
evidence. Every live run so far has stopped at "no good page found,"
never at "found a good page and it worked." That's a search-result-
quality problem (Bing isn't reliably surfacing the actual Go build-tag
docs for this query), not a rakitsu or Jev problem. Worth another look
if this pattern gets real use, but not worth more live-CI iteration
right now.

**Not wired into CI.** This is a standalone example only. Wiring an
equivalent step into `scripts/ci-review.sh` is tracked separately
and needs its own explicit go-ahead given the sandbox notes above — a real
browser is a materially bigger attack surface than the plain-fetch approach
originally scoped there, and the search-reliability gap above should be
closed first.
