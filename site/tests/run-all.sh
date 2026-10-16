#!/bin/sh
# Starts a throwaway server on :8765 (override with RK_PORT), runs validate + functional + ui + gating + pages tests, kills the server.
cd "$(dirname "$0")/../.." || exit 1
python3 -m http.server "${RK_PORT:-8765}" --bind 127.0.0.1 -d site >/dev/null 2>&1 & SRV=$!
trap 'kill "$SRV" 2>/dev/null || true' EXIT
sleep 1
kill -0 "$SRV" 2>/dev/null || exit 1
rc=0
node site/tests/validate.mjs || rc=1
node site/tests/functional.mjs http://127.0.0.1:${RK_PORT:-8765}/ site || rc=1
node site/tests/ui.mjs http://127.0.0.1:${RK_PORT:-8765}/ || rc=1
node site/tests/gating.mjs || rc=1
node site/tests/pages.mjs || rc=1
exit $rc
