# Invoice payment hold report

Rakitsu tested build: `v0.3.2 (09c37a8e)`  
Local Ollama model: `qwen3:4b-q4_K_M`

Compare billed prices and quantities with approved purchase orders and goods received. The CSVs are synthetic.

Run the following commands in a freshly extracted recipe folder.

```sh
ollama pull qwen3:4b-q4_K_M
rakitsu version
rakitsu run --no-hub --timeout 300 config.yaml "$(cat input.txt)"
```

Rakitsu reads only the two configured CSV paths and writes
`output/report.md`. Re-running overwrites that report. The config uses local
Ollama and filesystem tools. Review the report before taking action.
See `example-output.md` for the captured report from a test run.
