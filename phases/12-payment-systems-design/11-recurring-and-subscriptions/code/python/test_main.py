"""Tests for phases/12-payment-systems-design/11-recurring-and-subscriptions. Run: python3 -m unittest -v"""

import unittest
from datetime import date, timedelta

from main import (BASIC, EUR, PRO, USD, BillingCycle, BillingEngine, ChargeOutcome, CurrencyMismatchError, DunningPolicy,
                  FakeClock, IllegalTransitionError, Interval, InvoiceStatus, MapCardUpdater, Money, Plan, Proration,
                  ScriptedGateway, SubscriptionStatus, SubscriptionTransitions, add_months)

JAN31 = date(2026, 1, 31)


class Rig:
    def __init__(self, start: date, policy: DunningPolicy) -> None:
        self.clock = FakeClock(start)
        self.gateway = ScriptedGateway()
        self.updater = MapCardUpdater()
        self.engine = BillingEngine(self.clock, self.gateway, self.updater, policy)

    def run_days(self, days: int) -> None:
        for _ in range(days):
            self.engine.run_day()
            self.clock.advance_days(1)


class BillingCycleTest(unittest.TestCase):
    def test_periods_are_computed_from_the_anchor(self) -> None:
        self.assertEqual(BillingCycle.period_start(JAN31, Interval.MONTHLY, 1), date(2026, 2, 28), "clamps to Feb 28")
        self.assertEqual(BillingCycle.period_start(JAN31, Interval.MONTHLY, 2), date(2026, 3, 31), "returns to Mar 31")
        self.assertEqual(BillingCycle.period_start(date(2028, 1, 31), Interval.MONTHLY, 1), date(2028, 2, 29), "leap year")
        self.assertEqual(BillingCycle.chained_starts(JAN31, Interval.MONTHLY, 3)[2], date(2026, 3, 28), "chained dates drift")
        self.assertEqual(BillingCycle.period_start(date(2028, 2, 29), Interval.YEARLY, 1), date(2029, 2, 28))
        self.assertEqual(BillingCycle.period_start(date(2028, 2, 29), Interval.YEARLY, 4), date(2032, 2, 29))
        self.assertEqual(BillingCycle.period_start(date(2025, 11, 30), Interval.QUARTERLY, 1), date(2026, 2, 28))
        self.assertEqual(BillingCycle.period_end(JAN31, Interval.MONTHLY, 0), date(2026, 2, 28))
        with self.assertRaises(ValueError):
            BillingCycle.period_start(JAN31, Interval.MONTHLY, -1)

    def test_add_months_matches_java_plus_months(self) -> None:
        self.assertEqual(add_months(date(2026, 1, 31), 1), date(2026, 2, 28))
        self.assertEqual(add_months(date(2026, 3, 31), -1), date(2026, 2, 28))
        self.assertEqual(add_months(date(2025, 12, 15), 13), date(2027, 1, 15))


class ProrationTest(unittest.TestCase):
    def test_worked_example_to_the_cent(self) -> None:
        p = Proration.compute(BASIC, PRO, date(2026, 3, 1), date(2026, 4, 1), date(2026, 3, 11))
        self.assertEqual(p.used_days, 10)
        self.assertEqual((p.remaining_days, p.period_days), (21, 31))
        self.assertEqual(p.credit, Money.parse("20.32", USD))
        self.assertEqual(p.charge, Money.parse("33.87", USD))
        self.assertEqual(p.net(), Money.parse("13.55", USD))
        down = Proration.compute(PRO, BASIC, date(2026, 3, 1), date(2026, 4, 1), date(2026, 3, 11))
        self.assertEqual(down.net(), Money.parse("-13.55", USD))
        first_day = Proration.compute(BASIC, PRO, date(2026, 3, 1), date(2026, 4, 1), date(2026, 3, 1))
        self.assertEqual(first_day.credit, BASIC.price, "change on the first day credits the whole old price")

    def test_allocation_conserves_the_price(self) -> None:
        odd = Plan("odd", Money.parse("9.99", USD), Interval.MONTHLY)
        conserved = True
        for start in (date(2026, 2, 1), date(2026, 4, 1), date(2026, 1, 1), date(2028, 2, 1)):
            end = add_months(start, 1)
            day = start
            while day < end:
                r = Proration.compute(odd, PRO, start, end, day)
                split = odd.price.allocate(r.used_days, r.remaining_days)
                conserved = conserved and split[0].plus(split[1]) == odd.price and split[1] == r.credit
                day += timedelta(days=1)
        self.assertTrue(conserved, "used portion + credit == price for every change day")
        halves = odd.price.allocate(15, 15)
        self.assertEqual((halves[0].minor_units, halves[1].minor_units), (500, 499))
        with self.assertRaises(ValueError):
            Proration.compute(BASIC, PRO, date(2026, 3, 1), date(2026, 4, 1), date(2026, 4, 1))
        with self.assertRaises(CurrencyMismatchError):
            Proration.compute(BASIC, Plan("eu", Money.parse("30.00", EUR), Interval.MONTHLY),
                              date(2026, 3, 1), date(2026, 4, 1), date(2026, 3, 11))


