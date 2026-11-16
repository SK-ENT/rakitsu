# From paper discussion to a tested module

Tested on Rakitsu `v0.3.2 (09c37a8e)` with Qwen3 4B for the discussion
and Qwen2.5-Coder 3B for implementation.

1. **Paper reader.** Explained how a bit array can reject an absent value
   quickly and why a positive match can be a collision.
2. **Design reviewer.** Used the one-bit example to establish two separate
   results: a possible match can still be an exact miss, and an empty
   precheck should skip the exact lookup.
3. **Implementer.** Completed the hashing starter by generating both
   membership methods. The launcher saved the result as Python.
4. **Independent tests.** All four tests passed against the generated code.

| Probe | Bloom candidate | Exact result |
| --- | --- | --- |
| Inserted value | True | True |
| Absent sample | Possible or absent | False |
| Forced one-bit collision | True | False |
| Empty filter | False | False, exact lookup skipped |

The earlier full-module attempts exposed output limits and errors in
generated hashing and collision logic. Supplying a reviewed hashing starter
kept the study focused on the behavior under test. The ZIP includes the
actual passing generated module as `captured-implementation.py`.

The lab uses an in-memory exact set and does not establish a speedup.
See [Bloom (1970)](https://doi.org/10.1145/362686.362692) for the original tradeoff.
