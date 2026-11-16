# Code change risk review

Rakitsu tested build: `v0.3.2 (09c37a8e)`  
Local model: `qwen3:4b-q4_K_M`

Identify risks in a proposed change and have a second agent turn them into checks a reviewer can run.

Install the exact Rakitsu build and start Ollama on port 11434. Run these
commands from the extracted folder.

```sh
ollama pull qwen3:4b-q4_K_M
rakitsu version
rakitsu run --no-hub --timeout 180 config.yaml "$(cat input.txt)"
```

The recipe sends the bundled synthetic prompt to your local Ollama instance.
It configures zero agent tools. Rakitsu can save run history locally.
See `example-output.md` for one captured result. Model output can vary.
