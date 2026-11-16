# Import quality review

Rakitsu tested build: `v0.3.2 (09c37a8e)`  
Local model: `qwen3:4b-q4_K_M`

Check a small data extract against its schema and produce a quarantine list.

Start Ollama on port 11434 and install the model. Run from the extracted folder.

```sh
ollama pull qwen3:4b-q4_K_M
rakitsu version
rakitsu run --no-hub --timeout 180 config.yaml "$(cat input.txt)"
```

This recipe uses synthetic data and configures zero agent tools.
See `example-output.md` for a captured run. Review outputs before acting on them.
