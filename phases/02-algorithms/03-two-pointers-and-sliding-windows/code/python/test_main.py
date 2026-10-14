"""Tests for phases/02-algorithms/03-two-pointers-and-sliding-windows. Run: python3 -m unittest -v"""

import unittest

from main import USD, Currency, Decision, Event, Money, SlidingWindow, TwoPointers, VelocityChecker


class JavaRandom:
    """java.util.Random (48-bit LCG), so the random trials are the same cases as the Java test's seed 11."""

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


# brute-force oracles
def brute_max_sum_fixed(a, k):
    return max(sum(a[i:i + k]) for i in range(len(a) - k + 1))


def brute_shortest_run(a, target):
    best = 0
    for i in range(len(a)):
        s = 0
        for j in range(i, len(a)):
            s += a[j]
            if s >= target:
                length = j - i + 1
                if best == 0 or length < best:
                    best = length
                break
    return best


def brute_max_of_each_window(a, k):
    return [max(a[i:i + k]) for i in range(len(a) - k + 1)]


SORTED = [75, 500, 500, 1999, 1999, 2575, 4500, 12000]
DAILY = [12000, 4500, 80000, 2575, 30000, 1999, 500, 75]
T0 = 1_700_000_000_000


class TwoAndWindowTest(unittest.TestCase):
    """One shared generator in the same draw order as the Java test, so every random case is identical."""

    def test_pointers_and_windows_with_oracles(self):
        rng = JavaRandom(11)

        # two pointers
        p = TwoPointers.pair_with_sum(SORTED, 4574)
        self.assertTrue(p is not None and SORTED[p[0]] + SORTED[p[1]] == 4574 and p[0] < p[1])
        self.assertIsNotNone(TwoPointers.pair_with_sum(SORTED, 1000))
        self.assertIsNone(TwoPointers.pair_with_sum(SORTED, 1))
        self.assertIsNone(TwoPointers.pair_with_sum([], 5))
        self.assertIsNone(TwoPointers.pair_with_sum([5], 10))
        pair_oracle = True
        for _ in range(100):
            a = sorted(rng.next_int(30) for _ in range(rng.next_int(12)))
            target = rng.next_int(60)
            exists = any(a[i] + a[j] == target for i in range(len(a)) for j in range(i + 1, len(a)))
            got = TwoPointers.pair_with_sum(a, target)
            if exists != (got is not None):
                pair_oracle = False
            if got is not None and a[got[0]] + a[got[1]] != target:
                pair_oracle = False
        self.assertTrue(pair_oracle, "pairWithSum agrees with brute force on 100 random arrays")

        # fixed window
        self.assertEqual(SlidingWindow.max_sum_fixed(DAILY, 3), 112_575)
        self.assertEqual(SlidingWindow.max_sum_fixed(DAILY, 1), 80000)
        self.assertEqual(SlidingWindow.max_sum_fixed(DAILY, len(DAILY)), 131_649)
        with self.assertRaises(ValueError):
            SlidingWindow.max_sum_fixed(DAILY, 0)
        with self.assertRaises(ValueError):
            SlidingWindow.max_sum_fixed(DAILY, 9)
        fixed_oracle = True
        for _ in range(100):
            a = [rng.next_int(200) - 50 for _ in range(1 + rng.next_int(20))]  # negatives allowed here
            k = 1 + rng.next_int(len(a))
            if SlidingWindow.max_sum_fixed(a, k) != brute_max_sum_fixed(a, k):
                fixed_oracle = False
        self.assertTrue(fixed_oracle, "maxSumFixed agrees with brute force (negatives included)")

        # variable window
        self.assertEqual(SlidingWindow.shortest_run_with_sum_at_least(DAILY, 100_000), 3)
        self.assertEqual(SlidingWindow.shortest_run_with_sum_at_least(DAILY, 80_000), 1)
        self.assertEqual(SlidingWindow.shortest_run_with_sum_at_least(DAILY, 1_000_000), 0)
        self.assertEqual(SlidingWindow.shortest_run_with_sum_at_least([1, 1, 1], 3), 3)
        with self.assertRaises(ValueError):  # negatives break monotonicity
            SlidingWindow.shortest_run_with_sum_at_least([5, -1, 5], 4)
        var_oracle = True
        for _ in range(200):
            a = [rng.next_int(10) for _ in range(rng.next_int(15))]
            target = 1 + rng.next_int(30)
            if SlidingWindow.shortest_run_with_sum_at_least(a, target) != brute_shortest_run(a, target):
                var_oracle = False
        self.assertTrue(var_oracle, "shortestRun agrees with brute force on 200 random arrays")

        # monotonic deque oracle (draws after the variable-window trials, as in Java)
        deque_oracle = True
        for _ in range(200):
            a = [rng.next_int(10) for _ in range(1 + rng.next_int(25))]  # small range: many ties
            k = 1 + rng.next_int(len(a))
            if SlidingWindow.max_of_each_window(a, k) != brute_max_of_each_window(a, k):
                deque_oracle = False
        self.assertTrue(deque_oracle, "maxOfEachWindow agrees with brute force on 200 random arrays")


