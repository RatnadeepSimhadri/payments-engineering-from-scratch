"""Tests for phases/01-complexity-and-core-structures/03-dynamic-arrays. Run: python3 -m unittest -v"""
import unittest

from main import DynamicArray, GrowthPolicy, measure_appends


def invariant_holds(a, expected_size):
    """The structural invariant: size within capacity, live prefix intact."""
    if a.size() != expected_size or a.size() > a.capacity():
        return False
    return all(a.get(i) is not None for i in range(a.size()))


class GrowthPolicyTest(unittest.TestCase):
    def test_next_capacity(self):
        self.assertEqual(GrowthPolicy.GROW_1_5X.next(8), 12)
        self.assertEqual(GrowthPolicy.GROW_1_5X.next(1), 2)  # never stalls
        self.assertEqual(GrowthPolicy.GROW_1_5X.next(2), 3)
        self.assertEqual(GrowthPolicy.GROW_2X.next(8), 16)
        self.assertEqual(GrowthPolicy.GROW_2X.next(1), 2)


class AddAndBoundsTest(unittest.TestCase):
    def setUp(self):
        self.a = DynamicArray(4, GrowthPolicy.GROW_2X)
        for i in range(4):
            self.a.add(i)

    def test_grow_path(self):
        a = self.a
        self.assertTrue(a.capacity() == 4 and a.stats().reallocations == 0)
        a.add(4)
        self.assertTrue(a.capacity() == 8 and a.size() == 5)
        self.assertTrue(a.stats().elements_copied == 4 and a.stats().reallocations == 1)
        self.assertTrue(a.get(0) == 0 and a.get(3) == 3 and a.get(4) == 4)
        self.assertTrue(invariant_holds(a, 5))

    def test_bounds_checked_against_size_not_capacity(self):
        a = self.a
        a.add(4)
        with self.assertRaises(IndexError):
            a.get(5)  # the slot exists (capacity 8) but is not an element
        with self.assertRaises(IndexError):
            a.get(-1)  # negative indexes are rejected, not wrapped like a Python list
        with self.assertRaises(IndexError):
            a.set(7, 1)
        with self.assertRaises(IndexError):
            a.insert(6, 1)
        a.insert(a.size(), 99)  # insert at size is an append
        self.assertEqual(a.get(5), 99)


class InsertRemoveTest(unittest.TestCase):
    def test_shifts(self):
        b = DynamicArray(2, GrowthPolicy.GROW_1_5X, False)
        b.add(10)
        b.add(30)
        b.insert(1, 20)
        self.assertTrue(b.get(0) == 10 and b.get(1) == 20 and b.get(2) == 30 and b.size() == 3)
        self.assertEqual(b.capacity(), 3)  # full array grew by the 1.5x rule (2 -> 3)
        self.assertEqual(b.remove(1), 20)
        self.assertTrue(b.get(1) == 30 and b.size() == 2)
        self.assertEqual(b.set(0, 11), 10)
        self.assertEqual(b.get(0), 11)
        self.assertEqual(b.remove_last(), 30)
        self.assertEqual(b.size(), 1)
        b.remove_last()
        with self.assertRaises(IndexError):
            b.remove_last()


class AmortizedTest(unittest.TestCase):
    def test_copies_bounded_by_constant_times_n(self):
        n = 100_000
        two = measure_appends(GrowthPolicy.GROW_2X, n, 8)
        one_point_five = measure_appends(GrowthPolicy.GROW_1_5X, n, 8)
        self.assertLess(two.elements_copied, 2 * n)
        self.assertLess(one_point_five.elements_copied, 3 * n)
        self.assertGreater(one_point_five.elements_copied, two.elements_copied)
        self.assertGreater(one_point_five.reallocations, two.reallocations)
        self.assertLess(one_point_five.peak_capacity, 1.5 * n)
        self.assertLess(two.peak_capacity, 2 * n)
        self.assertEqual(bin(two.peak_capacity // 8).count("1"), 1)  # a power of two times 8
        self.assertEqual(DynamicArray.capacity_sequence(GrowthPolicy.GROW_1_5X, 8, 40)[:5], [8, 12, 18, 27, 40])


class ShrinkTest(unittest.TestCase):
    def test_hysteresis(self):
        c = DynamicArray(4, GrowthPolicy.GROW_2X)
        for i in range(64):
            c.add(i)
        self.assertEqual(c.capacity(), 64)
        while c.size() > 16:
            c.remove_last()
        self.assertTrue(c.capacity() == 64 and c.stats().shrinks == 0)  # exactly one quarter full: no shrink
        c.remove_last()
        self.assertTrue(c.capacity() == 32 and c.size() == 15 and c.stats().shrinks == 1)
        self.assertTrue(invariant_holds(c, 15))
        reallocs = c.stats().reallocations
        for i in range(500):
            c.add(i)
            c.remove_last()
        self.assertEqual(c.stats().reallocations, reallocs)  # no thrash at the boundary
        while c.size() > 0:
            c.remove_last()
        self.assertEqual(c.capacity(), DynamicArray.MIN_CAPACITY)

    def test_shrink_disabled_keeps_capacity(self):
        d = DynamicArray(4, GrowthPolicy.GROW_2X, False)
        for i in range(64):
            d.add(i)
        d.clear()
        self.assertTrue(d.capacity() == 64 and d.is_empty())


class IterationAndConstructionTest(unittest.TestCase):
    def test_iterator_visits_live_prefix_only(self):
        e = DynamicArray(1, GrowthPolicy.GROW_1_5X)
        for s in ("a", "b", "c"):
            e.add(s)
        self.assertEqual("".join(e), "abc")

    def test_initial_capacity_must_be_positive(self):
        with self.assertRaises(ValueError):
            DynamicArray(0, GrowthPolicy.GROW_2X)


if __name__ == "__main__":
    unittest.main()
