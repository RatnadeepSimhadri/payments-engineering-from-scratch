"""Tests for phases/02-algorithms/09-shortest-paths. Run: python3 -m unittest -v"""
import unittest

from main import (
    CURRENCIES, JPY, RATE_SCALE, USD, Money, Rate, Rounding, WeightedGraph, arbitrage, bellman_ford,
    best_conversion_path, convert_along, cycle_product, dijkstra, from_rates, market, rotate_to, weight_of,
)


class SeededRandom:
    """Tiny 64-bit LCG, identical in all three languages, so the random graph is the same everywhere."""

    MASK = (1 << 64) - 1

    def __init__(self, seed: int) -> None:
        self.state = seed & self.MASK

    def next_int(self, bound: int) -> int:
        self.state = (self.state * 6364136223846793005 + 1442695040888963407) & self.MASK
        return (self.state >> 33) % bound


def spreads() -> WeightedGraph:
    g = WeightedGraph()
    for frm, to, w in [("USD", "EUR", 20), ("EUR", "JPY", 20), ("USD", "JPY", 100), ("EUR", "GBP", 20), ("GBP", "JPY", 15)]:
        g.add_edge(frm, to, w)
    g.add_node("CHF")
    return g


def negative_edge_graph() -> WeightedGraph:
    g = WeightedGraph()
    g.add_edge("A", "B", 5)
    g.add_edge("A", "C", 2)
    g.add_edge("C", "B", -4)
    return g


class DijkstraTest(unittest.TestCase):
    def test_spreads(self):
        d = dijkstra(spreads(), "USD")
        self.assertEqual(d.distance["JPY"], 40)
        self.assertEqual(d.path_to("JPY"), ["USD", "EUR", "JPY"])
        self.assertEqual(d.distance["GBP"], 40)
        self.assertEqual(d.path_to("GBP"), ["USD", "EUR", "GBP"])
        self.assertEqual(d.distance["USD"], 0)
        self.assertEqual(d.path_to("USD"), ["USD"])
        self.assertFalse(d.reached("CHF"))
        self.assertEqual(d.path_to("CHF"), [])

    def test_decrease_key_and_zero_weights(self):
        g = WeightedGraph()
        g.add_edge("A", "B", 10)
        g.add_edge("A", "C", 1)
        g.add_edge("C", "B", 2)
        r = dijkstra(g, "A")
        self.assertEqual(r.distance["B"], 3)
        self.assertEqual(r.path_to("B"), ["A", "C", "B"])
        zero = WeightedGraph()
        zero.add_edge("A", "B", 0)
        zero.add_edge("B", "C", 0)
        self.assertEqual(dijkstra(zero, "A").distance["C"], 0)

    def test_rejections(self):
        with self.assertRaises(ValueError):
            dijkstra(negative_edge_graph(), "A")
        with self.assertRaises(KeyError):
            dijkstra(spreads(), "XXX")


class BellmanFordTest(unittest.TestCase):
    def test_matches_dijkstra_on_spreads(self):
        bf = bellman_ford(spreads(), "USD")
        self.assertEqual(bf.paths.distance, dijkstra(spreads(), "USD").distance)
        self.assertIsNone(bf.negative_cycle)
        self.assertEqual(bf.paths.path_to("JPY"), ["USD", "EUR", "JPY"])

    def test_matches_dijkstra_on_seeded_random_graph(self):
        rng = SeededRandom(42)
        g = WeightedGraph()
        for i in range(40):
            g.add_node(f"n{i}")
        for _ in range(160):
            g.add_edge(f"n{rng.next_int(40)}", f"n{rng.next_int(40)}", rng.next_int(100))
        dr = dijkstra(g, "n0")
        br = bellman_ford(g, "n0")
        self.assertEqual(dr.distance, br.paths.distance)
        self.assertGreater(len(dr.distance), 1)
        for node, dist in dr.distance.items():
            path = dr.path_to(node)
            total = 0
            for a, b in zip(path, path[1:]):
                total += min(e.weight for e in g.out_edges(a) if e.to == b)
            self.assertEqual(total, dist, node)

    def test_negative_edge(self):
        r = bellman_ford(negative_edge_graph(), "A")
        self.assertEqual(r.paths.distance["B"], -2)
        self.assertIsNone(r.negative_cycle)

    def test_negative_cycle(self):
        g = WeightedGraph()
        g.add_edge("A", "B", 1)
        g.add_edge("B", "C", -3)
        g.add_edge("C", "A", 1)
        cycle = bellman_ford(g, "A").negative_cycle
        self.assertIsNotNone(cycle)
        self.assertEqual(len(cycle), 4)
        self.assertEqual(cycle[0], cycle[3])
        self.assertEqual(set(cycle), {"A", "B", "C"})

    def test_unreachable_cycle_not_reported(self):
        g = WeightedGraph()
        g.add_edge("S", "T", 1)
        g.add_edge("P", "Q", -5)
        g.add_edge("Q", "P", 1)
        self.assertIsNone(bellman_ford(g, "S").negative_cycle)


