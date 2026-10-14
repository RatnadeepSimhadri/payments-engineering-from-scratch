"""Tests for phases/07-storage-and-transactions/05-two-phase-locking. Run: python3 -m unittest -v"""
import unittest

from main import (
    INT64_MAX,
    Currency,
    LockManager,
    LockMode,
    Money,
    Op,
    Scheduler,
    TwoPhaseViolationException,
    Txn,
    VictimPolicy,
    WaitForGraph,
    transfer,
    usd,
)

S, X = LockMode.S, LockMode.X


def opposite_transfers(policy=VictimPolicy.YOUNGEST):
    s = Scheduler(policy, True)
    s.load("acct:A", usd(100_000))
    s.load("acct:B", usd(50_000))
    t1 = s.begin(transfer("acct:A", "acct:B", 10_000))
    t2 = s.begin(transfer("acct:B", "acct:A", 5_000))
    return s, t1, t2


class LockTableTest(unittest.TestCase):
    def test_compatibility_matrix(self):
        self.assertTrue(S.compatible_with(S))
        self.assertTrue(not S.compatible_with(X) and not X.compatible_with(S))
        self.assertFalse(X.compatible_with(X))

    def test_fifo_queue_and_grants(self):
        lm = LockManager([])
        self.assertTrue(lm.acquire(1, "a", S))
        self.assertTrue(lm.acquire(2, "a", S))
        self.assertFalse(lm.acquire(3, "a", X))  # X waits behind two readers
        self.assertFalse(lm.acquire(4, "a", S))  # FIFO: no writer starvation
        self.assertEqual(set(lm.blockers_of(4, "a", S)), {3})
        self.assertTrue(lm.acquire(1, "a", S))  # re-request is a no-op grant
        self.assertEqual(lm.release_all(1), [])
        g2 = lm.release_all(2)
        self.assertTrue(g2 == [3] and lm.table["a"].holders[3] is X)
        self.assertEqual(lm.release_all(3), [4])
        lm.release_all(4)
        self.assertEqual(lm.table, {})


class WaitForGraphTest(unittest.TestCase):
    def test_chain_then_cycle(self):
        g = WaitForGraph()
        g.add_edge(1, 2)
        g.add_edge(2, 3)
        self.assertIsNone(g.find_cycle())
        g.add_edge(3, 1)
        self.assertEqual(g.find_cycle(), [1, 2, 3])

    def test_waiter_outside_cycle_is_not_reported(self):
        g = WaitForGraph()
        g.add_edge(5, 6)
        g.add_edge(7, 5)
        g.add_edge(6, 7)
        g.add_edge(9, 5)  # T9 waits on the cycle but is not part of it
        self.assertEqual(g.find_cycle(), [5, 6, 7])


class TransferDeadlockTest(unittest.TestCase):
    def test_reproduced_and_resolved(self):
        s, t1, t2 = opposite_transfers()
        s.run(1, 2, 1, 2)
        self.assertEqual(s.deadlocks_detected, 1)
        self.assertEqual(s.cycles[0], [1, 2])
        self.assertEqual(s.victims, [2])
        self.assertTrue(t2.restarts == 1 and t2.status is Txn.Status.COMMITTED)
        self.assertTrue(t1.restarts == 0 and t1.status is Txn.Status.COMMITTED)
        self.assertEqual(s.db["acct:A"], usd(95_000))
        self.assertEqual(s.db["acct:B"], usd(55_000))
        self.assertEqual(s.total(Currency.USD), usd(150_000))
        self.assertTrue(s.lock_table_empty())

    def test_serial_schedule_matches(self):
        s, _, _ = opposite_transfers()
        s.run(1, 2, 1, 2)
        serial, _, _ = opposite_transfers()
        serial.run(1, 1, 1, 2, 2, 2)
        self.assertEqual(serial.deadlocks_detected, 0)
        self.assertEqual(serial.db, s.db)

    def test_fewest_locks_policy(self):
        f = Scheduler(VictimPolicy.FEWEST_LOCKS, True)
        f.load("acct:A", usd(100_000))
        f.load("acct:B", usd(50_000))
        f.load("fees", usd(0))
        f.begin(transfer("acct:A", "acct:B", 10_000))
        f.begin([Op.Add("acct:B", usd(-5_050)), Op.Add("fees", usd(50)), Op.Add("acct:A", usd(5_000)), Op.Commit()])
        f.run(1, 2, 2, 1, 2)
        self.assertEqual(f.victims, [1])
        self.assertTrue(f.total(Currency.USD) == usd(150_000) and f.db["acct:A"] == usd(95_000)
                        and f.db["acct:B"] == usd(54_950) and f.db["fees"] == usd(50))

    def test_upgrade_deadlock(self):
        u = Scheduler(VictimPolicy.YOUNGEST, True)
        u.load("acct:A", usd(100_000))
        u.begin([Op.Read("acct:A"), Op.Add("acct:A", usd(1_000)), Op.Commit()])
        u.begin([Op.Read("acct:A"), Op.Add("acct:A", usd(2_000)), Op.Commit()])
        u.run(1, 2, 1, 2)
        self.assertEqual(u.deadlocks_detected, 1)
        self.assertEqual(u.db["acct:A"], usd(103_000))
        self.assertEqual(u.txns[2].reads["acct:A"], usd(101_000))


