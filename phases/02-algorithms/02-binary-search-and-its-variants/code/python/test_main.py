"""Tests for phases/02-algorithms/02-binary-search-and-its-variants. Run: python3 -m unittest -v"""

import bisect
import unittest

from main import LONG_MAX, USD, BinarySearch, CappedBatch, Currency, Money, to_int32, trunc_div


class JavaRandom:
    """java.util.Random (48-bit LCG), so the random trials match the Java test's seed 7."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        return self._seed >> (48 - bits)

    def next_int(self, bound: int) -> int:
        if bound & (bound - 1) == 0:
            return (bound * self._next(31)) >> 31
        while True:
            bits = self._next(31)
            val = bits % bound
            if bits - val + (bound - 1) < (1 << 31):
                return val


def linear_lower_bound(a, t):
    i = 0
    while i < len(a) and a[i] < t:
        i += 1
    return i


def linear_upper_bound(a, t):
    i = 0
    while i < len(a) and a[i] <= t:
        i += 1
    return i


FEES = [30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500]
EMPTY: list = []
ONE = [42]


class ClassicSearchTest(unittest.TestCase):
    def test_index_of(self):
        self.assertEqual(FEES[BinarySearch.index_of(FEES, 75)], 75)
        self.assertEqual(BinarySearch.index_of(FEES, 100), 6)
        self.assertEqual(BinarySearch.index_of(FEES, 60), -(4 + 1))
        self.assertEqual(BinarySearch.index_of(FEES, 1), -1)
        self.assertEqual(BinarySearch.index_of(FEES, 9999), -(len(FEES) + 1))
        self.assertEqual(BinarySearch.index_of(EMPTY, 5), -1)
        self.assertEqual(BinarySearch.index_of(ONE, 42), 0)
        self.assertEqual(BinarySearch.index_of(ONE, 43), -2)
        # the JDK's Arrays.binarySearch encoding, built from the stdlib bisect module
        self.assertEqual(BinarySearch.index_of(FEES, 60), -(bisect.bisect_left(FEES, 60) + 1))


class BoundsTest(unittest.TestCase):
    def test_bounds_agree_with_linear_oracle(self):
        rng = JavaRandom(7)
        all_agree = True
        for _ in range(200):
            n = rng.next_int(40)
            a = sorted(rng.next_int(20) for _ in range(n))
            for t in range(-1, 21):
                if BinarySearch.lower_bound(a, t) != linear_lower_bound(a, t):
                    all_agree = False
                if BinarySearch.upper_bound(a, t) != linear_upper_bound(a, t):
                    all_agree = False
        self.assertTrue(all_agree)

    def test_bounds_and_counts(self):
        self.assertEqual(BinarySearch.lower_bound(FEES, 250), 7)
        self.assertEqual(BinarySearch.upper_bound(FEES, 250), 11)
        self.assertTrue(BinarySearch.lower_bound(FEES, 60) == 4 and BinarySearch.upper_bound(FEES, 60) == 4)
        self.assertEqual(BinarySearch.lower_bound(FEES, 10_000), len(FEES))
        self.assertEqual(BinarySearch.upper_bound(FEES, 0), 0)
        self.assertEqual(BinarySearch.count(FEES, 250), 4)
        self.assertEqual(BinarySearch.count(FEES, 60), 0)
        self.assertEqual(BinarySearch.count_in_range(FEES, 30, 100), 7)
        self.assertEqual(BinarySearch.count_in_range(FEES, 51, 74), 0)


class FirstTrueTest(unittest.TestCase):
    def test_first_true(self):
        self.assertEqual(BinarySearch.first_true(0, len(FEES), lambda i: FEES[i] > 200), 7)
        self.assertEqual(BinarySearch.first_true(0, len(FEES), lambda i: False), len(FEES))
        self.assertEqual(BinarySearch.first_true(0, len(FEES), lambda i: True), 0)
        self.assertEqual(BinarySearch.first_true(5, 5, lambda i: True), 5)
        self.assertEqual(BinarySearch.first_true_long(0, 10_000_000, lambda x: x * x >= 1_000_000_000_000), 1_000_000)

    def test_range_whose_sum_overflows_java_int(self):
        calls = [0]

        def pred(i):
            calls[0] += 1
            return i >= 1_999_999_999

        big = BinarySearch.first_true(1_500_000_000, 2_000_000_000, pred)
        self.assertEqual(big, 1_999_999_999)
        self.assertLessEqual(calls[0], 30)

    def test_java_int_midpoint_wraps_but_python_does_not(self):
        lo, hi = 1_500_000_000, 2_000_000_000
        self.assertEqual(trunc_div(to_int32(lo + hi), 2), -397_483_648)  # what Java's (lo + hi) / 2 prints
        self.assertEqual((lo + hi) // 2, lo + ((hi - lo) >> 1))  # Python ints never wrap
        self.assertEqual(trunc_div(-7, 2), -3)  # Java truncates; -7 // 2 is -4


class RotatedTest(unittest.TestCase):
    def test_rotated(self):
        rotated = [40, 50, 60, 70, 10, 20, 30]
        self.assertEqual(BinarySearch.rotation_point(rotated), 4)
        self.assertEqual(BinarySearch.rotation_point([1, 2, 3]), 0)
        self.assertEqual(BinarySearch.index_of_rotated(rotated, 20), 5)
        self.assertEqual(BinarySearch.index_of_rotated(rotated, 60), 2)
        self.assertEqual(BinarySearch.index_of_rotated(rotated, 65), -1)
        self.assertEqual(BinarySearch.index_of_rotated(EMPTY, 1), -1)

    def test_every_rotation_of_ten_elements(self):
        rotated_ok = True
        for shift in range(10):
            base = [(i + shift) % 10 * 3 for i in range(10)]  # distinct, rotated by shift
            for t in range(30):
                idx = BinarySearch.index_of_rotated(base, t)
                present = t % 3 == 0
                if present and (idx < 0 or base[idx] != t):
                    rotated_ok = False
                if not present and idx != -1:
                    rotated_ok = False
        self.assertTrue(rotated_ok)


class AnswerSpaceTest(unittest.TestCase):
    def test_integer_sqrt(self):
        self.assertEqual(BinarySearch.integer_sqrt(1_000_000), 1000)
        self.assertEqual(BinarySearch.integer_sqrt(999_999), 999)
        self.assertTrue(BinarySearch.integer_sqrt(0) == 0 and BinarySearch.integer_sqrt(1) == 1)
        self.assertEqual(BinarySearch.integer_sqrt(LONG_MAX), 3_037_000_499)
        with self.assertRaises(ArithmeticError):
            BinarySearch.integer_sqrt(-1)

    def test_capped_batch(self):
        payouts = [Money.of_minor(12_000, USD), Money.of_minor(4_500, USD), Money.of_minor(80_000, USD),
                   Money.of_minor(2_575, USD), Money.of_minor(30_000, USD)]
        cap = CappedBatch.largest_cap_within(payouts, Money.of_minor(60_000, USD))
        self.assertLessEqual(CappedBatch.total_with_cap(payouts, cap).minor_units, 60_000)
        self.assertGreater(CappedBatch.total_with_cap(payouts, cap + 1).minor_units, 60_000)
        self.assertEqual(cap, 20_462)  # 190.75 fixed + 2 capped items, (600.00 - 190.75) / 2 rounded down
        self.assertEqual(CappedBatch.largest_cap_within(payouts, Money.of_minor(1_000_000, USD)), 80_000)
        self.assertEqual(CappedBatch.largest_cap_within(payouts, Money.of_minor(0, USD)), 0)
        self.assertEqual(CappedBatch.largest_cap_within(payouts, Money.of_minor(-1, USD)), -1)
        with self.assertRaises(ValueError):
            CappedBatch.largest_cap_within(payouts, Money.of_minor(1, Currency("EUR", 2)))


if __name__ == "__main__":
    unittest.main()
