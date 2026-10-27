"""Tests for phases/14-payments-at-scale/06-observability-for-money-movement. Run: python3 -m unittest -v"""
import unittest
from datetime import timedelta

from main import (
    EUR, USD, AuthOutcome, CurrencyMismatchError, JavaRandom, ManualClock, Money, MoneyMovementMonitor, PaymentState,
    Ratio, Severity, Simulation, Slo, instant,
)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def has(alerts, name: str) -> bool:
    return any(a.name == name for a in alerts)


def get(alerts, name: str):
    return next((a for a in alerts if a.name == name), None)


class BurnRateTest(unittest.TestCase):
    def test_arithmetic_is_exact(self):
        slo = Slo("x", 1_000)
        self.assertEqual(slo.burn_rate_x100(6, 200), 3000)
        self.assertEqual(slo.burn_rate_x100(40, 2_400), 1666, "rounded down")
        self.assertEqual(slo.burn_rate_x100(1, 1_000), 100, "exactly at budget is 1.00")
        self.assertEqual(slo.burn_rate_x100(0, 0), 0)
        self.assertEqual(Ratio.bps(1, 3), 3333)
        self.assertEqual(Ratio.bps(0, 0), -1)


class StuckDetectionTest(unittest.TestCase):
    def setUp(self):
        self.clock = ManualClock(instant("2026-03-31T12:00:00Z"))
        self.m = MoneyMovementMonitor(self.clock, USD)
        self.m.transition("p1", "proc_a", usd(40_000), PaymentState.CAPTURE_PENDING)
        self.m.transition("p2", "proc_a", usd(5_000), PaymentState.AUTHORIZED)

    def test_stuck_by_state_age(self):
        m, clock = self.m, self.clock
        clock.advance(timedelta(minutes=15))
        self.assertEqual(m.stuck(), [], "exactly at the limit is not stuck")
        clock.advance(timedelta(minutes=1))
        stuck = m.stuck()
        self.assertEqual(len(stuck), 1)
        self.assertEqual(stuck[0].payment_id, "p1")
        self.assertFalse(any(s.payment_id == "p2" for s in stuck), "authorized for 16m is not stuck")
        self.assertEqual(stuck[0].age, timedelta(minutes=16))
        stuck_alert = get(m.evaluate_alerts(), "stuck-payments")
        self.assertIsNotNone(stuck_alert)
        self.assertIs(stuck_alert.severity, Severity.TICKET, "400.00 stuck is a ticket (page threshold 1000.00)")
        m.transition("p3", "proc_b", usd(70_000), PaymentState.CAPTURE_PENDING)
        clock.advance(timedelta(minutes=16))
        paged = get(m.evaluate_alerts(), "stuck-payments")
        self.assertIsNotNone(paged)
        self.assertIs(paged.severity, Severity.PAGE, "1100.00 stuck pages")
        self.assertEqual(m.stuck()[0].payment_id, "p1", "oldest stuck listed first")
        m.transition("p1", "proc_a", usd(40_000), PaymentState.CAPTURED)
        self.assertEqual(m.age_of("p1"), timedelta(0), "a transition resets the age")
        self.assertEqual(len(m.stuck()), 1)
        m.transition("p1", "proc_a", usd(40_000), PaymentState.SETTLED)
        self.assertIsNone(m.age_of("p1"), "terminal state stops tracking")

    def test_exposure_and_histogram(self):
        m, clock = self.m, self.clock
        clock.advance(timedelta(minutes=16))
        m.transition("p3", "proc_b", usd(70_000), PaymentState.CAPTURE_PENDING)
        clock.advance(timedelta(minutes=16))
        m.transition("p1", "proc_a", usd(40_000), PaymentState.SETTLED)
        exposure = m.in_flight_exposure()
        self.assertEqual(exposure[PaymentState.AUTHORIZED], usd(5_000))
        self.assertEqual(exposure[PaymentState.CAPTURE_PENDING], usd(70_000))
        self.assertEqual(m.total_in_flight(), usd(75_000))
        self.assertNotIn(PaymentState.SETTLED, exposure)
        self.assertNotIn(PaymentState.FAILED, exposure)
        with self.assertRaises(CurrencyMismatchError):
            m.transition("e", "proc_a", Money.of_minor(1, EUR), PaymentState.CREATED)
        hist = m.state_age_histogram()[PaymentState.CAPTURE_PENDING]
        self.assertEqual(hist[3], 1, "a 16m-old payment lands in the 15-60m band")


