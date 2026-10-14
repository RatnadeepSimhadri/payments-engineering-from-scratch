"""Tests for phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation. Run: python3 -m unittest -v"""
import unittest

from main import INT64_MAX, Currency, Money, MvccStore, TimestampOracle, TxnStatus, WriteConflictException, usd


def seeded() -> MvccStore:
    store = MvccStore(TimestampOracle())
    t = store.begin()
    store.write(t, "A", 100)
    store.write(t, "B", 100)
    store.commit(t)
    return store


class SnapshotReadTest(unittest.TestCase):
    def test_snapshot_reads(self):
        s = seeded()
        r = s.begin()
        self.assertEqual(s.read(r, "A"), 100)
        w = s.begin()
        s.write(w, "A", 40)
        self.assertEqual(s.read(r, "A"), 100)  # buffered write invisible to other txn
        self.assertEqual(s.read(w, "A"), 40)  # ... visible to itself
        self.assertEqual(s.commit(w), 2)
        self.assertEqual(s.read(r, "A"), 100)  # reader keeps its snapshot
        self.assertEqual(s.read(s.begin(), "A"), 40)
        self.assertIsNone(s.read(r, "Z"))
        self.assertEqual(s.commit(r), r.start_ts)  # read-only commit consumes no timestamp
        self.assertEqual(s.version_count("A"), 2)


class FirstCommitterWinsTest(unittest.TestCase):
    def test_second_committer_aborts(self):
        f = seeded()
        t1, t2 = f.begin(), f.begin()
        f.write(t1, "A", 90)
        f.write(t2, "A", 75)
        f.commit(t1)
        with self.assertRaises(WriteConflictException):
            f.commit(t2)
        self.assertIs(t2.status, TxnStatus.ABORTED)
        self.assertEqual(f.read(f.begin(), "A"), 90)
        self.assertTrue(f.aborts() == 1 and f.commits() == 2)
        with self.assertRaises(RuntimeError) as ctx:
            f.read(t2, "A")
        self.assertNotIsInstance(ctx.exception, WriteConflictException)
        t3 = f.begin()
        f.write(t3, "A", 80)
        f.commit(t3)
        self.assertEqual(f.read(f.begin(), "A"), 80)


class WriteSkewTest(unittest.TestCase):
    def test_write_skew_is_allowed(self):
        skew = seeded()
        w1, w2 = skew.begin(), skew.begin()
        sum1 = skew.read(w1, "A") + skew.read(w1, "B")
        sum2 = skew.read(w2, "A") + skew.read(w2, "B")
        if sum1 >= 150:
            skew.write(w1, "A", skew.read(w1, "A") - 150)
        if sum2 >= 150:
            skew.write(w2, "B", skew.read(w2, "B") - 150)
        skew.commit(w1)
        skew.commit(w2)
        audit = skew.begin()
        self.assertEqual(skew.commits(), 3)
        self.assertEqual(skew.read(audit, "A") + skew.read(audit, "B"), -100)


class TombstoneTest(unittest.TestCase):
    def test_deletes(self):
        d = seeded()
        old = d.begin()
        dele = d.begin()
        d.delete(dele, "B")
        self.assertIsNone(d.read(dele, "B"))
        d.commit(dele)
        self.assertEqual(d.read(old, "B"), 100)
        self.assertIsNone(d.read(d.begin(), "B"))
        self.assertTrue(d.version_count("B") == 2 and d.history("B")[1].is_tombstone())


class VacuumTest(unittest.TestCase):
    def test_vacuum_respects_oldest_snapshot(self):
        g = seeded()
        pin = g.begin()
        for _ in range(5):
            t = g.begin()
            g.write(t, "A", g.read(t, "A") + 1)
            g.commit(t)
        self.assertEqual(g.version_count("A"), 6)
        self.assertTrue(g.vacuum() == 0 and g.version_count("A") == 6)
        self.assertEqual(g.read(pin, "A"), 100)
        g.commit(pin)
        self.assertTrue(g.vacuum() == 5 and g.version_count("A") == 1)
        after = g.begin()
        self.assertEqual(g.read(after, "A"), 105)
        g.commit(after)
        del2 = g.begin()
        g.delete(del2, "B")
        g.commit(del2)
        self.assertTrue(g.vacuum() == 2 and g.version_count("B") == 0)
        self.assertEqual(g.active_count(), 0)


class MoneyHelperTest(unittest.TestCase):
    def test_usd_and_str(self):
        self.assertTrue(usd("19.99").minor_units == 1999 and usd("5").minor_units == 500)
        self.assertEqual(str(usd("-50.00").minus(usd("0.05"))), "-50.05 USD")

    def test_overflow_like_math_add_exact(self):
        # Python-only: Java gets ArithmeticException from Math.addExact; here the 64-bit bound is explicit.
        with self.assertRaises(OverflowError):
            Money.of_minor(INT64_MAX, Currency.USD).plus(usd("0.01"))


if __name__ == "__main__":
    unittest.main()
