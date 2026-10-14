"""Tests for phases/03-systems-data-structures/04-b-trees. Run: python3 -m unittest -v"""
import unittest

from main import BTree, JavaRandom, java_shuffle

N = 50_000
INT_MIN, INT_MAX = -(1 << 31), (1 << 31) - 1


def is_sorted(xs):
    return all(xs[i - 1] < xs[i] for i in range(1, len(xs)))


class ConstructionTest(unittest.TestCase):
    def test_bounds(self):
        with self.assertRaises(ValueError):
            BTree(2)
        o4 = BTree(4)
        self.assertTrue(o4.max_keys == 3 and o4.min_keys == 1)
        o5 = BTree(5)
        self.assertTrue(o5.max_keys == 4 and o5.min_keys == 2)
        self.assertTrue(o4.height() == 1 and o4.size() == 0 and o4.get(1) is None)


class FirstSplitTest(unittest.TestCase):
    def test_first_split_by_hand(self):
        o4 = BTree(4)
        o4.put(10, "a")
        o4.put(20, "b")
        o4.put(30, "c")
        self.assertTrue(o4.height() == 1 and o4.node_count() == 1 and o4.stats.splits == 0)
        o4.put(40, "d")  # overflow: split at median 20
        self.assertTrue(o4.height() == 2 and o4.node_count() == 3 and o4.stats.splits == 1)
        self.assertTrue(o4.keys() == [10, 20, 30, 40] and o4.check_invariants())
        self.assertTrue(o4.get(10) == "a" and o4.get(20) == "b" and o4.get(40) == "d" and o4.get(25) is None)
        o4.put(20, "B")
        self.assertTrue(o4.get(20) == "B" and o4.size() == 4)


def build_sequential():
    seq = BTree(32)
    for i in range(N):
        seq.put(i, i)
    return seq


def shuffled_keys():
    xs = list(range(N))
    java_shuffle(xs, JavaRandom(3))
    return xs


class SequentialTest(unittest.TestCase):
    def test_sequential(self):
        seq = build_sequential()
        self.assertTrue(seq.check_invariants())
        self.assertEqual(seq.size(), N)
        self.assertLessEqual(seq.height(), BTree.height_bound(32, N))
        self.assertLessEqual(seq.height(), 5)
        self.assertTrue(is_sorted(seq.keys()) and len(seq.keys()) == N)
        seq.stats.reset()
        seq.get(31_337)
        self.assertEqual(seq.stats.reads, seq.height())
        seq.stats.reset()
        hits = seq.range(1_000, 1_099)
        self.assertTrue(len(hits) == 100 and hits[0] == 1_000 and hits[99] == 1_099)
        self.assertLessEqual(seq.stats.reads, seq.height() + 100 // seq.min_keys + 2)
        self.assertEqual(seq.range(N + 1, N + 100), [])
        self.assertEqual(seq.range(INT_MIN, INT_MAX), seq.keys())


class RandomAndDeleteTest(unittest.TestCase):
    def test_random_then_deletes(self):
        seq = build_sequential()
        shuffled = shuffled_keys()
        rnd = BTree(32)
        for k in shuffled:
            rnd.put(k, k)
        self.assertTrue(rnd.check_invariants())
        self.assertEqual(rnd.keys(), seq.keys())
        seq_fill = seq.size() / (seq.node_count() * seq.max_keys)
        rnd_fill = rnd.size() / (rnd.node_count() * rnd.max_keys)
        self.assertTrue(0.45 < seq_fill < 0.55)
        self.assertTrue(0.60 < rnd_fill < 0.78)
        reference = {k: k for k in shuffled}  # sorted(dict) plays TreeMap.subMap's role
        self.assertEqual(rnd.range(123, 4567), [k for k in sorted(reference) if 123 <= k <= 4567])

        for i in range(0, N, 2):
            rnd.remove(i)
        self.assertEqual(rnd.size(), N // 2)
        self.assertTrue(rnd.check_invariants() and rnd.stats.merges > 0)
        self.assertTrue(rnd.get(0) is None and rnd.get(1_000) is None and rnd.get(1) == 1 and rnd.get(999) == 999)
        rnd.remove(-5)
        self.assertTrue(rnd.size() == N // 2 and rnd.check_invariants())

    def test_shrink_to_one_level(self):
        shrink = BTree(4)
        for i in range(1_000):
            shrink.put(i, "x")
        before = shrink.height()
        for i in range(1_000):
            shrink.remove(i)
        self.assertTrue(before > 1 and shrink.height() == 1 and shrink.size() == 0 and shrink.check_invariants())


class TracedDeleteTest(unittest.TestCase):
    def test_borrow_merge_separator(self):
        traced = BTree(4)
        for i in range(1, 11):
            traced.put(i, "v")
        traced.stats.reset()
        traced.remove(10)
        self.assertTrue(traced.node_count() == 5 and traced.stats.merges == 0 and traced.check_invariants())
        traced.remove(9)
        self.assertTrue(traced.node_count() == 4 and traced.stats.merges == 1 and traced.check_invariants())
        traced.remove(6)
        self.assertTrue(traced.node_count() == 4 and traced.keys() == [1, 2, 3, 4, 5, 7, 8]
                        and traced.check_invariants())

    def test_delete_separator_keys(self):
        internal = BTree(4)
        for i in range(1, 11):
            internal.put(i, "v")
        all_keys = list(internal.keys())
        ok = True
        for k in (4, 2, 8, 6):
            internal.remove(k)
            all_keys.remove(k)
            ok &= internal.keys() == all_keys and internal.check_invariants()
        self.assertTrue(ok)


class ChurnTest(unittest.TestCase):
    def test_mixed_operations_agree_with_model(self):
        rng = JavaRandom(99)
        churn = BTree(5)
        model: dict[int, int] = {}
        agree = True
        step = 0
        while step < 20_000 and agree:
            key = rng.next_int(2_000)
            if rng.next_int(3) == 0:
                churn.remove(key)
                model.pop(key, None)
            else:
                churn.put(key, step)
                model[key] = step
            if step % 1_000 == 999:
                agree = churn.keys() == sorted(model) and churn.check_invariants() and churn.size() == len(model)
            step += 1
        self.assertTrue(agree)


class LowerBoundTest(unittest.TestCase):
    def test_lower_bound(self):
        node = BTree.Node()
        node.keys.extend([10, 20, 30])
        self.assertEqual(BTree.lower_bound(node, 5), 0)
        self.assertEqual(BTree.lower_bound(node, 20), 1)
        self.assertEqual(BTree.lower_bound(node, 25), 2)
        self.assertEqual(BTree.lower_bound(node, 31), 3)


if __name__ == "__main__":
    unittest.main()
