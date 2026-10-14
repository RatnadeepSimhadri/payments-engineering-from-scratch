"""Tests for phases/02-algorithms/07-greedy-algorithms. Run: python3 -m unittest -v"""

import unittest

from main import LONG_MAX, ChangeMaking, Huffman, Interval, IntervalScheduling


class JavaRandom:
    """java.util.Random (48-bit LCG), so the random instances are the same as the Java test's seed 21."""

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


def pairwise_disjoint(xs):
    for i in range(len(xs)):
        for j in range(i + 1, len(xs)):
            a, b = xs[i], xs[j]
            if a.start < b.end and b.start < a.end:
                return False
    return True


def brute_max_non_overlapping(xs):
    """The largest pairwise-disjoint subset, by enumerating all 2^n subsets (n <= 12)."""
    best = 0
    for mask in range(1 << len(xs)):
        sub = [xs[i] for i in range(len(xs)) if mask & (1 << i)]
        if pairwise_disjoint(sub):
            best = max(best, len(sub))
    return best


def max_depth(xs):
    """Maximum number of intervals covering any single point: the lower bound on resources."""
    best = 0
    for a in xs:
        best = max(best, sum(1 for b in xs if b.start <= a.start < b.end))
    return best


WINDOWS = [
    Interval("ACH-batch", 0, 6), Interval("card-settle", 1, 4), Interval("wire-cutoff", 3, 5),
    Interval("fx-fixing", 3, 8), Interval("payout-run", 4, 7), Interval("recon", 5, 9),
    Interval("eod-close", 6, 10), Interval("reporting", 8, 11)]
FREQ = {0: 850_000, 5: 60_000, 51: 45_000, 14: 20_000, 54: 15_000, 91: 7_000, 96: 3_000}
USD_COINS = [1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000]
ODD = [1, 3, 4]
OLD_UK = [1, 3, 6, 12, 24, 30, 60, 240]


class IntervalSchedulingTest(unittest.TestCase):
    def test_earliest_end_first(self):
        chosen = IntervalScheduling.max_non_overlapping(WINDOWS)
        self.assertEqual(len(chosen), 3)
        self.assertTrue(pairwise_disjoint(chosen))
        self.assertEqual([iv.name for iv in chosen], ["card-settle", "payout-run", "reporting"])
        self.assertEqual(len(chosen), brute_max_non_overlapping(WINDOWS))
        self.assertEqual(IntervalScheduling.max_non_overlapping([]), [])
        # touching intervals are compatible ([0,5) and [5,8))
        self.assertEqual(len(IntervalScheduling.max_non_overlapping([Interval("a", 0, 5), Interval("b", 5, 8)])), 2)
        with self.assertRaises(ValueError):
            Interval("bad", 5, 5)

    def test_the_wrong_greedy_fails_on_the_trap(self):
        trap = [Interval("long-1", 0, 10), Interval("short", 9, 12), Interval("long-2", 11, 20)]
        self.assertEqual(len(IntervalScheduling.shortest_first(trap)), 1)
        self.assertEqual(len(IntervalScheduling.max_non_overlapping(trap)), 2)

    def test_min_resources(self):
        self.assertEqual(IntervalScheduling.min_resources(WINDOWS), 4)  # four overlap at t=3
        self.assertEqual(IntervalScheduling.min_resources([Interval("a", 0, 1), Interval("b", 1, 2)]), 1)
        self.assertEqual(IntervalScheduling.min_resources([]), 0)


