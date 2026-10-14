"""Tests for phases/02-algorithms/04-recursion-and-backtracking. Run: python3 -m unittest -v"""

import sys
import unittest

from main import USD, Counter, Currency, Depth, Enumerate, Money, NQueens, SettlementAllocator


def total(ms):
    return sum(m.minor_units for m in ms)


RECEIVABLES = [Money.of_minor(4_500, USD), Money.of_minor(7_500, USD), Money.of_minor(2_000, USD),
               Money.of_minor(12_000, USD), Money.of_minor(5_500, USD), Money.of_minor(1_000, USD),
               Money.of_minor(3_000, USD)]
TARGET = Money.of_minor(12_000, USD)


class EnumerateTest(unittest.TestCase):
    def test_permutations(self):
        perms = Enumerate.permutations(["A", "B", "C"])
        self.assertEqual(len(perms), 6)
        self.assertEqual(len({tuple(p) for p in perms}), 6)
        self.assertEqual(perms[0], ["A", "B", "C"])
        self.assertEqual(perms[5], ["C", "B", "A"])
        self.assertEqual(len(Enumerate.permutations([])), 1)  # one empty permutation
        self.assertEqual(len(Enumerate.permutations([1, 2, 3, 4, 5])), 120)

    def test_subsets(self):
        subs = Enumerate.subsets(["A", "B", "C"])
        self.assertEqual(len(subs), 8)
        self.assertEqual(len({tuple(s) for s in subs}), 8)
        self.assertEqual(subs[0], [])
        self.assertEqual(subs[7], ["A", "B", "C"])
        self.assertTrue(all(all(s[i - 1] < s[i] for i in range(1, len(s))) for s in subs))
        self.assertEqual(len(Enumerate.subsets(list(range(10)))), 1024)

    def test_combinations(self):
        self.assertEqual(len(Enumerate.combinations(["A", "B", "C", "D"], 2)), 6)
        self.assertEqual(len(Enumerate.combinations([1, 2, 3, 4, 5], 0)), 1)
        self.assertEqual(len(Enumerate.combinations([1, 2, 3, 4, 5], 5)), 1)
        self.assertEqual(len(Enumerate.combinations(list(range(10)), 3)), 120)
        with self.assertRaises(ValueError):
            Enumerate.combinations([1, 2], 3)


class SettlementAllocatorTest(unittest.TestCase):
    def test_allocations(self):
        c_no, c_yes, c_it = Counter(), Counter(), Counter()
        no_prune = SettlementAllocator.allocations(RECEIVABLES, TARGET, False, c_no)
        pruned = SettlementAllocator.allocations(RECEIVABLES, TARGET, True, c_yes)
        it = SettlementAllocator.allocations_iterative(RECEIVABLES, TARGET, c_it)
        self.assertTrue(all(total(a) == 12_000 for a in pruned))
        self.assertEqual(len({tuple(a) for a in pruned}), len(pruned))
        self.assertEqual(len(pruned), 3)
        self.assertEqual(no_prune, pruned)  # pruning does not change the answer
        self.assertEqual(it, pruned)  # iterative matches recursive, same order
        self.assertLess(c_yes.visited, c_no.visited)
        self.assertLessEqual(c_no.visited, (1 << 8) - 1)  # 2^(n+1) - 1 bound
        self.assertEqual(c_it.visited, c_yes.visited)
        self.assertIn([Money.of_minor(12_000, USD)], pruned)
        self.assertEqual(SettlementAllocator.allocations(RECEIVABLES, Money.of_minor(1, USD), True, Counter()), [])
        self.assertEqual(SettlementAllocator.allocations(RECEIVABLES, Money.of_minor(0, USD), True, Counter()), [[]])
        with self.assertRaises(ValueError):
            SettlementAllocator.allocations([Money.of_minor(1, Currency("EUR", 2))], TARGET, True, Counter())
        with self.assertRaises(ValueError):
            SettlementAllocator.allocations([Money.of_minor(0, USD)], TARGET, True, Counter())

    def test_pruning_at_scale(self):
        many = [Money.of_minor(100 * i * (i % 5 + 1), USD) for i in range(1, 21)]
        big1, big2 = Counter(), Counter()
        r1 = SettlementAllocator.allocations(many, Money.of_minor(30_000, USD), False, big1)
        r2 = SettlementAllocator.allocations(many, Money.of_minor(30_000, USD), True, big2)
        self.assertEqual(r1, r2)
        self.assertLess(big2.visited * 10, big1.visited)


class NQueensTest(unittest.TestCase):
    def test_n_queens(self):
        q4, q6, q8 = Counter(), Counter(), Counter()
        self.assertEqual(NQueens.count_solutions(4, q4), 2)
        self.assertEqual(NQueens.count_solutions(6, q6), 4)
        self.assertEqual(NQueens.count_solutions(8, q8), 92)
        self.assertLess(q8.visited, 20_000)
        self.assertEqual(NQueens.count_solutions(1, Counter()), 1)
        self.assertEqual(NQueens.count_solutions(3, Counter()), 0)


class DepthTest(unittest.TestCase):
    """Python's limit is sys.getrecursionlimit(), not a stack size; no assertion depends on the exact number."""

    def test_recursive_and_iterative_agree(self):
        with Depth.recursion_limit(5_000):  # 1,000 frames is past CPython's default limit of 1,000
            self.assertTrue(Depth.sum_recursive(1_000) == Depth.sum_iterative(1_000)
                            and Depth.sum_iterative(1_000) == 500_500)

    def test_deep_recursion_fails_and_iteration_does_not(self):
        with Depth.recursion_limit(10_000):
            with self.assertRaises(RecursionError):  # the failure the lesson warns about
                Depth.sum_recursive(50_000_000)
        self.assertEqual(Depth.sum_iterative(50_000_000), 50_000_000 * 50_000_001 // 2)

    def test_limit_is_a_setting_and_the_probe_respects_it(self):
        with Depth.recursion_limit(500):
            with self.assertRaises(RecursionError):
                Depth.sum_recursive(1_000)
            depth = Depth.measure_max_depth()
        self.assertTrue(0 < depth < 500)
        limit = sys.getrecursionlimit()
        with Depth.recursion_limit(limit + 1):
            pass
        self.assertEqual(sys.getrecursionlimit(), limit)  # restored on exit


if __name__ == "__main__":
    unittest.main()
