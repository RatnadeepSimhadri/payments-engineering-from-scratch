"""Tests for phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees. Run: python3 -m unittest -v"""
import unittest

from main import (LONG_MAX, USD, BalanceHistory, FenwickTree, JavaRandom, LazySegmentTree, SegmentTree,
                  naive_range_sum)

VALUES = [12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000]


def incremental(values):
    f = FenwickTree(len(values))
    for i, v in enumerate(values):
        f.add(i, v)
    return f


class FenwickTest(unittest.TestCase):
    def test_happy_path(self):
        fw = FenwickTree.build(VALUES)
        self.assertEqual(fw.prefix_sum(0), 12_500)
        self.assertEqual(fw.prefix_sum(1), 9_300)
        self.assertEqual(fw.prefix_sum(7), 18_300)
        self.assertEqual(fw.prefix_sum(-1), 0)
        self.assertEqual(fw.range_sum(2, 5), 10_250)
        self.assertEqual(fw.range_sum(4, 4), -900)
        self.assertEqual(fw.get(5), 7_000)
        fw.add(1, -100)
        self.assertEqual(fw.prefix_sum(1), 9_200)
        self.assertEqual(fw.prefix_sum(7), 18_200)
        self.assertEqual(fw.prefix_sum(0), 12_500)
        self.assertEqual(FenwickTree.build(VALUES).range_sum(0, 7), incremental(VALUES).range_sum(0, 7))
        one = FenwickTree(1)
        one.add(0, 42)
        self.assertTrue(one.prefix_sum(0) == 42 and one.range_sum(0, 0) == 42)

    def test_failures(self):
        fw = FenwickTree.build(VALUES)
        with self.assertRaises(ValueError):
            FenwickTree(0)
        with self.assertRaises(IndexError):
            fw.add(8, 1)
        with self.assertRaises(IndexError):
            fw.prefix_sum(8)
        with self.assertRaises(ValueError):
            fw.range_sum(5, 2)
        with self.assertRaises(IndexError):
            fw.add(-1, 1)  # a Python list would accept -1; the tree must not

    def test_overflow_is_an_error_not_a_wrap(self):
        f = FenwickTree(2)
        f.add(0, LONG_MAX)
        with self.assertRaises(ArithmeticError):
            f.add(1, 1)
            f.prefix_sum(1)

    def test_descent(self):
        vol = FenwickTree.build([5_000, 3_000, 8_000, 2_000, 6_000, 1_000])
        self.assertEqual(vol.first_index_with_prefix_at_least(8_000), 1)
        self.assertEqual(vol.first_index_with_prefix_at_least(20_000), 4)
        self.assertEqual(vol.first_index_with_prefix_at_least(1), 0)
        self.assertEqual(vol.first_index_with_prefix_at_least(25_000), 5)
        self.assertEqual(vol.first_index_with_prefix_at_least(25_001), -1)
        self.assertEqual(vol.first_index_with_prefix_at_least(0), 0)
        pow2 = FenwickTree.build([1] * 8)
        self.assertTrue(pow2.first_index_with_prefix_at_least(8) == 7 and pow2.first_index_with_prefix_at_least(9) == -1)


class SegmentTreeTest(unittest.TestCase):
    def test_point_set_range_sum(self):
        st = SegmentTree(VALUES)
        self.assertEqual(st.range_sum(0, 7), 18_300)
        self.assertEqual(st.range_sum(2, 5), 10_250)
        self.assertEqual(st.range_sum(0, 0), 12_500)
        self.assertEqual(st.range_sum(7, 7), 1_000)
        st.set(3, 10_000)
        self.assertEqual(st.get(3), 10_000)
        self.assertTrue(st.range_sum(2, 5) == 16_100 and st.range_sum(0, 7) == 24_150)

    def test_non_power_of_two(self):
        odd = SegmentTree([1, 2, 3, 4, 5])
        self.assertEqual(odd.range_sum(0, 4), 15)
        self.assertEqual(odd.range_sum(3, 4), 9)
        self.assertEqual(odd.range_sum(4, 4), 5)
        with self.assertRaises(ValueError):
            SegmentTree([])
        with self.assertRaises(ValueError):
            odd.range_sum(3, 1)
        with self.assertRaises(IndexError):
            odd.range_sum(0, 5)