class StateMachineTest(unittest.TestCase):
    def test_transitions(self) -> None:
        self.assertTrue(SubscriptionTransitions.allowed(SubscriptionStatus.PAST_DUE, SubscriptionStatus.ACTIVE))
        self.assertFalse(SubscriptionTransitions.allowed(SubscriptionStatus.ACTIVE, SubscriptionStatus.UNPAID))
        self.assertEqual(SubscriptionTransitions.ALLOWED[SubscriptionStatus.CANCELED], frozenset())
        r = Rig(JAN31, DunningPolicy.standard())
        s = r.engine.subscribe("s", "c", BASIC, "tok", 0)
        r.engine.cancel("s", False)
        with self.assertRaises(IllegalTransitionError):
            s.transition(SubscriptionStatus.ACTIVE)


class DunningTest(unittest.TestCase):
    def test_schedule_then_exhaustion(self) -> None:
        r = Rig(JAN31, DunningPolicy.standard())
        r.gateway.script("tok_broke", *([ChargeOutcome.INSUFFICIENT_FUNDS] * 5))
        r.engine.subscribe("sub_trial", "cus", PRO, "tok_broke", 14)
        self.assertIs(r.engine.subscription("sub_trial").status, SubscriptionStatus.TRIALING)
        r.run_days(14)
        self.assertEqual(r.gateway.charges(), [], "no charge during the trial")
        r.run_days(1)  # Feb 14
        inv = r.engine.invoice("sub_trial:2026-02-14")
        assert inv is not None
        self.assertIs(r.engine.subscription("sub_trial").status, SubscriptionStatus.PAST_DUE)
        self.assertEqual(inv.next_attempt, date(2026, 2, 15), "first retry is +1 day")
        r.run_days(1)
        self.assertEqual(inv.next_attempt, date(2026, 2, 17), "second retry is +3 days")
        r.run_days(13)  # Feb 16 .. Feb 28: attempts on Feb 17, Feb 21, Feb 28
        self.assertEqual(inv.attempts, DunningPolicy.standard().max_attempts())
        self.assertIs(inv.status, InvoiceStatus.UNCOLLECTIBLE)
        self.assertIs(r.engine.subscription("sub_trial").status, SubscriptionStatus.CANCELED)
        r.run_days(60)
        self.assertEqual(len(r.gateway.charges()), 5, "a canceled subscription is never billed again")

    def test_recovery_idempotent_days_and_catch_up(self) -> None:
        r = Rig(JAN31, DunningPolicy.standard())
        r.gateway.script("tok_flaky", ChargeOutcome.APPROVED, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.APPROVED)
        r.engine.subscribe("s", "c", BASIC, "tok_flaky", 0)
        r.engine.run_day()
        r.engine.run_day()
        self.assertEqual(len(r.gateway.charges()), 1, "same-day rerun does not charge twice")
        r.clock.set(date(2026, 2, 28))
        r.engine.run_day()
        self.assertIs(r.engine.subscription("s").status, SubscriptionStatus.PAST_DUE)
        r.clock.set(date(2026, 3, 1))
        r.engine.run_day()
        self.assertIs(r.engine.subscription("s").status, SubscriptionStatus.ACTIVE)
        self.assertTrue(r.gateway.charges()[2].startswith("s:2026-02-28#2 "), "retry used a new reference")
        r.clock.set(date(2026, 6, 15))
        r.engine.run_day()
        self.assertIsNotNone(r.engine.invoice("s:2026-03-31"))
        self.assertIsNotNone(r.engine.invoice("s:2026-04-30"))
        self.assertIsNotNone(r.engine.invoice("s:2026-05-31"))
        self.assertEqual(len(r.engine.invoices()), 5, "catch-up bills each missed period exactly once")
        self.assertEqual(r.engine.subscription("s").current_period_end, date(2026, 6, 30))

    def test_hard_declines_and_card_updater(self) -> None:
        r = Rig(JAN31, DunningPolicy.standard())
        r.gateway.script("tok_stolen", ChargeOutcome.DO_NOT_RETRY)
        r.gateway.script("tok_expired", ChargeOutcome.EXPIRED_CARD)
        r.gateway.script("tok_old", ChargeOutcome.EXPIRED_CARD)
        r.updater.replace("tok_old", "tok_new")
        r.engine.subscribe("stolen", "c1", BASIC, "tok_stolen", 0)
        r.engine.subscribe("expired", "c2", BASIC, "tok_expired", 0)
        r.engine.subscribe("updated", "c3", BASIC, "tok_old", 0)
        r.engine.run_day()
        stolen = r.engine.invoice("stolen:2026-01-31")
        expired = r.engine.invoice("expired:2026-01-31")
        updated = r.engine.invoice("updated:2026-01-31")
        assert stolen is not None and expired is not None and updated is not None
        self.assertEqual(stolen.attempts, 1, "DO_NOT_RETRY exhausts after one attempt")
        self.assertIs(r.engine.subscription("stolen").status, SubscriptionStatus.CANCELED)
        self.assertIs(expired.status, InvoiceStatus.UNCOLLECTIBLE, "expired card with no update is not retried")
        self.assertEqual(r.engine.subscription("updated").card_token, "tok_new")
        self.assertIs(updated.status, InvoiceStatus.PAID, "updated card charged the same day")
        self.assertEqual(r.updater.calls(), 2)

    def test_unpaid_policy_stops_billing(self) -> None:
        r = Rig(JAN31, DunningPolicy((2,), SubscriptionStatus.UNPAID))
        r.gateway.script("tok_x", ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS)
        r.engine.subscribe("u", "c", BASIC, "tok_x", 0)
        r.run_days(40)
        self.assertIs(r.engine.subscription("u").status, SubscriptionStatus.UNPAID)
        self.assertEqual(len(r.engine.invoices()), 1, "UNPAID creates no further invoices")


