"""Tests for phases/06-replication-and-consensus/12-linearizability-checking. Run: python3 -m unittest -v"""
import unittest

from main import Checker, Consistency, FakeClock, History, HistoryGenerator, HistoryRecorder, OpKind, RegisterModel


def burst(writers: int, expected_read: int) -> History:
    h = History()
    for i in range(writers):
        h.write(f"c{i}", "x", i + 1, 0, 100)
    return h.read("reader", "x", expected_read, 101, 102)


GOOD = (History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
        .read("risk", "bal", 300, 10, 12).read("app", "bal", 300, 13, 15))
STALE = (History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
         .read("risk", "bal", 500, 10, 12).read("app", "bal", 300, 13, 15))
SEQ_ONLY = History().write("ledger", "bal", 300, 0, 4).read("risk", "bal", 0, 6, 8)
WINDOW = History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 5, 15).read("app", "bal", 300, 6, 8)
SHRUNK = History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 11, 15).read("app", "bal", 300, 6, 8)


class KnownGoodTest(unittest.TestCase):
    def setUp(self):
        self.checker = Checker(RegisterModel())

    def test_known_good_history_passes_in_real_time_order(self):
        r = self.checker.check(GOOD)
        self.assertTrue(r.ok)
        self.assertEqual(r.order, (0, 1, 2, 3))

    def test_empty_history_and_initial_value(self):
        self.assertTrue(self.checker.check(History()).ok)
        self.assertTrue(self.checker.check(History().read("a", "x", 0, 0, 1)).ok)
        self.assertFalse(self.checker.check(History().read("a", "x", 7, 0, 1)).ok)

    def test_independent_keys(self):
        keys = (History().write("a", "x", 1, 0, 1).write("a", "y", 2, 2, 3)
                .read("b", "x", 1, 4, 5).read("b", "y", 2, 4, 5))
        self.assertTrue(self.checker.check(keys).ok)
        self.assertFalse(self.checker.check(History().write("a", "x", 1, 0, 1).read("b", "y", 1, 2, 3)).ok)


class StaleReadTest(unittest.TestCase):
    def setUp(self):
        self.checker = Checker(RegisterModel())

    def test_stale_read_fails_and_reports_longest_prefix(self):
        r = self.checker.check(STALE)
        self.assertFalse(r.ok)
        self.assertEqual(r.longest_prefix, (0, 1))
        self.assertEqual(r.order, ())

    def test_sequentially_consistent_but_not_linearizable(self):
        self.assertFalse(self.checker.check(SEQ_ONLY, Consistency.LINEARIZABLE).ok)
        sq = self.checker.check(SEQ_ONLY, Consistency.SEQUENTIAL)
        self.assertTrue(sq.ok)
        self.assertEqual(sq.order, (1, 0))

    def test_sequential_consistency_keeps_program_order(self):
        program_order = History().write("a", "x", 1, 0, 1).read("a", "x", 0, 2, 3)
        self.assertFalse(self.checker.check(program_order, Consistency.SEQUENTIAL).ok)
        self.assertTrue(self.checker.check(GOOD, Consistency.SEQUENTIAL).ok)


class ConcurrencyWindowTest(unittest.TestCase):
    def setUp(self):
        self.checker = Checker(RegisterModel())

    def test_overlapping_read_of_old_value_is_linearizable(self):
        r = self.checker.check(WINDOW)
        self.assertTrue(r.ok)
        self.assertEqual(r.order, (1, 0, 2))
        self.assertFalse(self.checker.check(SHRUNK).ok)

    def test_two_concurrent_writes_explore_both_orders(self):
        a = History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 1, 11, 12)
        b = History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 2, 11, 12)
        neither = History().write("a", "x", 1, 0, 10).write("b", "x", 2, 0, 10).read("c", "x", 0, 11, 12)
        self.assertEqual(self.checker.check(a).order, (1, 0, 2))
        self.assertEqual(self.checker.check(b).order, (0, 1, 2))
        self.assertFalse(self.checker.check(neither).ok)

    def test_precedes_and_concurrent_with(self):
        self.assertTrue(GOOD.op(0).precedes(GOOD.op(1)))
        self.assertTrue(WINDOW.op(0).concurrent_with(WINDOW.op(1)))
        self.assertFalse(WINDOW.op(1).precedes(WINDOW.op(0)))


class RecorderTest(unittest.TestCase):
    def test_recorder_stamps_from_the_injected_clock(self):
        clock = FakeClock()
        rec = HistoryRecorder(clock)
        w1 = rec.invoke_write("ledger", "bal", 500); clock.advance(2); rec.complete(w1, 0)
        clock.advance(1)
        r1 = rec.invoke_read("risk", "bal"); clock.advance(1); rec.complete(r1, 500)
        h = rec.history()
        self.assertEqual((h.op(0).invoked_at, h.op(0).returned_at), (0, 2))
        self.assertEqual((h.op(1).invoked_at, h.op(1).returned_at), (3, 4))
        self.assertEqual((h.op(0).value, h.op(1).value, h.op(1).kind), (500, 500, OpKind.READ))
        self.assertTrue(Checker(RegisterModel()).check(h).ok)

    def test_recorder_refuses_incomplete_histories(self):
        rec = HistoryRecorder(FakeClock())
        rec.invoke_write("a", "x", 1)
        self.assertEqual(rec.pending_count(), 1)
        with self.assertRaises(RuntimeError):
            rec.history()
        with self.assertRaises(ValueError):
            rec.complete(99, 0)


class ValidationTest(unittest.TestCase):
    def test_return_before_invoke_is_rejected(self):
        with self.assertRaises(ValueError):
            History().read("a", "x", 0, 5, 4)

    def test_more_than_64_ops_is_rejected(self):
        h = History()
        with self.assertRaises(ValueError):
            for i in range(History.MAX_OPS + 1):
                h.write("a", "x", i, i, i)


class PerformanceTest(unittest.TestCase):
    def test_memo_prunes_repeated_configurations(self):
        with_memo = Checker(RegisterModel(), True, 5_000_000).check(burst(8, 1))
        without_memo = Checker(RegisterModel(), False, 5_000_000).check(burst(8, 1))
        self.assertTrue(with_memo.ok and without_memo.ok)
        self.assertEqual(with_memo.order[7], 0)
        self.assertLess(with_memo.explored * 5, without_memo.explored)
        self.assertGreater(with_memo.memo_hits, 0)
        self.assertLessEqual(with_memo.explored, 8 * 256)

    def test_state_budget(self):
        self.assertTrue(Checker(RegisterModel(), True, 100_000).check(burst(12, 1)).ok)
        with self.assertRaises(RuntimeError):
            Checker(RegisterModel(), False, 100_000).check(burst(12, 1))

    def test_generated_histories_pass_within_bound(self):
        checker = Checker(RegisterModel())
        max_explored = 0
        for seed in range(1, 51):
            r = checker.check(HistoryGenerator.linearizable(seed, 20, 3, 15))
            self.assertTrue(r.ok, f"seed {seed}")
            max_explored = max(max_explored, r.explored)
        self.assertLessEqual(max_explored, 500)

    def test_generator_is_deterministic(self):
        gen = HistoryGenerator.linearizable(7, 20, 3, 15)
        self.assertEqual(gen.ops, HistoryGenerator.linearizable(7, 20, 3, 15).ops)
        self.assertEqual(gen.size(), 20)


if __name__ == "__main__":
    unittest.main()
