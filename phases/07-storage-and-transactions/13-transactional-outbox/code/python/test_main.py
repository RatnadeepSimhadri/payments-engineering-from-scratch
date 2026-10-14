"""Tests for phases/07-storage-and-transactions/13-transactional-outbox. Run: python3 -m unittest -v"""
import unittest

from main import (
    Broker,
    BrokerUnavailableException,
    CrashException,
    CrashPoint,
    Currency,
    CurrencyMismatchException,
    DualWritePaymentService,
    Events,
    LedgerConsumer,
    Money,
    OutboxPaymentService,
    OutboxRelay,
    Payment,
    SimClock,
    Store,
    payment,
    usd,
)


class World:
    """One store, one broker, an outbox service, a relay with the given batch size, a ledger consumer."""

    def __init__(self, batch_size: int) -> None:
        self.clock = SimClock(0)
        self.store = Store()
        self.broker = Broker()
        self.svc = OutboxPaymentService(self.store, self.clock)
        self.relay = OutboxRelay(self.store, self.broker, batch_size)
        self.ledger = LedgerConsumer(self.broker)

    def pay(self, id: str, merchant: str, amount: str) -> Payment:
        return payment(id, merchant, amount, self.clock)


class StoreTransactionTest(unittest.TestCase):
    def test_both_rows_or_neither(self):
        store = Store()
        tx = (store.begin().insert_payment(Payment("p1", "m", usd("1.00"), 0))
              .insert_outbox("evt-p1", "m", "PaymentAccepted", "x", 0))
        self.assertTrue(store.payment_count() == 0 and not store.outbox_rows())
        tx.commit()
        self.assertTrue(store.payment_count() == 1 and len(store.outbox_rows()) == 1)
        self.assertTrue(store.outbox_rows()[0].seq == 1 and not store.outbox_rows()[0].published)
        with self.assertRaises(RuntimeError):
            tx.commit()
        tx2 = (store.begin().insert_payment(Payment("p2", "m", usd("2.00"), 0))
               .insert_outbox("evt-p2", "m", "PaymentAccepted", "y", 0))
        tx2.rollback()
        self.assertTrue(store.payment_count() == 1 and len(store.outbox_rows()) == 1 and store.rollbacks == 1)
        with self.assertRaises(RuntimeError):
            store.begin().insert_payment(Payment("p1", "m", usd("1.00"), 0))

    def test_context_manager_commits_or_rolls_back(self):
        # Python-only convenience suggested by the lesson: `with store.begin() as tx:`.
        store = Store()
        with store.begin() as tx:
            tx.insert_payment(Payment("c1", "m", usd("1.00"), 0)).insert_outbox("evt-c1", "m", "PaymentAccepted", "x", 0)
        with self.assertRaises(ValueError):
            with store.begin() as tx:
                tx.insert_payment(Payment("c2", "m", usd("1.00"), 0))
                raise ValueError("validation failed")
        self.assertTrue(store.payment_count() == 1 and store.commits == 1 and store.rollbacks == 1)


class DualWriteTest(unittest.TestCase):
    def test_commit_then_publish_orphans_rows(self):
        clock = SimClock(0)
        store, broker = Store(), Broker()
        svc = DualWritePaymentService(store, broker)
        broker.down = True
        with self.assertRaises(BrokerUnavailableException):
            svc.accept(payment("d1", "acme", "20.00", clock))
        self.assertTrue(store.payment_count() == 1 and broker.size() == 0)
        broker.down = False
        svc.crash_point = CrashPoint.AFTER_COMMIT_BEFORE_PUBLISH
        with self.assertRaises(CrashException):
            svc.accept(payment("d2", "acme", "5.00", clock))
        self.assertTrue(store.payment_count() == 2 and broker.size() == 0)
        ledger = LedgerConsumer(broker)
        ledger.consume()
        self.assertEqual(ledger.balance("acme"), usd("0.00"))

    def test_publish_then_commit_ghost_event(self):
        clock = SimClock(0)
        store, broker = Store(), Broker()
        svc = DualWritePaymentService(store, broker)
        svc.publish_first = True
        svc.crash_point = CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT
        with self.assertRaises(CrashException):
            svc.accept(payment("g1", "acme", "12.50", clock))
        ledger = LedgerConsumer(broker)
        ledger.consume()
        self.assertTrue(store.payment_count() == 0 and broker.size() == 1)
        self.assertEqual(ledger.balance("acme"), usd("12.50"))


