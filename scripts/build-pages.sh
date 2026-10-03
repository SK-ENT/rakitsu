#!/usr/bin/env bash
# Build the static spec-site tree for GitHub Pages (one directory per release tag).
# Usage: scripts/build-pages.sh [--verify-tags <git-remote-or-url>] [outdir]
#   outdir defaults to /tmp/rk-pages-out and must be outside the git repo.
#   --verify-tags (optional, uses the network): fail if any data file's "commit"
#   differs from the peeled commit of that tag on the given (public) remote.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
verify=""; out_arg=""
while [ $# -gt 0 ]; do
  case "$1" in
    --verify-tags) [ $# -ge 2 ] || { echo "--verify-tags needs a remote or url" >&2; exit 1; }; verify="$2"; shift 2;;
    *) out_arg="$1"; shift;;
  esac
done
out="${out_arg:-/tmp/rk-pages-out}"
mkdir -p "$out"; out="$(cd "$out" && pwd)"
top="$(git -C "$here" rev-parse --show-toplevel)"
case "$out/" in "$top"/*|"$here"/*) echo "refusing to write inside the git repo: $out" >&2; exit 1;; esac
[ "$out" != "/" ] || { echo "bad outdir" >&2; exit 1; }
site="$here/site"
python3 - "$site" <<'PY'
import json,os,sys
s=sys.argv[1]; v=json.load(open(s+"/data/versions.json"))
if v["default"] not in v["versions"]: sys.exit("default tag %s is not in versions" % v["default"])
for t in v["versions"]:
    if not os.path.isfile("%s/data/%s.json"%(s,t)): sys.exit("missing data file for "+t)
    if "/" in t or t.startswith("."): sys.exit("bad tag "+t)
PY
if [ -n "$verify" ]; then
  bad=0
  for f in "$site"/data/v[0-9]*.json; do
    t="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["tag"])' "$f")"
    have="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["commit"])' "$f")"
    want="$(git -C "$top" ls-remote --tags "$verify" "refs/tags/$t^{}" | awk '{print $1}')"
    [ -n "$want" ] || want="$(git -C "$top" ls-remote --tags "$verify" "refs/tags/$t" | awk '{print $1}')"
    if [ -z "$want" ]; then echo "verify-tags: tag $t not found on $verify" >&2; bad=1
    elif [ "$have" != "$want" ]; then echo "verify-tags: $t commit is $have but $verify tag is $want" >&2; bad=1
    else echo "verify-tags: $t ok"; fi
  done
  [ "$bad" = 0 ] || exit 1
fi
find "$out" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
mkdir -p "$out/assets" "$out/data" "$out/latest"
cp "$site"/assets/{app.css,app.js,viz.css,viz.js} "$out/assets/"
cp -R "$site/assets/fonts" "$out/assets/fonts"
cp "$site"/data/*.json "$out/data/"
touch "$out/.nojekyll"
default="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["default"])' "$site/data/versions.json")"
tags="$(python3 -c 'import json,sys;print("\n".join(json.load(open(sys.argv[1]))["versions"]))' "$site/data/versions.json")"
redirect() { # $1 prefix to default dir
cat <<EOF
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Rakitsu Spec Reference</title>
<meta http-equiv="refresh" content="0; url=$1$default/">
<script>location.replace("$1$default/"+location.hash);</script></head>
<body><p><a href="$1$default/">Rakitsu Spec Reference ($default)</a></p>
<noscript><p><a href="$1$default/">Continue to $default</a></p></noscript></body></html>
EOF
}
redirect "" > "$out/index.html"
redirect "../" > "$out/latest/index.html"
{
cat <<EOF
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Page not found - Rakitsu Spec Reference</title>
<style>body{font:16px/1.6 system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem}</style></head>
<body><h1>Page not found</h1>
<p>This page does not exist. Try the <a id="lt" href="latest/">latest version</a> or pick a release:</p><ul id="tl">
EOF
for t in $tags; do echo "<li><a href=\"$t/\">$t</a></li>"; done
cat <<EOF
</ul>
<script>
/* On github.io project pages the first path segment is the repo; make links absolute to it so they work at any depth. */
if(/\.github\.io$/.test(location.hostname)){var b="/"+location.pathname.split("/")[1]+"/";
Array.prototype.forEach.call(document.querySelectorAll("a"),function(a){a.setAttribute("href",b+a.getAttribute("href"));});}
</script></body></html>
EOF
} > "$out/404.html"
for t in $tags; do
  mkdir -p "$out/$t"
  python3 - "$site/index.html" "$t" > "$out/$t/index.html" <<'PY'
import sys,re
h=open(sys.argv[1]).read(); t=sys.argv[2]
h=h.replace('href="assets/','href="../assets/').replace('src="assets/','src="../assets/')
h=h.replace('<script src="../assets/viz.js">','<script>window.RK_TAG=%s;</script>\n<script src="../assets/viz.js">'%__import__("json").dumps(t),1)
h=h.replace('<title>Rakitsu Spec Reference</title>','<title>Rakitsu Spec Reference %s</title>'%t,1)
assert 'RK_TAG' in h and '"../assets/app.css' in h
sys.stdout.write(h)
PY
done
if grep -rIEn '(href|src)="/|(["(])/(assets|data)/' "$out" >/dev/null; then echo "absolute URL found in output" >&2; exit 1; fi
echo "Built $out (default $default)"
(cd "$out" && find . -type f | sort | while read -r f; do printf '%8s  %s\n' "$(wc -c <"$f" | tr -d ' ')" "$f"; done; du -sh . | awk '{print "total "$1}')
