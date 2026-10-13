"""Tests for phases/02-algorithms/08-graph-traversal-bfs-dfs. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR, USD, CurrencyMismatchError, Graph, Money, Transfer, bfs, circular_flow, connected_components,
    dfs_iterative, find_cycle, from_transfers, rings, shortest_hop_path, volume,
)


def flow_graph() -> Graph:
    """Ring A->B->C->A, tail C->D->E, shortcut A->D, separate payroll cluster, one isolated node."""
    g = Graph(directed=True)
    for frm, to in [("A", "B"), ("B", "C"), ("C", "A"), ("C", "D"), ("D", "E"), ("A", "D"),
                    ("payroll", "X"), ("X", "landlord"), ("X", "grocer")]:
        g.add_edge(frm, to)
    g.add_node("dormant")
    return g


class GraphTest(unittest.TestCase):
    def test_counts_and_order(self):
        g = flow_graph()
        self.assertEqual(g.node_count(), 10)
        self.assertEqual(g.edge_count(), 9)
        self.assertEqual(g.neighbors("A"), ("B", "D"))
        with self.assertRaises(KeyError):
            g.neighbors("nope")

    def test_undirected(self):
        und = Graph(directed=False)
        und.add_edge("p", "q")
        self.assertEqual(und.neighbors("q"), ("p",))
        self.assertEqual(und.edge_count(), 1)
        self.assertEqual(flow_graph().undirected_view().neighbors("B"), ("A", "C"))


class BfsTest(unittest.TestCase):
    def test_distances_and_order(self):
        r = bfs(flow_graph(), "A")
        self.assertEqual(r.distance, {"A": 0, "B": 1, "D": 1, "C": 2, "E": 2})
        self.assertEqual(r.order, ["A", "B", "D", "C", "E"])
        self.assertFalse(r.reached("X"))
        self.assertFalse(r.reached("dormant"))
        self.assertEqual(r.parent["E"], "D")
        self.assertEqual(r.parent["D"], "A")

    def test_paths(self):
        g = flow_graph()
        self.assertEqual(shortest_hop_path(g, "A", "E"), ["A", "D", "E"])
        self.assertEqual(shortest_hop_path(g, "A", "A"), ["A"])
        self.assertEqual(shortest_hop_path(g, "A", "X"), [])
        self.assertEqual(len(shortest_hop_path(g, "A", "C")), bfs(g, "A").distance["C"] + 1)
        with self.assertRaises(KeyError):
            bfs(g, "nope")


class DfsTest(unittest.TestCase):
    def test_preorder(self):
        g = flow_graph()
        self.assertEqual(dfs_iterative(g, "A"), ["A", "B", "C", "D", "E"])
        self.assertEqual(dfs_iterative(g, "dormant"), ["dormant"])

    def test_deep_chain_does_not_overflow(self):
        chain = Graph(directed=True)
        for i in range(100_000):
            chain.add_edge(f"n{i}", f"n{i + 1}")
        self.assertEqual(len(dfs_iterative(chain, "n0")), 100_001)
        self.assertEqual(bfs(chain, "n0").distance["n100000"], 100_000)


class ComponentsTest(unittest.TestCase):
    def test_components(self):
        comps = connected_components(flow_graph())
        self.assertEqual(comps, [["A", "B", "D", "C", "E"], ["payroll", "X", "landlord", "grocer"], ["dormant"]])
        self.assertEqual(len(rings(flow_graph(), 3)), 2)
        self.assertEqual(len(rings(flow_graph(), 5)), 1)


class CycleTest(unittest.TestCase):
    def test_ring_found(self):
        self.assertEqual(find_cycle(flow_graph()), ["A", "B", "C", "A"])

    def test_diamond_is_not_a_cycle(self):
        dag = Graph(directed=True)
        dag.add_edge("payroll", "X")
        dag.add_edge("X", "landlord")
        dag.add_edge("payroll", "landlord")
        self.assertIsNone(find_cycle(dag))

    def test_self_loop_and_later_component(self):
        self_loop = Graph(directed=True)
        self_loop.add_edge("s", "s")
        self.assertEqual(find_cycle(self_loop), ["s", "s"])
        later = Graph(directed=True)
        later.add_edge("a", "b")
        later.add_edge("c", "d")
        later.add_edge("d", "c")
        self.assertEqual(find_cycle(later), ["c", "d", "c"])
        two = Graph(directed=True)
        two.add_edge("p", "q")
        two.add_edge("q", "r")
        two.add_edge("r", "q")
        self.assertEqual(find_cycle(two), ["q", "r", "q"])

    def test_rejects_undirected(self):
        und = Graph(directed=False)
        und.add_edge("p", "q")
        with self.assertRaises(ValueError):
            find_cycle(und)


class TransfersTest(unittest.TestCase):
    TRANSFERS = [
        Transfer("A", "B", Money.of_minor(50000, USD)),
        Transfer("A", "B", Money.of_minor(12000, USD)),
        Transfer("B", "A", Money.of_minor(100, USD)),
    ]

    def test_graph_and_volume(self):
        g = from_transfers(self.TRANSFERS)
        self.assertEqual(g.edge_count(), 2)
        self.assertEqual(g.neighbors("A"), ("B",))
        vol = volume(self.TRANSFERS)
        self.assertEqual(vol["A->B"], Money.of_minor(62000, USD))
        self.assertEqual(vol["B->A"], Money.of_minor(100, USD))
        self.assertEqual(str(vol["A->B"]), "620.00 USD")
        self.assertEqual(circular_flow(g), ["A", "B", "A"])

    def test_money_safety(self):
        with self.assertRaises(CurrencyMismatchError):
            volume([Transfer("A", "B", Money.of_minor(1, USD)), Transfer("A", "B", Money.of_minor(1, EUR))])
        with self.assertRaises(TypeError):
            Money(1.0, USD)


if __name__ == "__main__":
    unittest.main()
