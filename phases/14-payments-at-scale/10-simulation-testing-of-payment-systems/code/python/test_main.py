"""Tests for phases/14-payments-at-scale/10-simulation-testing-of-payment-systems. Run: python3 -m unittest -v
Everything is deterministic: no wall clock, no threads, no network. A failing check names the invariant it broke."""
import unittest

from main import (
    USD, AuthRequest, Currency, CurrencyMismatchError, Defect, FaultSchedule, Gateway, Harness, JournalEntry, Ledger, Money,
    Network, NetworkConfig, PaymentRequest, Posting, Processor, Rng, SimConfig, Simulator, Status,
)

TEN = Money.of_minor(1000, USD)


class RngTest(unittest.TestCase):
    def test_splitmix64_reference_vectors(self):
        r = Rng(0)
        self.assertEqual(r.next_long(), 0xE220A8397B1DCDAF)
        self.assertEqual(r.next_long(), 0x6E789E6AA1B965F4)

    def test_bounds_and_chances(self):
        bounded = Rng(42)
        self.assertTrue(all(0 <= bounded.next_int(7) < 7 for _ in range(10_000)))
        self.assertFalse(Rng(1).chance(0))
        self.assertTrue(Rng(1).chance(10_000))
        self.assertLess(Rng(3).next_long_bounded(10_000_000_000), 10_000_000_000)
        with self.assertRaises(ValueError):
            Rng(1).next_int(0)
        s1, s2 = Rng(7), Rng(7)
        self.assertEqual(s1.next_long(), s2.next_long())
        self.assertEqual(s1.next_int(100), s2.next_int(100))


class SimulatorTest(unittest.TestCase):
    def test_ordering(self):
        sim = Simulator()
        order = []
        sim.schedule(50, "b", lambda: order.append("b"))
        sim.schedule(10, "a", lambda: order.append("a"))
        sim.schedule(50, "c", lambda: order.append("c"))
        sim.run_until_idle(100)
        self.assertEqual(order, ["a", "b", "c"], "time order, ties in scheduling order")
        self.assertEqual(sim.now, 50)

    def test_clock_rules(self):
        idle = Simulator()
        idle.run_until(5_000)
        self.assertEqual(idle.now, 5_000, "run_until advances the clock even with nothing scheduled")
        with self.assertRaises(ValueError):
            Simulator().schedule(-1, "x", lambda: None)
        loop = Simulator()

        def again() -> None:
            loop.schedule(1, "again", again)

        loop.schedule(0, "again", again)
        with self.assertRaises(RuntimeError):
            loop.run_until_idle(1_000)


class NetworkTest(unittest.TestCase):
    def test_latency_partitions_and_drops(self):
        nsim = Simulator()
        reliable = Network(nsim, Rng(1), NetworkConfig(20, 200, 0))
        got = [False]
        reliable.send("client-0", "gateway", "ping", lambda: got.__setitem__(0, True))
        nsim.run_until_idle(10)
        self.assertTrue(got[0])
        self.assertTrue(20 <= nsim.now <= 200, "delivered within the latency window")
        reliable.partition("client-0", "gateway")
        got2 = [False]
        reliable.send("gateway", "client-0", "pong", lambda: got2.__setitem__(0, True))
        nsim.run_until_idle(10)
        self.assertFalse(got2[0], "a partition blocks both directions")
        self.assertEqual(reliable.blocked, 1)
        reliable.heal("client-0", "gateway")
        self.assertFalse(reliable.is_partitioned("client-0", "gateway"))
        self.assertFalse(reliable.is_partitioned("gateway", "client-0"))
        lossy = Network(Simulator(), Rng(5), NetworkConfig(1, 1, 10_000))
        lossy.send("a", "b", "x", lambda: None)
        self.assertEqual(lossy.dropped, 1)
        self.assertEqual(lossy.delivered, 0)


class LedgerTest(unittest.TestCase):
    def test_money_and_ledger_discipline(self):
        with self.assertRaises(CurrencyMismatchError):
            TEN.plus(Money.of_minor(1, Currency("EUR", 2)))
        with self.assertRaises(ValueError):
            JournalEntry.transfer("e1", "a", "b", Money.zero(USD))
        ledger = Ledger(USD)
        ledger.post(JournalEntry.transfer("pay-1", "asset:receivable:c1", "liability:clearing", TEN))
        self.assertTrue(ledger.trial_balance().is_zero())
        self.assertEqual(ledger.balance("asset:receivable:c1"), TEN)
        with self.assertRaises(ValueError):
            ledger.post(JournalEntry("bad", (Posting("a", TEN),)))


