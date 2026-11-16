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

    def might_contain(self, value):
        for position in self.positions(value):
            if not self.bits & (1 << position):
                return False
        return True

    def confirm(self, value, exact_values):
        candidate = self.might_contain(value)
        exact_match = value in exact_values if candidate else False
        return {'value': value, 'bloom_candidate': candidate, 'exact_match': exact_match}
