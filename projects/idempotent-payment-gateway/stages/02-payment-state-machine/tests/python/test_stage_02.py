"""Stage 2 tests for projects/idempotent-payment-gateway (Python): the transition table, versioned history, terminal
states, and partial refunds in integer minor units. Port of Stage02Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    USD,
    CurrencyMismatchError,
    FakeClock,
    IllegalTransitionError,
    Money,
    Payment,
    PaymentState,
    PaymentStateMachine,
)


def s2_usd(text: str) -> Money:
    return Money.parse(text, USD)


class Stage02StateMachineTest(unittest.TestCase):
    def test_the_table_is_the_single_source_of_truth(self):
        self.assertTrue(PaymentStateMachine.can_transition(PaymentState.CREATED, PaymentState.PROCESSING), "created -> processing")
        self.assertTrue(PaymentStateMachine.can_transition(PaymentState.CREATED, PaymentState.CANCELED), "created -> canceled")
        self.assertFalse(PaymentStateMachine.can_transition(PaymentState.CREATED, PaymentState.SUCCEEDED), "created -/-> succeeded")
        self.assertTrue(len(PaymentStateMachine.next_states(PaymentState.PROCESSING)) == 2
                        and PaymentStateMachine.can_transition(PaymentState.PROCESSING, PaymentState.SUCCEEDED)
                        and PaymentStateMachine.can_transition(PaymentState.PROCESSING, PaymentState.FAILED), "processing -> succeeded|failed")
        self.assertFalse(PaymentStateMachine.can_transition(PaymentState.PROCESSING, PaymentState.CANCELED), "processing -/-> canceled")
        self.assertTrue(len(PaymentStateMachine.next_states(PaymentState.SUCCEEDED)) == 1
                        and PaymentStateMachine.can_transition(PaymentState.SUCCEEDED, PaymentState.REFUNDED), "succeeded -> refunded only")
        self.assertTrue(not PaymentStateMachine.next_states(PaymentState.FAILED)
                        and not PaymentStateMachine.next_states(PaymentState.CANCELED)
                        and not PaymentStateMachine.next_states(PaymentState.REFUNDED), "terminal states have no exits")
        self.assertTrue(PaymentState.FAILED.is_terminal() and PaymentState.CANCELED.is_terminal() and PaymentState.REFUNDED.is_terminal()
                        and not PaymentState.SUCCEEDED.is_terminal() and not PaymentState.CREATED.is_terminal() and not PaymentState.PROCESSING.is_terminal(), "is_terminal")
        self.assertTrue(PaymentState.SUCCEEDED.wire() == "succeeded" and PaymentState.CANCELED.wire() == "canceled", "wire names are lower-case")

    def test_happy_path_illegal_moves_and_refunds(self):
        clock = FakeClock(5_000)
        p = Payment("pay_1", s2_usd("50.00"), clock)
        self.assertTrue(p.state() is PaymentState.CREATED and p.version() == 1 and len(p.history()) == 0, "new payment is CREATED v1")
        self.assertTrue(p.captured().is_zero() and p.refunded().is_zero() and p.refundable().is_zero(), "nothing captured yet")
        p.start_processing()
        self.assertTrue(p.state() is PaymentState.PROCESSING and p.version() == 2, "processing v2")
        clock.advance(250)
        p.succeed("auth-77")
        self.assertTrue(p.state() is PaymentState.SUCCEEDED and p.version() == 3, "succeeded v3")
        self.assertTrue(p.captured() == s2_usd("50.00") and p.refundable() == s2_usd("50.00"), "captured equals amount")
        self.assertEqual(p.processor_reference(), "auth-77", "processor reference kept")
        self.assertEqual(len(p.history()), 2, "history has two entries")
        self.assertTrue(p.history()[0].from_state is PaymentState.CREATED and p.history()[0].to_state is PaymentState.PROCESSING
                        and p.history()[1].from_state is PaymentState.PROCESSING and p.history()[1].to_state is PaymentState.SUCCEEDED, "history records from/to")
        self.assertTrue(p.history()[0].at_millis == 5_000 and p.history()[1].at_millis == 5_250, "history timestamps come from the clock")
        with self.assertRaises((AttributeError, TypeError), msg="history is read-only"):
            p.history().append(None)  # type: ignore[attr-defined]

        # Illegal moves are rejected and leave the payment untouched
        with self.assertRaises(IllegalTransitionError, msg="succeed twice"):
            p.succeed("again")
        with self.assertRaises(IllegalTransitionError, msg="fail after success"):
            p.fail("late")
        with self.assertRaises(IllegalTransitionError, msg="cancel after success"):
            p.cancel("late")
        self.assertTrue(p.version() == 3 and p.state() is PaymentState.SUCCEEDED, "rejected moves do not bump the version")
        with self.assertRaises(RuntimeError, msg="IllegalTransitionError is a RuntimeError"):
            p.succeed("again")

        fresh = Payment("pay_2", s2_usd("10.00"), clock)
        with self.assertRaises(IllegalTransitionError, msg="succeed from CREATED"):
            fresh.succeed("x")
        with self.assertRaises(IllegalTransitionError, msg="fail from CREATED"):
            fresh.fail("x")
        fresh.cancel("customer left")
        self.assertTrue(fresh.state() is PaymentState.CANCELED and fresh.version() == 2, "canceled from CREATED")
        with self.assertRaises(IllegalTransitionError, msg="nothing leaves CANCELED"):
            fresh.start_processing()

        declined = Payment("pay_3", s2_usd("10.00"), clock)
        declined.start_processing()
        with self.assertRaises(IllegalTransitionError, msg="cancel while PROCESSING"):
            declined.cancel("too late")
        declined.fail("declined decl-3")
        self.assertTrue(declined.state() is PaymentState.FAILED and declined.captured().is_zero(), "failed payment captured nothing")
        with self.assertRaises(IllegalTransitionError, msg="refund a FAILED payment"):
            declined.refund(s2_usd("1.00"), "no")

        # Refunds: partial, then full, never more than captured
        self.assertEqual(p.refund(s2_usd("20.00"), "customer request"), s2_usd("30.00"), "partial refund returns remaining")
        self.assertTrue(p.state() is PaymentState.SUCCEEDED and p.refunded() == s2_usd("20.00"), "still SUCCEEDED after partial refund")
        self.assertTrue(p.version() == 4
                        and p.history()[2].from_state is PaymentState.SUCCEEDED and p.history()[2].to_state is PaymentState.SUCCEEDED
                        and "20.00 USD" in p.history()[2].reason, "partial refund bumps version and logs SUCCEEDED -> SUCCEEDED")
        with self.assertRaises(ValueError, msg="refund more than refundable"):
            p.refund(s2_usd("30.01"), "too much")
        with self.assertRaises(ValueError, msg="refund zero"):
            p.refund(s2_usd("0.00"), "zero")
        with self.assertRaises(ValueError, msg="refund negative"):
            p.refund(s2_usd("-1.00"), "negative")
        with self.assertRaises(CurrencyMismatchError, msg="refund in another currency"):
            p.refund(Money.parse("1.00", EUR), "eur")
        self.assertTrue(p.refunded() == s2_usd("20.00") and p.version() == 4, "rejected refunds change nothing")
        self.assertEqual(p.refund(s2_usd("29.99"), "goodwill"), s2_usd("0.01"), "second partial refund")
        self.assertTrue(p.refund(s2_usd("0.01"), "last cent").is_zero() and p.state() is PaymentState.REFUNDED, "final cent closes the payment")
        self.assertTrue(p.refunded() == p.captured() and p.version() == 6, "refunded equals captured")
        self.assertIs(p.history()[-1].to_state, PaymentState.REFUNDED, "last transition is SUCCEEDED -> REFUNDED")
        with self.assertRaises(IllegalTransitionError, msg="nothing leaves REFUNDED"):
            p.refund(s2_usd("0.01"), "again")

        # Construction guards
        with self.assertRaises(ValueError, msg="amount must be positive"):
            Payment("pay_x", s2_usd("0.00"), clock)
        with self.assertRaises(ValueError, msg="id is required"):
            Payment(" ", s2_usd("1.00"), clock)


if __name__ == "__main__":
    unittest.main()
