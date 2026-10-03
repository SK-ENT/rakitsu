# Rakitsu spec reference site

A static, no-build site: plain HTML, CSS and JS. Teaching diagrams plus a per-release spec reference, English and Japanese.

Preview locally (fetch() does not work on file:// pages, so use the server):

    python3 -m http.server 8765 -d site      # then open http://127.0.0.1:8765/

Layout: `index.html` shell, `assets/` (app.js page script, viz.js diagrams, two CSS files), `data/` (versions.json and one JSON file per release tag), `tests/`.

Links: every section, row and diagram item has a stable id. A deep link is `#<lang>.<id>` (for example `#en.wake`) or just `#<id>`. The grammar lives only in mkLink/parseToken in `assets/app.js`. The full list of ids is the "All link targets" list at the bottom of the page.

Rule: ids are never renamed. Add new ids; if one must go, keep it working as an alias (see ALIAS in app.js).

Doc versions: a doc version exists only for a release whose docs changed. Each has one data file `data/<tag>.json` (fields `tag`, `commit` (see below), `docsGenerated`, `meta`, `features`) and is listed in `versions` in `data/versions.json`, newest first. Docs start at the oldest listed version; tags before it have no docs, are not in the dropdown, and their URLs show the 404 page with "No docs for this release".

`data/versions.json`: `default` (a doc version), `versions` (doc versions only), and optional `covers`, a map from a release tag to the doc version whose docs apply, for example `{"v0.3.0-alpha.15": "v0.3.0-alpha.14"}`. The value must be a listed doc version and the nearest earlier one. A covered tag gets its own page `/<tag>/` rendered from that doc version's data, with the notice "This release has no doc changes. Showing the docs for <doc version>.", and appears in the dropdown as `<tag> (docs from <doc version>)`. It has no data file.

New tag: diff the verified data for the new tag against the previous doc version. If they are identical apart from `tag`/`commit` (and similar release fields), add a `covers` entry and no data file. Otherwise add `data/<tag>.json` and list it in `versions`. The build refuses a data file that is not listed, a missing data file, and a bad `covers` entry.

Fill `commit` from the PUBLIC repo with `git ls-remote --tags <public-remote> 'refs/tags/<tag>^{}'` (the peeled commit).
Never take it from a local `git rev-parse` in a checkout that also has the internal remote: tag names can be shared and point at different commits.

Optional check: `scripts/build-pages.sh --verify-tags <git-remote-or-url> [outdir]` runs `git ls-remote --tags` against that remote and fails if any doc version's `commit` differs from the peeled tag commit, or if a doc version or covered tag is not a public tag (read-only `ls-remote`). Without the flag the build uses no network.

Tests (start the server first, or run `tests/run-all.sh`, which starts and stops it): `node site/tests/validate.mjs` (data, ids, parser), `node site/tests/functional.mjs` (Play/Step, captions), `node site/tests/ui.mjs` (links, hash, screenshots), `node site/tests/gating.mjs` (version gating), `node site/tests/pages.mjs` (Pages build, covers fixture, 404).
