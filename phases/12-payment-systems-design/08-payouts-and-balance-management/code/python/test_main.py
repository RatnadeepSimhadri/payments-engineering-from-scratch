"""Tests for phases/12-payment-systems-design/08-payouts-and-balance-management. Run: python3 -m unittest -v"""

import unittest
from datetime import date

from main import (USD, BalanceService, DayOfWeek, FakeClock, InMemoryPayoutFileSink, Ledger, Merchant, Money,
                  PayoutDirection, PayoutFrequency, PayoutSchedule, PayoutState, ReservePolicy, SettlementReport)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


class Fixture:
    """Monday 2026-03-02; merchant "m" pays out daily with a 10.00 minimum and a 10% / 90-day reserve."""

    def __init__(self, reserve_bps: int) -> None:
        self.clock = FakeClock(date(2026, 3, 2))
        self.files = InMemoryPayoutFileSink()
        self.svc = BalanceService(Ledger(USD), self.clock, self.files, 7, USD)
        self.svc.onboard(Merchant("m", "ba_tok_1", PayoutSchedule(PayoutFrequency.DAILY, None, usd(1_000)), ReservePolicy(reserve_bps, 90)))


def consistent(svc: BalanceService, m: str) -> bool:
    return svc.ledger().balances() and svc.scheduled_reserve(m) == svc.balance(m).reserve


class SettlementAndPayoutTest(unittest.TestCase):
    def test_pending_settlement_payout_file(self) -> None:
        f = Fixture(1_000)
        svc = f.svc
        c1 = svc.capture("c1", "m", usd(10_000), 290, 30)
        self.assertEqual(c1.net(), usd(9_680))
        self.assertEqual(svc.balance("m").pending, usd(9_680))
        self.assertTrue(svc.balance("m").available.is_zero())
        self.assertEqual(svc.run_payouts(), [], "no payout from pending funds")
        self.assertEqual(f.files.files, {})
        f.clock.advance_days(10)
        self.assertEqual(svc.run_payouts(), [], "time alone does not settle anything")
        self.assertFalse(svc.is_settled("c1"))

        # settlement reconciles before it moves money
        with self.assertRaises(RuntimeError):
            svc.settle(SettlementReport("bad", f.clock.today(), ("c1",), usd(9_999)))
        with self.assertRaises(RuntimeError):
            svc.settle(SettlementReport("bad2", f.clock.today(), ("c1", "nope"), usd(10_000)))
        self.assertEqual(svc.balance("m").pending, usd(9_680), "refused reports changed nothing")
        self.assertTrue(svc.ledger().balance(BalanceService.BANK).is_zero())
        svc.settle(SettlementReport("s1", f.clock.today(), ("c1",), usd(10_000)))
        self.assertEqual(svc.balance("m").reserve, usd(968), "reserve = floor(9680 x 10%)")
        self.assertEqual(svc.balance("m").available, usd(8_712))
        self.assertTrue(svc.balance("m").pending.is_zero())
        self.assertEqual(svc.ledger().balance(BalanceService.BANK), usd(10_000))
        with self.assertRaises(RuntimeError):
            svc.settle(SettlementReport("s1b", f.clock.today(), ("c1",), usd(10_000)))
        self.assertTrue(consistent(svc, "m"))

        # payout and its file (2026-03-12, a Thursday)
        batch = svc.run_payouts()
        self.assertEqual(len(batch), 1)
        self.assertEqual(batch[0].amount, usd(8_712))
        self.assertTrue(svc.balance("m").available.is_zero())
        self.assertEqual(svc.balance("m").in_transit, usd(8_712))
        file = next(iter(f.files.files.values()))
        self.assertEqual(len(file), 3)
        self.assertTrue(file[0].startswith("H,"))
        self.assertTrue(file[2].startswith("T,"))
        self.assertEqual(file[2], "T,1,8712,0")
        self.assertEqual(file[1], f"D,{batch[0].id},m,ba_tok_1,CREDIT,8712")
        self.assertEqual(svc.balance("m").total(), usd(9_680), "total owed unchanged by initiating a payout")
        svc.confirm(batch[0].id)
        self.assertTrue(svc.balance("m").in_transit.is_zero())
        self.assertEqual(svc.ledger().balance(BalanceService.BANK), usd(1_288))
        self.assertIs(batch[0].state, PayoutState.PAID)
        svc.confirm(batch[0].id)
        self.assertEqual(svc.ledger().balance(BalanceService.BANK), usd(1_288), "duplicate confirmation is a no-op")
        self.assertEqual(svc.run_payouts(), [], "a second run the same day pays nothing")

    def test_minimums_and_schedules(self) -> None:
        g = Fixture(0)
        g.svc.capture("g1", "m", usd(1_000), 290, 30)  # net 9.41 < 10.00 minimum
        g.svc.settle(SettlementReport("gs", g.clock.today(), ("g1",), usd(1_000)))
        self.assertEqual(g.svc.run_payouts(), [], "below minimum: no payout")
        self.assertEqual(g.svc.balance("m").available, usd(941))
        g.svc.capture("g2", "m", usd(100), 0, 0)
        g.svc.settle(SettlementReport("gs2", g.clock.today(), ("g2",), usd(100)))
        self.assertEqual(len(g.svc.run_payouts()), 1, "at or above minimum: paid")
        weekly = PayoutSchedule(PayoutFrequency.WEEKLY, DayOfWeek.FRIDAY, usd(0))
        self.assertTrue(weekly.due_on(date(2026, 3, 6)))
        self.assertFalse(weekly.due_on(date(2026, 3, 5)))
        daily = PayoutSchedule(PayoutFrequency.DAILY, None, usd(0))
        self.assertTrue(daily.due_on(date(2026, 3, 2)))
        self.assertFalse(daily.due_on(date(2026, 3, 7)))
        self.assertFalse(daily.due_on(date(2026, 3, 8)))


