# Rakitsu spec reference site

A static, no-build site: plain HTML, CSS and JS. Teaching diagrams plus a per-release spec reference, English and Japanese.

Preview locally (fetch() does not work on file:// pages, so use the server):

    python3 -m http.server 8765 -d site      # then open http://127.0.0.1:8765/

Layout: `index.html` shell, `assets/` (app.js page script, viz.js diagrams, two CSS files), `data/` (versions.json and one JSON file per release tag), `tests/`.

Links: every section, row and diagram item has a stable id. A deep link is `#<lang>.<id>` (for example `#en.wake`) or just `#<id>`. The grammar lives only in mkLink/parseToken in `assets/app.js`. The full list of ids is the "All link targets" list at the bottom of the page.

Rule: ids are never renamed. Add new ids; if one must go, keep it working as an alias (see ALIAS in app.js).

Add a release tag: create `data/<tag>.json` (fields `tag`, `commit` from `git rev-parse <tag>^{commit}`, `docsGenerated`, `meta`, `features`; copy a placeholder file as a start), then add the tag to `versions` in `data/versions.json`.

Tests (start the server first, or run `tests/run-all.sh`, which starts and stops it): `node site/tests/validate.mjs` (data, ids, parser), `node site/tests/functional.mjs` (Play/Step, captions), `node site/tests/ui.mjs` (links, hash, screenshots).