class RandomizedOracleTest(unittest.TestCase):
    """One generator, drawn in the Java test's order: 300 interval instances, then 200 USD amounts."""

    def test_greedy_against_brute_force_and_dp(self):
        rng = JavaRandom(21)
        oracle = resources_ok = True
        for _ in range(300):
            n = rng.next_int(11)
            xs = []
            for i in range(n):
                s = rng.next_int(30)
                xs.append(Interval(f"i{i}", s, s + 1 + rng.next_int(10)))
            g = IntervalScheduling.max_non_overlapping(xs)
            if not pairwise_disjoint(g) or len(g) != brute_max_non_overlapping(xs):
                oracle = False
            if IntervalScheduling.min_resources(xs) != max_depth(xs):
                resources_ok = False
        self.assertTrue(oracle, "earliest-end equals brute-force optimum on 300 random instances")
        self.assertTrue(resources_ok, "minResources equals maximum overlap depth on 300 random instances")
        usd_oracle = True
        for _ in range(200):
            amt = rng.next_int(5000)
            if ChangeMaking.total(ChangeMaking.greedy(USD_COINS, amt)) != ChangeMaking.optimal_count(USD_COINS, amt):
                usd_oracle = False
        self.assertTrue(usd_oracle, "greedy equals DP optimum on 200 random USD amounts")


class HuffmanTest(unittest.TestCase):
    def test_huffman(self):
        freq = dict(sorted(FREQ.items()))
        tree = Huffman.build(freq)
        codes = Huffman.codes(tree)
        self.assertEqual(len(codes), 7)
        self.assertTrue(Huffman.is_prefix_free(codes))
        self.assertEqual(len(codes[0]), 1)  # most frequent symbol gets a 1-bit code
        self.assertTrue(len(codes[96]) >= len(codes[5]) and len(codes[91]) >= len(codes[5]))
        bits = Huffman.encoded_bits(freq, codes)
        self.assertLess(bits, 3 * sum(freq.values()))
        self.assertEqual(bits, 1_320_000)
        # deterministic across builds, and independent of the caller's dict order
        self.assertEqual(Huffman.codes(Huffman.build(dict(reversed(list(FREQ.items()))))), codes)
        msg = [0, 0, 0, 5, 0, 51, 0, 0, 14, 0]
        self.assertEqual(Huffman.decode(Huffman.encode(msg, codes), tree), msg)
        with self.assertRaises(ValueError):  # truncated input
            Huffman.decode(codes[96][:2], tree)

    def test_huffman_edges(self):
        self.assertEqual(Huffman.codes(Huffman.build({7: 10}))[7], "0")  # one symbol still gets one bit
        with self.assertRaises(ValueError):
            Huffman.build({})
        with self.assertRaises(ValueError):
            Huffman.build({1: 0})

    def test_huffman_clrs_example(self):
        small = {1: 45, 2: 13, 3: 12, 4: 16, 5: 9, 6: 5}
        small_codes = Huffman.codes(Huffman.build(small))
        self.assertEqual(Huffman.encoded_bits(small, small_codes), 224)
        kraft = sum(2.0 ** -len(c) for c in small_codes.values())  # a full binary tree has Kraft sum exactly 1
        self.assertLess(abs(kraft - 1.0), 1e-12)


class ChangeMakingTest(unittest.TestCase):
    def test_canonical_and_not(self):
        self.assertTrue(ChangeMaking.is_canonical(USD_COINS))
        self.assertFalse(ChangeMaking.is_canonical(ODD))
        self.assertEqual(ChangeMaking.total(ChangeMaking.greedy(ODD, 6)), 3)  # 4+1+1
        self.assertEqual(ChangeMaking.optimal_count(ODD, 6), 2)  # 3+3
        self.assertFalse(ChangeMaking.is_canonical(OLD_UK))
        self.assertTrue(ChangeMaking.total(ChangeMaking.greedy(OLD_UK, 48)) == 3
                        and ChangeMaking.optimal_count(OLD_UK, 48) == 2)

    def test_usd_payout_and_edges(self):
        g = ChangeMaking.greedy(USD_COINS, 18_741)
        self.assertEqual(ChangeMaking.total(g), 11)
        self.assertEqual(ChangeMaking.total(g), ChangeMaking.optimal_count(USD_COINS, 18_741))
        self.assertIsNone(ChangeMaking.greedy([5, 10], 7))
        self.assertEqual(ChangeMaking.optimal_count([5, 10], 7), LONG_MAX)
        self.assertEqual(ChangeMaking.total(ChangeMaking.greedy(USD_COINS, 0)), 0)
        self.assertFalse(ChangeMaking.is_canonical([2, 5]))


if __name__ == "__main__":
    unittest.main()