class ComponentTest(unittest.TestCase):
    def test_processor_idempotent_on_auth_key(self):
        psim = Simulator()
        pnet = Network(psim, Rng(2), NetworkConfig(1, 1, 0))
        processor = Processor(psim, pnet, Rng(3), 0)
        gw = Gateway(psim, pnet, processor, Defect.NONE, 3_000, 2_000)
        processor.receive(AuthRequest("auth-pay-1", "pay-1", "c1", TEN, 1), gw)
        processor.receive(AuthRequest("auth-pay-1", "pay-1", "c1", TEN, 2), gw)
        self.assertEqual(len(processor.approvals), 1)
        self.assertEqual(processor.replayed, 1)

    def test_gateway_idempotent_on_client_key(self):
        gsim = Simulator()
        gnet = Network(gsim, Rng(4), NetworkConfig(1, 1, 0))
        proc2 = Processor(gsim, gnet, Rng(5), 0)
        gw2 = Gateway(gsim, gnet, proc2, Defect.NONE, 3_000, 2_000)
        replies = []
        gw2.handle(PaymentRequest("key-1", "c1", TEN), replies.append)
        gw2.handle(PaymentRequest("key-1", "c1", TEN), replies.append)
        gsim.run_until_idle(1_000)
        self.assertEqual(len(gw2.payments), 1)
        self.assertEqual(gw2.replays, 1)
        self.assertEqual(replies, [Status.APPROVED, Status.APPROVED], "the duplicate waits and gets the same answer")
        self.assertEqual(gw2.ledger.count_entries("pay-000001"), 1)
        self.assertTrue(gw2.ledger.trial_balance().is_zero())

    def test_crash_when_busy(self):
        csim = Simulator()
        cnet = Network(csim, Rng(6), NetworkConfig(1, 1, 0))
        proc3 = Processor(csim, cnet, Rng(7), 0)
        gw3 = Gateway(csim, cnet, proc3, Defect.NONE, 3_000, 2_000)
        gw3.crash_when_busy()
        self.assertEqual(gw3.crashes, 0, "an armed crash does not fire while nothing is pending")
        proc3.down = True  # the first auth attempt is swallowed
        csim.schedule(1_000, "processor-up", lambda: setattr(proc3, "down", False))
        late = []
        gw3.handle(PaymentRequest("key-9", "c9", TEN), late.append)
        self.assertEqual(gw3.crashes, 1, "the armed crash fires once a payment is in flight")
        self.assertEqual(late, [])
        csim.run_until_idle(1_000)
        self.assertIs(gw3.payments["pay-000001"].status, Status.APPROVED)
        self.assertEqual(gw3.recovered, 1)
        self.assertEqual(gw3.ledger.count_entries("pay-000001"), 1)
        self.assertEqual(gw3.auth_timeouts, 0, "the timer armed before the crash was invalidated by the epoch")
        self.assertEqual(gw3.auth_sends, 2)

    def test_fault_schedule(self):
        horizon = 3000 * Harness.HOUR
        schedule = FaultSchedule.generate(Rng(11), horizon, 100)
        starts = [f.start_ms for f in schedule.faults]
        self.assertEqual(starts, sorted(starts))
        self.assertTrue(any(f.start_ms > 2 ** 31 - 1 for f in schedule.faults), "fault starts reach beyond 2^31 ms")
        self.assertTrue(all(0 <= f.start_ms <= f.end_ms <= horizon for f in schedule.faults))


class WholeSystemTest(unittest.TestCase):
    def test_determinism_and_invariants(self):
        a = Harness.run(SimConfig.standard(1, 48))
        b = Harness.run(SimConfig.standard(1, 48))
        self.assertEqual(a.fingerprint, b.fingerprint)
        self.assertEqual(a.events, b.events)
        self.assertEqual(a.payments, b.payments)
        self.assertNotEqual(a.fingerprint, Harness.run(SimConfig.standard(2, 48)).fingerprint)
        self.assertTrue(a.passed(), "the correct gateway survives 48 hours of faults")
        self.assertEqual(a.pending, 0)
        self.assertGreater(a.payments, 500)
        self.assertGreater(a.crashes, 0)
        self.assertGreater(a.net_dropped, 0)
        self.assertGreater(a.auth_timeouts, 0)
        self.assertTrue(a.trial_balance.is_zero())
        self.assertEqual(a.fingerprint, "ff8801a677b7abd9", "the same seed gives the same fingerprint as the Java run")

    def test_planted_defects_are_caught(self):
        fresh = Harness.find_failing_seed(SimConfig.standard(1, 24).with_defect(Defect.FRESH_AUTH_KEY_ON_RETRY), 1, 20)
        self.assertIsNotNone(fresh)
        self.assertEqual(fresh.first_broken_invariant(), "no-double-authorization")
        skip = Harness.find_failing_seed(SimConfig.standard(1, 24).with_defect(Defect.RECOVERY_SKIPS_PENDING), 1, 20)
        self.assertIsNotNone(skip)
        self.assertIn(skip.first_broken_invariant(), ("no-stuck-payments", "approval-posted-once"))
        self.assertIn(f"SimConfig.standard({skip.seed}, 24)", skip.reproduction())
        self.assertIsNone(Harness.find_failing_seed(SimConfig.standard(1, 12), 1, 2), "a passing search returns None")


if __name__ == "__main__":
    unittest.main()
