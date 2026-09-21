#!/bin/sh
# Wrapper for Microsoft's official Playwright MCP server (`@playwright/mcp`).
#
# NOTICE — new dependency, different from this repo's existing browser MCP:
# examples/dogfood/14-full-dev-agent uses `puppeteer-mcp-server` (a
# third-party npm package), invoked through its own wrapper with an
# env-var-casing workaround (see that example's mcp/browser-server/
# browser-server.sh for why). This example uses `@playwright/mcp`
# instead — Microsoft's own package, plain CLI flags, no env-var casing
# issue to work around. Deliberate choice for this example, not a repo-
# wide switch: the dogfood config is unaffected and keeps using Puppeteer.
# Don't assume both examples share one browser MCP going forward without
# checking which one a given config actually declares.
#
# --isolated: fresh browser profile per run, no persisted cookies/state
# between invocations — this tool only ever reads public search results
# and doc pages, no login state is wanted or expected.
# --no-sandbox: disables Chromium's OWN internal renderer sandbox, not just
# profile isolation, so a compromised/malicious result page could otherwise
# escape to the OS user's own privileges. This agent navigates to public
# search results, which are influenceable by third parties, so keeping
# Chromium's real sandbox intact matters whenever it's actually usable.
# It is set on two paths, not purely auto-detected: explicitly via
# PLAYWRIGHT_NO_SANDBOX=1, or as a fallback when running as root (`id -u`
# = 0), since an unprivileged sandbox helper generally isn't available
# there either. An earlier version of this script relied on the root
# check ALONE, on the theory that only root-without-setuid-helper needs
# it. That theory was wrong — confirmed live on GitHub Actions'
# `ubuntu-latest` runner (a non-root user): Chromium's sandbox still
# failed to initialize there (unprivileged user namespaces restricted on
# that image), so root-detection alone under-set the flag and the
# browser never started. The explicit env var exists so a caller in a
# situation like that can say so directly instead of the heuristic
# guessing wrong. CI wiring for this example sets PLAYWRIGHT_NO_SANDBOX=1;
# a normal dev machine should not need either path.
# --executable-path: point at a locally-installed Chrome if present, so
# this doesn't download and cache its own Chromium on every fresh run.
#
# SANDBOX NOTES — read before running this anywhere but a developer's own
# machine, and definitely before wiring anything like this into CI:
#
# 1. This is an `mcp_server`-type tool, not a `cli`-type tool. Rakitsu's
#    `cli` tool sandbox (command allowlist, resource_limits) does NOT
#    apply here — this wrapper spawns a real Chromium subprocess with
#    whatever network/filesystem access the OS user running rakitsu has.
#    The only restrictions in effect are what THIS script and the
#    Playwright MCP server's own flags impose. `--allow-unrestricted-
#    file-access` is deliberately NOT set — file:// navigation stays
#    blocked and filesystem access stays limited to workspace roots.
#
# 2. If this ever runs inside a memory/PID-constrained container (a
#    Docker container with a small /dev/shm, as opposed to a full VM),
#    headless Chromium can crash ("Target crashed", SIGSEGV) — no flag
#    for `--disable-dev-shm-usage`-equivalent is exposed by this version
#    of the Playwright MCP server. GitHub Actions' hosted `ubuntu-latest`
#    runners are full VMs with Chrome pre-installed, not this kind of
#    constrained container, so this is a lower risk there than it would
#    be inside e.g. this repo's own Docker-based dogfood sandbox — but
#    test in the actual target environment before trusting it
#    unsupervised. `--browser firefox` is a fallback worth trying if
#    Chromium proves unreliable in a specific sandbox.
#
# 3. Bigger point, security not reliability: a real browser with JS
#    execution (`browser_evaluate`) driven by an LLM agent that is
#    ultimately processing PR-diff-derived text is a materially larger
#    attack surface than the plain HTTP-fetch script originally scoped
#    for this feature. The trade-off accepted so far
#    (AskUserQuestion, 2026-09-21) was "web search/doc lookup reopens
#    tool-use risk" in the abstract — it did not specifically size a full
#    browser with JS eval. If/when this pattern gets wired into
#    unsupervised CI (not just a manually-run example), that specific
#    delta needs its own explicit go-ahead, not inherited from the
#    earlier answer.
#
# 4. Specific instance of point 3: this wrapper has no network allowlist,
#    so a search result a malicious or prompt-injection-controlled page
#    could point the agent at (e.g. `http://127.0.0.1:<port>` or a cloud
#    metadata endpoint like `169.254.169.254`) is reachable the same as
#    any public URL — the browser's file:// block (point 1) doesn't cover
#    this, it's a different access path. No allowlist/proxy is
#    implemented here; running this example means accepting that an
#    untrusted search result can cause SSRF-style access to whatever the
#    OS user's network can already reach. Worth real mitigation (an
#    egress allowlist or proxy) before any unsupervised/CI use — not
#    attempted here, this is a standalone example, not a hardened service.

set -e

if [ -z "$PLAYWRIGHT_EXECUTABLE_PATH" ] && [ -z "$playwright_executable_path" ]; then
  for candidate in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    /usr/bin/chromium \
    /usr/bin/chromium-browser \
    /usr/bin/google-chrome; do
    if [ -x "$candidate" ]; then
      PLAYWRIGHT_EXECUTABLE_PATH="$candidate"
      break
    fi
  done
fi
: "${PLAYWRIGHT_EXECUTABLE_PATH:=${playwright_executable_path:-}}"

# Build the argument list with `set --` (POSIX sh has no arrays) so each
# argument stays a single word through final expansion — a plain string
# expanded unquoted word-splits on spaces, breaking any path containing
# them (e.g. "/Applications/Google Chrome.app/..."). Prepending onto "$@"
# in one `set --` call preserves any args this wrapper was itself invoked
# with (rakitsu's tool `args:` field, if ever set) — the right-hand side
# expands before the positional parameters are replaced.
if [ "$PLAYWRIGHT_NO_SANDBOX" = "1" ] || [ "$(id -u)" = "0" ]; then
  set -- --headless --isolated --no-sandbox "$@"
else
  set -- --headless --isolated "$@"
fi
if [ -n "$PLAYWRIGHT_EXECUTABLE_PATH" ]; then
  set -- --executable-path "$PLAYWRIGHT_EXECUTABLE_PATH" "$@"
fi

# Pinned, not @latest: this launches a real browser with network access
# and JS execution, so an unpinned version means every invocation can
# silently pull in a new, unreviewed release. Bump deliberately.
exec npx -y @playwright/mcp@0.0.82 "$@"
