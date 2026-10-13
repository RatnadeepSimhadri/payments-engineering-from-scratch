"""Tests for phases/02-algorithms/06-dynamic-programming. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR, INF, UNREACHABLE, USD, Alignment, Batch, CurrencyMismatchError, Money, Payout,
    best_batch, count_ways, greedy_batch, greedy_coins, ids, lcs_align, lcs_length,
    min_coins_memo, min_coins_recursive, min_coins_table,
)

US = [1, 5, 10, 25]
ODD = [1, 3, 4]
PAYOUTS = [
    Payout("P-A", Money.of_minor(6000, USD), 100),
    Payout("P-B", Money.of_minor(5000, USD), 90),
    Payout("P-C", Money.of_minor(5000, USD), 90),
    Payout("P-D", Money.of_minor(3000, USD), 10),
    Payout("P-E", Money.of_minor(1000, USD), 5),
    Payout("P-F", Money.of_minor(12000, USD), 999),
]
LIMIT = Money.of_minor(10000, USD)


def is_subsequence(sub, seq):
    i = 0
    for s in seq:
        if i < len(sub) and sub[i] == s:
            i += 1
    return i == len(sub)


class CoinChangeTest(unittest.TestCase):
    def test_us_coins_63(self):
        r = min_coins_table(US, 63)
        self.assertEqual(r.coin_count, 6)
        self.assertEqual(r.coins(US), [25, 25, 10, 1, 1, 1])
        self.assertEqual(sum(r.coins(US)), 63)

    def test_non_canonical_greedy_is_wrong(self):
        self.assertEqual(min_coins_table(ODD, 6).coin_count, 2)
        self.assertEqual(min_coins_table(ODD, 6).coins(ODD), [3, 3])
        self.assertEqual(greedy_coins(ODD, 6), 3)
        self.assertEqual(greedy_coins(US, 63), 6)

    def test_boundaries(self):
        self.assertEqual(min_coins_table(US, 0).coin_count, 0)
        self.assertEqual(min_coins_table([2], 3).coin_count, UNREACHABLE)
        self.assertFalse(min_coins_table([2], 3).reachable())
        self.assertEqual(min_coins_table([25, 1, 10, 5], 63).coin_count, 6)

    def test_validation(self):
        with self.assertRaises(ValueError):
            min_coins_table(US, -1)
        with self.assertRaises(ValueError):
            min_coins_table([0, 1], 5)
        with self.assertRaises(ValueError):
            min_coins_table([], 5)

    def test_three_forms_agree(self):
        for a in range(21):
            r = min_coins_recursive(ODD, a, [0])
            m = min_coins_memo(ODD, a, [0])
            t = min_coins_table(ODD, a).coin_count
            self.assertEqual(r, m, a)
            self.assertEqual(m, t, a)

    def test_call_counts(self):
        naive, memo = [0], [0]
        min_coins_recursive(ODD, 20, naive)
        min_coins_memo(ODD, 20, memo)
        self.assertEqual(naive[0], 20736)
        self.assertEqual(memo[0], 56)
        self.assertEqual(min_coins_memo([2], 3, [0]), INF)

    def test_count_ways(self):
        self.assertEqual(count_ways([1, 2, 5], 5), 4)
        self.assertEqual(count_ways(US, 0), 1)
        self.assertEqual(count_ways([2], 3), 0)
        self.assertEqual(count_ways(US, 100), 242)
        self.assertEqual(count_ways([5, 2, 1], 5), 4)


class KnapsackTest(unittest.TestCase):
    def test_best_batch(self):
        best = best_batch(PAYOUTS, LIMIT)
        self.assertEqual(best.priority, 180)
        self.assertEqual(ids(best.chosen), ["P-B", "P-C"])
        self.assertLessEqual(best.total, LIMIT)
        self.assertEqual(sum(p.priority for p in best.chosen), best.priority)
        self.assertEqual(best.total.minor_units, 10000)
        self.assertNotIn("P-F", ids(best.chosen))

    def test_greedy_is_worse(self):
        greedy = greedy_batch(PAYOUTS, LIMIT)
        self.assertEqual(greedy.priority, 115)
        self.assertLess(greedy.priority, best_batch(PAYOUTS, LIMIT).priority)

    def test_boundaries(self):
        self.assertEqual(best_batch([], LIMIT).chosen, [])
        self.assertEqual(best_batch(PAYOUTS, Money.of_minor(0, USD)).priority, 0)
        single = [Payout("X", Money.of_minor(700, USD), 7)]
        self.assertEqual(best_batch(single, Money.of_minor(700, USD)).priority, 7)

    def test_validation(self):
        with self.assertRaises(CurrencyMismatchError):
            best_batch([Payout("E", Money.of_minor(100, EUR), 1)], LIMIT)
        with self.assertRaises(ValueError):
            best_batch([Payout("Z", Money.of_minor(0, USD), 1)], LIMIT)

    def test_money_rejects_float(self):
        with self.assertRaises(TypeError):
            Money(10.0, USD)


class LcsTest(unittest.TestCase):
    X = ["A", "B", "C", "B", "D", "A", "B"]
    Y = ["B", "D", "C", "A", "B", "A"]

    def test_clrs_example(self):
        self.assertEqual(lcs_length(self.X, self.Y), 4)
        al = lcs_align(self.X, self.Y)
        self.assertEqual(len(al.common), 4)
        self.assertTrue(is_subsequence(al.common, self.X))
        self.assertTrue(is_subsequence(al.common, self.Y))
        self.assertEqual(len(al.left_only) + len(al.common), len(self.X))
        self.assertEqual(len(al.right_only) + len(al.common), len(self.Y))

    def test_boundaries(self):
        same = lcs_align(self.X, self.X)
        self.assertEqual(same.common, self.X)
        self.assertEqual(same.left_only, [])
        self.assertEqual(lcs_length(["A", "B"], ["C", "D"]), 0)
        self.assertEqual(lcs_length([], self.Y), 0)

    def test_statement_alignment(self):
        bank = ["DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00"]
        ledger = ["DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99"]
        al = lcs_align(bank, ledger)
        self.assertEqual(al, Alignment(["DEP 500.00", "CARD 19.99", "CARD 7.00"],
                                       ["CARD 42.10", "FEE 2.50"],
                                       ["CARD 42.10", "REFUND 19.99"]))


if __name__ == "__main__":
    unittest.main()
