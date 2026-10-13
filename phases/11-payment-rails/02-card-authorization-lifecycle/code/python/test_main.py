"""Tests for phases/11-payment-rails/02-card-authorization-lifecycle. Run: python3 -m unittest -v"""

import unittest
from datetime import datetime, timedelta, timezone

from main import (
    EUR,
    LEGAL,
    STANDARD,
    USD,
    Action,
    CardTransaction,
    CurrencyMismatchError,
    FakeClock,
    IllegalTransitionError,
    LifecyclePolicy,
    LimitExceededError,
    Money,
    TransactionState,
)

T0 = datetime(2026, 3, 1, 9, 0, tzinfo=timezone.utc)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def auth(amount: str, clock: FakeClock | None = None, policy: LifecyclePolicy = STANDARD) -> CardTransaction:
    return CardTransaction.authorize("t", usd(amount), clock or FakeClock(T0), policy, "test authorization")


class CreationTest(unittest.TestCase):
    def test_authorize_creates_authorized_with_one_event(self):
        t = auth("100.00")
        self.assertIs(t.state, TransactionState.AUTHORIZED)
        self.assertEqual(len(t.events), 1)
        first = t.events[0]
        self.assertIs(first.action, Action.AUTHORIZE)
        self.assertIsNone(first.from_state)
        self.assertEqual(first.reason, "test authorization")
        self.assertEqual(t.expires_at, T0 + timedelta(days=7))
        self.assertTrue(t.captured_amount.is_zero())
        self.assertEqual(t.remaining_authorization(), usd("100.00"))

    def test_authorize_rejects_zero_amount_and_blank_reason(self):
        with self.assertRaises(LimitExceededError):
            CardTransaction.authorize("z", usd("0.00"), FakeClock(T0), STANDARD, "r")
        with self.assertRaises(ValueError):
            CardTransaction.authorize("z", usd("1.00"), FakeClock(T0), STANDARD, " ")


class LegalTransitionTest(unittest.TestCase):
    def test_full_capture(self):
        t = auth("100.00")
        t.capture(usd("100.00"), "ship")
        self.assertIs(t.state, TransactionState.CAPTURED)
        self.assertEqual(t.captured_amount, usd("100.00"))
        self.assertTrue(t.remaining_authorization().is_zero())

    def test_partial_then_rest(self):
        t = auth("100.00")
        t.capture(usd("40.00"), "first shipment")
        self.assertIs(t.state, TransactionState.PARTIALLY_CAPTURED)
        self.assertEqual(t.remaining_authorization(), usd("60.00"))
        t.capture(usd("60.00"), "second shipment")
        self.assertIs(t.state, TransactionState.CAPTURED)

    def test_partial_then_close_releases_remainder(self):
        t = auth("100.00")
        t.capture(usd("35.50"), "only item in stock")
        t.close_capture("rest cancelled")
        self.assertIs(t.state, TransactionState.CAPTURED)
        self.assertIs(t.events[2].action, Action.CLOSE_CAPTURE)
        self.assertEqual(t.events[2].amount, usd("64.50"))
        self.assertEqual(t.captured_amount, usd("35.50"))
        self.assertTrue(t.remaining_authorization().is_zero())

    def test_void(self):
        t = auth("25.00")
        t.void_authorization("customer cancelled")
        self.assertIs(t.state, TransactionState.VOIDED)
        self.assertTrue(t.state.is_terminal())


class RefundArithmeticTest(unittest.TestCase):
    def test_partial_capture_then_refunds(self):
        t = auth("100.00")
        t.capture(usd("60.00"), "ship")
        t.close_capture("done")
        t.refund(usd("25.00"), "one item returned")
        self.assertIs(t.state, TransactionState.PARTIALLY_REFUNDED)
        self.assertEqual(t.net_settled(), usd("35.00"))
        t.refund(usd("35.00"), "second item returned")
        self.assertIs(t.state, TransactionState.REFUNDED)
        self.assertTrue(t.net_settled().is_zero())
        with self.assertRaises(IllegalTransitionError):
            t.refund(usd("0.01"), "again")

    def test_refund_above_captured_rejected_and_leaves_state(self):
        t = auth("100.00")
        t.capture(usd("100.00"), "ship")
        with self.assertRaises(LimitExceededError):
            t.refund(usd("100.01"), "too much")
        self.assertIs(t.state, TransactionState.CAPTURED)
        self.assertTrue(t.refunded_amount.is_zero())
        with self.assertRaises(LimitExceededError):
            t.refund(usd("0.00"), "zero")