class PlanChangeTest(unittest.TestCase):
    def test_cancel_at_period_end_and_plan_changes(self) -> None:
        r = Rig(date(2026, 3, 1), DunningPolicy.standard())
        r.engine.subscribe("up", "c", BASIC, "tok", 0)
        r.engine.subscribe("dn", "c", PRO, "tok", 0)
        r.engine.subscribe("bye", "c", BASIC, "tok", 0)
        r.engine.run_day()
        r.clock.set(date(2026, 3, 11))
        r.engine.change_plan("up", PRO)
        pro_inv = r.engine.invoice("up:proration:2026-03-11")
        assert pro_inv is not None
        self.assertEqual(pro_inv.amount, Money.parse("13.55", USD), "upgrade invoices the net now")
        self.assertIs(pro_inv.status, InvoiceStatus.PAID)
        r.engine.change_plan("dn", BASIC)
        self.assertEqual(r.engine.subscription("dn").credit_balance, Money.parse("13.55", USD), "downgrade becomes credit")
        r.engine.cancel("bye", True)
        r.clock.set(date(2026, 4, 1))
        r.engine.run_day()
        dn_inv = r.engine.invoice("dn:2026-04-01")
        up_inv = r.engine.invoice("up:2026-04-01")
        assert dn_inv is not None and up_inv is not None
        self.assertEqual(dn_inv.amount, Money.parse("16.45", USD), "credit reduces the next invoice")
        self.assertEqual(up_inv.amount, PRO.price, "upgraded plan bills the new price at renewal")
        self.assertIs(r.engine.subscription("bye").status, SubscriptionStatus.CANCELED)
        self.assertIsNone(r.engine.invoice("bye:2026-04-01"))


class ValidationTest(unittest.TestCase):
    def test_policy_and_money_discipline(self) -> None:
        with self.assertRaises(ValueError):
            DunningPolicy((3, 3), SubscriptionStatus.CANCELED)
        with self.assertRaises(ValueError):
            DunningPolicy((1,), SubscriptionStatus.ACTIVE)
        with self.assertRaises(CurrencyMismatchError):
            BASIC.price.plus(Money.parse("1.00", EUR))


if __name__ == "__main__":
    unittest.main()