class AuthRateTest(unittest.TestCase):
    def test_windowed_rates_and_alerts(self):
        c2 = ManualClock(instant("2026-03-31T12:00:00Z"))
        a = MoneyMovementMonitor(c2, USD)
        for minute in range(60):
            for i in range(10):
                a.auth_result("proc_a", AuthOutcome.ISSUER_DECLINE if i == 0 else AuthOutcome.APPROVED)
                a.auth_result("proc_b", AuthOutcome.PROCESSOR_ERROR if minute >= 50 and i < 5
                              else AuthOutcome.ISSUER_DECLINE if i == 0 else AuthOutcome.APPROVED)
            if minute < 59:
                c2.advance(timedelta(minutes=1))
        self.assertEqual(a.auth_rate_bps("proc_a", timedelta(hours=1)), 9000)
        self.assertEqual(a.auth_rate_bps("proc_b", timedelta(minutes=15)), 6333, "(45 + 50) / 150")
        self.assertEqual(a.attempts("proc_a", timedelta(minutes=5)), 50)
        self.assertEqual(a.auth_rate_bps("proc_a", timedelta(minutes=5)), -1, "below the 100 minimum: no rate")
        self.assertEqual(a.burn_rate_x100(timedelta(minutes=5)), 25_000, "issuer declines do not burn the SLO")
        alerts = a.evaluate_alerts()
        self.assertTrue(has(alerts, "auth-rate-drop:proc_b"))
        self.assertFalse(has(alerts, "auth-rate-drop:proc_a"))
        self.assertTrue(has(alerts, "slo-burn-60m"), "50 errors / 1200 = burn 41.66 and 5m burn 250")
        self.assertFalse(has(alerts, "slo-burn-360m"), "6h rule not armed with 1h of history")

    def test_multiwindow_spike_that_ended_does_not_page(self):
        c3 = ManualClock(instant("2026-03-31T12:00:00Z"))
        s = MoneyMovementMonitor(c3, USD)
        for minute in range(60):
            for i in range(40):
                s.auth_result("proc_a", AuthOutcome.PROCESSOR_ERROR if 40 <= minute < 50 and i < 4 else AuthOutcome.APPROVED)
            if minute < 59:
                c3.advance(timedelta(minutes=1))
        self.assertGreaterEqual(s.burn_rate_x100(timedelta(hours=1)), 1440, "long window still hot")
        self.assertEqual(s.burn_rate_x100(timedelta(minutes=5)), 0, "short window has recovered")
        self.assertFalse(has(s.evaluate_alerts(), "slo-burn-60m"))


class ReconBreakRateTest(unittest.TestCase):
    def test_break_rate(self):
        r = MoneyMovementMonitor(ManualClock(instant("2026-03-31T12:00:00Z")), USD)
        r.recon_run(9_990, 10)
        self.assertEqual(r.recon_break_rate_bps(4), 10)
        self.assertFalse(has(r.evaluate_alerts(), "recon-break-rate"))
        r.recon_run(9_900, 100)
        self.assertEqual(r.recon_break_rate_bps(4), 55)
        self.assertTrue(has(r.evaluate_alerts(), "recon-break-rate"))


class SimulationTest(unittest.TestCase):
    def test_alerts_fire_after_the_faults(self):
        sim = Simulation(42)
        sim.run(Simulation.MINUTES, {430})
        stuck_at = sim.first_fired.get("stuck-payments")
        drop_at = sim.first_fired.get("auth-rate-drop:proc_b")
        fast_at = sim.first_fired.get("slo-burn-60m")
        slow_at = sim.first_fired.get("slo-burn-360m")
        self.assertEqual(stuck_at, Simulation.LOST_WEBHOOK_MINUTE + 16)
        self.assertIsNotNone(drop_at)
        self.assertTrue(Simulation.OUTAGE_MINUTE < drop_at <= Simulation.OUTAGE_MINUTE + 10)
        self.assertIsNotNone(fast_at)
        self.assertTrue(Simulation.OUTAGE_MINUTE < fast_at <= Simulation.OUTAGE_MINUTE + 10)
        self.assertIsNotNone(slow_at)
        self.assertGreater(slow_at, fast_at, "slow burn fires after fast burn")
        self.assertTrue(all(x >= Simulation.LOST_WEBHOOK_MINUTE for x in sim.first_fired.values()), "quiet morning")
        self.assertIn("ALERT PAGE slo-burn-60m", sim.snapshots[430])
        again = Simulation(42)
        again.run(Simulation.MINUTES, {430})
        self.assertEqual(again.snapshots[430], sim.snapshots[430], "same seed, same dashboard")

    def test_java_random_sequence(self):
        """Values taken from `new java.util.Random(2026)` on the JDK; they pin the LCG that drives the simulation."""
        r = JavaRandom(2026)
        self.assertEqual([r.next_int(66), r.next_int(100)], [29, 50])


class ClockTest(unittest.TestCase):
    def test_clock_does_not_go_backwards(self):
        with self.assertRaises(ValueError):
            ManualClock(instant("2026-03-31T12:00:00Z")).advance(timedelta(minutes=-1))


if __name__ == "__main__":
    unittest.main()
