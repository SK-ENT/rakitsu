# All-agent company order desk

Rakitsu tested build: `v0.3.2 (09c37a8e)`.
Requires Python 3 and local Ollama with `qwen3:4b-q4_K_M`.

From the extracted folder:

```sh
ollama pull qwen3:4b-q4_K_M
rakitsu version
rakitsu run --no-hub --timeout 300 config.yaml "$(cat input.txt)"
```

Sales reads the customer order. Operations checks inventory and supplier
delivery dates. Finance applies the bundled fixed pricing and approval
rules, then writes `output/decision.json`. Customer Success uses the
hand-offs to draft a reply. The decision file is authoritative.

The synthetic order requests 120 mailers, and only 80 are in stock.
The supplier that arrives on time pushes the quote over budget.
The cheaper supplier misses the deadline. The requested discount also
exceeds policy. The report should say HOLD. The tools are fixed local
commands and create no orders, payments, or messages.
