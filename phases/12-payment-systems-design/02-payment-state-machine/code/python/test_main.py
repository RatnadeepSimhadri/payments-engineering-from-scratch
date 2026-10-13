"""Tests for phases/12-payment-systems-design/02-payment-state-machine. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR, RULES, USD, ApplyOutcome, ConcurrencyConflictError, CurrencyMismatchError, EventKind, FakeClock,
    FakeProcessor, Guard, IllegalTransitionError, Money, Payment, PaymentEvent, PaymentService, PaymentState,
    PaymentStore, ProcessorCallback, ProcessorOutcome, Transition, TransitionTable,
)

_counter = 0


def new_id() -> str:
    global _counter
    _counter += 1
    return f"evt_{_counter}"


def event(kind: EventKind) -> PaymentEvent:
    if kind is EventKind.REFUND_REQUESTED:
        return PaymentEvent.refund_requested(new_id(), Money.of_minor(100, USD))
    return PaymentEvent.of(new_id(), kind)


def payment_in(target: PaymentState, clock: FakeClock) -> Payment:
    """Drive a fresh 100.00 USD payment into the requested state along the canonical path."""
    p = Payment("pay_t", Money.of_minor(10_000, USD), clock)
    if target is PaymentState.PROCESSING:
        p.apply(event(EventKind.CONFIRM), "t")
    elif target is PaymentState.REQUIRES_ACTION:
        p.apply(event(EventKind.CONFIRM), "t")
        p.apply(event(EventKind.REQUIRE_ACTION), "t")
    elif target is PaymentState.SUCCEEDED:
        p.apply(event(EventKind.CONFIRM), "t")
        p.apply(event(EventKind.PROCESSOR_APPROVED), "t")
    elif target is PaymentState.FAILED:
        p.apply(event(EventKind.CONFIRM), "t")
        p.apply(event(EventKind.PROCESSOR_DECLINED), "t")
    elif target is PaymentState.CANCELED:
        p.apply(event(EventKind.CANCEL), "t")
    elif target is PaymentState.REFUNDING:
        p.apply(event(EventKind.CONFIRM), "t")
        p.apply(event(EventKind.PROCESSOR_APPROVED), "t")
        p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(2_500, USD)), "t")
    elif target is PaymentState.REFUNDED:
        p.apply(event(EventKind.CONFIRM), "t")
        p.apply(event(EventKind.PROCESSOR_APPROVED), "t")
        p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(10_000, USD)), "t")
        p.apply(event(EventKind.REFUND_SUCCEEDED), "t")
    return p


class TransitionTableTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(1_000)

    def test_table_has_eleven_rules(self):
        self.assertEqual(len(RULES), 11)

    def test_every_rule_moves_as_written(self):
        for r in RULES:
            with self.subTest(rule=r):
                if r.guard is Guard.REFUND_CLEARS_BALANCE:
                    p = payment_in(PaymentState.SUCCEEDED, self.clock)
                    p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(10_000, USD)), "full")
                    e = event(EventKind.REFUND_SUCCEEDED)
                else:
                    p = payment_in(r.from_state, self.clock)
                    e = event(r.kind)
                before = p.version
                self.assertIs(p.apply(e, "legal"), ApplyOutcome.APPLIED)
                self.assertIs(p.state, r.to_state)
                self.assertEqual(p.version, before + 1)

    def test_every_pair_outside_the_table_is_refused_untouched(self):
        illegal = 0
        for from_state in PaymentState:
            for kind in EventKind:
                if TransitionTable.has_row(from_state, kind):
                    continue
                with self.subTest(from_state=from_state, kind=kind):
                    p = payment_in(from_state, self.clock)
                    state, version = p.state, p.version
                    e = event(kind)
                    self.assertFalse(p.can_apply(e))
                    with self.assertRaises(IllegalTransitionError):
                        p.apply(e, "illegal")
                    self.assertIs(p.state, state)
                    self.assertEqual(p.version, version)
                    self.assertEqual(len(p.uncommitted()), version)
                illegal += 1
        self.assertEqual(illegal, 62, "8 states x 9 kinds - 10 legal pairs")

    def test_terminal_states(self):
        for s in PaymentState:
            self.assertEqual(s.is_terminal(), s in (PaymentState.REFUNDED, PaymentState.FAILED, PaymentState.CANCELED))
        self.assertFalse(TransitionTable.has_row(PaymentState.PROCESSING, EventKind.CANCEL))


class RefundGuardTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(1_000)
        self.p = payment_in(PaymentState.SUCCEEDED, self.clock)

    def test_over_balance_zero_and_wrong_currency_are_refused(self):
        with self.assertRaises(IllegalTransitionError):
            self.p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(10_001, USD)), "x")
        with self.assertRaises(IllegalTransitionError):
            self.p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(0, USD)), "x")
        with self.assertRaises(CurrencyMismatchError):
            self.p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(1, EUR)), "x")
        self.assertIs(self.p.state, PaymentState.SUCCEEDED)
        self.assertEqual(self.p.version, 2)

    def test_partial_then_failed_then_full(self):
        p = self.p
        p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(4_000, USD)), "partial")
        self.assertEqual(p.pending_refund, Money.of_minor(4_000, USD))
        self.assertEqual(p.refundable(), Money.of_minor(6_000, USD))
        with self.assertRaises(IllegalTransitionError):
            p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(1, USD)), "second while pending")
        p.apply(event(EventKind.REFUND_SUCCEEDED), "settled")
        self.assertIs(p.state, PaymentState.SUCCEEDED)
        self.assertEqual(p.refunded, Money.of_minor(4_000, USD))
        self.assertTrue(p.pending_refund.is_zero())
        p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(6_000, USD)), "rest")
        p.apply(event(EventKind.REFUND_FAILED), "processor refused")
        self.assertIs(p.state, PaymentState.SUCCEEDED)
        self.assertEqual(p.refundable(), Money.of_minor(6_000, USD))
        p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(6_000, USD)), "rest again")
        p.apply(event(EventKind.REFUND_SUCCEEDED), "settled")
        self.assertIs(p.state, PaymentState.REFUNDED)
        self.assertTrue(p.refundable().is_zero())
        self.assertEqual(p.refunded, p.amount)
        with self.assertRaises(IllegalTransitionError):
            p.apply(PaymentEvent.refund_requested(new_id(), Money.of_minor(1, USD)), "x")

    def test_event_and_payment_validation(self):
        with self.assertRaises(ValueError):
            PaymentEvent("e", EventKind.REFUND_REQUESTED, None)
        with self.assertRaises(ValueError):
            PaymentEvent("e", EventKind.CONFIRM, Money.of_minor(1, USD))
        with self.assertRaises(ValueError):
            Payment("p", Money.of_minor(0, USD), self.clock)


class HistoryAndReplayTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(1_000)
        self.p = Payment("pay_log", Money.of_minor(4_999, USD), self.clock)
        self.p.apply(PaymentEvent.of("e1", EventKind.CONFIRM), "confirmed")
        self.clock.advance(500)
        self.p.apply(PaymentEvent.of("e2", EventKind.REQUIRE_ACTION), "3ds")
        self.p.apply(PaymentEvent.of("e3", EventKind.ACTION_COMPLETED), "challenge done")
        self.p.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "auth 00")
        self.p.apply(PaymentEvent.refund_requested("e5", Money.of_minor(1_500, USD)), "partial")

    def test_log_is_complete_contiguous_and_chained(self):
        log = self.p.history()
        self.assertEqual(len(log), 5)
        self.assertEqual(self.p.version, 5)
        for i, t in enumerate(log):
            self.assertEqual(t.version, i + 1)
            if i > 0:
                self.assertIs(t.from_state, log[i - 1].to_state)
        self.assertIs(log[0].from_state, PaymentState.CREATED)
        self.assertIs(log[-1].to_state, self.p.state)
        self.assertEqual(log[1].reason, "3ds")
        self.assertEqual(log[4].event_id, "e5")
        self.assertEqual(log[4].amount, Money.of_minor(1_500, USD))
        self.assertEqual((log[0].at_millis, log[1].at_millis), (1_000, 1_500))

    def test_duplicate_event_id_is_a_noop(self):
        self.assertIs(self.p.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "redelivery"), ApplyOutcome.DUPLICATE)
        self.assertEqual(self.p.version, 5)
        self.assertIs(self.p.state, PaymentState.REFUNDING)
        self.assertFalse(self.p.can_apply(PaymentEvent.of("e99", EventKind.PROCESSOR_APPROVED)))

    def test_replay_reproduces_the_payment(self):
        rebuilt = Payment.replay("pay_log", Money.of_minor(4_999, USD), self.p.history(), self.clock)
        self.assertIs(rebuilt.state, self.p.state)
        self.assertEqual(rebuilt.version, 5)
        self.assertEqual(rebuilt.pending_refund, Money.of_minor(1_500, USD))
        self.assertIs(rebuilt.apply(PaymentEvent.of("e4", EventKind.PROCESSOR_APPROVED), "x"), ApplyOutcome.DUPLICATE)
        self.assertEqual(rebuilt.uncommitted(), ())

    def test_replay_detects_corruption(self):
        log = list(self.p.history())
        gap = log[:2] + log[3:]
        with self.assertRaises(RuntimeError):
            Payment.replay("pay_log", Money.of_minor(4_999, USD), gap, self.clock)
        swapped = list(log)
        swapped[1] = Transition("pay_log", 2, "e2", EventKind.REQUIRE_ACTION, PaymentState.SUCCEEDED,
                                PaymentState.REQUIRES_ACTION, None, "forged", 0)
        with self.assertRaises(RuntimeError):
            Payment.replay("pay_log", Money.of_minor(4_999, USD), swapped, self.clock)


class StoreTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(0)
        self.store = PaymentStore(self.clock)
        a = self.store.create("pay_s", Money.of_minor(2_500, USD))
        a.apply(PaymentEvent.of("s1", EventKind.CONFIRM), "c")
        self.store.save(a)
        self.a = a

    def test_save_load_round_trip(self):
        self.assertEqual(self.a.uncommitted(), ())
        self.assertEqual(self.store.current_version("pay_s"), 1)
        loaded = self.store.load("pay_s")
        self.assertIs(loaded.state, PaymentState.PROCESSING)
        self.assertEqual(loaded.version, 1)
        self.assertIsNot(self.store.load("pay_s"), self.store.load("pay_s"))

    def test_optimistic_concurrency_rejects_the_second_writer(self):
        h1 = self.store.load("pay_s")
        h2 = self.store.load("pay_s")
        h1.apply(PaymentEvent.of("s2", EventKind.PROCESSOR_APPROVED), "approved")
        h2.apply(PaymentEvent.of("s3", EventKind.PROCESSOR_DECLINED), "declined")
        self.store.save(h1)
        with self.assertRaises(ConcurrencyConflictError):
            self.store.save(h2)
        self.assertEqual(self.store.current_version("pay_s"), 2)
        self.assertIs(self.store.load("pay_s").state, PaymentState.SUCCEEDED)
        self.assertFalse(self.store.load("pay_s").can_apply(PaymentEvent.of("s4", EventKind.PROCESSOR_DECLINED)))

    def test_multiple_uncommitted_save_atomically(self):
        h = self.store.load("pay_s")
        h.apply(PaymentEvent.of("s2", EventKind.PROCESSOR_APPROVED), "approved")
        h.apply(PaymentEvent.refund_requested("s5", Money.of_minor(500, USD)), "r")
        h.apply(PaymentEvent.of("s6", EventKind.REFUND_SUCCEEDED), "ok")
        self.store.save(h)
        self.assertEqual(self.store.current_version("pay_s"), 4)
        self.assertEqual(self.store.load("pay_s").refunded, Money.of_minor(500, USD))

    def test_unknown_and_duplicate_ids(self):
        with self.assertRaises(RuntimeError):
            self.store.create("pay_s", Money.of_minor(1, USD))
        with self.assertRaises(RuntimeError):
            self.store.load("nope")


class ServiceAndProcessorTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(0)
        self.store = PaymentStore(self.clock)
        self.processor = FakeProcessor(self.clock, 1_000)
        self.svc = PaymentService(self.store, self.processor)

    def test_three_ds_flow_is_driven_by_the_clock(self):
        self.processor.script("pay_v", ProcessorOutcome.CHALLENGE, ProcessorOutcome.APPROVED)
        self.svc.create("pay_v", Money.of_minor(4_999, USD))
        self.svc.confirm("pay_v", "c1")
        self.assertIs(self.store.load("pay_v").state, PaymentState.PROCESSING)
        self.assertEqual(len(self.processor.calls), 1)
        self.assertEqual(self.svc.pump(), 0)
        self.clock.advance(999)
        self.assertEqual(self.svc.pump(), 0)
        self.clock.advance(1)
        self.assertEqual(self.svc.pump(), 1)
        self.assertIs(self.store.load("pay_v").state, PaymentState.REQUIRES_ACTION)
        self.svc.complete_action("pay_v", "a1")
        self.clock.advance(1_000)
        self.assertEqual(self.svc.pump(), 1)
        self.assertIs(self.store.load("pay_v").state, PaymentState.SUCCEEDED)
        self.assertEqual(self.store.current_version("pay_v"), 4)
        again = ProcessorCallback("pr_0002", "pay_v", ProcessorOutcome.APPROVED, "approved", 0)
        self.assertIs(self.svc.deliver(again), ApplyOutcome.DUPLICATE)
        self.assertEqual(self.store.current_version("pay_v"), 4)

    def test_refund_failure_then_full_refund(self):
        self.svc.create("pay_v", Money.of_minor(4_999, USD))
        self.svc.confirm("pay_v", "c1")
        self.clock.advance(1_000)
        self.svc.pump()
        self.processor.script("pay_v", ProcessorOutcome.REFUND_FAILED)
        self.svc.request_refund("pay_v", "r1", Money.of_minor(999, USD))
        self.clock.advance(1_000)
        self.svc.pump()
        self.assertIs(self.store.load("pay_v").state, PaymentState.SUCCEEDED)
        self.assertEqual(self.store.load("pay_v").refundable(), Money.of_minor(4_999, USD))
        self.svc.request_refund("pay_v", "r2", Money.of_minor(4_999, USD))
        self.clock.advance(1_000)
        self.svc.pump()
        self.assertIs(self.store.load("pay_v").state, PaymentState.REFUNDED)
        with self.assertRaises(IllegalTransitionError):
            self.svc.request_refund("pay_v", "r3", Money.of_minor(1, USD))
        self.assertEqual(len(self.processor.calls), 3)

    def test_decline_and_cancel_in_processing(self):
        self.processor.script("pay_d", ProcessorOutcome.DECLINED)
        self.svc.create("pay_d", Money.of_minor(100, USD))
        self.svc.confirm("pay_d", "d1")
        self.clock.advance(1_000)
        self.svc.pump()
        self.assertIs(self.store.load("pay_d").state, PaymentState.FAILED)
        self.svc.create("pay_c", Money.of_minor(100, USD))
        self.svc.confirm("pay_c", "cc1")
        with self.assertRaises(IllegalTransitionError):
            self.svc.cancel("pay_c", "cc2")
        self.assertIs(self.store.load("pay_c").state, PaymentState.PROCESSING)


if __name__ == "__main__":
    unittest.main()
