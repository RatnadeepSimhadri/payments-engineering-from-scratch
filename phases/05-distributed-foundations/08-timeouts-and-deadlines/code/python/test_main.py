"""Tests for phases/05-distributed-foundations/08-timeouts-and-deadlines. Run: python3 -m unittest -v"""
import unittest

from main import (
    INT64_MAX, Client, Context, Deadline, DeadlineAwareRetrier, DeadlineExceededException, EffectLog, FakeClock,
    FlakyService, Hedger, Status, build_chain,
)


class DeadlineTest(unittest.TestCase):
    def test_arithmetic(self):
        clock = FakeClock(1_000)
        d = Deadline.after(clock, 500)
        self.assertEqual(d.at_ms, 1_500, "deadline is absolute")
        self.assertEqual(d.remaining_ms(clock), 500)
        clock.advance(200)
        self.assertEqual(d.remaining_ms(clock), 300, "remaining shrinks as time passes")
        clock.advance(400)
        self.assertEqual(d.remaining_ms(clock), 0, "remaining clamps at zero, never negative")
        self.assertTrue(d.is_expired(clock))
        self.assertFalse(Deadline.NONE.is_expired(clock))
        self.assertEqual(Deadline.NONE.remaining_ms(clock), INT64_MAX)
        self.assertEqual(Deadline(10).earliest(Deadline(5)).at_ms, 5, "earliest picks the tighter deadline")
        self.assertEqual(Deadline.saturating_add(INT64_MAX - 1, 10), INT64_MAX, "saturating add does not wrap")
        with self.assertRaises(ValueError):
            Deadline.after(clock, -1)


class ContextTest(unittest.TestCase):
    def test_children_tighten_never_extend(self):
        c = FakeClock(0)
        parent = Context.root("r").with_timeout(c, 500)
        child = parent.with_timeout(c, 2_000)
        self.assertEqual(child.deadline.at_ms, 500, "child cannot extend parent's deadline")
        self.assertEqual(parent.with_timeout(c, 100).deadline.at_ms, 100, "child can tighten parent's deadline")
        self.assertEqual(child.request_id, "r")
        with self.assertRaises(DeadlineExceededException):
            parent.require_budget(c, 600, "test")
        c.advance(500)
        with self.assertRaises(DeadlineExceededException):
            parent.checkpoint(c, "test")

    def test_wire_encoding(self):
        sender = FakeClock(1_000_000)
        receiver = FakeClock(7)        # wildly different clock: must not matter
        out = Context.root("w").with_timeout(sender, 250)
        sender.advance(50)
        header = out.to_wire_timeout(sender)
        self.assertEqual(header, "200m", "wire header carries remaining ms with unit")
        inbound = Context.from_wire("w", header, receiver)
        self.assertEqual(inbound.remaining_ms(receiver), 200)
        self.assertEqual(inbound.deadline.at_ms, 207, "receiver rebuilds the budget on its own clock")
        self.assertIsNone(Context.root("x").to_wire_timeout(sender), "no deadline encodes as absent header")
        self.assertTrue(Context.from_wire("x", None, receiver).deadline.is_none())
        self.assertEqual(Context.from_wire("x", "2S", receiver).remaining_ms(receiver), 2_000)
        self.assertEqual(Context.from_wire("x", "1500u", receiver).remaining_ms(receiver), 1)
        with self.assertRaises(ValueError):
            Context.from_wire("x", "5x", receiver)
        with self.assertRaises(ValueError):
            Context.from_wire("x", "1_0m", receiver)   # Long.parseLong rejects what Python's int() would accept


class ChainTest(unittest.TestCase):
    def test_without_propagation_charge_outlives_caller(self):
        c1 = FakeClock(0)
        log1 = EffectLog()
        o1 = Client(c1).invoke(build_chain(c1, log1, False), "p1", 500)
        self.assertIs(o1.status, Status.TIMED_OUT, "without propagation the caller times out")
        self.assertTrue(log1.contains("CHARGE"), "without propagation the charge still happened")
        self.assertGreater(log1.all()[0].at_ms, 500, "the charge landed after the caller gave up")

    def test_with_propagation_fails_fast(self):
        c2 = FakeClock(0)
        log2 = EffectLog()
        o2 = Client(c2).invoke(build_chain(c2, log2, True), "p2", 500)
        self.assertIs(o2.status, Status.FAILED_FAST, "with propagation the processor refuses before charging")
        self.assertEqual(log2.all(), [], "with propagation no side effect is recorded")
        self.assertLess(o2.elapsed_ms, 500)
        self.assertEqual(o2.elapsed_ms, 220, "fail-fast returns well inside the budget")

    def test_budget_that_fits(self):
        c3 = FakeClock(0)
        log3 = EffectLog()
        o3 = Client(c3).invoke(build_chain(c3, log3, True), "p3", 700)
        self.assertIs(o3.status, Status.OK)
        self.assertEqual(len(log3.all()), 1)


class RetrierTest(unittest.TestCase):
    def test_generous_deadline(self):
        c4 = FakeClock(0)
        r4 = DeadlineAwareRetrier(c4, 300, 50, 50).call(FlakyService(c4, 150, 2),
                                                        Context.root("rt").with_timeout(c4, 1_000))
        self.assertIs(r4.status, Status.OK)
        self.assertEqual(r4.attempts, 3)
        self.assertEqual(r4.elapsed_ms, 150 + 50 + 150 + 50 + 150, "elapsed is attempts plus backoffs")

    def test_stops_at_deadline(self):
        c5 = FakeClock(0)
        r5 = DeadlineAwareRetrier(c5, 300, 50, 50).call(FlakyService(c5, 150, 2),
                                                        Context.root("rt").with_timeout(c5, 400))
        self.assertIs(r5.status, Status.FAILED_FAST)
        self.assertEqual(r5.attempts, 2, "retrier stops instead of starting a doomed attempt")
        self.assertEqual(r5.attempt_timeouts_ms, [300, 200], "second attempt budget was clipped")
        self.assertLessEqual(c5.now_ms(), 400, "retrier never overran the deadline")

    def test_attempt_that_cannot_fit(self):
        c6 = FakeClock(0)
        r6 = DeadlineAwareRetrier(c6, 300, 50, 50).call(FlakyService(c6, 150, 0),
                                                        Context.root("rt").with_timeout(c6, 100))
        self.assertIs(r6.status, Status.FAILED_FAST)
        self.assertEqual(c6.now_ms(), 0, "an attempt that cannot fit is refused before any work")


class HedgerTest(unittest.TestCase):
    def test_hedge_decisions(self):
        h1 = Hedger.call(800, 40, 60)
        self.assertEqual((h1.latency_ms, h1.winner, h1.requests_sent), (100, "backup", 2))
        h2 = Hedger.call(30, 40, 60)
        self.assertEqual((h2.requests_sent, h2.latency_ms), (1, 30), "no hedge sent when the primary is fast")
        h3 = Hedger.call(70, 500, 60)
        self.assertEqual((h3.winner, h3.requests_sent, h3.latency_ms), ("primary", 2, 70))

    def test_study(self):
        s = Hedger.study(42, 10_000, 60)
        self.assertLess(s.p99_hedged * 4, s.p99, "hedging cuts p99 by a large factor")
        self.assertLess(s.extra_load_percent, 10.0, "hedging costs only a few percent extra load")
        self.assertEqual(Hedger.study(42, 10_000, 60), s, "study is deterministic under the seed")
        self.assertEqual((s.p50, s.p99, s.p99_hedged), (41, 919, 113), "same numbers as java.util.Random(42)")


if __name__ == "__main__":
    unittest.main()
