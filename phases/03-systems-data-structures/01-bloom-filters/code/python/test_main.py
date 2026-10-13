"""Tests for phases/03-systems-data-structures/01-bloom-filters. Run: python3 -m unittest -v"""

import math
import unittest

from main import BloomFilter, fnv1a64, measure_false_positive_rate, mix64


class HashingTest(unittest.TestCase):
    def test_fnv1a_known_vectors(self) -> None:
        # Test vectors from the FNV reference implementation.
        self.assertEqual(fnv1a64(b""), 0xCBF29CE484222325)
        self.assertEqual(fnv1a64(b"a"), 0xAF63DC4C8601EC8C)
        self.assertEqual(fnv1a64(b"foobar"), 0x85944171F73967E8)

    def test_mix64_is_a_bijection_fixing_zero(self) -> None:
        self.assertEqual(mix64(0), 0)
        self.assertEqual(mix64(1), 0xB456BCFC34C2CB2C)
        self.assertEqual(len({mix64(i) for i in range(10_000)}), 10_000)

    def test_indexes_match_java_and_go(self) -> None:
        # The same key must land on the same bits in all three languages.
        bf = BloomFilter(1000, 3)
        self.assertEqual(bf.indexes(b"idem-key-1"), [994, 570, 146])
        self.assertEqual(bf.indexes(b"card-4111"), [343, 535, 727])

    def test_indexes_are_in_range_and_k_long(self) -> None:
        bf = BloomFilter(97, 5)
        for i in range(500):
            idx = bf.indexes(f"key-{i}".encode())
            self.assertEqual(len(idx), 5)
            self.assertTrue(all(0 <= x < 97 for x in idx))


class SizingTest(unittest.TestCase):
    def test_optimal_bits_and_hashes(self) -> None:
        self.assertEqual(BloomFilter.optimal_bits(10_000, 0.01), 95_851)
        self.assertEqual(BloomFilter.optimal_hashes(95_851, 10_000), 7)
        self.assertEqual(BloomFilter.optimal_bits(1_000, 0.001), 14_378)
        self.assertEqual(BloomFilter.optimal_hashes(14_378, 1_000), 10)

    def test_expected_rate_formula(self) -> None:
        p = BloomFilter.expected_false_positive_rate(95_851, 7, 10_000)
        self.assertAlmostEqual(p, 0.01004, places=4)
        self.assertEqual(BloomFilter.expected_false_positive_rate(100, 3, 0), 0.0)

    def test_create_uses_the_formulas(self) -> None:
        bf = BloomFilter.create(10_000, 0.01)
        self.assertEqual((bf.m, bf.k), (95_851, 7))

    def test_rejects_bad_parameters(self) -> None:
        with self.assertRaises(ValueError):
            BloomFilter(0, 3)
        with self.assertRaises(ValueError):
            BloomFilter(100, 0)
        with self.assertRaises(ValueError):
            BloomFilter.optimal_bits(100, 1.5)


class MembershipTest(unittest.TestCase):
    def test_empty_filter_contains_nothing(self) -> None:
        bf = BloomFilter(1024, 3)
        self.assertFalse(bf.might_contain("anything"))
        self.assertEqual(bf.bit_count(), 0)

    def test_no_false_negatives(self) -> None:
        # The invariant: every added key is reported as possibly present, always.
        bf = BloomFilter.create(1_000, 0.01)
        for i in range(1_000):
            bf.add(f"pay_{i}")
        for i in range(1_000):
            self.assertTrue(bf.might_contain(f"pay_{i}"))

    def test_add_reports_whether_bits_changed(self) -> None:
        bf = BloomFilter(4096, 4)
        self.assertTrue(bf.add("pay_7f3a"))
        self.assertFalse(bf.add("pay_7f3a"))
        self.assertEqual(bf.insertions, 2)
        self.assertEqual(bf.bit_count(), 4)

    def test_str_and_bytes_keys_are_the_same_key(self) -> None:
        bf = BloomFilter(4096, 4)
        bf.add("pay_7f3a")
        self.assertTrue(bf.might_contain(b"pay_7f3a"))

    def test_measured_false_positive_rate_matches_formula(self) -> None:
        bf = BloomFilter.create(10_000, 0.01)
        measured = measure_false_positive_rate(bf, 10_000, 20_000)
        predicted = bf.estimated_false_positive_rate()
        self.assertGreater(measured, 0.5 * predicted)
        self.assertLess(measured, 1.5 * predicted)
        self.assertAlmostEqual(bf.fill_ratio(), 1 - math.exp(-7 * 10_000 / 95_851), delta=0.01)

    def test_overfilling_destroys_the_guarantee(self) -> None:
        # The failure the lesson warns about: 10x the sized capacity makes the filter useless.
        bf = BloomFilter(95_851, 7)
        for i in range(100_000):
            bf.add(f"k-{i}")
        self.assertGreater(bf.estimated_false_positive_rate(), 0.9)
        self.assertGreater(bf.fill_ratio(), 0.99)

    def test_approximate_count(self) -> None:
        bf = BloomFilter.create(10_000, 0.01)
        for i in range(10_000):
            bf.add(f"k-{i}")
        self.assertLess(abs(bf.approximate_count() - 10_000), 300)
        self.assertEqual(BloomFilter(64, 2).approximate_count(), 0)


class UnionTest(unittest.TestCase):
    def test_union_answers_for_both_sets(self) -> None:
        a, b = BloomFilter(4096, 4), BloomFilter(4096, 4)
        a.add("shard-a-key")
        b.add("shard-b-key")
        both = a.union(b)
        self.assertTrue(both.might_contain("shard-a-key"))
        self.assertTrue(both.might_contain("shard-b-key"))
        self.assertFalse(both.might_contain("shard-c-key"))
        self.assertEqual(both.insertions, 2)

    def test_union_rejects_mismatched_parameters(self) -> None:
        with self.assertRaises(ValueError):
            BloomFilter(4096, 4).union(BloomFilter(2048, 4))
        with self.assertRaises(ValueError):
            BloomFilter(4096, 4).union(BloomFilter(4096, 3))


if __name__ == "__main__":
    unittest.main()
