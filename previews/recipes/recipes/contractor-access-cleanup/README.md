# Contractor access review queue

Rakitsu tested build: `v0.3.2 (09c37a8e)`  
Local Ollama model: `qwen3:4b-q4_K_M`  
Requires Python 3.9 or newer.

From the extracted folder, with Ollama running:

```sh
ollama pull qwen3:4b-q4_K_M
rakitsu version
rakitsu run --no-hub --timeout 300 config.yaml "$(cat input.txt)"
```

Rakitsu calls the bundled Python checker with a fixed command and no
model-supplied arguments. It reads the two synthetic CSVs in `fixtures/` and
writes `output/access-review.csv`. The checker flags an active grant when
the contract ended before the sample review date, 2026-11-14. Contracts ending
on that date are still valid for the sample policy. The report is for human
review and the script makes no changes to grants.

A successful rerun replaces the report. If validation fails, use the tool
error to diagnose the inputs and discard any report left by an earlier run.
See `example-output.md` for the captured sample decisions.
