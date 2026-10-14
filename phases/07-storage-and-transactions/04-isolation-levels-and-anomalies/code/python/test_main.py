"""Tests for phases/07-storage-and-transactions/04-isolation-levels-and-anomalies. Run: python3 -m unittest -v"""
import unittest

from main import AnomalyLab, ConflictException, Isolation, Store, Txn

RU, RC, RR = Isolation.READ_UNCOMMITTED, Isolation.READ_COMMITTED, Isolation.REPEATABLE_READ
SI, SER = Isolation.SNAPSHOT, Isolation.SERIALIZABLE


class MatrixTest(unittest.TestCase):
    """The matrix is Berenson et al. (1995), Table 4."""

    @classmethod
    def setUpClass(cls):
        cls.m = AnomalyLab.matrix()

    def test_dirty_read_only_read_uncommitted(self):
        row = self.m["dirty read"]
        self.assertTrue(row[RU] and not row[RC] and not row[RR] and not row[SI] and not row[SER])

    def test_non_repeatable_read(self):
        row = self.m["non-repeatable read"]
        self.assertTrue(row[RU] and row[RC])
        self.assertTrue(not row[RR] and not row[SI] and not row[SER])

    def test_phantom(self):
        row = self.m["phantom"]
        self.assertTrue(row[RR])  # lock-based REPEATABLE_READ still allows it
        self.assertFalse(row[SI])  # reads from a fixed snapshot
        self.assertFalse(row[SER])  # predicate locks

    def test_lost_update(self):
        row = self.m["lost update"]
        self.assertTrue(row[RU] and row[RC])
        self.assertTrue(not row[RR] and not row[SI] and not row[SER])

    def test_write_skew(self):
        row = self.m["write skew"]
        self.assertTrue(row[SI])
        self.assertFalse(row[RR])
        self.assertFalse(row[SER])
        self.assertTrue(row[RU] and row[RC])

    def test_extremes_and_incomparability(self):
        self.assertFalse(any(row[SER] for row in self.m.values()))
        self.assertTrue(all(row[RU] for row in self.m.values()))
        self.assertTrue(self.m["phantom"][RR] != self.m["phantom"][SI]
                        and self.m["write skew"][RR] != self.m["write skew"][SI])


class ScenarioTest(unittest.TestCase):
    """The scenarios say WHY, not just whether."""

    def test_write_skew_details(self):
        skew = AnomalyLab.write_skew(SI)
        self.assertTrue(skew.observed and skew.detail.startswith("combined balance -800"))
        ser = AnomalyLab.write_skew(SER)
        self.assertTrue("T1 ABORTED" in ser.detail and "T2 COMMITTED" in ser.detail)

    def test_lost_update_details(self):
        lu = AnomalyLab.lost_update(SI)
        self.assertIn("T1 COMMITTED, T2 ABORTED", lu.detail)
        self.assertTrue(any("first-committer-wins" in line for line in lu.trace))
        self.assertTrue(AnomalyLab.lost_update(RC).detail.startswith("final balance 1200"))

    def test_dirty_read_and_phantom_details(self):
        self.assertEqual(AnomalyLab.dirty_read(RU).detail, "T2 saw 900; committed value is 1000")
        self.assertEqual(AnomalyLab.phantom(RR).detail, "T1 counted 2 then 3 postings")


class StoreMechanicsTest(unittest.TestCase):
    def test_read_uncommitted_in_place_and_undo(self):
        s = Store()
        s.load("a", 10)
        t1 = s.begin(RU)
        t1.write("a", 5)
        self.assertEqual(s.committed["a"], 5)
        t1.abort()
        self.assertEqual(s.committed["a"], 10)
        self.assertEqual(s.locks, {})

    def test_read_committed_buffering_and_no_dirty_writes(self):
        s2 = Store()
        s2.load("a", 10)
        w = s2.begin(RC)
        w.write("a", 7)
        self.assertTrue(s2.committed["a"] == 10 and w.read("a") == 7)
        other = s2.begin(RC)
        with self.assertRaises(ConflictException):
            other.write("a", 99)
        self.assertIs(other.status, Txn.Status.ABORTED)
        with self.assertRaises(RuntimeError):
            other.read("a")
        w.commit()
        self.assertTrue(s2.committed["a"] == 7 and s2.commit_clock == 1 and s2.version_of["a"] == 1)
        with self.assertRaises(RuntimeError):
            w.commit()

    def test_repeatable_read_shared_locks(self):
        s3 = Store()
        s3.load("x", 1)
        r = s3.begin(RR)
        r.read("x")
        r2 = s3.begin(RR)
        self.assertTrue(r2.read("x") == 1 and len(s3.locks["x"].shared) == 2)
        with self.assertRaises(ConflictException):
            r2.write("x", 2)
        r.write("x", 3)
        self.assertEqual(s3.locks["x"].exclusive, r.id)
        r.commit()
        self.assertEqual(s3.committed["x"], 3)

    def test_serializable_predicate_locks(self):
        s4 = Store()
        s4.load("p:1", 1)
        scanner = s4.begin(SER)
        self.assertEqual(len(scanner.scan("p:")), 1)
        inserter = s4.begin(SER)
        with self.assertRaises(ConflictException):
            inserter.write("p:2", 2)
        elsewhere = s4.begin(SER)
        elsewhere.write("q:1", 5)
        elsewhere.commit()
        self.assertEqual(s4.committed["q:1"], 5)
        scanner.commit()
        self.assertEqual(s4.predicate_locks, {})

    def test_snapshot_first_committer_wins(self):
        s5 = Store()
        s5.load("k", 1)
        snap = s5.begin(SI)
        writer = s5.begin(RC)
        writer.write("k", 2)
        writer.commit()
        self.assertEqual(snap.read("k"), 1)
        snap.write("k", 10)
        with self.assertRaises(ConflictException):
            snap.commit()
        self.assertEqual(s5.committed["k"], 2)
        read_only = s5.begin(SI)
        w2 = s5.begin(RC)
        w2.write("k", 3)
        w2.commit()
        read_only.read("k")
        read_only.commit()
        self.assertIs(read_only.status, Txn.Status.COMMITTED)

    def test_conflict_is_not_a_runtime_error(self):
        # Python-only guard: _quietly and the tests rely on the two error kinds being distinct.
        self.assertFalse(issubclass(ConflictException, RuntimeError))


if __name__ == "__main__":
    unittest.main()
