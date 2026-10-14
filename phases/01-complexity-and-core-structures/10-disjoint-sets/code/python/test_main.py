"""Tests for phases/01-complexity-and-core-structures/10-disjoint-sets. Run: python3 -m unittest -v"""
import unittest

from main import DisjointSet, EntityLinker, JavaRandom, heuristics_table


class FreshForestTest(unittest.TestCase):
    def test_singletons_and_errors(self):
        ds = DisjointSet(6)
        self.assertTrue(ds.component_count() == 6 and ds.elements() == 6)
        self.assertEqual(ds.find(3), 3)
        self.assertFalse(ds.connected(0, 1))
        self.assertEqual(ds.component_size(4), 1)
        self.assertTrue(ds.check_invariant())
        self.assertTrue(DisjointSet(0).component_count() == 0 and DisjointSet(0).check_invariant())
        with self.assertRaises(ValueError):
            DisjointSet(-1)
        with self.assertRaises(IndexError):
            ds.find(6)


class UnionTest(unittest.TestCase):
    def test_union_connectivity(self):
        ds = DisjointSet(6)
        self.assertTrue(ds.union(0, 1))
        self.assertEqual(ds.component_count(), 5)
        self.assertTrue(ds.connected(0, 1) and ds.find(0) == ds.find(1))
        self.assertTrue(not ds.union(1, 0) and ds.component_count() == 5)
        ds.union(2, 3)
        ds.union(1, 3)
        self.assertTrue(ds.connected(0, 2) and ds.connected(3, 0))  # transitive via 1 and 3
        self.assertTrue(ds.component_size(0) == 4 and ds.component_size(5) == 1)
        self.assertFalse(ds.connected(0, 5))
        self.assertTrue(ds.check_invariant())
        self.assertTrue(ds.find(0) == ds.find(1) == ds.find(2) == ds.find(3))

    def test_union_by_rank(self):
        r = DisjointSet(8)
        r.union(0, 1)
        self.assertEqual(r.rank_of(0), 1)  # tie raises rank
        r.union(0, 2)
        self.assertTrue(r.rank_of(0) == 1 and r.depth(2) == 1)  # unequal union keeps rank
        r.union(3, 4)
        r.union(0, 3)
        self.assertEqual(r.rank_of(0), 2)
        self.assertLessEqual(r.max_depth(), r.rank_of(0))
        self.assertLessEqual(r.rank_of(0), r.component_size(0).bit_length() - 1)  # rank <= floor(log2(size))

    def test_path_compression(self):
        pc = DisjointSet(5, False, True)  # no rank so we can build a known chain
        pc.union(1, 0)
        pc.union(2, 1)
        pc.union(3, 2)
        pc.union(4, 3)  # chain 0 -> 1 -> 2 -> 3 -> 4
        self.assertEqual(pc.depth(0), 4)
        self.assertEqual(pc.find(0), 4)
        self.assertTrue(pc.depth(0) == 1 and pc.depth(1) == 1 and pc.depth(2) == 1)
        self.assertTrue(pc.connected(0, 4) and pc.component_size(0) == 5 and pc.check_invariant())


class HeuristicsTest(unittest.TestCase):
    def test_naive_is_correct_but_slow(self):
        naive = DisjointSet(2_000, False, False)
        for i in range(1, 2_000):
            naive.union(i, i - 1)
        self.assertEqual(naive.max_depth(), 1_999)
        naive.reset_hops()
        naive.find(0)
        self.assertEqual(naive.hops(), 1_999)
        self.assertTrue(naive.connected(0, 1_999) and naive.component_count() == 1 and naive.check_invariant())

    def test_both_heuristics_stay_shallow(self):
        both = DisjointSet(2_000)
        for i in range(1, 2_000):
            both.union(i, i - 1)
        self.assertLessEqual(both.max_depth(), 11)
        both.reset_hops()
        for i in range(2_000):
            both.find(i)
        self.assertLess(both.hops(), 2 * 2_000)

    def test_demo_table_matches_java_at_n_2000(self):
        # The Python demo runs the heuristics workloads at n = 2,000; these are the numbers Java's Main prints
        # when its demoHeuristics is run with n = 2_000 and 3_000 random unions.
        rows = heuristics_table(2_000, 3_000)
        self.assertEqual([r[:3] for r in rows], [(1999, 999, 1999), (1, 0, 1), (1999, 1, 1), (1, 0, 1)])
        self.assertEqual([(r[3], "%.2f" % r[4], r[5]) for r in rows],
                         [(328, "213.19", 328), (5, "1.98", 5), (6, "1.88", 1), (3, "1.06", 1)])


class RandomizedTest(unittest.TestCase):
    def test_agrees_with_brute_force_labels(self):
        n = 300
        mine = DisjointSet(n)
        label = list(range(n))
        rnd = JavaRandom(99)
        agree = True
        step = 0
        while step < 2_000 and agree:
            a, b = rnd.next_int(n), rnd.next_int(n)
            merged = mine.union(a, b)
            was_separate = label[a] != label[b]
            if merged != was_separate:
                agree = False
            if was_separate:
                frm, to = label[b], label[a]
                label = [to if x == frm else x for x in label]
            if step % 97 == 0:
                i = 0
                while i < n and agree:
                    j = rnd.next_int(n)
                    if mine.connected(i, j) != (label[i] == label[j]):
                        agree = False
                    i += 1
            step += 1
        self.assertTrue(agree)
        self.assertTrue(mine.component_count() == len(set(label)) and mine.check_invariant())


class EntityLinkerTest(unittest.TestCase):
    def test_linking(self):
        linker = EntityLinker(16)
        self.assertTrue(linker.link("acct:1", "device:A"))
        self.assertTrue(linker.link("acct:2", "device:A"))
        self.assertTrue(linker.same_ring("acct:1", "acct:2"))
        self.assertEqual(linker.ring("acct:2"), ["acct:1", "acct:2", "device:A"])
        self.assertFalse(linker.link("acct:2", "device:A"))
        linker.link("acct:3", "card:X")
        self.assertTrue(not linker.same_ring("acct:1", "acct:3") and linker.rings() == 2)
        self.assertTrue(not linker.same_ring("acct:1", "acct:999") and linker.ring_size("acct:999") == 0
                        and linker.ring("nobody") == [])
        linker.link("card:X", "acct:1")
        self.assertTrue(linker.same_ring("acct:3", "acct:2") and linker.ring_size("acct:3") == 5 and linker.rings() == 1)
        self.assertTrue(len(linker.rings_of_at_least(5)) == 1 and linker.rings_of_at_least(6) == [])

    def test_capacity_exceeded(self):
        tiny = EntityLinker(2)
        tiny.link("a", "b")
        with self.assertRaises(RuntimeError):
            tiny.link("a", "c")


if __name__ == "__main__":
    unittest.main()
