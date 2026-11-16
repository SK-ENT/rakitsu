# Study notes on Bloom (1970)

[Space/Time Trade-offs in Hash Coding with Allowable Errors](https://doi.org/10.1145/362686.362692)

A compact bit array can screen membership queries before an exact lookup.
Insert by setting several hash-selected bit positions. On lookup, one
missing bit proves absence for the same inserted set and hash scheme. All
bits set only means a possible match because distinct values can share
positions. Smaller arrays use less space but can increase collisions.

Our engineering extension is to confirm candidates against an exact set.
This lab uses a small in-memory set so that the correctness boundary is
easy to inspect. It does not benchmark a real database.
