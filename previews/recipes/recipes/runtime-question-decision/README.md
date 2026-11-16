# Ask a Decision question at runtime

Rakitsu tested build: `v0.3.0-alpha.1-internal.1897` (source `b7ea2bfc`).
Requires Python 3.9+ and Rakitsu on your PATH. If needed, set
`RAKITSU_BIN` to the absolute path of the pinned binary.

Start [Laya](https://github.com/NandhaKishorM/laya) on loopback port 8000.
From the extracted folder, run:

```sh
python3 run.py --question 'Does the clinic radio still need repair?' \
  --input 'The shelter heater is restored. The clinic radio is broken and waiting for a replacement part.'
python3 run.py --question 'Does the shelter heater still need repair?' \
  --input 'The shelter heater is restored. The clinic radio is broken and waiting for a replacement part.'
```

Same input, different question. The launcher parses the bundled Decision
template, changes only the backend and question text, then serializes a fresh
config in a temporary folder. The allowed answer IDs remain `yes` and `no`.

Choose another supported backend with `--backend jev`, `--backend openai`,
or `--backend gemini`. Before using a cloud provider, load its authorized key
from your secret store into the shell environment. The corresponding
variables are `TYPESAFE_API_KEY`, `OPENAI_API_KEY`, and `GEMINI_API_KEY`
(or `GOOGLE_API_KEY`). Cloud runs send both your question and input to the
provider. The two bundled questions were tested with local Laya.
