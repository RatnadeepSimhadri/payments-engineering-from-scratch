"""Tests for phases/10-money-and-ledgers/07-holds-and-available-balance. Run: python3 -m unittest -v"""
import random
import unittest

from main import (
    DAY,
    EUR,
    HOUR,
    USD,
    Account,
    CurrencyMismatchError,
    DuplicateHoldError,
    HoldError,
    HoldNotActiveError,
    HoldStatus,
    InsufficientAvailableError,
    ManualClock,
    Money,
    OverCaptureError,
    UnknownHoldError,
    race_authorizations,
)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def funded(clock: ManualClock) -> Account:
    """An account with 500.00 USD settled and nothing held."""
    a = Account("acct", USD, clock)
    a.credit(usd("500.00"))
    return a


class HoldsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = ManualClock(1_000_000)
        self.a = funded(self.clock)

    def test_credit_raises_ledger_and_available_together(self) -> None:
        self.assertEqual(self.a.ledger_balance(), usd("500.00"))
        self.assertEqual(self.a.available_balance(), self.a.ledger_balance())
        self.assertTrue(self.a.check_invariant())

    def test_authorize_reduces_available_but_not_ledger(self) -> None:
        h = self.a.authorize("hotel", usd("300.00"), 7 * DAY)
        self.assertEqual(self.a.ledger_balance(), usd("500.00"))
        self.assertEqual(self.a.available_balance(), usd("200.00"))
        self.assertTrue(h.is_active())
        self.assertIs(h.status, HoldStatus.ACTIVE)
        self.assertTrue(self.a.check_invariant())

    def test_holds_stack_and_available_never_goes_negative(self) -> None:
        self.a.authorize("hotel", usd("300.00"), 7 * DAY)
        self.a.authorize("fuel", usd("125.00"), 2 * HOUR)
        self.assertEqual(self.a.held_total(), usd("425.00"))
        self.assertEqual(self.a.available_balance(), usd("75.00"))
        self.assertEqual(len(self.a.active_holds()), 2)
        with self.assertRaises(InsufficientAvailableError):
            self.a.authorize("too-much", usd("75.01"), DAY)
        self.a.authorize("exact", usd("75.00"), DAY)
        self.assertTrue(self.a.available_balance().is_zero())
        self.assertTrue(self.a.check_invariant())

    def test_void_returns_the_whole_amount(self) -> None:
        self.a.authorize("exact", usd("75.00"), DAY)
        released = self.a.void_hold("exact")
        self.assertEqual(released, usd("75.00"))
        self.assertEqual(self.a.available_balance(), usd("500.00"))
        self.assertIs(self.a.hold("exact").status, HoldStatus.VOIDED)
        with self.assertRaises(HoldNotActiveError):
            self.a.void_hold("exact")

    def test_available_not_ledger_gates_a_settled_debit(self) -> None:
        self.a.authorize("hotel", usd("300.00"), 7 * DAY)
        self.a.authorize("fuel", usd("125.00"), 2 * HOUR)
        with self.assertRaises(InsufficientAvailableError):
            self.a.debit(usd("80.00"))  # ledger 500 covers it; available 75 does not
        self.a.debit(usd("5.00"))
        self.assertEqual(self.a.ledger_balance(), usd("495.00"))
        self.assertEqual(self.a.available_balance(), usd("70.00"))

    def test_partial_capture_releases_the_remainder(self) -> None:
        self.a.authorize("fuel", usd("125.00"), 2 * HOUR)
        released = self.a.capture("fuel", usd("42.17"))
        self.assertEqual(released, usd("82.83"))
        self.assertEqual(self.a.ledger_balance(), usd("457.83"))
        self.assertEqual(self.a.available_balance(), usd("457.83"))
        self.assertIs(self.a.hold("fuel").status, HoldStatus.CAPTURED)
        self.assertEqual(self.a.hold("fuel").captured, usd("42.17"))
        self.assertTrue(self.a.check_invariant())
        with self.assertRaises(HoldNotActiveError):
            self.a.capture("fuel", usd("1.00"))

    def test_over_capture_is_rejected_and_changes_nothing(self) -> None:
        self.a.authorize("hotel", usd("300.00"), 7 * DAY)
        with self.assertRaises(OverCaptureError):
            self.a.capture("hotel", usd("300.01"))
        self.assertEqual(self.a.ledger_balance(), usd("500.00"))
        self.assertTrue(self.a.hold("hotel").is_active())

    def test_expired_hold_is_released_by_the_clock(self) -> None:
        self.a.authorize("hotel", usd("300.00"), 7 * DAY)
        self.clock.advance(7 * DAY - 1)
        self.assertEqual(len(self.a.active_holds()), 1)
        self.assertEqual(self.a.available_balance(), usd("200.00"))
        self.clock.advance(1)
        self.assertEqual(self.a.active_holds(), [])
        self.assertEqual(self.a.available_balance(), usd("500.00"))
        self.assertIs(self.a.hold("hotel").status, HoldStatus.EXPIRED)
        with self.assertRaises(HoldNotActiveError):
            self.a.capture("hotel", usd("300.00"))
        self.assertTrue(self.a.check_invariant())

    def test_incremental_authorization_grows_the_hold(self) -> None:
        b = funded(ManualClock(0))
        b.authorize("h", usd("310.00"), DAY)
        b.increment("h", usd("45.00"))
        self.assertEqual(b.hold("h").amount, usd("355.00"))
        self.assertEqual(b.available_balance(), usd("145.00"))
        with self.assertRaises(InsufficientAvailableError):
            b.increment("h", usd("145.01"))
        self.assertTrue(b.capture("h", usd("355.00")).is_zero())
        self.assertEqual(b.ledger_balance(), usd("145.00"))
        self.assertTrue(b.check_invariant())

    def test_failures_the_lesson_warns_about(self) -> None:
        self.a.authorize("h", usd("1.00"), DAY)
        with self.assertRaises(DuplicateHoldError):
            self.a.authorize("h", usd("1.00"), DAY)
        with self.assertRaises(UnknownHoldError):
            self.a.capture("nope", usd("1.00"))
        with self.assertRaises(CurrencyMismatchError):
            self.a.authorize("eur", Money.parse("1.00", EUR), DAY)
        with self.assertRaises(ValueError):
            self.a.authorize("zero", usd("0.00"), DAY)
        with self.assertRaises(ValueError):
            ManualClock(5).advance(-1)

    def test_seeded_random_operations_keep_the_invariant(self) -> None:
        rnd = random.Random(7)
        clock = ManualClock(0)
        r = Account("r", USD, clock)
        r.credit(usd("1000.00"))
        n = 0
        for _ in range(2000):
            amt = Money.of_minor(1 + rnd.randrange(30_000), USD)
            op = rnd.randrange(5)
            try:
                if op == 0:
                    r.authorize(f"x{n}", amt, 1 + rnd.randrange(3600))
                    n += 1
                elif op == 1 and n > 0:
                    r.capture(f"x{rnd.randrange(n)}", amt)
                elif op == 2 and n > 0:
                    r.void_hold(f"x{rnd.randrange(n)}")
                elif op == 3:
                    clock.advance(rnd.randrange(1800))
                elif op == 4:
                    r.debit(amt)
            except HoldError:
                pass  # rejected operations must leave the invariant intact
            self.assertTrue(r.check_invariant())
            self.assertFalse(r.available_balance().is_negative())

    def test_concurrent_holds_against_one_account(self) -> None:
        shared = Account("shared", USD, ManualClock(0))
        shared.credit(usd("500.00"))
        accepted, rejected = race_authorizations(shared, 50, usd("20.00"))
        self.assertEqual((accepted, rejected), (25, 25))
        self.assertTrue(shared.available_balance().is_zero())
        self.assertEqual(len(shared.active_holds()), 25)
        self.assertTrue(shared.check_invariant())

    def test_eight_threads_racing_for_the_whole_balance_one_wins(self) -> None:
        solo = Account("solo", USD, ManualClock(0))
        solo.credit(usd("100.00"))
        self.assertEqual(race_authorizations(solo, 8, usd("100.00")), (1, 7))


if __name__ == "__main__":
    unittest.main()