class CaptureCeilingTest(unittest.TestCase):
    def test_no_tolerance(self):
        t = auth("100.00")
        with self.assertRaises(LimitExceededError):
            t.capture(usd("100.01"), "tip")
        self.assertIs(t.state, TransactionState.AUTHORIZED)
        self.assertEqual(len(t.events), 1)

    def test_tolerance_ceiling_and_rounding(self):
        tips = LifecyclePolicy.for_mcc("5812")
        self.assertEqual(tips.capture_ceiling(usd("100.00")), usd("115.00"))
        self.assertEqual(tips.capture_ceiling(usd("0.33")), usd("0.37"))  # 37.95 cents rounds DOWN
        self.assertEqual(LifecyclePolicy.for_mcc("5999").over_capture_tolerance_bps, 0)

    def test_over_capture_within_and_above_tolerance(self):
        tips = LifecyclePolicy.for_mcc("5812")
        ok = auth("100.00", policy=tips)
        ok.capture(usd("115.00"), "bill plus tip")
        self.assertIs(ok.state, TransactionState.CAPTURED)
        self.assertEqual(ok.captured_amount, usd("115.00"))
        too_much = auth("100.00", policy=tips)
        with self.assertRaises(LimitExceededError):
            too_much.capture(usd("115.01"), "tip")
        cumulative = auth("100.00", policy=tips)
        cumulative.capture(usd("90.00"), "part")
        with self.assertRaises(LimitExceededError):
            cumulative.capture(usd("25.01"), "part two")


class IncrementalAuthorizationTest(unittest.TestCase):
    def test_increment_raises_amount_and_resets_expiry(self):
        clock = FakeClock(T0)
        t = auth("50.00", clock)
        clock.advance(timedelta(days=2))
        t.increment_authorization(usd("20.00"), "extended stay")
        self.assertEqual(t.authorized_amount, usd("70.00"))
        self.assertIs(t.state, TransactionState.AUTHORIZED)
        self.assertEqual(t.expires_at, T0 + timedelta(days=9))
        t.capture(usd("70.00"), "checkout")
        self.assertIs(t.state, TransactionState.CAPTURED)

    def test_increment_after_partial_capture_is_illegal(self):
        t = auth("10.00")
        t.capture(usd("4.00"), "part")
        with self.assertRaises(IllegalTransitionError):
            t.increment_authorization(usd("1.00"), "late")


class IllegalTransitionTest(unittest.TestCase):
    def test_each_forbidden_edge(self):
        full = auth("100.00")
        full.capture(usd("100.00"), "ship")
        voided = auth("25.00")
        voided.void_authorization("cancel")
        fresh = auth("10.00")
        partial = auth("10.00")
        partial.capture(usd("4.00"), "part")
        cases = [
            ("void after capture", lambda: full.void_authorization("late")),
            ("capture after void", lambda: voided.capture(usd("1.00"), "late")),
            ("refund from AUTHORIZED", lambda: fresh.refund(usd("1.00"), "early")),
            ("refund from PARTIALLY_CAPTURED", lambda: partial.refund(usd("1.00"), "early")),
            ("close capture from AUTHORIZED", lambda: fresh.close_capture("nothing")),
            ("capture after full capture", lambda: full.capture(usd("1.00"), "again")),
            ("void after void", lambda: voided.void_authorization("again")),
        ]
        for name, attempt in cases:
            with self.subTest(name):
                with self.assertRaises(IllegalTransitionError):
                    attempt()

    def test_exception_carries_from_state_and_action(self):
        full = auth("100.00")
        full.capture(usd("100.00"), "ship")
        with self.assertRaises(IllegalTransitionError) as ctx:
            full.void_authorization("late")
        self.assertIs(ctx.exception.from_state, TransactionState.CAPTURED)
        self.assertIs(ctx.exception.action, Action.VOID)

    def test_currency_mismatch(self):
        with self.assertRaises(CurrencyMismatchError):
            auth("10.00").capture(Money.parse("1.00", EUR), "wrong currency")

    def test_terminal_states_accept_nothing(self):
        for state in (TransactionState.VOIDED, TransactionState.EXPIRED, TransactionState.REFUNDED):
            self.assertEqual(LEGAL[state], frozenset())
            self.assertTrue(state.is_terminal())


