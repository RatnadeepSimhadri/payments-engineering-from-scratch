"""Tests for phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog. Run: python3 -m unittest -v"""
import math
import unittest

from main import CountMinSketch, Hashing, HyperLogLog, JavaRandom


class HashingTest(unittest.TestCase):
    def test_hashing(self):
        self.assertEqual(Hashing.fnv1a64("tok_1"), Hashing.fnv1a64("tok_1"))
        self.assertNotEqual(Hashing.hash64("tok_1"), Hashing.hash64("tok_2"))
        self.assertEqual(Hashing.mix64(0), 0)

    def test_hash_values_match_java_longs(self):
        # FNV-1a 64 of the empty string is the offset basis; values below are the Java longs, unsigned.
        self.assertEqual(Hashing.fnv1a64(""), 0xCBF29CE484222325)
        self.assertEqual(Hashing.fnv1a64("a"), 0xAF63DC4C8601EC8C)
        self.assertTrue(0 <= Hashing.hash64("merchant_0") <= 0xFFFFFFFFFFFFFFFF)


class CountMinSizingTest(unittest.TestCase):
    def test_sizing(self):
        sized = CountMinSketch.for_error_bounds(0.01, 0.01)
        self.assertEqual(sized.width, 272)
        self.assertEqual(sized.depth, 5)
        self.assertEqual(sized.bytes(), 272 * 5 * 8)

    def test_rejects_bad_parameters(self):
        with self.assertRaises(ValueError):
            CountMinSketch.for_error_bounds(1.5, 0.1)
        with self.assertRaises(ValueError):
            CountMinSketch.for_error_bounds(0.1, 0.0)
        with self.assertRaises(ValueError):
            CountMinSketch(0, 3)


class CountMinBehaviourTest(unittest.TestCase):
    def test_exact_on_small_inputs(self):
        cms = CountMinSketch(1024, 4)
        cms.add("m1", 5)
        cms.add("m2", 3)
        cms.add("m1")
        self.assertEqual(cms.estimate("m1"), 6)
        self.assertEqual(cms.estimate("m2"), 3)
        self.assertEqual(cms.total(), 9)
        self.assertEqual(cms.estimate("never"), 0)
        with self.assertRaises(ValueError):
            cms.add("m1", -1)

    def test_never_underestimates_and_bounds(self):
        rng = JavaRandom(7)
        busy = CountMinSketch.for_error_bounds(0.005, 0.001)
        truth: dict[str, int] = {}
        for _ in range(100_000):
            key = f"merchant_{int(abs(rng.next_gaussian()) * 300)}"
            busy.add(key)
            truth[key] = truth.get(key, 0) + 1
        never_under = True
        violations = 0
        for key, count in truth.items():
            est = busy.estimate(key)
            if est < count:
                never_under = False
            if est > count + busy.error_bound():
                violations += 1
        self.assertTrue(never_under)
        self.assertLessEqual(violations, max(1, int(len(truth) * busy.failure_probability() * 10)))
        self.assertLess(abs(busy.error_bound() - math.e / busy.width * 100_000), 1e-6)
        self.assertLess(abs(busy.failure_probability() - math.exp(-busy.depth)), 1e-12)

    def test_merge(self):
        rng = JavaRandom(7)
        left, right, both = CountMinSketch(256, 4), CountMinSketch(256, 4), CountMinSketch(256, 4)
        for _ in range(2_000):
            k = f"k{rng.next_int(50)}"
            left.add(k)
            both.add(k)
            j = f"k{rng.next_int(50)}"
            right.add(j)
            both.add(j)
        merged = left.merge(right)
        same = merged.total() == both.total()
        for i in range(50):
            same &= merged.estimate(f"k{i}") == both.estimate(f"k{i}")
        self.assertTrue(same)
        with self.assertRaises(ValueError):
            left.merge(CountMinSketch(128, 4))


class HyperLogLogRegistersTest(unittest.TestCase):
    def test_rho(self):
        self.assertEqual(HyperLogLog.rho(0x0008000000000000, 12), 1)
        self.assertEqual(HyperLogLog.rho(0x0000100000000000, 12), 8)
        self.assertEqual(HyperLogLog.rho(0, 12), 53)
        self.assertEqual(HyperLogLog.rho(0xFFF0000000000000, 12), 53)

    def test_precision_bounds(self):
        with self.assertRaises(ValueError):
            HyperLogLog(3)
        with self.assertRaises(ValueError):
            HyperLogLog(17)

    def test_alpha(self):
        self.assertEqual(HyperLogLog.alpha(16), 0.673)
        self.assertLess(abs(HyperLogLog.alpha(4096) - 0.7213 / (1 + 1.079 / 4096)), 1e-12)


class HyperLogLogCardinalityTest(unittest.TestCase):
    def test_small_cases(self):
        self.assertEqual(HyperLogLog(10).cardinality(), 0)
        one = HyperLogLog(10)
        for _ in range(3):
            one.add("tok_a")
        self.assertEqual(one.cardinality(), 1)
        small = HyperLogLog(12)
        for i in range(100):
            small.add(f"card_{i}")
        self.assertLessEqual(abs(small.cardinality() - 100), 2)

    def test_large_accuracy(self):
        rng = JavaRandom(7)
        hll = HyperLogLog(12)
        distinct: set[str] = set()
        for _ in range(200_000):
            card = f"tok_{rng.next_int(60_000)}"
            hll.add(card)
            distinct.add(card)
        rel_err = abs(hll.cardinality() - len(distinct)) / len(distinct)
        self.assertLess(abs(hll.standard_error() - 0.01625), 1e-9)
        self.assertLess(rel_err, 3 * hll.standard_error())
        self.assertEqual(hll.bytes(), 4096)

    def test_merge_is_union(self):
        a, b, union = HyperLogLog(12), HyperLogLog(12), HyperLogLog(12)
        for i in range(20_000):
            a.add(f"c{i}")
            union.add(f"c{i}")
        for i in range(10_000, 30_000):
            b.add(f"c{i}")
            union.add(f"c{i}")
        ab = a.merge(b)
        self.assertTrue(all(ab.register_at(i) == union.register_at(i) for i in range(ab.registers)))
        self.assertEqual(ab.cardinality(), union.cardinality())
        self.assertEqual(b.merge(a).cardinality(), ab.cardinality())
        with self.assertRaises(ValueError):
            a.merge(HyperLogLog(10))


if __name__ == "__main__":
    unittest.main()
