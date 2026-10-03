#!/bin/sh
# Starts a throwaway server on :8765, runs validate + functional + ui + pages tests, kills the server.
cd "$(dirname "$0")/../.." || exit 1
python3 -m http.server 8765 -d site >/dev/null 2>&1 & SRV=$!
sleep 1
rc=0
node site/tests/validate.mjs || rc=1
node site/tests/functional.mjs http://127.0.0.1:8765/ site || rc=1
node site/tests/ui.mjs http://127.0.0.1:8765/ || rc=1
node site/tests/pages.mjs || rc=1
kill $SRV
exit $rc
