"""Starter class. The study implementer supplies both membership methods."""
import hashlib


class BloomPrecheck:
    def __init__(self, size=128, hashes=3):
        if size < 1 or hashes < 1:
            raise ValueError("size and hashes must be positive")
        self.size = size
        self.hashes = hashes
        self.bits = 0

    def positions(self, value):
        for index in range(self.hashes):
            data = f"{index}:".encode() + value.encode("utf-8")
            yield int.from_bytes(hashlib.sha256(data).digest(), "big") % self.size

    def add(self, value):
        for position in self.positions(value):
            self.bits |= 1 << position