class AbortAndTwoPhaseRuleTest(unittest.TestCase):
    def test_abort_undoes_in_reverse(self):
        a = Scheduler(VictimPolicy.YOUNGEST, False)
        a.load("x", usd(10))
        a.load("y", usd(20))
        ta = a.begin([Op.Add("x", usd(5)), Op.Add("y", usd(-5)), Op.Add("x", usd(1)), Op.Commit()])
        a.step(ta)
        a.step(ta)
        a.step(ta)
        self.assertTrue(a.db["x"] == usd(16) and a.db["y"] == usd(15))
        a.abort(ta)
        self.assertTrue(a.db["x"] == usd(10) and a.db["y"] == usd(20))
        self.assertTrue(ta.status is Txn.Status.ABORTED and a.lock_table_empty())

    def test_basic_2pl_exposes_uncommitted_value(self):
        b = Scheduler(VictimPolicy.YOUNGEST, False)
        b.load("acct:A", usd(100_000))
        writer = b.begin([Op.Add("acct:A", usd(-90_000)), Op.Unlock("acct:A"), Op.Read("acct:A")])
        reader = b.begin([Op.Read("acct:A"), Op.Commit()])
        b.step(writer)
        b.step(writer)
        b.step(reader)
        b.step(reader)
        b.abort(writer)
        self.assertTrue(reader.reads["acct:A"] == usd(10_000) and reader.status is Txn.Status.COMMITTED
                        and b.db["acct:A"] == usd(100_000))
        self.assertIs(writer.phase, Txn.Phase.SHRINKING)

    def test_growing_after_shrinking_is_rejected(self):
        v = Scheduler(VictimPolicy.YOUNGEST, False)
        v.load("k", usd(1))
        v.load("j", usd(1))
        tv = v.begin([Op.Add("k", usd(1)), Op.Unlock("k"), Op.Read("j")])
        v.step(tv)
        v.step(tv)
        with self.assertRaises(TwoPhaseViolationException):
            v.step(tv)

    def test_blocked_without_cycle_waits_then_wakes(self):
        w = Scheduler(VictimPolicy.YOUNGEST, True)
        w.load("acct:A", usd(100_000))
        w.begin([Op.Add("acct:A", usd(-1)), Op.Commit()])
        w.begin([Op.Read("acct:A"), Op.Commit()])
        w.step(w.txns[1])
        w.step(w.txns[2])
        self.assertTrue(w.txns[2].status is Txn.Status.BLOCKED and w.deadlocks_detected == 0)
        w.step(w.txns[1])
        self.assertTrue(w.txns[2].status is Txn.Status.ACTIVE and w.txns[2].reads["acct:A"] == usd(99_999))


class MoneyTest(unittest.TestCase):
    """Python-only: Java gets Math.addExact for free; here the 64-bit bound is checked by hand."""

    def test_plus_overflows_like_add_exact(self):
        with self.assertRaises(OverflowError):
            Money.of_minor(INT64_MAX, Currency.USD).plus(usd(1))
        self.assertEqual(str(usd(-5_050)), "-50.50 USD")


if __name__ == "__main__":
    unittest.main()
