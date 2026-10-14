"""Tests for phases/02-algorithms/01-sorting-algorithms. Run: python3 -m unittest -v"""

import unittest

from main import (
    BY_AMOUNT,
    BY_SEQ,
    USD,
    CountingComparator,
    Currency,
    JavaRandom,
    Money,
    Sorts,
    Transaction,
    natural_order,
    random_ints,
    sample_ledger,
    then_comparing,
)

NAMES = ["insertion", "merge", "quick", "heap"]
ALL = [
    lambda a, c: Sorts.insertion_sort(a, c),
    lambda a, c: Sorts.merge_sort(a, c),
    lambda a, c: Sorts.quick_sort(a, c, JavaRandom(99)),
    lambda a, c: Sorts.heap_sort(a, c),
]
NLOGN = 49_152  # round(4096 * log2(4096))


def same_multiset_and_sorted(inp: list, out: list) -> bool:
    """The reference: every sort must produce exactly what the built-in stable sort produces."""
    return sorted(inp) == out


class SortingTest(unittest.TestCase):
    def test_happy_path_random_arrays_every_algorithm(self):
        rng = JavaRandom(2024)
        for k, sort in enumerate(ALL):
            for n in (0, 1, 2, 15, 16, 17, 100, 1000):
                a = random_ints(n, 50, rng)
                out = list(a)
                sort(out, natural_order)
                with self.subTest(algorithm=NAMES[k], n=n):
                    self.assertTrue(same_multiset_and_sorted(a, out))

    def test_boundaries_ascending_descending_all_equal(self):
        asc = list(range(500))
        desc = list(range(499, -1, -1))
        same = [7] * 500
        for k, sort in enumerate(ALL):
            a, d, s = list(asc), list(desc), list(same)
            sort(a, natural_order)
            sort(d, natural_order)
            sort(s, natural_order)
            with self.subTest(algorithm=NAMES[k]):
                self.assertEqual(a, asc)
                self.assertEqual(d, asc)
                self.assertEqual(s, same)

    def test_stability_on_the_sample_ledger(self):
        ledger = sample_ledger()
        by_insertion = list(ledger)
        Sorts.insertion_sort(by_insertion, BY_AMOUNT)
        by_merge = list(ledger)
        Sorts.merge_sort(by_merge, BY_AMOUNT)
        by_heap = list(ledger)
        Sorts.heap_sort(by_heap, BY_AMOUNT)
        self.assertTrue(Sorts.is_stable_permutation(ledger, by_insertion, BY_AMOUNT, BY_SEQ))
        self.assertTrue(Sorts.is_stable_permutation(ledger, by_merge, BY_AMOUNT, BY_SEQ))
        self.assertEqual([t.seq for t in by_merge], [8, 2, 4, 7, 1, 3, 6, 5])
        self.assertTrue(Sorts.is_sorted(by_heap, BY_AMOUNT))
        self.assertFalse(Sorts.is_stable_permutation(ledger, by_heap, BY_AMOUNT, BY_SEQ))

    def test_stability_at_scale_and_the_total_key_fix(self):
        rng = JavaRandom(2024)
        for n in (0, 1, 2, 15, 16, 17, 100, 1000):  # advance the generator exactly as the Java test does
            for _ in range(len(ALL)):
                random_ints(n, 50, rng)
        big = [Transaction(i, "a", Money.of_minor(rng.next_int(5) * 100, USD)) for i in range(2000)]
        big_merge = list(big)
        Sorts.merge_sort(big_merge, BY_AMOUNT)
        self.assertTrue(Sorts.is_stable_permutation(big, big_merge, BY_AMOUNT, BY_SEQ))
        big_quick = list(big)
        Sorts.quick_sort(big_quick, BY_AMOUNT, JavaRandom(5))
        self.assertTrue(Sorts.is_sorted(big_quick, BY_AMOUNT))
        self.assertFalse(Sorts.is_stable_permutation(big, big_quick, BY_AMOUNT, BY_SEQ))
        big_heap_fixed = list(big)
        Sorts.heap_sort(big_heap_fixed, then_comparing(BY_AMOUNT, BY_SEQ))
        self.assertEqual(big_heap_fixed, big_merge)

    def test_comparison_counts_match_the_complexity(self):
        data = random_ints(4096, 1_000_000, JavaRandom(42))
        c_ins = CountingComparator.natural()
        Sorts.insertion_sort(list(data), c_ins)
        c_merge = CountingComparator.natural()
        Sorts.merge_sort(list(data), c_merge)
        c_quick = CountingComparator.natural()
        Sorts.quick_sort(list(data), c_quick, JavaRandom(7))
        c_heap = CountingComparator.natural()
        Sorts.heap_sort(list(data), c_heap)
        self.assertGreater(c_ins.comparisons, 4096 * 4096 // 8)
        self.assertLessEqual(c_merge.comparisons, 1.1 * NLOGN)
        self.assertLessEqual(c_quick.comparisons, 2 * NLOGN)
        self.assertLessEqual(c_heap.comparisons, 2.2 * NLOGN)
        self.assertGreater(c_heap.comparisons, c_merge.comparisons)

    def test_three_way_partition_is_linear_on_all_equal_keys(self):
        equal = [5] * 4096
        c_eq = CountingComparator.natural()
        Sorts.quick_sort(equal, c_eq, JavaRandom(7))
        self.assertLess(c_eq.comparisons, 3 * 4096)

    def test_adaptivity_insertion_vs_heap_on_nearly_sorted(self):
        nearly = list(range(4096))
        Sorts.swap(nearly, 10, 20)
        Sorts.swap(nearly, 3000, 3001)
        c_near = CountingComparator.natural()
        Sorts.insertion_sort(list(nearly), c_near)
        c_near_heap = CountingComparator.natural()
        Sorts.heap_sort(list(nearly), c_near_heap)
        self.assertLess(c_near.comparisons, 2 * 4096)
        self.assertGreater(c_near_heap.comparisons, NLOGN // 2)

    def test_random_pivot_defeats_sorted_input_worst_case(self):
        sorted_big = list(range(8192))
        c = CountingComparator.natural()
        Sorts.quick_sort(sorted_big, c, JavaRandom(11))
        self.assertLess(c.comparisons, 8192 * 8192 // 50)

    def test_merge_helper_and_stability_checker(self):
        halves = [1, 4, 7, 2, 3, 9]
        Sorts.merge(halves, [None] * 6, 0, 3, 6, natural_order)
        self.assertEqual(halves, [1, 2, 3, 4, 7, 9])
        ledger = sample_ledger()
        self.assertFalse(Sorts.is_stable_permutation(ledger, list(ledger), BY_AMOUNT, BY_SEQ))

    def test_money_guards(self):
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).compare_to(Money.of_minor(1, Currency("EUR", 2)))
        self.assertEqual(str(Money.of_minor(1999, USD)), "19.99 USD")


if __name__ == "__main__":
    unittest.main()
