"""Tests for phases/02-algorithms/10-topological-sort-and-dags. Run: python3 -m unittest -v"""

import unittest

from main import CycleException, DagScheduler, Digraph, Task, TopoSort, settlement_graph, settlement_tasks

KAHN_TRACE = ["ingest-captures", "fetch-fx-rates", "compute-fees", "fx-fixing", "net-positions",
              "generate-payout-file", "post-ledger", "reconcile", "submit-to-bank"]


def is_cycle(g, cyc):
    return all(cyc[(i + 1) % len(cyc)] in g.successors(cyc[i]) for i in range(len(cyc)))


def broken_graph():
    return settlement_graph().add_edge("submit-to-bank", "reconcile")


class GraphTest(unittest.TestCase):
    def test_graph_basics(self):
        g = settlement_graph()
        self.assertEqual(g.node_count(), 9)
        self.assertEqual(g.edge_count(), 10)
        self.assertEqual(Digraph().add_edge("a", "b").add_edge("a", "b").edge_count(), 1)  # duplicate ignored
        self.assertEqual(g.in_degrees()["net-positions"], 2)
        with self.assertRaises(ValueError):
            g.successors("nope")


class TopoSortTest(unittest.TestCase):
    def test_kahn(self):
        g = settlement_graph()
        kahn = TopoSort.kahn(g)
        self.assertTrue(TopoSort.is_topological_order(g, kahn))
        self.assertEqual(kahn, KAHN_TRACE)  # exactly the order the lesson text traces
        self.assertEqual(kahn, TopoSort.kahn(settlement_graph()))  # deterministic

    def test_dfs(self):
        g = settlement_graph()
        dfs = TopoSort.dfs(g)
        self.assertTrue(TopoSort.is_topological_order(g, dfs))
        self.assertEqual(g.in_degrees()[dfs[0]], 0)  # starts with a source
        self.assertEqual(len(g.successors(dfs[-1])), 0)  # ends with a sink

    def test_validator(self):
        g = settlement_graph()
        self.assertFalse(TopoSort.is_topological_order(g, list(reversed(KAHN_TRACE))))
        self.assertFalse(TopoSort.is_topological_order(g, KAHN_TRACE[:8]))
        self.assertFalse(TopoSort.is_topological_order(Digraph().add_edge("a", "b"), ["a", "a"]))

    def test_boundaries(self):
        self.assertTrue(TopoSort.kahn(Digraph()) == [] and TopoSort.dfs(Digraph()) == [])
        self.assertEqual(TopoSort.kahn(Digraph().add_node("x")), ["x"])
        disconnected = Digraph().add_edge("a", "b").add_edge("c", "d")
        self.assertTrue(TopoSort.is_topological_order(disconnected, TopoSort.kahn(disconnected))
                        and TopoSort.is_topological_order(disconnected, TopoSort.dfs(disconnected)))

    def test_deep_chain_does_not_hit_the_recursion_limit(self):
        chain = Digraph()
        for i in range(100_000):
            chain.add_edge(f"n{i}", f"n{i + 1}")
        self.assertEqual(len(TopoSort.dfs(chain)), 100_001)
        self.assertEqual(TopoSort.kahn(chain)[0], "n0")


class CycleTest(unittest.TestCase):
    def test_cycles_detected_and_reported(self):
        broken = broken_graph()
        with self.assertRaises(CycleException):
            TopoSort.kahn(broken)
        with self.assertRaises(CycleException):
            TopoSort.dfs(broken)
        self.assertEqual(TopoSort.find_cycle(broken), ["submit-to-bank", "reconcile"])
        self.assertEqual(TopoSort.find_cycle(settlement_graph()), [])
        self.assertEqual(TopoSort.find_cycle(Digraph().add_edge("a", "a")), ["a"])  # self-loop
        triangle = Digraph().add_edge("x", "y").add_edge("y", "z").add_edge("z", "x").add_edge("w", "x")
        cyc = TopoSort.find_cycle(triangle)
        self.assertTrue(len(cyc) == 3 and is_cycle(triangle, cyc))
        with self.assertRaises(CycleException) as ctx:
            TopoSort.kahn(broken)
        self.assertIn("submit-to-bank -> reconcile -> submit-to-bank", str(ctx.exception))


class SchedulerTest(unittest.TestCase):
    def test_levels(self):
        levels = DagScheduler(settlement_graph(), settlement_tasks()).levels()
        self.assertEqual(len(levels), 6)
        self.assertEqual(levels[0], ["ingest-captures", "fetch-fx-rates"])
        self.assertEqual(levels[3], ["generate-payout-file", "post-ledger"])
        self.assertEqual(levels[5], ["submit-to-bank"])

    def test_critical_path(self):
        s = DagScheduler(settlement_graph(), settlement_tasks()).schedule()
        self.assertEqual(s.makespan, 115)
        self.assertEqual(s.critical_path, ["ingest-captures", "compute-fees", "net-positions", "post-ledger",
                                           "reconcile", "submit-to-bank"])
        self.assertTrue(all(s.slots[t].slack == 0 for t in s.critical_path))
        self.assertEqual(s.slots["fetch-fx-rates"].slack, 20)
        self.assertEqual(s.slots["fx-fixing"].slack, 5)
        self.assertEqual(s.slots["generate-payout-file"].slack, 40)
        self.assertTrue(s.slots["net-positions"].earliest_start == 35 and s.slots["net-positions"].earliest_finish == 60)
        minutes = {t.name: t.minutes for t in settlement_tasks()}
        self.assertEqual(sum(minutes[t] for t in s.critical_path), 115)

    def test_scheduler_rejections(self):
        with self.assertRaises(ValueError):  # needs every duration
            DagScheduler(settlement_graph(), [Task("ingest-captures", 1)])
        with self.assertRaises(ValueError):
            Task("t", -1)
        with self.assertRaises(CycleException):
            DagScheduler(broken_graph(), settlement_tasks()).schedule()
        self.assertNotIsInstance(CycleException(["a"]), ValueError)  # IllegalStateException, not an argument error


if __name__ == "__main__":
    unittest.main()
