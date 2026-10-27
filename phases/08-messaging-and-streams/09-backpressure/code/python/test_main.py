"""Tests for phases/08-messaging-and-streams/09-backpressure. Run: python3 -m unittest -v"""
import unittest

from main import (
    LONG_MAX,
    USD,
    BoundedQueue,
    DemandSubscription,
    FlowSimulation,
    JavaRandom,
    OfferResult,
    OverflowPolicy,
    Strategy,
)


def overloaded(s):
    # ~12/tick in, 10/tick out with 10% stalls, capacity 64, 2000 ticks, seed 42: the demo's load
    return FlowSimulation(s, 10, 4, 10, 0.10, 64, 42).run(2000)


def comfortable(s):
    return FlowSimulation(s, 10, 4, 13, 0.0, 64, 42).run(2000)


class BoundedQueueTest(unittest.TestCase):
    def test_block_policy(self):
        block = BoundedQueue(2, OverflowPolicy.BLOCK)
        self.assertEqual(block.offer("a"), OfferResult.ACCEPTED)
        self.assertEqual(block.offer("b"), OfferResult.ACCEPTED)
        self.assertTrue(block.is_full())
        self.assertEqual(block.offer("c"), OfferResult.REJECTED_FULL)
        self.assertEqual(block.dropped(), 0)
        self.assertEqual(block.size(), 2)
        self.assertEqual((block.poll(), block.poll(), block.poll()), ("a", "b", None))
        with self.assertRaises(TypeError):
            block.offer(None)

    def test_drop_policies(self):
        newest = BoundedQueue(2, OverflowPolicy.DROP_NEWEST)
        newest.offer("a")
        newest.offer("b")
        self.assertEqual(newest.offer("c"), OfferResult.DROPPED_NEWEST)
        self.assertEqual(newest.dropped(), 1)
        self.assertEqual((newest.poll(), newest.poll()), ("a", "b"))
        oldest = BoundedQueue(2, OverflowPolicy.DROP_OLDEST)
        oldest.offer("a")
        oldest.offer("b")
        self.assertEqual(oldest.offer("c"), OfferResult.EVICTED_OLDEST)
        self.assertEqual(oldest.dropped(), 1)
        self.assertEqual((oldest.poll(), oldest.poll()), ("b", "c"))
        self.assertEqual(oldest.max_depth(), 2)
        with self.assertRaises(ValueError):
            BoundedQueue(0, OverflowPolicy.BLOCK)


class DemandSubscriptionTest(unittest.TestCase):
    def test_request_semantics(self):
        sub = DemandSubscription()
        self.assertFalse(sub.has_demand())
        self.assertFalse(sub.try_consume_one())
        sub.request(2)
        self.assertEqual(sub.outstanding(), 2)
        sub.request(3)
        self.assertEqual(sub.outstanding(), 5)
        for _ in range(5):
            sub.try_consume_one()
        self.assertFalse(sub.has_demand())
        self.assertFalse(sub.try_consume_one())
        with self.assertRaises(ValueError):
            sub.request(0)
        with self.assertRaises(ValueError):
            sub.request(-1)
        sub.request(LONG_MAX)
        sub.request(1)
        self.assertEqual(sub.outstanding(), LONG_MAX)
        sub.try_consume_one()
        self.assertEqual(sub.outstanding(), LONG_MAX)


class SimulationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.unbounded = overloaded(Strategy.UNBOUNDED)
        cls.drop_newest = overloaded(Strategy.DROP_NEWEST)
        cls.drop_oldest = overloaded(Strategy.DROP_OLDEST)
        cls.blocked = overloaded(Strategy.BLOCK)
        cls.pulled = overloaded(Strategy.PULL)

    def test_conservation_and_identical_load(self):
        for s in (self.unbounded, self.drop_newest, self.drop_oldest, self.blocked, self.pulled):
            self.assertTrue(s.conserves(), s.strategy)
        self.assertEqual(self.unbounded.offered_load, self.blocked.offered_load)
        self.assertEqual(self.unbounded.offered_load, self.pulled.offered_load)

    def test_unbounded(self):
        u = self.unbounded
        self.assertEqual((u.dropped, u.throttled), (0, 0))
        self.assertGreater(u.max_queue_depth, 64 * 10)
        self.assertGreater(u.max_latency_ticks, 100)
        self.assertEqual(u.in_queue, u.produced - u.delivered)

    def test_drop_strategies(self):
        self.assertEqual(self.drop_newest.max_queue_depth, 64)
        self.assertGreater(self.drop_newest.dropped, 0)
        self.assertEqual(self.drop_newest.throttled, 0)
        self.assertEqual(self.drop_oldest.dropped, self.drop_newest.dropped)
        self.assertLess(self.drop_oldest.max_latency_ticks, self.drop_newest.max_latency_ticks)

    def test_block_and_pull(self):
        b, p, u = self.blocked, self.pulled, self.unbounded
        self.assertEqual(b.dropped, 0)
        self.assertEqual(b.max_queue_depth, 64)
        self.assertLessEqual(b.max_latency_ticks, 64 // 10 * 3)
        self.assertGreater(b.throttled, 0)
        self.assertGreater(b.rejected_offers, 0)
        self.assertEqual(p.dropped, 0)
        self.assertEqual(p.max_queue_depth, 64)
        self.assertEqual(p.rejected_offers, 0)
        self.assertLessEqual(abs(p.throttled - b.throttled), 1)
        self.assertEqual(b.delivered, u.delivered)
        self.assertEqual(p.delivered, u.delivered)
        self.assertEqual(b.delivered_amount.currency, USD)
        self.assertGreater(b.delivered_amount.minor_units, 0)

    def test_comfortable_load(self):
        for s in Strategy:
            c = comfortable(s)
            self.assertEqual(c.delivered, c.offered_load, s)
            self.assertEqual((c.dropped, c.throttled), (0, 0), s)

    def test_deterministic_and_matches_java(self):
        self.assertEqual(overloaded(Strategy.BLOCK), self.blocked)
        # the same seed yields the same counts as java.util.Random(42) in the Java demo
        self.assertEqual((self.blocked.offered_load, self.blocked.produced, self.blocked.throttled, self.blocked.rejected_offers),
                         (23969, 18205, 5764, 1977))
        self.assertEqual((self.unbounded.max_queue_depth, self.unbounded.max_latency_ticks), (5829, 489))


class JavaRandomTest(unittest.TestCase):
    def test_matches_jdk_vectors(self):
        # new java.util.Random(42): nextInt(5) -> 0, nextInt(9900) -> 8463, nextDouble() -> 0.7275636800328681 on a fresh instance
        r = JavaRandom(42)
        self.assertEqual(r.next_int(5), 0)
        self.assertEqual(r.next_int(9900), 8463)
        self.assertAlmostEqual(JavaRandom(42).next_double(), 0.7275636800328681, places=15)
        with self.assertRaises(ValueError):
            r.next_int(0)


if __name__ == "__main__":
    unittest.main()