class DedupeMergeTest(unittest.TestCase):
    def test_dedupe_and_merge(self):
        dupes = [1, 1, 2, 3, 3, 3, 7]
        n = TwoPointers.dedupe_sorted(dupes)
        self.assertEqual(n, 4)
        self.assertEqual(dupes[:n], [1, 2, 3, 7])
        self.assertEqual(TwoPointers.dedupe_sorted([]), 0)
        self.assertEqual(TwoPointers.dedupe_sorted([4, 4, 4]), 1)
        self.assertEqual(TwoPointers.merge_sorted([1, 4, 7], [2, 3, 9]), [1, 2, 3, 4, 7, 9])
        self.assertEqual(TwoPointers.merge_sorted([], [2, 3]), [2, 3])


class LongestWithoutRepeatTest(unittest.TestCase):
    def test_longest_without_repeat(self):
        self.assertEqual(SlidingWindow.longest_without_repeat("abcabcbb"), 3)
        self.assertEqual(SlidingWindow.longest_without_repeat("bbbb"), 1)
        self.assertEqual(SlidingWindow.longest_without_repeat("pwwkew"), 3)
        self.assertEqual(SlidingWindow.longest_without_repeat(""), 0)
        # jumps left past the repeat, not by one; the stale 'a' must not pull left backward
        self.assertEqual(SlidingWindow.longest_without_repeat("abba"), 2)


class MonotonicDequeTest(unittest.TestCase):
    def test_max_of_each_window(self):
        self.assertEqual(SlidingWindow.max_of_each_window([1, 3, -1, -3, 5, 3, 6, 7], 3), [3, 3, 5, 5, 6, 7])
        self.assertEqual(SlidingWindow.max_of_each_window(DAILY, 1), DAILY)
        self.assertEqual(SlidingWindow.max_of_each_window(DAILY, len(DAILY)), [80000])
        self.assertEqual(SlidingWindow.max_of_each_window([9, 8, 7, 6], 2), [9, 8, 7])
        self.assertEqual(SlidingWindow.max_of_each_window([5, 5, 5, 5], 2), [5, 5, 5])


class VelocityCheckerTest(unittest.TestCase):
    def test_demo_script_decisions(self):
        vc = VelocityChecker(60_000, 4, Money.of_minor(50_000, USD))
        self.assertEqual(vc.record(Event(T0, Money.of_minor(12_000, USD))), Decision.ALLOW)
        self.assertEqual(vc.record(Event(T0 + 10_000, Money.of_minor(4_500, USD))), Decision.ALLOW)
        self.assertEqual(vc.record(Event(T0 + 20_000, Money.of_minor(25_000, USD))), Decision.ALLOW)
        # count == max is fine
        self.assertEqual(vc.record(Event(T0 + 25_000, Money.of_minor(1_999, USD))), Decision.ALLOW)
        self.assertEqual(vc.record(Event(T0 + 30_000, Money.of_minor(100, USD))), VelocityChecker.Decision.DENY_COUNT)
        # the denied event still counts
        self.assertTrue(vc.count_in_window() == 5 and vc.total_in_window().minor_units == 43_599)
        # at +70 s two events aged out, the amount rule fires
        self.assertEqual(vc.record(Event(T0 + 70_000, Money.of_minor(30_000, USD))), Decision.DENY_AMOUNT)
        self.assertTrue(vc.count_in_window() == 4
                        and vc.total_in_window().minor_units == 25_000 + 1_999 + 100 + 30_000)
        # far future clears most of the window
        self.assertTrue(vc.record(Event(T0 + 125_000, Money.of_minor(75, USD))) == Decision.ALLOW
                        and vc.count_in_window() == 2)
        with self.assertRaises(ValueError):  # out-of-order event
            vc.record(Event(T0, Money.of_minor(1, USD)))
        with self.assertRaises(ValueError):
            VelocityChecker(0, 3, Money.of_minor(1, USD))

    def test_boundary_is_strict(self):
        edge = VelocityChecker(60_000, 1, Money.of_minor(1_000_000, USD))
        edge.record(Event(T0, Money.of_minor(1, USD)))
        self.assertEqual(edge.record(Event(T0 + 60_000, Money.of_minor(1, USD))), Decision.ALLOW)
        edge2 = VelocityChecker(60_000, 1, Money.of_minor(1_000_000, USD))
        edge2.record(Event(T0, Money.of_minor(1, USD)))
        self.assertEqual(edge2.record(Event(T0 + 59_999, Money.of_minor(1, USD))), Decision.DENY_COUNT)


class MoneyTest(unittest.TestCase):
    def test_money_discipline(self):
        self.assertEqual(
            Money.of_minor(1999, USD).plus(Money.of_minor(1, USD)).minus(Money.of_minor(2000, USD)).minor_units, 0)
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).plus(Money.of_minor(1, Currency("EUR", 2)))

    def test_long_overflow_is_an_error_not_a_wrap(self):
        with self.assertRaises(ArithmeticError):
            Money.of_minor(2**63 - 1, USD).plus(Money.of_minor(1, USD))


if __name__ == "__main__":
    unittest.main()