class ReserveTest(unittest.TestCase):
    def test_release(self) -> None:
        r = Fixture(1_000)
        r.svc.capture("r1", "m", usd(10_000), 290, 30)
        r.svc.settle(SettlementReport("rs", r.clock.today(), ("r1",), usd(10_000)))
        r.clock.advance_days(89)
        self.assertTrue(r.svc.release_reserves().is_zero(), "not released on day 89")
        self.assertEqual(r.svc.balance("m").reserve, usd(968))
        r.clock.advance_days(1)
        self.assertEqual(r.svc.release_reserves(), usd(968), "released on day 90")
        self.assertTrue(r.svc.balance("m").reserve.is_zero())
        self.assertEqual(r.svc.balance("m").available, usd(9_680))
        self.assertTrue(r.svc.release_reserves().is_zero(), "release is not repeated")
        self.assertTrue(consistent(r.svc, "m"))


class NegativeBalanceTest(unittest.TestCase):
    def test_negative_balance_path(self) -> None:
        n = Fixture(1_000)
        ns = n.svc
        ns.capture("n1", "m", usd(10_000), 290, 30)
        ns.settle(SettlementReport("ns1", n.clock.today(), ("n1",), usd(10_000)))
        ns.run_payouts()
        ns.debit_merchant("m", usd(20_000), "chargeback")
        self.assertEqual(ns.balance("m").available, usd(-20_000))
        self.assertEqual(ns.negative_since("m"), n.clock.today())
        used = ns.cover_from_reserve("m")
        self.assertEqual(used, usd(968), "reserve covers what it can")
        self.assertTrue(ns.balance("m").reserve.is_zero())
        self.assertEqual(ns.balance("m").available, usd(-19_032))
        self.assertTrue(consistent(ns, "m"))
        self.assertEqual(ns.run_payouts(), [], "no credit payout while negative")
        ns.capture("n2", "m", usd(5_000), 0, 0)
        ns.settle(SettlementReport("ns2", n.clock.today(), ("n2",), usd(5_000)))
        ns.run_payouts()
        self.assertEqual(ns.balance("m").available, usd(-14_032), "settlement nets against the deficit")
        n.clock.advance_days(6)
        self.assertEqual(ns.run_payouts(), [], "day 6 negative: no collection yet")
        n.clock.advance_days(1)
        coll = ns.run_payouts()
        self.assertEqual(len(coll), 1)
        self.assertIs(coll[0].direction, PayoutDirection.DEBIT)
        self.assertEqual(coll[0].amount, usd(14_032))
        self.assertTrue(ns.balance("m").available.is_zero())
        self.assertIsNone(ns.negative_since("m"))
        coll_file = list(n.files.files.values())[-1]
        self.assertEqual(coll_file[-1], "T,1,0,14032")
        ns.returned(coll[0].id, "R01")
        self.assertEqual(ns.balance("m").available, usd(-14_032), "returned collection restores the deficit")
        self.assertIsNotNone(ns.negative_since("m"))
        self.assertIs(coll[0].state, PayoutState.RETURNED)
        with self.assertRaises(RuntimeError):
            ns.confirm(coll[0].id)
        self.assertTrue(ns.ledger().balances())

    def test_returned_credit_pauses_payouts(self) -> None:
        p = Fixture(0)
        p.svc.capture("p1", "m", usd(5_000), 0, 0)
        p.svc.settle(SettlementReport("ps", p.clock.today(), ("p1",), usd(5_000)))
        po = p.svc.run_payouts()[0]
        p.svc.returned(po.id, "R03")
        self.assertEqual(p.svc.balance("m").available, usd(5_000))
        self.assertTrue(p.svc.balance("m").in_transit.is_zero())
        self.assertTrue(p.svc.is_paused("m"))
        p.clock.advance_days(1)
        self.assertEqual(p.svc.run_payouts(), [], "paused merchant is not paid")
        p.svc.resume("m", "fixed")
        self.assertEqual(len(p.svc.run_payouts()), 1, "resumed merchant is paid")


class ConservationAndValidationTest(unittest.TestCase):
    def test_conservation(self) -> None:
        k = Fixture(1_000)
        ks = k.svc
        gross = 0
        for i in range(20):
            ks.capture(f"k{i}", "m", usd(1_000 + 37 * i), 290, 30)
            gross += 1_000 + 37 * i
        ks.settle(SettlementReport("ks", k.clock.today(), tuple(f"k{i}" for i in range(20)), usd(gross)))
        kp = ks.run_payouts()[0]
        ks.confirm(kp.id)
        owed = ks.balance("m").total()
        fees = ks.ledger().balance(BalanceService.FEES)
        self.assertEqual(usd(gross), kp.amount.plus(owed).plus(fees), "gross = paid out + still owed + fees")
        self.assertEqual(ks.ledger().balance(BalanceService.BANK), usd(gross).minus(kp.amount), "bank = settled - paid")
        with self.assertRaises(ValueError):
            ks.balance("nobody")
        with self.assertRaises(ValueError):
            ReservePolicy(10_001, 1)
        with self.assertRaises(ValueError):
            ks.debit_merchant("m", usd(0), "x")


if __name__ == "__main__":
    unittest.main()
