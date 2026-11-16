# Rakitsu Recipes preview

Serve this directory to browse the local, read-only catalog.

```sh
python3 -m http.server 8757 --bind 127.0.0.1
```

Open http://127.0.0.1:8757 in a browser. The catalog, search, and graph run
from local assets. A recipe runs separately in Rakitsu after download.

## Catalog maintenance

Each entry in `catalog.json` has a directory under `recipes/` with its config,
synthetic input, instructions, and captured output. Test any recipe change
using the recorded Rakitsu version and model before refreshing its output.
The version field refers to the Rakitsu CLI build, independent of the
workflow version in the config. Tool-backed entries list their bundled files
in `fixtures` and disclose read/write access via `toolAccess`. For those runs,
check the trace for successful tool results and compare the generated
`output/report.md` to the fixture policy before capturing it.

```sh
python3 package_recipes.py
python3 verify.py
```

## Refresh recipe previews

Run `node build_previews.cjs /path/to/web/node_modules/js-yaml` from this
directory after editing a configuration. The generator produces each recipe's
SVG and configuration preview. Review those files before staging a deployment.
Project recipes with multiple configuration files have an explicit preview
manifest in the generator.

## Private preview analytics

On DGX, use `serve.py` instead of the static HTTP server. Point `--directory`
at the staged site and `--database` at a private writable location outside
that directory. The server binds to loopback for the existing tailnet proxy.

The Analytics footer link opens the per-recipe totals for the last 30 UTC
days. A view is an explicit graph selection or a recipe deep link. Preview
clicks include opening a workflow or report and selecting a configuration file.
Downloads count ZIP clicks. The catalog's default selection and search-driven
selection do not emit analytics events.

The event accepts only the catalog recipe ID and event type. The server stores
one counter per UTC date, recipe and event. It removes expired rows when
analytics is used. It writes no request access log, and the catalog sends no
search text, cookies or visitor identifiers to the analytics endpoint.