class FxTest(unittest.TestCase):
    def test_weights(self):
        self.assertEqual(weight_of(RATE_SCALE), 0)
        self.assertEqual(weight_of(91_816_000), 85384)
        self.assertEqual(weight_of(108_478_260), -81380)
        self.assertEqual(weight_of(15_000_000_000), -5010635)
        self.assertEqual(weight_of(50_000_000) + weight_of(200_000_000), 0)

    def test_best_path_and_conversion(self):
        rates = market()
        best = best_conversion_path(rates, "USD", "JPY")
        self.assertEqual(best, ["USD", "EUR", "JPY"])
        ten_k = Money.of_minor(1_000_000, USD)
        self.assertEqual(convert_along(ten_k, best, rates, CURRENCIES), Money.of_minor(1_493_975, JPY))
        self.assertEqual(convert_along(ten_k, ["USD", "JPY"], rates, CURRENCIES), Money.of_minor(1_485_000, JPY))
        self.assertEqual(Money.of_minor(10_000, USD).convert(15_000_000_000, JPY, Rounding.HALF_EVEN), Money.of_minor(15_000, JPY))
        self.assertEqual(Money.of_minor(15_000, JPY).convert(666_667, USD, Rounding.HALF_EVEN), Money.of_minor(10_000, USD))
        with self.assertRaises(ValueError):
            convert_along(ten_k, ["EUR", "USD"], rates, CURRENCIES)
        with self.assertRaises(ValueError):
            Rate("A", "B", 0)
        with self.assertRaises(TypeError):
            Money(1.0, USD)

    def test_arbitrage(self):
        rates = market()
        self.assertIsNone(arbitrage(rates))
        stale = rates[:8] + [Rate("GBP", "USD", 128_000_000)]
        arb = arbitrage(stale)
        self.assertIsNotNone(arb)
        self.assertEqual(len(arb), 4)
        self.assertEqual(set(arb), {"USD", "EUR", "GBP"})
        p = cycle_product(stale, arb)
        self.assertTrue(p.exceeds_one())
        self.assertEqual(p.gain_basis_points(), 86)
        from_usd = rotate_to(arb, "USD")
        self.assertEqual((from_usd[0], from_usd[3], len(from_usd)), ("USD", "USD", 4))
        self.assertEqual(convert_along(Money.of_minor(1_000_000, USD), from_usd, stale, CURRENCIES), Money.of_minor(1_008_689, USD))
        with self.assertRaises(RuntimeError):
            best_conversion_path(stale, "USD", "JPY")

    def test_rounding_artefact_is_rejected_by_exact_product(self):
        exact = [Rate("A", "B", 16_000_000), Rate("B", "C", 25_000_000), Rate("C", "A", 2_500_000_000)]
        self.assertEqual(weight_of(16_000_000) + weight_of(25_000_000) + weight_of(2_500_000_000), -1)
        self.assertIsNotNone(bellman_ford(from_rates(exact), "A").negative_cycle)
        self.assertIsNone(arbitrage(exact))
        p = cycle_product(exact, ["A", "B", "C", "A"])
        self.assertFalse(p.exceeds_one())
        self.assertEqual(p.gain_basis_points(), 0)


if __name__ == "__main__":
    unittest.main()
