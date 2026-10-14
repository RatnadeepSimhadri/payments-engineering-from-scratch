"""Tests for phases/07-storage-and-transactions/07-serializable-snapshot-isolation. Run: python3 -m unittest -v"""
import unittest

from main import (
    INT64_MAX,
    Currency,
    Money,
    SerializationFailureException,
    SsiStore,
    TimestampOracle,
    TxnStatus,
    WriteConflictException,
    usd,
)


def seeded() -> SsiStore:
    store = SsiStore(TimestampOracle())
    t = store.begin()
    store.write(t, "A", 100)
    store.write(t, "B", 100)
    store.commit(t)
    return store


def withdraw(s: SsiStore, t, frm: str, other: str, amount: int) -> bool:
    """Lesson 06's write-skew program: check A+B >= amount, then withdraw from one account."""
    total = s.read(t, frm) + s.read(t, other)
    if total < amount:
        return False
    s.write(t, frm, s.read(t, frm) - amount)
    return True


class SnapshotBehaviourTest(unittest.TestCase):
    def test_si_behaviour_is_unchanged(self):
        si = seeded()
        r, w = si.begin(), si.begin()
        si.write(w, "A", 40)
        self.assertEqual(si.read(r, "A"), 100)
        si.commit(w)
        self.assertEqual(si.read(r, "A"), 100)
        self.assertTrue(si.commit(r) == r.start_ts and r.status is TxnStatus.COMMITTED)
        x1, x2 = si.begin(), si.begin()
        si.write(x1, "A", 1)
        si.write(x2, "A", 2)
        si.commit(x1)
        with self.assertRaises(WriteConflictException):
            si.commit(x2)


class WriteSkewTest(unittest.TestCase):
    def test_exactly_one_aborts_and_invariant_holds(self):
        skew = seeded()
        t1, t2 = skew.begin(), skew.begin()
        self.assertTrue(withdraw(skew, t1, "A", "B", 150))
        self.assertTrue(withdraw(skew, t2, "B", "A", 150))
        self.assertTrue(t1.is_pivot() and t2.is_pivot())
        self.assertTrue(set(t1.in_conflicts) == {t2.id} and set(t1.out_conflicts) == {t2.id})
        with self.assertRaises(SerializationFailureException):
            skew.commit(t1)
        self.assertTrue(not t2.in_conflicts and not t2.out_conflicts)
        self.assertTrue(skew.commit(t2) > 0 and t2.status is TxnStatus.COMMITTED)
        audit = skew.begin()
        self.assertEqual(skew.read(audit, "A") + skew.read(audit, "B"), 50)
        skew.commit(audit)
        self.assertTrue(skew.aborts() == 1 and skew.commits() == 3)
        with self.assertRaises(RuntimeError) as ctx:
            skew.read(t1, "A")
        self.assertNotIsInstance(ctx.exception, SerializationFailureException)  # Java: IllegalStateException

    def test_committed_pivot_aborts_newcomer_on_the_spot(self):
        skew2 = seeded()
        p, q = skew2.begin(), skew2.begin()
        withdraw(skew2, p, "A", "B", 150)
        skew2.commit(p)
        self.assertIs(p.status, TxnStatus.COMMITTED)
        q_sum = skew2.read(q, "A") + skew2.read(q, "B")
        self.assertTrue(q_sum == 200 and p.id in q.out_conflicts and q.id in p.in_conflicts)
        with self.assertRaises(SerializationFailureException):
            skew2.write(q, "B", -50)
        self.assertTrue(q.status is TxnStatus.ABORTED and f"T{p.id}" in q.abort_reason)


class NoSpuriousAbortsTest(unittest.TestCase):
    def test_disjoint_transactions(self):
        dj = seeded()
        d1, d2 = dj.begin(), dj.begin()
        dj.write(d1, "A", dj.read(d1, "A") + 1)
        dj.write(d2, "B", dj.read(d2, "B") + 1)
        dj.commit(d1)
        dj.commit(d2)
        self.assertTrue(dj.commits() == 3 and dj.aborts() == 0)

    def test_later_read_only_txn(self):
        ro = seeded()
        t_a, t_b = ro.begin(), ro.begin()
        ro.read(t_a, "B")
        ro.write(t_b, "B", 1)  # t_a -rw-> t_b
        ro.commit(t_b)
        ro.write(t_a, "A", 1)
        ro.commit(t_a)  # t_a has only an out edge: fine
        t_r = ro.begin()  # started after t_a's commit, so it is not concurrent with t_a
        self.assertTrue(ro.read(t_r, "A") == 1 and ro.read(t_r, "B") == 1)
        ro.commit(t_r)
        self.assertEqual(ro.aborts(), 0)


class SireadRetentionTest(unittest.TestCase):
    def test_locks_outlive_commit_until_nothing_is_concurrent(self):
        ret = seeded()
        early, late = ret.begin(), ret.begin()
        ret.read(early, "B")
        ret.commit(early)
        self.assertIn(early.id, ret.readers_of("B"))
        self.assertTrue(ret.release_finished() == 1 and early.id in ret.readers_of("B"))
        ret.write(late, "B", 1)
        self.assertIn(early.id, late.in_conflicts)
        ret.commit(late)
        self.assertIs(late.status, TxnStatus.COMMITTED)
        self.assertTrue(ret.release_finished() == 2 and ret.retained_read_locks() == 0)


class DeleteTest(unittest.TestCase):
    def test_deletes_participate(self):
        d = seeded()
        rd, dl = d.begin(), d.begin()
        d.read(rd, "A")
        d.delete(dl, "A")
        self.assertIn(dl.id, rd.out_conflicts)
        d.commit(dl)
        self.assertEqual(d.read(rd, "A"), 100)
        d.commit(rd)
        self.assertIsNone(d.read(d.begin(), "A"))


class MoneyTest(unittest.TestCase):
    def test_overflow_like_math_add_exact(self):
        # Python-only: Java gets ArithmeticException from Math.addExact; here the 64-bit bound is explicit.
        with self.assertRaises(OverflowError):
            Money.of_minor(INT64_MAX, Currency.USD).plus(usd("0.01"))
        self.assertEqual(str(usd("-50.00")), "-50.00 USD")


if __name__ == "__main__":
    unittest.main()