class OutboxTest(unittest.TestCase):
    def test_broker_outage_loses_nothing(self):
        w = World(10)
        w.broker.down = True
        w.svc.accept(w.pay("o1", "acme", "20.00"))
        w.svc.accept(w.pay("o2", "acme", "35.00"))
        self.assertTrue(w.store.payment_count() == 2 and w.store.unpublished_count() == 2)
        self.assertEqual(w.broker.publish_attempts, 0)
        self.assertTrue(w.relay.drain(5) == 0 and w.relay.failed_attempts >= 1)
        self.assertEqual(w.store.unpublished_count(), 2)
        w.broker.down = False
        self.assertTrue(w.relay.drain(5) == 2 and w.store.unpublished_count() == 0)
        w.ledger.consume()
        self.assertEqual(w.ledger.balance("acme"), usd("55.00"))
        self.assertEqual(w.relay.poll(), 0)

    def test_relay_crash_is_at_least_once_and_consumer_dedupes(self):
        w = World(10)
        w.svc.accept(w.pay("r1", "globex", "100.00"))
        w.svc.accept(w.pay("r2", "globex", "1.00"))
        w.relay.crash_point = CrashPoint.AFTER_PUBLISH_BEFORE_MARK
        with self.assertRaises(CrashException):
            w.relay.poll()
        self.assertTrue(w.broker.size() == 1 and w.store.unpublished_count() == 2)
        w.relay.drain(5)
        self.assertTrue(w.broker.size() == 3 and w.store.unpublished_count() == 0)
        self.assertEqual(w.broker.messages()[0].event_id, w.broker.messages()[1].event_id)
        applied = w.ledger.consume()
        self.assertTrue(applied == 2 and w.ledger.duplicates_ignored == 1)
        self.assertEqual(w.ledger.balance("globex"), usd("101.00"))
        self.assertEqual(w.ledger.offset(), 3)
        self.assertTrue(w.ledger.consume() == 0 and w.ledger.balance("globex") == usd("101.00"))


class OrderingTest(unittest.TestCase):
    def test_seq_is_commit_order_across_batches(self):
        w = World(2)
        for i in range(1, 6):
            w.svc.accept(w.pay(f"s{i}", "acme", f"{i}.00"))
        polls = 0
        while w.store.unpublished_count() > 0:
            w.relay.poll()
            polls += 1
        self.assertEqual(polls, 3)
        self.assertEqual("".join(m.event_id + "," for m in w.broker.messages()), "evt-s1,evt-s2,evt-s3,evt-s4,evt-s5,")
        self.assertEqual(World(10).store.unpublished(3), [])
        w2 = World(10)
        for i in range(1, 6):
            w2.svc.accept(w2.pay(f"t{i}", "acme", "1.00"))
        self.assertTrue(len(w2.store.unpublished(3)) == 3 and w2.store.unpublished(3)[2].seq == 3)

    def test_failure_mid_batch_keeps_order(self):
        w = World(10)
        w.svc.accept(w.pay("b1", "acme", "1.00"))
        w.svc.accept(w.pay("b2", "acme", "2.00"))
        w.broker.down = True
        self.assertTrue(w.relay.poll() == 0 and w.store.unpublished_count() == 2)
        w.broker.down = False
        w.relay.poll()
        self.assertTrue(w.broker.messages()[0].event_id == "evt-b1" and w.broker.messages()[1].event_id == "evt-b2")


class RollbackAndMoneyTest(unittest.TestCase):
    def test_rollback_path(self):
        w = World(10)
        w.svc.accept_then_fail(w.pay("x1", "acme", "999.00"))
        self.assertTrue(w.store.payment_count() == 0 and not w.store.outbox_rows())
        self.assertTrue(w.relay.poll() == 0 and w.broker.size() == 0)

    def test_money_discipline(self):
        self.assertTrue(Events.payload_for(Payment("p", "m", usd("0.05"), 0)).endswith("0.05 USD"))
        self.assertEqual(LedgerConsumer.parse_money("1234.56 USD").minor_units, 123456)
        with self.assertRaises(CurrencyMismatchException):
            usd("1.00").plus(Money.of_minor(100, Currency("EUR", 2)))
        with self.assertRaises(OverflowError):  # Python-only: Math.addExact checked by hand
            Money.of_minor((1 << 63) - 1, Currency.USD).plus(usd("0.01"))


if __name__ == "__main__":
    unittest.main()
