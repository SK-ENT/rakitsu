#!/bin/sh
# Regenerate the recordings, the screenshots and the HTML pages.
#   sh docs/interactive/tools/regenerate.sh            # casts + pages
#   PW_NODE_PATH=/path/to/node_modules sh docs/interactive/tools/regenerate.sh shots
# Optional: RAKITSU_BIN=/path/to/rakitsu to skip the `go build` the recorders do otherwise.
# Needs: Go (to build the binary), Python 3, curl. Screenshots also need Playwright for node.
set -e
cd "$(dirname "$0")"
python3 record_ask.py
python3 record_wake.py
python3 record_monitor.py
python3 record_msg.py
if [ "$1" = "shots" ]; then
  python3 record_shots.py
fi
python3 build_pages.py
