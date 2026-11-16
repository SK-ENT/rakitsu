import unittest

from bloom_precheck import BloomPrecheck


class BloomPrecheckTests(unittest.TestCase):
    def test_empty_filter_rejects_without_exact_lookup(self):
        class DoNotLookup:
            def __contains__(self, value):
                raise AssertionError("Negative precheck must skip the exact lookup.")

        check = BloomPrecheck(size=1, hashes=1)
        result = check.confirm("alice", DoNotLookup())
        self.assertFalse(result["bloom_candidate"])
        self.assertFalse(result["exact_match"])

    def test_known_value_requires_exact_confirmation(self):
        check = BloomPrecheck(size=64, hashes=3)
        exact = {"alice", "bob"}
        for value in exact:
            check.add(value)
        result = check.confirm("alice", exact)
        self.assertTrue(result["bloom_candidate"])
        self.assertTrue(result["exact_match"])

    def test_absent_value_is_not_reported_present(self):
        check = BloomPrecheck(size=64, hashes=3)
        exact = {"alice", "bob"}
        for value in exact:
            check.add(value)
        result = check.confirm("mallory", exact)
        self.assertFalse(result["exact_match"])

    def test_collision_candidate_is_rejected_by_exact_set(self):
        check = BloomPrecheck(size=1, hashes=1)
        exact = {"alice"}
        check.add("alice")
        result = check.confirm("mallory", exact)
        self.assertTrue(result["bloom_candidate"])
        self.assertFalse(result["exact_match"])


if __name__ == "__main__":
    unittest.main()
