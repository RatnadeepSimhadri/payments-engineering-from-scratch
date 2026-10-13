"""Tests for phases/05-distributed-foundations/03-lamport-and-vector-clocks. Run: python3 -m unittest -v"""
import unittest

from main import (
    CAPTURE,
    REFUND,
    USD,
    Event,
    LamportClock,
    LamportTimestamp,
    Message,
    Money,
    Node,
    Relation,
    SimulatedNetwork,
    VectorClock,
    causal_refund_scenario,
    concurrent_pairs,
    concurrent_refund_scenario,
    postings,
    total_order,
)


def clock_condition(events: list[Event]) -> bool:
    """happens-before (by vector clock) must imply a strictly smaller Lamport timestamp, for every pair."""
    for x in events:
        for y in events:
            if x.vector.happens_before(y.vector) and not x.lamport < y.lamport:
                return False
    return True


def log_of(net: SimulatedNetwork) -> str:
    return "\n".join(str(e) for e in total_order(net.all_events()))


class LamportClockTest(unittest.TestCase):
    def test_tick_increments_by_one(self):
        lc = LamportClock()
        self.assertEqual(lc.tick(), 1)
        self.assertEqual(lc.tick(), 2)
        self.assertEqual(lc.send(), 3)

    def test_receive_rules(self):
        lc = LamportClock()
        lc.tick(); lc.tick(); lc.tick()
        self.assertEqual(lc.receive(10), 11, "jump past a remote clock that is ahead")
        self.assertEqual(lc.receive(2), 12, "still advance when the local clock is ahead")
        self.assertEqual(lc.receive(12), 13, "equal time advances by one")

    def test_monotonic_across_mixed_operations(self):
        lc = LamportClock()
        remotes = [0, 5, 2, 9, 9, 1, 40, 3]
        prev = 0
        for i in range(50):
            now = lc.receive(remotes[i % len(remotes)]) if i % 3 == 0 else lc.tick()
            self.assertGreater(now, prev)
            prev = now
        self.assertEqual(lc.time(), prev)

    def test_total_order_tie_break_by_node_id(self):
        t1, t2, t3 = LamportTimestamp(3, "payments"), LamportTimestamp(3, "refunds"), LamportTimestamp(2, "refunds")
        self.assertLess(t3, t1)
        self.assertLess(t1, t2)
        self.assertEqual(t1, LamportTimestamp(3, "payments"))
        self.assertEqual(str(t1), "3@payments")


class VectorClockTest(unittest.TestCase):
    def test_increment_is_local_and_immutable(self):
        zero = VectorClock.zero(3)
        v = zero.increment(0)
        self.assertEqual(v.counters, (1, 0, 0))
        self.assertEqual(zero.counters, (0, 0, 0))

    def test_merge_is_componentwise_max(self):
        a, b = VectorClock.of(2, 0, 1), VectorClock.of(1, 3, 1)
        self.assertEqual(a.merge(b), VectorClock.of(2, 3, 1))

    def test_merge_is_commutative_idempotent_associative(self):
        a, b, zero = VectorClock.of(2, 0, 1), VectorClock.of(1, 3, 1), VectorClock.zero(3)
        self.assertEqual(a.merge(b), b.merge(a))
        self.assertEqual(a.merge(a), a)
        self.assertEqual(a.merge(b).merge(b), a.merge(b))
        self.assertEqual(a.merge(b).merge(zero), a.merge(b.merge(zero)))
        self.assertEqual(a.compare(a.merge(b)), Relation.BEFORE)
        self.assertEqual(b.compare(a.merge(b)), Relation.BEFORE)

    def test_compare_before_after_equal_concurrent(self):
        self.assertEqual(VectorClock.of(1, 0, 0).compare(VectorClock.of(2, 0, 0)), Relation.BEFORE)
        self.assertEqual(VectorClock.of(2, 0, 0).compare(VectorClock.of(1, 0, 0)), Relation.AFTER)
        self.assertEqual(VectorClock.of(2, 2, 2).compare(VectorClock.of(2, 2, 2)), Relation.EQUAL)
        a, b = VectorClock.of(2, 0, 1), VectorClock.of(1, 3, 1)
        self.assertEqual(a.compare(b), Relation.CONCURRENT)
        self.assertTrue(a.concurrent_with(b) and b.concurrent_with(a))
        self.assertFalse(VectorClock.of(1, 1, 1).happens_before(VectorClock.of(1, 1, 1)), "happens_before is strict")

    def test_mismatched_cluster_sizes_rejected(self):
        with self.assertRaises(ValueError):
            VectorClock.of(1, 0).compare(VectorClock.of(1, 0, 0))


