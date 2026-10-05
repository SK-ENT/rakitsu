#!/bin/bash
# Mechanical drift check for docs/reference:
#   1. every command, subcommand and flag in `<binary> --help` appears in docs/reference
#   2. every mapstructure key in internal/config/*.go appears in docs/reference
#   3. every relative link and anchor in docs/reference resolves
# Usage: scripts/check-reference-docs.sh [path-to-built-binary]   (run from the repo root)
# Exit 0 when nothing is missing.
set -u
B=${1:-./bin/rakitsu}
miss=0
subs() { awk '/^Available Commands:/{f=1;next} /^$/{f=0} f{print $1}' | grep -v -e '^help$' -e '^completion$'; }
check() {
  out=$("$B" "$@" --help 2>&1)
  for fl in $(echo "$out" | grep -oE '^ +(-[a-zA-Z], )?--[a-z][a-z0-9-]+' | grep -oE -e '--[a-z][a-z0-9-]+'); do
    if ! grep -rqF -- "$fl" docs/reference; then echo "UNDOC flag: $* $fl"; miss=$((miss+1)); fi
  done
  for s in $(echo "$out" | subs); do
    grep -rqF -- "$* $s" docs/reference || { echo "UNDOC subcommand: $* $s"; miss=$((miss+1)); }
    check "$@" "$s"
  done
}
for c in $("$B" --help | subs); do
  grep -rqF -- "rakitsu $c" docs/reference || { echo "UNDOC command: $c"; miss=$((miss+1)); }
  check "$c"
done
for k in $(grep -rhoE 'mapstructure:"[a-z_0-9]+' internal/config/*.go | grep -v _test | sed 's/mapstructure:"//' | sort -u); do
  grep -rqE -- "\b$k\b" docs/reference || { echo "UNDOC config key: $k"; miss=$((miss+1)); }
done
python3 "$(dirname "$0")/check_doc_links.py" . docs/reference || miss=$((miss+1))
echo "missing=$miss"
[ "$miss" = 0 ]
