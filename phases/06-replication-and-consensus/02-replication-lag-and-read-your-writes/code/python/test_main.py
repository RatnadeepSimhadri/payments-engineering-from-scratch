"""Tests for phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, USD, AnomalyDetector, Leader, Money, ReadMode, Replica, ReplicatedStore, Rng, Session)


class LeaderLogTest(unittest.TestCase):
    def test_lsns_and_since(self):
        leader = Leader()
        self.assertEqual(leader.lsn(), 0, "fresh leader has lsn 0")
        self.assertEqual(leader.write("a", "1"), 1, "first write is lsn 1")
        self.assertEqual(leader.write("b", "2"), 2, "second write is lsn 2")
        self.assertEqual(leader.read("b"), "2", "leader reads its own write")
        self.assertTrue(len(leader.since(1)) == 1 and leader.since(1)[0].lsn == 2)
        self.assertEqual(leader.since(2), [], "since(lsn) returns nothing when caught up")
        with self.assertRaises(ValueError):
            leader.since(3)


class ReplicaApplyTest(unittest.TestCase):
    def test_replicas_apply_with_delay(self):
        store = ReplicatedStore.of(1, True, 3, 10)
        lsn = store.write("payment:p1", "CAPTURED")
        self.assertEqual(lsn, 1)
        self.assertTrue(store.replicas[0].applied_lsn == 0 and store.replicas[0].pending() == 1)
        store.run(10)
        self.assertTrue(store.replicas[0].applied_lsn == 1 and store.replicas[1].applied_lsn == 1)
        self.assertEqual(store.replicas[1].lag_behind(store.leader), 0)
        self.assertEqual(store.replicas[1].read("payment:p1"), "CAPTURED")


class ReadYourWritesTest(unittest.TestCase):
    def test_bug_and_fix(self):
        lagging = ReplicatedStore.of(2, True, 20)
        alice = Session(1)
        lagging.write_session(alice, "payment:p2", "CAPTURED")
        lagging.tick()
        stale = lagging.read(alice, "payment:p2", ReadMode.ANY_REPLICA)
        self.assertTrue(stale.value is None and stale.at_lsn == 0, "ANY_REPLICA right after a write misses it")
        self.assertEqual(lagging.stale_reads, 1, "the store counted that read as stale")

        ryw = lagging.read(alice, "payment:p2", ReadMode.READ_YOUR_WRITES)
        self.assertEqual(ryw.value, "CAPTURED", "READ_YOUR_WRITES sees the own write")
        self.assertTrue(ryw.served_by == "leader" and ryw.redirected, "served by the leader when no replica caught up")
        self.assertEqual(alice.floor(ReadMode.READ_YOUR_WRITES), 1)
        bob = Session(2)
        bob_read = lagging.read(bob, "payment:p2", ReadMode.READ_YOUR_WRITES)
        self.assertTrue(bob.floor(ReadMode.READ_YOUR_WRITES) == 0 and bob_read.value is None)
        lagging.run(25)
        later = lagging.read(alice, "payment:p2", ReadMode.READ_YOUR_WRITES)
        self.assertTrue(later.served_by == "r0" and not later.redirected, "replica serves once caught up")

    def test_guarantee_holds_across_seeds(self):
        ryw_stale = any_stale = session_backwards = any_backwards = 0
        for seed in range(1, 16):
            for mode in (ReadMode.ANY_REPLICA, ReadMode.READ_YOUR_WRITES, ReadMode.SESSION):
                s = ReplicatedStore.of(seed, True, 5, 15, 45)
                user = Session(seed)
                det = AnomalyDetector()
                for i in range(1, 101):
                    s.write_session(user, "k", f"v{i}")
                    s.run(1 + (i % 5))
                    r = s.read(user, "k", mode)
                    det.observe(r)
                    missed = r.value != f"v{i}"
                    if mode == ReadMode.ANY_REPLICA and missed:
                        any_stale += 1
                    if mode == ReadMode.READ_YOUR_WRITES and missed:
                        ryw_stale += 1
                if mode == ReadMode.ANY_REPLICA:
                    any_backwards += det.monotonic_violations
                if mode == ReadMode.SESSION:
                    session_backwards += det.monotonic_violations
        self.assertGreater(any_stale, 0, "ANY_REPLICA misses own writes (the bug is reproducible)")
        self.assertEqual(ryw_stale, 0, "READ_YOUR_WRITES never misses an own write")
        self.assertGreater(any_backwards, 0, "ANY_REPLICA reads travel back in time")
        self.assertEqual(session_backwards, 0, "SESSION reads never go backwards")


class MonotonicReadsTest(unittest.TestCase):
    def test_monotonic_floor(self):
        mono = ReplicatedStore.of(5, True, 2, 30)
        carol = Session(1)   # sticky replica r1 is the slow one
        mono.write_session(carol, "k", "v1")
        mono.run(3)
        first = mono.read(carol, "k", ReadMode.MONOTONIC)
        mono.write("k", "v2")   # not carol's write: her write floor stays at 1
        mono.run(3)
        second = mono.read(carol, "k", ReadMode.MONOTONIC)
        self.assertGreaterEqual(carol.floor(ReadMode.MONOTONIC), first.at_lsn)
        self.assertGreaterEqual(second.at_lsn, first.at_lsn)
        self.assertEqual(carol.floor(ReadMode.SESSION), max(carol.last_write_lsn, carol.last_read_lsn))


class ConsistentPrefixTest(unittest.TestCase):
    def test_in_order_vs_on_arrival(self):
        ordered = ReplicatedStore.of(11, True, 20)
        unordered = ReplicatedStore.of(11, False, 20)
        ordered_det, unordered_det = AnomalyDetector(), AnomalyDetector()
        for p in range(1, 101):
            for s in (ordered, unordered):
                s.write(f"p{p}:auth", "AUTHORIZED")
                s.write(f"p{p}:capture", "CAPTURED")
            for _ in range(8):
                ordered.tick()
                unordered.tick()
                ordered_det.observe_pair(ordered.replicas[0].read(f"p{p}:auth"), ordered.replicas[0].read(f"p{p}:capture"))
                unordered_det.observe_pair(unordered.replicas[0].read(f"p{p}:auth"), unordered.replicas[0].read(f"p{p}:capture"))
        self.assertTrue(unordered_det.prefix_violations > 0 and unordered.replicas[0].out_of_order_applies > 0)
        self.assertTrue(ordered_det.prefix_violations == 0 and ordered.replicas[0].out_of_order_applies == 0)
        ordered.run(40)
        unordered.run(40)
        self.assertTrue(ordered.replicas[0].applied_lsn == 200 and unordered.replicas[0].applied_lsn == 200)
        self.assertEqual(ordered.replicas[0].buffered(), 0, "in-order replica's buffer drains to zero")


class CatchUpAndDeterminismTest(unittest.TestCase):
    def test_catch_up_ships_nothing_when_current(self):
        cu = ReplicatedStore.of(4, True, 2)
        for i in range(1, 6):
            cu.write(f"k{i}", "v")
        cu.run(5)
        self.assertEqual(cu.catch_up(cu.replicas[0]), 0)

    def test_same_seed_same_trace(self):
        d1, d2 = ReplicatedStore.of(21, True, 5, 15), ReplicatedStore.of(21, True, 5, 15)
        t1, t2 = [], []
        for i in range(30):
            d1.write("k", f"v{i}")
            d2.write("k", f"v{i}")
            d1.run(2)
            d2.run(2)
            t1.append(str(d1.read(Session(i), "k", ReadMode.ANY_REPLICA)))
            t2.append(str(d2.read(Session(i), "k", ReadMode.ANY_REPLICA)))
        self.assertEqual("".join(t1), "".join(t2))


class MoneyAndValidationTest(unittest.TestCase):
    def test_money_and_validation(self):
        self.assertEqual(str(Money.of_minor(4999, USD)), "49.99 USD")
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).plus(Money.of_minor(1, EUR))
        with self.assertRaises(ValueError):
            Replica(0, Rng(1), 0, 5, True)


if __name__ == "__main__":
    unittest.main()
