"""Tests for phases/09-reliability-and-scale/02-load-balancing-algorithms. Run: python3 -m unittest -v"""
import unittest

from main import (Backend, ConsistentHash, HealthChecker, LeastConnections, NoHealthyBackends, Pool,
                  PowerOfTwoChoices, Request, Rng, RoundRobin, WeightedRoundRobin, balls_in_bins, fleet,
                  hash64, percentile, simulate)


def req(i: int, key: str = "k") -> Request:
    return Request(i, key, 10.0)


class RngTest(unittest.TestCase):
    def test_splitmix64_matches_java_and_go(self):
        self.assertEqual(Rng(42).next_long(), -4767286540954276203)

    def test_next_double_and_int_ranges(self):
        rng = Rng(1)
        for _ in range(100):
            d = rng.next_double()
            self.assertTrue(0.0 <= d < 1.0)
            self.assertIn(rng.next_int(5), range(5))


class RoundRobinTest(unittest.TestCase):
    def test_cycles_and_shifts_with_healthy_subset(self):
        abc = [Backend("a"), Backend("b"), Backend("c")]
        rr = RoundRobin()
        self.assertEqual("".join(rr.pick(abc, req(i)).id for i in range(6)), "abcabc")
        ac = [abc[0], abc[2]]
        self.assertIn(rr.pick(ac, req(7)), ac)

    def test_no_backends_raises(self):
        with self.assertRaises(NoHealthyBackends):
            RoundRobin().pick([], req(0))


class WeightedRoundRobinTest(unittest.TestCase):
    def test_smooth_interleaving(self):
        weighted = [Backend("a", 5, 1), Backend("b", 1, 1), Backend("c", 1, 1)]
        wrr = WeightedRoundRobin()
        self.assertEqual("".join(wrr.pick(weighted, req(i)).id for i in range(7)), "aabacaa")
        a_count = sum(1 for i in range(70) if wrr.pick(weighted, req(i)).id == "a")
        self.assertEqual(a_count, 50)

    def test_backend_rejects_zero_weight(self):
        with self.assertRaises(ValueError):
            Backend("x", 0, 1.0)


class LeastConnectionsTest(unittest.TestCase):
    def test_picks_fewest_first_on_tie(self):
        l1, l2, l3 = Backend("l1"), Backend("l2"), Backend("l3")
        l1.active_connections, l2.active_connections, l3.active_connections = 3, 1, 1
        self.assertIs(LeastConnections().pick([l1, l2, l3], req(0)), l2)
        l2.active_connections = 5
        self.assertIs(LeastConnections().pick([l1, l2, l3], req(0)), l3)


class PowerOfTwoChoicesTest(unittest.TestCase):
    def test_never_the_more_loaded_of_two(self):
        p1, p2 = Backend("p1"), Backend("p2")
        p1.active_connections = 10
        p2c = PowerOfTwoChoices(Rng(5))
        for i in range(50):
            self.assertIs(p2c.pick([p1, p2], req(i)), p2)
        self.assertIs(p2c.pick([p1], req(0)), p1)

    def test_balls_in_bins_two_choices_beat_one(self):
        one = balls_in_bins(10_000, 10_000, 1, Rng(7))
        two = balls_in_bins(10_000, 10_000, 2, Rng(7))
        self.assertEqual(one, 6)
        self.assertEqual(two, 3)
        self.assertLess(two, one)


class ConsistentHashTest(unittest.TestCase):
    def test_affinity_and_minimal_disruption(self):
        ch = ConsistentHash(100)
        six = fleet()
        self.assertIs(ch.pick(six, req(1, "merchant-77")), ch.pick(six, req(2, "merchant-77")))
        removed = six[2]
        five = six[:2] + six[3:]
        moved = moved_from_other = 0
        for k in range(1000):
            before = ch.pick(six, req(k, f"merchant-{k}"))
            after = ch.pick(five, req(k, f"merchant-{k}"))
            if before is not after:
                moved += 1
                if before is not removed:
                    moved_from_other += 1
        self.assertEqual(moved_from_other, 0)
        self.assertTrue(100 < moved < 250, moved)

    def test_spread_and_hash_stability(self):
        ch = ConsistentHash(100)
        six = fleet()
        owners = {ch.pick(six, req(k, f"merchant-{k}")).id for k in range(1000)}
        self.assertEqual(len(owners), 6)
        self.assertEqual(hash64("merchant-1"), hash64("merchant-1"))
        self.assertNotEqual(hash64("merchant-1"), hash64("merchant-2"))
        with self.assertRaises(ValueError):
            ConsistentHash(0)


class HealthCheckerTest(unittest.TestCase):
    def test_consecutive_not_cumulative(self):
        h = Backend("h")
        hc = HealthChecker(3, 2)
        for ok in (False, False, True, False, False):
            hc.record(h, ok)
        self.assertTrue(h.healthy)
        self.assertTrue(hc.record(h, False))
        self.assertFalse(h.healthy)
        hc.record(h, True)
        self.assertFalse(h.healthy)
        self.assertTrue(hc.record(h, True))
        self.assertTrue(h.healthy)


class PoolTest(unittest.TestCase):
    def test_routes_around_evicted_and_sheds_when_empty(self):
        x, y = Backend("x"), Backend("y")
        pool = Pool([x, y], RoundRobin(), HealthChecker(1, 1))
        first = pool.route(req(0))
        self.assertEqual((first.active_connections, first.total_assigned), (1, 1))
        pool.complete(first, False)
        self.assertFalse(first.healthy)
        self.assertEqual(first.active_connections, 0)
        self.assertIsNot(pool.route(req(1)), first)
        self.assertIsNot(pool.route(req(2)), first)
        other = y if first is x else x
        pool.complete(other, False)
        self.assertIsNone(pool.route(req(3)))


class SimulationTest(unittest.TestCase):
    def test_deterministic_and_load_aware_strategies_win(self):
        rr_a = simulate(RoundRobin(), 42, False)
        rr_b = simulate(RoundRobin(), 42, False)
        self.assertEqual((rr_a.p99_ms, rr_a.max_queue), (rr_b.p99_ms, rr_b.max_queue))
        lc = simulate(LeastConnections(), 42, False)
        p2c = simulate(PowerOfTwoChoices(Rng(11)), 42, False)
        self.assertLess(lc.p99_ms * 10, rr_a.p99_ms)
        self.assertLess(p2c.p99_ms * 10, rr_a.p99_ms)
        self.assertLessEqual(p2c.max_queue, 2 * lc.max_queue)
        self.assertLessEqual(rr_a.max_assigned - rr_a.min_assigned, 1)
        self.assertLess(lc.min_assigned, rr_a.min_assigned)

    def test_eviction_bounds_failures_during_outage(self):
        outage = simulate(LeastConnections(), 42, True)
        self.assertTrue(0 < outage.failed < 20, outage.failed)
        self.assertEqual(outage.shed, 0)

    def test_percentile_nearest_rank(self):
        self.assertEqual(percentile([1, 2, 3, 4], 50), 2)
        self.assertEqual(percentile([1, 2, 3, 4], 99), 4)
        self.assertEqual(percentile([], 50), 0.0)


if __name__ == "__main__":
    unittest.main()