class LazySegmentTreeTest(unittest.TestCase):
    def test_range_add_range_sum(self):
        lazy = LazySegmentTree([0] * 10)
        lazy.range_add(2, 6, 10)
        self.assertEqual(lazy.range_sum(2, 6), 50)
        self.assertEqual(lazy.range_sum(0, 3), 20)
        self.assertTrue(lazy.range_sum(7, 9) == 0 and lazy.get(1) == 0)
        lazy.range_add(5, 9, -3)
        self.assertTrue(lazy.get(5) == 7 and lazy.get(6) == 7 and lazy.get(7) == -3 and lazy.get(4) == 10)
        self.assertEqual(lazy.range_sum(0, 9), 50 - 15)
        lazy.range_add(0, 9, 1)
        self.assertTrue(lazy.range_sum(0, 9) == 45 and lazy.get(0) == 1 and lazy.get(9) == -2)
        single = LazySegmentTree([5])
        single.range_add(0, 0, 7)
        self.assertTrue(single.get(0) == 12 and single.range_sum(0, 0) == 12)

    def test_overflow_is_an_error(self):
        with self.assertRaises(ArithmeticError):
            t = LazySegmentTree([LONG_MAX, 1])
            t.range_sum(0, 1)


class RandomizedAgreementTest(unittest.TestCase):
    def test_all_three_agree_with_naive(self):
        rng = JavaRandom(7)
        n = 131
        model = [0] * n
        lz, fz, sz = LazySegmentTree(model), FenwickTree(n), SegmentTree(model)
        agree = True
        op = 0
        while op < 5_000 and agree:
            a, b = rng.next_int(n), rng.next_int(n)
            lo, hi = min(a, b), max(a, b)
            if rng.next_int(3) == 0:
                delta = rng.next_int(201) - 100
                for i in range(lo, hi + 1):
                    model[i] += delta
                    fz.add(i, delta)
                    sz.set(i, model[i])
                lz.range_add(lo, hi, delta)
            else:
                expect = naive_range_sum(model, lo, hi)
                agree = lz.range_sum(lo, hi) == expect and fz.range_sum(lo, hi) == expect and sz.range_sum(lo, hi) == expect
            op += 1
        self.assertTrue(agree)


class BalanceHistoryTest(unittest.TestCase):
    def test_payments_framing(self):
        h = BalanceHistory(100_000, 30, USD)
        self.assertTrue(h.balance_on(0).minor_units == 100_000 and h.balance_on(29).minor_units == 100_000)
        h.post(10, -40_000)
        self.assertEqual(h.balance_on(9).minor_units, 100_000)
        self.assertTrue(h.balance_on(10).minor_units == 60_000 and h.balance_on(29).minor_units == 60_000)
        self.assertEqual(h.sum_of_balances(0, 29).minor_units, 10 * 100_000 + 20 * 60_000)
        self.assertEqual(h.average_daily_balance(0, 29).minor_units, 73_333)  # 2,200,000 / 30 = 73333.33
        h.post(5, -7_500)
        self.assertEqual(h.sum_of_balances(0, 29).minor_units, 2_200_000 - 25 * 7_500)
        self.assertEqual(str(h.balance_on(29)), "525.00 USD")

    def test_divide_half_even(self):
        self.assertTrue(BalanceHistory.divide_half_even(25, 10) == 2 and BalanceHistory.divide_half_even(35, 10) == 4)
        # Java's / truncates toward zero; Python's // would give -3 for -25 // 10 and break the tie rule.
        self.assertTrue(BalanceHistory.divide_half_even(-25, 10) == -2 and BalanceHistory.divide_half_even(-26, 10) == -3)


if __name__ == "__main__":
    unittest.main()
