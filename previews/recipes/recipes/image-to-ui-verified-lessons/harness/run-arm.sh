#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
arm=${1:?Specify baseline or recall}
case "$arm" in
  baseline)
    mkdir -p harness/state
    sed -e 's#harness/state/memory#harness/state/empty-memory#' \
        -e '/auto_recall:/,/scopes:/s/enabled: true/enabled: false/' harness/generate.yaml > harness/state/baseline.yaml
    config=harness/state/baseline.yaml
    ;;
  recall)
    config=harness/generate.yaml
    ;;
  *) exit 2 ;;
esac

mkdir -p "generated/$arm"
model=${UI_LAB_MODEL:-gpt-6.1-sol}
"${UI_LAB_RAKITSU:-rakitsu}" run "$config" \
  "Build this image-to-UI screen with shared components. Existing source: apps/observatory/index.html, apps/rail-yard/index.html, kit/components.js, kit/tokens.css.

$(cat reference/spec.md)" \
  --model "$model" --attach reference/sort-control.png --no-hub \
  --timeout 180 --sessions-dir "harness/state/$arm" --trace > "harness/$arm.log" 2>&1
python3 harness/materialize.py "$arm"
