"""Tests for phases/01-complexity-and-core-structures/08-binary-search-trees. Run: python3 -m unittest -v"""
import bisect
import unittest

from main import USD, BinarySearchTree, JavaRandom, Money, TransactionIndex

INT_MIN = -(1 << 31)
INT_MAX = (1 << 31) - 1


def keys(t):
    return [e.key for e in t.in_order()]


def sample():
    t = BinarySearchTree()
    for k in (50, 30, 70, 20, 40, 60, 80, 35, 45):
        t.put(k, f"v{k}")
    return t


class EmptyTreeTest(unittest.TestCase):
    def test_boundaries(self):
        empty = BinarySearchTree()
        self.assertTrue(empty.size() == 0 and empty.is_empty())
        self.assertEqual(empty.height(), -1)
        self.assertTrue(empty.min_key() is None and empty.max_key() is None)
        self.assertIsNone(empty.get(1))
        self.assertIsNone(empty.remove(1))
        self.assertEqual(empty.range_query(0, 10), [])
        self.assertTrue(empty.check_invariant())


class InsertSearchTest(unittest.TestCase):
    def test_insert_and_search(self):
        t = sample()
        self.assertEqual(t.size(), 9)
        self.assertEqual(keys(t), [20, 30, 35, 40, 45, 50, 60, 70, 80])
        self.assertEqual(t.height(), 3)
        self.assertEqual(t.get(45), "v45")
        self.assertIsNone(t.get(55))
        self.assertTrue(t.contains_key(80) and not t.contains_key(81))
        self.assertTrue(t.min_key() == 20 and t.max_key() == 80)
        self.assertEqual(t.put(40, "new"), "v40")
        self.assertTrue(t.get(40) == "new" and t.size() == 9)
        self.assertTrue(t.check_invariant())
        with self.assertRaises(TypeError):
            t.put(None, "x")

    def test_floor_and_ceiling(self):
        t = sample()
        self.assertEqual(t.floor_key(45), 45)
        self.assertEqual(t.floor_key(42), 40)
        self.assertIsNone(t.floor_key(10))
        self.assertEqual(t.ceiling_key(42), 45)
        self.assertIsNone(t.ceiling_key(90))


class DeleteTest(unittest.TestCase):
    def test_leaf_one_child_two_children_root(self):
        d = sample()
        self.assertEqual(d.remove(20), "v20")
        self.assertEqual(keys(d), [30, 35, 40, 45, 50, 60, 70, 80])
        self.assertTrue(d.remove(999) is None and d.size() == 8)
        self.assertEqual(d.remove(30), "v30")  # two children: successor 35 takes its place
        self.assertTrue(keys(d) == [35, 40, 45, 50, 60, 70, 80] and d.check_invariant())
        self.assertEqual(d.remove(50), "v50")  # root with two children
        self.assertTrue(keys(d) == [35, 40, 45, 60, 70, 80] and d.check_invariant())
        self.assertEqual(d.remove(70), "v70")  # one child
        self.assertTrue(keys(d) == [35, 40, 45, 60, 80] and d.check_invariant())
        self.assertTrue(d.get(60) == "v60" and d.get(45) == "v45")
        for k in (35, 40, 45, 60, 80):
            d.remove(k)
        self.assertTrue(d.is_empty() and d.height() == -1 and d.check_invariant())


class RangeQueryTest(unittest.TestCase):
    def test_range(self):
        r = sample()
        self.assertEqual([e.key for e in r.range_query(35, 60)], [35, 40, 45, 50, 60])
        self.assertTrue(r.range_query(81, 100) == [] and r.range_query(0, 19) == [])
        self.assertEqual(len(r.range_query(70, 70)), 1)
        self.assertEqual(len(r.range_query(INT_MIN, INT_MAX)), 9)
        with self.assertRaises(ValueError):
            r.range_query(60, 35)
        r.reset_comparisons()
        r.range_query(35, 45)
        self.assertLess(r.comparisons(), 3 * 9)  # pruning beats a full scan


class RandomizedAndDegenerateTest(unittest.TestCase):
    def test_randomized_against_reference_then_degenerate(self):
        # Reference model: a dict plus sorted keys plays the role of java.util.TreeMap.
        rnd = JavaRandom(2024)
        mine = BinarySearchTree()
        ref = {}
        agree = True
        i = 0
        while i < 3_000 and agree:
            k = rnd.next_int(500)
            if rnd.next_int(3) == 0:
                agree = mine.remove(k) == ref.pop(k, None)
            else:
                agree = mine.put(k, i) == ref.get(k)
                ref[k] = i
            if i % 250 == 0:
                lo = rnd.next_int(500)
                hi = lo + rnd.next_int(100)
                ordered = sorted(ref)
                expected = [x for x in ordered if lo <= x <= hi]
                agree &= [e.key for e in mine.range_query(lo, hi)] == expected
                fi = bisect.bisect_right(ordered, lo) - 1
                agree &= mine.floor_key(lo) == (ordered[fi] if fi >= 0 else None)
                ci = bisect.bisect_left(ordered, hi)
                agree &= mine.ceiling_key(hi) == (ordered[ci] if ci < len(ordered) else None)
            i += 1
        self.assertTrue(agree and mine.size() == len(ref) and mine.check_invariant())
        self.assertEqual(keys(mine), sorted(ref))

        # degenerate case: sorted insert order gives height n-1
        n = 1_000
        chain = BinarySearchTree()
        for j in range(n):
            chain.put(j, j)
        self.assertEqual(chain.height(), n - 1)
        chain.reset_comparisons()
        chain.get(n - 1)
        self.assertEqual(chain.comparisons(), n)
        shuffled = BinarySearchTree()
        ks = list(range(n))
        for j in range(n - 1, 0, -1):
            m = rnd.next_int(j + 1)
            ks[j], ks[m] = ks[m], ks[j]
        for k in ks:
            shuffled.put(k, k)
        self.assertLess(shuffled.height(), 4 * 10)
        # a 1,000-deep chain would hit Python's recursion limit; the iterative traversal does not
        self.assertTrue(len(chain.in_order()) == n and chain.check_invariant())


class TransactionIndexTest(unittest.TestCase):
    def test_composite_keys_and_ranges(self):
        idx = TransactionIndex()
        t0 = 1_700_000_000_000
        a = idx.record("a", t0 + 1_000, Money.of_minor(1_000, USD))
        b = idx.record("b", t0 + 1_000, Money.of_minor(2_000, USD))
        c = idx.record("c", t0 + 5_000, Money.of_minor(300, USD))
        idx.record("d", t0 + 9_000, Money.of_minor(44, USD))
        self.assertTrue(idx.size() == 4 and len(idx.between(t0 + 1_000, t0 + 1_000)) == 2)
        self.assertEqual([tx.id for tx in idx.between(t0, t0 + 5_000)], ["a", "b", "c"])
        self.assertEqual(idx.sum_between(t0, t0 + 5_000, USD), Money.of_minor(3_300, USD))
        self.assertTrue(idx.remove(a) and idx.between(t0 + 1_000, t0 + 1_000) == [b])
        self.assertTrue(not idx.remove(a) and idx.remove(c) and idx.size() == 2)
        self.assertTrue(idx.tree().check_invariant())
        self.assertEqual(str(Money.of_minor(3_300, USD)), "33.00 USD")

    def test_money_overflow_is_an_error(self):
        big = Money.of_minor((1 << 63) - 1, USD)
        with self.assertRaises(OverflowError):
            big.plus(Money.of_minor(1, USD))


if __name__ == "__main__":
    unittest.main()
