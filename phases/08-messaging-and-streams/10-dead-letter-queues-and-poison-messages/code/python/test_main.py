"""Tests for phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages. Run: python3 -m unittest -v"""
import unittest

from main import (
    JPY,
    USD,
    DeadLetterQueue,
    Event,
    FailureKind,
    FaultInjector,
    Handler,
    HandlerError,
    LedgerPostingHandler,
    Money,
    ParkReason,
    ResilientConsumer,
    SimClock,
    StopTheWorldConsumer,
    partition,
)


class CountingHandler(Handler):
    """A handler that fails transiently a fixed number of times per offset, then succeeds. Fully deterministic."""

    def __init__(self, failures_left):
        self.failures_left = dict(failures_left)
        self.handled = []

    def handle(self, e):
        left = self.failures_left.get(e.offset, 0)
        if left > 0:
            self.failures_left[e.offset] = left - 1
            raise HandlerError(FailureKind.TRANSIENT, f"transient #{left}")
        self.handled.append(e.offset)


def ev(offset, key, amount, ccy):
    return Event(offset, key, amount, ccy)


class StopTheWorldTest(unittest.TestCase):
    def test_head_of_line_blocking(self):
        naive = StopTheWorldConsumer(LedgerPostingHandler(FaultInjector(1, 0.0)), 3)
        naive.consume(partition())
        self.assertIsNotNone(naive.stuck_on())
        self.assertEqual(naive.stuck_on().offset, 2)
        self.assertEqual(naive.processed(), 2)
        self.assertEqual(naive.committed_offset(), 2)


class ResilientConsumerTest(unittest.TestCase):
    def setUp(self):
        self.partition = partition()
        self.clock = SimClock()
        self.handler = LedgerPostingHandler(FaultInjector(1, 0.0))
        self.dlq = DeadLetterQueue()
        self.consumer = ResilientConsumer(self.handler, self.dlq, self.clock, 3, 100)
        self.consumer.consume(self.partition)

    def test_poison_and_quarantine(self):
        s = self.consumer.stats()
        self.assertEqual(s.committed_offset, 10)
        self.assertEqual(s.processed + s.parked, len(self.partition))
        self.assertTrue(any(d.event.offset == 2 and d.reason is ParkReason.PERMANENT_FAILURE and d.attempts == 1 for d in self.dlq.all()))
        self.assertTrue(any(d.event.offset == 6 and d.reason is ParkReason.PERMANENT_FAILURE for d in self.dlq.all()))
        self.assertEqual(s.retries, 0)
        self.assertEqual(self.clock.now(), 0)
        self.assertEqual(sum(1 for d in self.dlq.all() if d.event.account_key == "acct-A" and d.reason is ParkReason.KEY_QUARANTINED), 2)
        self.assertEqual(self.handler.balance("acct-A").minor_units, 10000)
        self.assertEqual(self.handler.balance("acct-B").minor_units, 3975)
        self.assertEqual(self.handler.balance("acct-C"), Money(1500, JPY))
        self.assertTrue(self.consumer.is_quarantined("acct-A"))
        self.assertTrue(self.consumer.is_quarantined("acct-D"))
        self.assertFalse(self.consumer.is_quarantined("acct-B"))
        self.assertIn("offset 2", self.dlq.for_key("acct-A")[1].last_error)

    def test_replay_with_correction(self):
        replayed = self.consumer.replay("acct-A", {2: ev(2, "acct-A", "12.34", "USD")})
        self.assertEqual(replayed, 3)
        self.assertEqual(self.handler.balance("acct-A").minor_units, 7734)
        self.assertFalse(self.consumer.is_quarantined("acct-A"))
        self.assertEqual(self.dlq.size(), 1)
        self.assertEqual(self.dlq.all()[0].event.account_key, "acct-D")
        self.assertEqual(self.consumer.stats().processed, 9)
        self.assertEqual(self.consumer.stats().parked, 1)
        replayed_d = self.consumer.replay("acct-D", {})
        self.assertEqual(replayed_d, 0)
        self.assertTrue(self.consumer.is_quarantined("acct-D"))
        self.assertEqual(len(self.dlq.for_key("acct-D")), 1)
        self.handler.handle(ev(0, "acct-A", "100.00", "USD"))
        self.assertEqual(self.handler.balance("acct-A").minor_units, 7734)


class TransientFailureTest(unittest.TestCase):
    def test_retries_with_exponential_backoff(self):
        clock2 = SimClock()
        counting = CountingHandler({1: 2})  # offset 1 fails twice, then succeeds
        dlq2 = DeadLetterQueue()
        c2 = ResilientConsumer(counting, dlq2, clock2, 3, 100)
        c2.consume([ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k1", "3.00", "USD")])
        self.assertEqual(counting.handled, [0, 1, 2])
        self.assertEqual(c2.stats().retries, 2)
        self.assertEqual(clock2.now(), 300)
        self.assertEqual(dlq2.size(), 0)
        self.assertFalse(c2.is_quarantined("k1"))
        with self.assertRaises(ValueError):
            ResilientConsumer(counting, dlq2, clock2, 0, 100)

    def test_max_attempts_exceeded_then_replay(self):
        clock3 = SimClock()
        counting3 = CountingHandler({1: 5})  # offset 1 fails five times: more than max_attempts
        dlq3 = DeadLetterQueue()
        c3 = ResilientConsumer(counting3, dlq3, clock3, 3, 100)
        c3.consume([ev(0, "k1", "1.00", "USD"), ev(1, "k1", "2.00", "USD"), ev(2, "k2", "3.00", "USD"), ev(3, "k1", "4.00", "USD")])
        self.assertIs(dlq3.for_key("k1")[0].reason, ParkReason.MAX_ATTEMPTS_EXCEEDED)
        self.assertEqual(dlq3.for_key("k1")[0].attempts, 3)
        self.assertEqual(dlq3.for_key("k1")[0].parked_at, 300)
        self.assertIs(dlq3.for_key("k1")[1].reason, ParkReason.KEY_QUARANTINED)
        self.assertIn(2, counting3.handled)
        self.assertEqual(c3.stats().committed_offset, 4)
        counting3.failures_left[1] = 1
        replayed3 = c3.replay("k1", {})
        self.assertEqual(replayed3, 2)
        self.assertEqual(counting3.handled, [0, 2, 1, 3])


class BoundaryTest(unittest.TestCase):
    def test_boundaries_and_money(self):
        with self.assertRaises(ValueError):
            SimClock().advance(-1)
        with self.assertRaises(ArithmeticError):
            Money.parse("12.345", USD)
        with self.assertRaises(ValueError):
            Money(1, USD).plus(Money(1, JPY))
        with self.assertRaises(ValueError):
            Money.parse("12,34", USD)
        with self.assertRaises(TypeError):
            Money(1.0, USD)

    def test_seeded_fault_injector_matches_java(self):
        # java.util.Random(11) with rate 0.25: the demo's second run retries exactly twice and parks offset 5
        faults = FaultInjector(11, 0.25)
        pattern = [faults.should_fail() for _ in range(12)]
        self.assertEqual(sum(pattern), 4)
        self.assertEqual(pattern[:6], [False, False, False, True, True, True])


if __name__ == "__main__":
    unittest.main()
