"""Tests for phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks.
Run: python3 -m unittest -v"""
import unittest

from main import (
    ClockDriftException, DriftingClock, HlcTimestamp, HybridLogicalClock, JavaRandom, Node, NtpClient, NtpSample,
    TrueTime, java_round, payment_trace, tdiv,
)

T0 = 1_700_000_000_000


class DriftingClockTest(unittest.TestCase):
    def test_drift_and_step(self):
        t = TrueTime(T0)
        exact = DriftingClock(t, 0, 0)
        fast = DriftingClock(t, 50, 0)
        behind = DriftingClock(t, 0, -80)
        self.assertEqual(exact.now(), T0, "exact clock reads true time")
        self.assertEqual(behind.error(), -80)
        t.advance(86_400_000)
        self.assertEqual(fast.error(), 4320, "50 ppm gains 4320 ms per day")
        self.assertEqual(exact.error(), 0)
        fast.step(-fast.error())
        self.assertEqual(fast.error(), 0, "step corrects the offset")
        t.advance(1_000_000)
        self.assertEqual(fast.error(), 50, "drift continues after a step")
        with self.assertRaises(ValueError):
            t.advance(-1)


class NtpArithmeticTest(unittest.TestCase):
    def test_rfc5905_formulas(self):
        s = NtpSample(1000, 1110, 1110, 1020)
        self.assertEqual(s.offset(), 100)
        self.assertEqual(s.delay(), 20, "delay formula excludes server processing")
        busy = NtpSample(1000, 1110, 1115, 1025)
        self.assertEqual(busy.delay(), 20, "server processing time is excluded from delay")
        self.assertEqual(busy.offset(), 100, "server processing does not bias offset")
        asym = NtpSample(1000, 1130, 1130, 1040)
        self.assertEqual(asym.offset(), 110, "asymmetric path biases offset by half the asymmetry")
        self.assertLessEqual(abs(asym.offset() - 100), asym.delay() // 2)

    def test_java_integer_semantics(self):
        # A client 150.5 ms ahead: Java's / truncates to -150, Python's // would give -151.
        self.assertEqual(NtpSample(0, -150, -150, 1).offset(), -150)
        self.assertEqual(tdiv(-301, 2), -150)
        self.assertEqual(java_round(2.5), 3, "Math.round rounds ties up; Python's round(2.5) is 2")
        self.assertEqual(java_round(-2.5), -2)


class NtpExchangeTest(unittest.TestCase):
    def test_simulated_exchange(self):
        t2 = TrueTime(T0)
        server = DriftingClock(t2, 0, 0)
        client = DriftingClock(t2, 0, -150)
        sym = NtpClient(t2, client, server, 1, 10, 0).exchange()
        self.assertEqual(sym.offset(), 150, "symmetric exchange recovers the exact offset")
        self.assertEqual(sym.delay(), 20)
        jittery = NtpClient(t2, client, server, 99, 10, 60)
        all_bounded = True
        best = None
        for _ in range(20):
            x = jittery.exchange()
            if abs(x.offset() - 150) > x.delay() // 2:
                all_bounded = False
            if best is None or x.delay() < best.delay():
                best = x
        self.assertTrue(all_bounded, "every jittery sample's error is within delay/2")
        self.assertLessEqual(abs(best.offset() - 150), best.delay() // 2)
        correction = jittery.synchronize(16)
        self.assertLessEqual(abs(correction - 150), 30)
        self.assertLessEqual(abs(client.error()), 30, "residual error after synchronize is small")

    def test_java_random_next_long_bound(self):
        # Same seed, same draws as java.util.Random(7).nextLong(41): the demo's first jittery latencies.
        r = JavaRandom(7)
        self.assertEqual([r.next_long(41) for _ in range(4)], [33, 16, 8, 7])


class HlcTimestampTest(unittest.TestCase):
    def test_order_and_packing(self):
        a = HlcTimestamp(T0, 3)
        b = HlcTimestamp(T0, 4)
        c = HlcTimestamp(T0 + 1, 0)
        self.assertTrue(a.compare_to(b) < 0 and b.compare_to(c) < 0, "HLC compares by l then c")
        self.assertTrue(a.pack() < b.pack() < c.pack(), "pack preserves order")
        self.assertEqual(HlcTimestamp.unpack(c.pack()), c)
        self.assertEqual(HlcTimestamp.unpack(a.pack()), a)
        with self.assertRaises(ValueError):
            HlcTimestamp(T0, 70_000)
        with self.assertRaises(ValueError):
            HlcTimestamp(1 << 48, 0)


class HlcAlgorithmTest(unittest.TestCase):
    def test_figure_5(self):
        t3 = TrueTime(T0)
        pc = DriftingClock(t3, 0, 0)
        hlc = HybridLogicalClock(pc, 1000)
        e1 = hlc.now()
        e2 = hlc.now()
        self.assertEqual((e1.l, e2.l, e2.c), (T0, T0, e1.c + 1), "same millisecond: l unchanged, c increments")
        t3.advance(5)
        e3 = hlc.now()
        self.assertEqual((e3.l, e3.c), (T0 + 5, 0), "physical time advanced: l follows pt and c resets")
        self.assertGreaterEqual(e3.l, pc.now())
        from_future = HlcTimestamp(T0 + 300, 2)
        e4 = hlc.receive(from_future)
        self.assertEqual((e4.l, e4.c), (T0 + 300, 3))
        self.assertGreater(e4.compare_to(from_future), 0, "receive is after the message")
        e5 = hlc.now()
        self.assertEqual((e5.l, e5.c), (T0 + 300, 4))
        e6 = hlc.receive(HlcTimestamp(T0 + 100, 9))
        self.assertEqual((e6.l, e6.c), (T0 + 300, 5), "receive from the past does not move l backwards")
        e7 = hlc.receive(HlcTimestamp(T0 + 300, 40))
        self.assertEqual((e7.l, e7.c), (T0 + 300, 41), "equal l on both sides: c = max(c, c_m) + 1")
        t3.advance(1000)
        e8 = hlc.now()
        self.assertEqual((e8.l, e8.c), (T0 + 1005, 0), "once pt passes l, l rejoins pt and c resets")
        with self.assertRaises(ClockDriftException):
            hlc.receive(HlcTimestamp(pc.now() + 5000, 0))
        self.assertEqual(hlc.peek().l, T0 + 1005, "rejected receive did not corrupt the clock")

    def test_counter_overflow(self):
        hlc = HybridLogicalClock(DriftingClock(TrueTime(T0), 0, 0), 1000)
        with self.assertRaises(ArithmeticError):
            hlc.receive(HlcTimestamp(T0, HlcTimestamp.COUNTER_MAX))


class PaymentTraceTest(unittest.TestCase):
    def test_causality(self):
        t4 = TrueTime(T0)
        gateway = Node("gateway", t4, 0, 0, 1000)
        ledger = Node("ledger", t4, 0, -80, 1000)
        notifier = Node("notifier", t4, 0, 35, 1000)
        trace = payment_trace(t4, gateway, ledger, notifier)
        r = trace.report()
        self.assertEqual(r.pairs, 4)
        self.assertGreater(r.wall_violations, 0, "wall clocks violate causality on the skewed node")
        self.assertEqual(r.lamport_violations, 0)
        self.assertEqual(r.hlc_violations, 0)
        self.assertLessEqual(r.max_hlc_ahead_of_true_ms, 35)
        ledger_stamps = [e.hlc for e in trace.events if e.node == "ledger"]
        self.assertTrue(all(a.compare_to(b) < 0 for a, b in zip(ledger_stamps, ledger_stamps[1:])),
                        "HLC is strictly monotonic on one node")


if __name__ == "__main__":
    unittest.main()
