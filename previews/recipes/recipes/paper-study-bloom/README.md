# Paper study group: Bloom filter precheck

Rakitsu tested build: `v0.3.2 (09c37a8e)`.

Start Ollama on port 11434. The discussion uses `qwen3:4b-q4_K_M` and
the implementer uses `qwen2.5-coder:3b`. Install Python 3
and put the pinned Rakitsu binary on your PATH, or set `RAKITSU_BIN`
to its absolute path.

## Run the study

```sh
ollama pull qwen3:4b-q4_K_M
ollama pull qwen2.5-coder:3b
python3 run.py
```

The paper reader explains the bit array and its false-positive tradeoff.
The design reviewer responds with a one-bit collision case. The
implementer uses both contributions to implement the two missing membership methods in the supplied starter. The launcher
saves the discussion and syntax-checked module under `output/`.

The study uses original notes on [Bloom's 1970 paper](https://doi.org/10.1145/362686.362692).
The paper itself is available through that DOI.

## Test the generated module

Read `output/discussion.md` and review `output/bloom_precheck.py` before
executing it. Then run the independent tests against **the generated module**:

```sh
cp test_bloom_precheck.py output/
(cd output && python3 -m unittest -v test_bloom_precheck.py)
```

The archive includes the passing run as `captured-implementation.py`.

The tests include present and absent keys and a forced collision.
A positive Bloom precheck must still be confirmed against the exact set.
The included reference implementation also has a small stdin JSON CLI:

```sh
printf '%s\n' '{"known_values":["alice","bob"],"queries":["alice","mallory"]}' | python3 bloom_precheck.py
```

Real deployments might use the Bloom precheck ahead of a slower exact lookup.
This sample uses an in-memory set and does not measure production performance.
