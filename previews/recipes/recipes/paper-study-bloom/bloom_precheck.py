#!/usr/bin/env python3
"""Bloom-filter precheck followed by exact confirmation.

The filter is only a fast negative test.  A positive bit match is always
confirmed against the exact collection before it is reported as present.
"""

from __future__ import annotations

import hashlib
import json
import sys
from dataclasses import dataclass
from typing import Iterable


@dataclass
class BloomPrecheck:
    size: int = 128
    hashes: int = 3

    def __post_init__(self) -> None:
        if self.size < 1 or self.hashes < 1:
            raise ValueError("size and hashes must be positive")
        self._bits = 0

    def _positions(self, value: str) -> Iterable[int]:
        raw = value.encode("utf-8")
        for index in range(self.hashes):
            digest = hashlib.sha256(index.to_bytes(2, "big") + raw).digest()
            yield int.from_bytes(digest[:8], "big") % self.size

    def add(self, value: str) -> None:
        for position in self._positions(value):
            self._bits |= 1 << position

    def might_contain(self, value: str) -> bool:
        return all(self._bits & (1 << position) for position in self._positions(value))

    def confirm(self, value: str, exact_values: set[str]) -> dict[str, object]:
        candidate = self.might_contain(value)
        exact = value in exact_values if candidate else False
        return {"value": value, "bloom_candidate": candidate, "exact_match": exact}


def main() -> int:
    payload = json.load(sys.stdin)
    values = {str(value) for value in payload["known_values"]}
    precheck = BloomPrecheck(size=int(payload.get("size", 128)), hashes=int(payload.get("hashes", 3)))
    for value in values:
        precheck.add(value)
    results = [precheck.confirm(str(value), values) for value in payload["queries"]]
    json.dump({"results": results}, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