class ExpiryTest(unittest.TestCase):
    def test_capture_at_deadline_expires_and_persists_event(self):
        clock = FakeClock(T0)
        t = auth("45.00", clock)
        clock.advance(timedelta(days=7) - timedelta(seconds=1))
        self.assertFalse(t.expire_if_due("probe"))
        self.assertIs(t.state, TransactionState.AUTHORIZED)
        clock.advance(timedelta(seconds=1))
        with self.assertRaises(IllegalTransitionError):
            t.capture(usd("45.00"), "late shipment")
        self.assertIs(t.state, TransactionState.EXPIRED)
        self.assertEqual(len(t.events), 2)
        expiry = t.events[1]
        self.assertIs(expiry.action, Action.EXPIRE)
        self.assertEqual(expiry.amount, usd("45.00"))
        self.assertIn("late shipment", expiry.reason)
        with self.assertRaises(IllegalTransitionError):
            t.void_authorization("late")
        self.assertFalse(t.expire_if_due("probe"))
        self.assertEqual(len(t.events), 2)

    def test_partially_captured_also_expires_with_remainder(self):
        clock = FakeClock(T0)
        t = auth("100.00", clock)
        t.capture(usd("30.00"), "first")
        clock.advance(timedelta(days=8))
        with self.assertRaises(IllegalTransitionError):
            t.capture(usd("70.00"), "second")
        self.assertEqual(t.events[2].amount, usd("70.00"))

    def test_configurable_validity(self):
        clock = FakeClock(T0)
        t = auth("10.00", clock, LifecyclePolicy(timedelta(hours=1), 0))
        clock.advance(timedelta(minutes=59))
        t.capture(usd("10.00"), "in time")
        self.assertIs(t.state, TransactionState.CAPTURED)


class EventLogTest(unittest.TestCase):
    def test_log_replays_to_totals(self):
        t = auth("120.00")
        t.increment_authorization(usd("30.00"), "minibar")
        t.capture(usd("100.00"), "folio")
        t.close_capture("checkout")
        t.refund(usd("20.00"), "goodwill")
        log = t.events
        self.assertEqual([e.sequence for e in log], [1, 2, 3, 4, 5])
        self.assertEqual([e.to_state for e in log], [
            TransactionState.AUTHORIZED, TransactionState.AUTHORIZED, TransactionState.PARTIALLY_CAPTURED,
            TransactionState.CAPTURED, TransactionState.PARTIALLY_REFUNDED])
        self.assertEqual(log[0].amount.plus(log[1].amount), t.authorized_amount)
        self.assertEqual(log[2].amount, t.captured_amount)
        self.assertEqual(log[4].amount, t.refunded_amount)
        self.assertTrue(all(e.reason.strip() for e in log))
        self.assertIsInstance(t.events, tuple)  # read-only view


class PolicyTest(unittest.TestCase):
    def test_policy_validation(self):
        with self.assertRaises(ValueError):
            LifecyclePolicy(timedelta(0), 0)
        with self.assertRaises(ValueError):
            LifecyclePolicy(timedelta(days=1), 10_001)


if __name__ == "__main__":
    unittest.main()
