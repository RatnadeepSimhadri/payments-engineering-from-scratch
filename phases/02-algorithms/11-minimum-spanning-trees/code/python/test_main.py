"""Tests for phases/02-algorithms/11-minimum-spanning-trees. Run: python3 -m unittest -v"""

import unittest

from main import Mst, SpanningForest, UnionFind, WeightedEdge, WeightedGraph, same_edge_set, site_network


class JavaRandom:
    """java.util.Random (48-bit LCG), so the 30 random graphs are the same as the Java test's seed 42."""

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


class UnionFindTest(unittest.TestCase):
    def test_union_find(self):
        uf = UnionFind(["a", "b", "c", "d"])
        self.assertTrue(uf.components() == 4 and not uf.connected("a", "b"))
        self.assertTrue(uf.union("a", "b") and uf.connected("a", "b") and uf.components() == 3)
        self.assertTrue(not uf.union("b", "a") and uf.components() == 3)
        self.assertTrue(uf.union("c", "d") and uf.union("b", "c") and uf.connected("a", "d") and uf.components() == 1)
        self.assertEqual(uf.find(uf.find("d")), uf.find("d"))

    def test_edge_validation(self):
        with self.assertRaises(ValueError):
            WeightedEdge("a", "a", 1)
        with self.assertRaises(ValueError):
            WeightedEdge("a", "b", -1)
        self.assertEqual(WeightedEdge("a", "b", 1).other("a"), "b")


class KruskalPrimTest(unittest.TestCase):
    def test_kruskal_hand_trace(self):
        g = site_network()
        k = Mst.kruskal(g)
        self.assertTrue(k.is_tree() and len(k.edges) == 7)
        self.assertEqual(k.total_weight, 39)
        self.assertEqual([e.weight for e in k.edges], [2, 3, 4, 5, 6, 7, 12])
        self.assertFalse(any(e.same_endpoints(WeightedEdge("newyork", "chicago", 0)) for e in k.edges))
        self.assertTrue(Mst.is_minimum_spanning_forest(g, k))

    def test_prim_agrees(self):
        g = site_network()
        k = Mst.kruskal(g)
        p = Mst.prim(g, "ashburn")
        self.assertTrue(p.total_weight == 39 and p.is_tree())
        self.assertTrue(p.edges[0].same_endpoints(WeightedEdge("ashburn", "newyork", 4)))
        self.assertTrue(same_edge_set(k, p))
        self.assertTrue(all(Mst.prim(g, n).total_weight == 39 for n in g.nodes()))
        with self.assertRaises(ValueError):
            Mst.prim(g, "mars")

    def test_checker_rejects_wrong_answers(self):
        g = site_network()
        k = Mst.kruskal(g)
        wrong = list(k.edges)
        wrong[-1] = g.edges()[5]  # swap newyork-london 12 for chicago-london 15
        self.assertFalse(Mst.is_minimum_spanning_forest(g, SpanningForest(wrong, 42, 1)))
        self.assertFalse(Mst.is_minimum_spanning_forest(g, SpanningForest(k.edges[:6], 27, 1)))

    def test_ties(self):
        square = WeightedGraph().add_edge("a", "b", 1).add_edge("b", "c", 1).add_edge("c", "d", 1).add_edge("d", "a", 1)
        ks, ps = Mst.kruskal(square), Mst.prim(square, "c")
        self.assertTrue(ks.total_weight == 3 and Mst.is_minimum_spanning_forest(square, ks))
        self.assertTrue(ps.total_weight == 3 and Mst.is_minimum_spanning_forest(square, ps))
        self.assertEqual(ks.edges, list(square.edges()[:3]))  # kruskal keeps insertion order
        self.assertTrue(Mst.kruskal(square).edges == ks.edges and Mst.prim(square, "c").edges == ps.edges)

    def test_boundaries(self):
        empty = Mst.kruskal(WeightedGraph())
        self.assertTrue(empty.edges == [] and empty.component_count == 0)
        self.assertTrue(Mst.kruskal(WeightedGraph().add_node("x")).component_count == 1
                        and Mst.prim(WeightedGraph().add_node("x"), "x").edges == [])
        parallel = WeightedGraph().add_edge("a", "b", 5).add_edge("a", "b", 3)
        self.assertTrue(Mst.kruskal(parallel).total_weight == 3 and Mst.prim(parallel, "a").total_weight == 3)

    def test_disconnected_graph_gives_a_forest(self):
        two = WeightedGraph().add_edge("a", "b", 1).add_edge("c", "d", 2).add_node("e")
        f = Mst.kruskal(two)
        self.assertTrue(f.component_count == 3 and len(f.edges) == 2 and not f.is_tree())
        self.assertTrue(Mst.prim(two, "a").component_count == 3 and Mst.prim(two, "a").total_weight == 3)
        self.assertTrue(Mst.is_minimum_spanning_forest(two, f))

    def test_randomized_cross_check(self):
        rng = JavaRandom(42)
        agree = verified = True
        for _ in range(30):
            r = WeightedGraph()
            n = 4 + rng.next_int(6)
            for i in range(n):
                r.add_node(f"n{i}")
            m = n + rng.next_int(n * 2)
            for _ in range(m):
                a, b = rng.next_int(n), rng.next_int(n)
                if a != b:
                    r.add_edge(f"n{a}", f"n{b}", 1 + rng.next_int(20))
            rk, rp = Mst.kruskal(r), Mst.prim(r, "n0")
            agree &= rk.total_weight == rp.total_weight and rk.component_count == rp.component_count
            verified &= Mst.is_minimum_spanning_forest(r, rk) and Mst.is_minimum_spanning_forest(r, rp)
        self.assertTrue(agree, "30 random graphs: kruskal and prim agree on total")
        self.assertTrue(verified, "30 random graphs: both pass the cut-property checker")


class ClusteringTest(unittest.TestCase):
    def test_clusters(self):
        g = site_network()
        self.assertEqual(Mst.clusters(g, 3), [["ashburn", "newyork"], ["chicago", "dallas", "phoenix", "sanjose"],
                                              ["london", "frankfurt"]])
        self.assertTrue(len(Mst.clusters(g, 1)) == 1 and len(Mst.clusters(g, 1)[0]) == 8)
        self.assertEqual(len(Mst.clusters(g, 10)), 8)  # k >= n gives singletons
        with self.assertRaises(ValueError):
            Mst.clusters(g, 0)


class OverflowTest(unittest.TestCase):
    def test_total_weight_overflow_is_an_error(self):
        huge = WeightedGraph().add_edge("a", "b", 2**62).add_edge("b", "c", 2**62)
        with self.assertRaises(ArithmeticError):  # Math.addExact semantics, not a silent bignum
            Mst.kruskal(huge)


if __name__ == "__main__":
    unittest.main()