class NodeTest(unittest.TestCase):
    def test_send_and_receive_stamp_both_clocks(self):
        payments, refunds = Node("payments", 0, 3), Node("refunds", 1, 3)
        payments.local_event("a")
        m = payments.send("refunds", "x")
        self.assertEqual((m.lamport_time, m.vector), (2, VectorClock.of(2, 0, 0)))
        refunds.local_event("b")
        recv = refunds.receive(m)
        self.assertEqual(recv.lamport.time, 3, "max(1,2)+1")
        self.assertEqual(recv.vector, VectorClock.of(2, 2, 0), "merge then increment own")
        self.assertTrue(m.vector.happens_before(recv.vector))
        self.assertTrue(refunds.log[0].vector.concurrent_with(m.vector))


class SimulatedNetworkTest(unittest.TestCase):
    def test_same_seed_same_log(self):
        self.assertEqual(log_of(concurrent_refund_scenario(7)), log_of(concurrent_refund_scenario(7)))

    def test_delivery_bookkeeping(self):
        net = concurrent_refund_scenario(7)
        self.assertEqual((net.in_flight(), net.delivered()), (0, 2))
        self.assertGreaterEqual(net.tick(), 1)

    def test_lcg_matches_other_languages(self):
        lcg = SimulatedNetwork(7, 3)
        self.assertEqual([lcg.next_delay() for _ in range(3)], [2, 3, 1])
        no_delay = SimulatedNetwork(1, 0)
        self.assertEqual([no_delay.next_delay(), no_delay.next_delay()], [0, 0])

    def test_rejects_bad_input(self):
        with self.assertRaises(ValueError):
            SimulatedNetwork(1, -1)
        with self.assertRaises(ValueError):
            SimulatedNetwork(1, 0).send(Message("payments", "mars", "p", 1, VectorClock.of(1, 0, 0)))

    def test_seed_controls_arrival_order(self):
        self.assertTrue(concurrent_refund_scenario(7).node("ledger").log[0].description.startswith("recv captured"))
        self.assertTrue(concurrent_refund_scenario(1).node("ledger").log[0].description.startswith("recv refunded"))


class ScenarioTest(unittest.TestCase):
    def test_causal_refund_is_ordered_by_both_clocks(self):
        net = causal_refund_scenario(7)
        captured, refunded = net.node("payments").log[0], net.node("refunds").log[1]
        self.assertTrue(refunded.description.startswith("post refunded"))
        self.assertTrue(captured.vector.happens_before(refunded.vector))
        self.assertLess(captured.lamport, refunded.lamport)
        self.assertEqual(concurrent_pairs(postings(net.all_events())), [])
        settle = net.node("ledger").log[2]
        for e in net.all_events():
            if not e.description.startswith("post settle"):
                self.assertTrue(e.vector.happens_before(settle.vector))

    def test_concurrent_refund_is_detected(self):
        net = concurrent_refund_scenario(7)
        events = net.all_events()
        captured, refunded = net.node("payments").log[0], net.node("refunds").log[0]
        settle = net.node("ledger").log[-1]
        self.assertTrue(captured.vector.concurrent_with(refunded.vector))
        self.assertEqual(captured.lamport.time, refunded.lamport.time)
        self.assertLess(captured.lamport, refunded.lamport, "tie broken by node id")
        self.assertTrue(captured.vector.happens_before(settle.vector) and refunded.vector.happens_before(settle.vector))
        self.assertEqual(len(postings(events)), 3)
        self.assertEqual(len(concurrent_pairs(postings(events))), 1)
        self.assertTrue(all(x.node_id != y.node_id for x, y in concurrent_pairs(events)))

    def test_clock_condition_and_total_order(self):
        net = concurrent_refund_scenario(7)
        events = net.all_events()
        self.assertTrue(clock_condition(events))
        order = total_order(events)
        for x in events:
            for y in events:
                if x.vector.happens_before(y.vector):
                    self.assertLess(order.index(x), order.index(y))
        self.assertTrue(any(order[i].vector.concurrent_with(order[j].vector)
                            for i in range(len(order)) for j in range(i + 1, len(order))),
                        "some lamport-ordered pair is actually concurrent")
        self.assertEqual(len(order), len(events))
        self.assertEqual(len(set(order)), len(events))

    def test_clock_condition_holds_across_seeds(self):
        for seed in range(1, 31):
            self.assertTrue(clock_condition(concurrent_refund_scenario(seed).all_events()), f"seed {seed}")
            self.assertTrue(clock_condition(causal_refund_scenario(seed).all_events()), f"seed {seed}")


class MoneyTest(unittest.TestCase):
    def test_money_is_integer_minor_units(self):
        self.assertEqual(str(Money.of_minor(4_999, USD)), "49.99 USD")
        self.assertEqual(str(Money.of_minor(-4_999, USD)), "-49.99 USD")
        self.assertEqual(CAPTURE.plus(REFUND).minor_units, 0)
        with self.assertRaises(TypeError):
            Money(49.99, USD)


if __name__ == "__main__":
    unittest.main()
