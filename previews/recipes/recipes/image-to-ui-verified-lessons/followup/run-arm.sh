#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
arm=${1:?Specify control or learned}
case "$arm" in control|learned) ;; *) exit 2 ;; esac
dir="followup/state/$arm"
mkdir -p "$dir" "generated/intake-$arm"
sed -e "s#harness/state/memory#followup/state/$arm-memory#" \
    -e 's/top_k: 1/top_k: 2/' \
    -e 's/fetch the full procedure/fetch each relevant full procedure/' \
    -e 's/Use it as a guide/Use them as a guide/' \
    harness/generate.yaml > "$dir/config.yaml"
"${UI_LAB_RAKITSU:-rakitsu}" run "$dir/config.yaml" \
  "Build this image-to-UI screen with shared components. Existing source: apps/observatory/index.html, apps/rail-yard/index.html, kit/components.js, kit/tokens.css.

$(cat followup/reference/spec.md)" \
  --model "${UI_LAB_MODEL:-gpt-6.1-sol}" --attach followup/reference/intake.png --no-hub \
  --timeout 200 --sessions-dir "$dir" > "$dir/run.log" 2>&1
python3 - "$arm" "$dir" <<'PY'
import json
import re
import sys
from pathlib import Path

arm, directory = sys.argv[1:]
directory = Path(directory)
session = re.search(r'Session ID: ([\w-]+)', (directory / 'run.log').read_text(errors='replace')).group(1)
events = [json.loads(line) for line in (directory / f'{session}.jsonl').read_text().splitlines()]
response, = [row['payload']['final_answer'] for row in events if row.get('event_type') == 'EXECUTION_COMPLETE']
html = re.search(r'<!doctype html\b.*?</html\s*>', response, re.I | re.S)
if not html:
    raise SystemExit(f'{arm}: no complete HTML in output')
Path(f'generated/intake-{arm}/index.html').write_text(html.group() + '\n')
print(f'{arm}: {session}')
PY
